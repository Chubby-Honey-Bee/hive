package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/runner"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// fakeLocalModel is the one model the fake endpoint serves, as a local
// server serves the models it has pulled.
const fakeLocalModel = "hive-local:4b"

// Words in each node's prompt, which name the node a request serves: the
// dispatch, its repair, the evaluator, and the synthesis, whose repair
// repeats its prompt, as the evaluator's does.
const (
	dispatchMarker       = "Carry out the hive's research actions"
	dispatchRepairMarker = "Your answer to the hive's research actions"
	evaluateMarker       = "Judge the hive's research on wave"
	synthesisMarker      = "Write the final synthesis of the hive's research"
)

// dispatchReadPath is the project file the fake dispatch reads with its one
// tool call.
const dispatchReadPath = "notes/fan.md"

// fakeHiveCall is one chat request the fake answered.
type fakeHiveCall struct {
	model        string
	prompt       string // the node's own prompt, the first user message
	withSchema   bool   // the request set response_format
	offeredTools bool
	toolResult   string // the tool message the request carried, if any
}

// fakeHiveModel is an OpenAI-compatible endpoint standing in for a local
// server with one model, for the hive nodes that call a model. Like Ollama
// it lists its models and answers 404 for any other. reply gives each node's
// answer. With readFirst, the dispatch's first request is answered with one
// read_file call, and the request after it with the dispatch's reply; with
// readOnRepair, the dispatch's repair is answered the same way. With
// shellFirst, the dispatch's first request is answered with one shell call
// running that command instead. With toolsEveryTurn, every dispatch request
// that offers tools is answered with one read_file call, so only the
// wrap-up call after the turn cap gets the dispatch's reply.
type fakeHiveModel struct {
	mu             sync.Mutex
	reply          map[string]string
	readFirst      bool
	readOnRepair   bool
	shellFirst     string
	toolsEveryTurn bool
	calls          map[string][]fakeHiveCall // node → requests, in order
	probes         int
	unexpected     []string
}

// fakeCompletionTokens is what the fake reports for each reply.
const fakeCompletionTokens = 5

func (f *fakeHiveModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{map[string]any{"id": fakeLocalModel}}})
		return
	}
	if r.URL.Path != "/chat/completions" {
		f.unexpected = append(f.unexpected, r.URL.Path)
		http.NotFound(w, r)
		return
	}
	var body struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		ResponseFormat json.RawMessage `json:"response_format"`
		Tools          json.RawMessage `json:"tools"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Model != fakeLocalModel {
		f.unexpected = append(f.unexpected, "model "+body.Model)
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": fmt.Sprintf("model %q not found", body.Model)}})
		return
	}
	var first, toolResult string
	for _, m := range body.Messages {
		if m.Role == "user" && first == "" {
			first = m.Content
		}
		if m.Role == "tool" {
			toolResult = m.Content
		}
	}
	var node, content string
	switch {
	case strings.HasPrefix(first, "Describe today's weather"):
		f.probes++
		content = `{"probe":"hive-constraint-ok","order":"second"}`
	case strings.Contains(first, dispatchMarker), strings.Contains(first, dispatchRepairMarker):
		node = "dispatch"
	case strings.Contains(first, evaluateMarker):
		node = "evaluate"
	case strings.Contains(first, synthesisMarker):
		node = "final-synthesis"
	default:
		f.unexpected = append(f.unexpected, first)
		http.Error(w, "unexpected prompt", http.StatusBadRequest)
		return
	}
	offered := len(body.Tools) > 0 && string(body.Tools) != "null"
	if node != "" {
		content = f.reply[node]
		f.calls[node] = append(f.calls[node], fakeHiveCall{
			model: body.Model, prompt: first,
			withSchema:   len(body.ResponseFormat) > 0 && string(body.ResponseFormat) != "null",
			offeredTools: offered,
			toolResult:   toolResult,
		})
	}
	if f.toolsEveryTurn && node == "dispatch" && offered {
		writeFakeToolCall(w, "read_file", map[string]string{"path": dispatchReadPath})
		return
	}
	// The repair prompt carries the task again, so it holds both markers.
	repair := strings.Contains(first, dispatchRepairMarker)
	read := f.readFirst && !repair && strings.Contains(first, dispatchMarker)
	if f.readOnRepair && repair {
		read = true
	}
	if f.shellFirst != "" && !repair && strings.Contains(first, dispatchMarker) && toolResult == "" {
		writeFakeToolCall(w, "shell", map[string]string{"command": f.shellFirst})
		return
	}
	if read && toolResult == "" {
		writeFakeToolCall(w, "read_file", map[string]string{"path": dispatchReadPath})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{
			"finish_reason": "stop",
			"message":       map[string]any{"role": "assistant", "content": content},
		}},
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": fakeCompletionTokens},
	})
}

// writeFakeToolCall answers a chat request with one call of tool with args.
func writeFakeToolCall(w http.ResponseWriter, tool string, args map[string]string) {
	raw, _ := json.Marshal(args)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{
			"finish_reason": "tool_calls",
			"message": map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
				"id": "call_1", "type": "function",
				"function": map[string]any{"name": tool, "arguments": string(raw)},
			}}},
		}},
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": fakeCompletionTokens},
	})
}

// offlineHive is a database seeded with one open critical gap, a project
// directory holding the note the dispatch reads, a fake local endpoint, and
// the environment that points a run at both: the test binary stands in for
// chb, and the planner tier resolves to the one model the endpoint serves.
type offlineHive struct {
	fake   *fakeHiveModel
	store  *db.Store
	dbPath string
	dir    string
	gapID  int64
}

const offlineProject = "offline"

func newOfflineHive(t *testing.T, reply map[string]string) *offlineHive {
	t.Helper()
	fake := &fakeHiveModel{reply: reply, readFirst: true, calls: map[string][]fakeHiveCall{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	userTiers(t, "planner", fakeLocalModel)
	t.Setenv(runAsChbEnv, "1")
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "offline")
	t.Setenv("HIVE_BUDGET_MODE", "")
	t.Setenv("HIVE_EMBED_PROVIDER", "stub")
	t.Setenv("HIVE_DISABLE_RATE_LIMIT", "1")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")

	h := &offlineHive{fake: fake, dir: dir, dbPath: filepath.Join(dir, "hive.db")}
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(dispatchReadPath)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, dispatchReadPath), []byte(fanNote), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := db.NewStore(h.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	h.store = s
	zero := 0
	if err := s.Gaps().AddGap(1, "seed", "How many workers does the fan run at once?", "critical", &zero, &zero, &zero, &zero); err != nil {
		t.Fatal(err)
	}
	if err := s.ReadDB.QueryRow(`SELECT id FROM gaps WHERE agent='seed'`).Scan(&h.gapID); err != nil {
		t.Fatal(err)
	}
	return h
}

func hiveWorkflowPath(t *testing.T) string {
	t.Helper()
	wf, err := filepath.Abs(filepath.Join("..", "..", "workflows", "hive.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return wf
}

func (h *offlineHive) run(t *testing.T, maxIterations int) (*runner.Result, error) {
	t.Helper()
	return runner.Run(context.Background(), h.store, runner.Config{
		WorkflowYAML:  hiveWorkflowPath(t),
		ProjectName:   offlineProject,
		ProjectDir:    h.dir,
		DBPath:        h.dbPath,
		Inputs:        map[string]any{"project": offlineProject, "max_iterations": maxIterations},
		MaxIterations: 200,
		Provider:      "openai",
		Log:           io.Discard,
	})
}

func (h *offlineHive) iteration(t *testing.T) int {
	t.Helper()
	var n int
	if err := h.store.ReadDB.QueryRow(`SELECT iteration FROM hive_state WHERE project=?`, offlineProject).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (h *offlineHive) nodeStatus(t *testing.T, runID int64, node string) string {
	t.Helper()
	var status string
	if err := h.store.ReadDB.QueryRow(
		`SELECT status FROM workflow_node_states WHERE run_id=? AND node_name=?`, runID, node,
	).Scan(&status); err != nil {
		t.Fatalf("%s: %v", node, err)
	}
	return status
}

// fanNote is the project file the dispatch reads.
const fanNote = "The fan runs 4 workers at once.\n"

// The answers a model that follows each node's format gives.
var wellFormedHiveReplies = map[string]string{
	"dispatch":        `{"dispatch_results":"offline: ran nothing","findings_written":0,"gaps_resolved":0}`,
	"final-synthesis": `{"report_markdown":"# Offline synthesis\n\nNothing was established.","open_questions":["How many workers does the fan run at once?"]}`,
}

// The hive workflow run offline, end to end: its relays are command nodes
// that run this binary as chb against the run's database, and its model
// nodes reach a fake local endpoint that serves one model. They name a tier,
// which the tiers config maps to that model. Seeded with one critical gap
// that nothing resolves, every scan plans a gap fill for it and no gate, so
// the hive runs to its cap; each dispatch reads a project note, its one tool
// call. A second run on the capped hive does no pass.
func TestHiveWorkflow_OfflineRun(t *testing.T) {
	const iterations = 2
	h := newOfflineHive(t, wellFormedHiveReplies)
	res, err := h.run(t, iterations)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	s := h.store
	run, err := s.Workflows().GetWorkflowRun(res.RunID)
	if err != nil || run.Status != "completed" {
		t.Fatalf("run = %+v (%v), want completed", run, err)
	}

	defn, err := workflow.LoadYAML(hiveWorkflowPath(t))
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal([]byte(run.StateJSON), &st); err != nil {
		t.Fatal(err)
	}
	// Once per run: the setup before the loop and everything after it. The
	// gate's nodes never: a critical gap is open, so no plan asks for it.
	once := map[string]bool{"init": true, "report": true, "write-synthesis": true, "export-graph": true}
	never := map[string]bool{"validate-sources": true, "gate-state": true, "gate": true}
	nodes, _ := defn["nodes"].(map[string]any)
	commandNodes := 0
	for name, raw := range nodes {
		node, _ := raw.(map[string]any)
		if node["type"] != "command" {
			continue
		}
		commandNodes++
		runs := iterations
		switch {
		case once[name]:
			runs = 1
		case never[name]:
			runs = 0
		}
		invs := commandInvocations(t, s, res.RunID, name)
		if len(invs) != runs {
			t.Fatalf("%s ran %d time(s), want %d", name, len(invs), runs)
		}
		for _, inv := range invs {
			if inv.argv[0] != exe || inv.exitCode != 0 || len(inv.argv) != len(node["argv"].([]any)) {
				t.Fatalf("%s invocation = %+v, want this binary as chb, %d arguments and exit 0", name, inv, len(node["argv"].([]any)))
			}
			for _, a := range inv.argv {
				if strings.Contains(a, "{") {
					t.Fatalf("%s ran with an unfilled placeholder: %q", name, inv.argv)
				}
			}
		}
		in, out := nodeTokens(t, s, res.RunID, name)
		if in != 0 || out != 0 {
			t.Fatalf("%s tokens = %d/%d, want 0/0: a relay calls no model", name, in, out)
		}
	}
	if commandNodes == 0 {
		t.Fatal("hive.yaml has no command nodes")
	}

	scans := 0
	for _, inv := range commandInvocations(t, s, res.RunID, "scan") {
		if strings.Contains(inv.stdout, `"phase": "dispatching"`) {
			scans++
		}
	}
	if got := h.iteration(t); got != iterations || got != scans {
		t.Fatalf("hive_state.iteration = %d, counted scans = %d, want both %d", got, scans, iterations)
	}
	if st["stop_reason"] != fmt.Sprintf("iteration %d reached max_iterations %d", iterations, iterations) {
		t.Fatalf("stop_reason = %v, want the cap", st["stop_reason"])
	}

	h.fake.mu.Lock()
	if len(h.fake.unexpected) > 0 {
		t.Fatalf("the fake endpoint got requests no hive node should send: %q", h.fake.unexpected)
	}
	// Each dispatch is two requests: its read_file call and its answer.
	wantCalls := map[string]int{"dispatch": 2 * iterations, "evaluate": 0, "final-synthesis": 1}
	for node, want := range wantCalls {
		if want == 0 {
			if got := len(h.fake.calls[node]); got != 0 {
				t.Fatalf("%s model calls = %d, want none", node, got)
			}
			continue
		}
		if got := len(h.fake.calls[node]); got != want {
			t.Fatalf("%s model calls = %d, want %d", node, got, want)
		}
		if _, out := nodeTokens(t, s, res.RunID, node); out != int64(want*fakeCompletionTokens) {
			t.Fatalf("%s tokens_out = %d, want %d", node, out, want*fakeCompletionTokens)
		}
	}
	// The plan reaches dispatch as JSON, gap id included, so the dispatch
	// can close the gap it fills. dispatch keeps its tools, so its schema
	// comes only on a finalize call; final-synthesis has none, so its one
	// call carries the schema.
	gapRef := `"gap_id":` + strconv.FormatInt(h.gapID, 10)
	for i, c := range h.fake.calls["dispatch"] {
		if !strings.Contains(c.prompt, gapRef) || !c.offeredTools || c.withSchema {
			t.Fatalf("dispatch call %d = %+v, want the plan with %s, tools offered and no schema", i+1, c, gapRef)
		}
		// The second request of each dispatch carries what read_file read.
		if read := strings.Contains(c.toolResult, strings.TrimSpace(fanNote)); read != (i%2 == 1) {
			t.Fatalf("dispatch call %d carries tool result %q, want the note on every second call", i+1, c.toolResult)
		}
	}
	if c := h.fake.calls["final-synthesis"][0]; c.offeredTools || !c.withSchema {
		t.Fatalf("final-synthesis call = %+v, want the schema and no tools", c)
	}
	synthesisPrompt := h.fake.calls["final-synthesis"][0].prompt
	h.fake.mu.Unlock()
	if !strings.Contains(synthesisPrompt, fmt.Sprintf("%d of them in this", iterations)) {
		t.Fatalf("final-synthesis prompt does not count %d passes in this run:\n%s", iterations, synthesisPrompt)
	}

	var reply struct {
		Report    string   `json:"report_markdown"`
		Questions []string `json:"open_questions"`
	}
	if err := json.Unmarshal([]byte(wellFormedHiveReplies["final-synthesis"]), &reply); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(h.dir, "workspace", offlineProject, "final-synthesis.md"))
	if err != nil {
		t.Fatalf("final-synthesis.md: %v", err)
	}
	if !strings.HasPrefix(string(written), reply.Report) || !strings.Contains(string(written), "- "+reply.Questions[0]) {
		t.Fatalf("final-synthesis.md = %q, want the report and its open question", written)
	}

	// The hive is at its cap: a new run does no pass and calls no dispatch.
	h.fake.mu.Lock()
	h.fake.calls = map[string][]fakeHiveCall{}
	h.fake.mu.Unlock()
	again, err := h.run(t, iterations)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if got := h.iteration(t); got != iterations {
		t.Fatalf("after a run on a capped hive, iteration = %d, want %d", got, iterations)
	}
	if status := h.nodeStatus(t, again.RunID, "dispatch"); status != "skipped" {
		t.Fatalf("dispatch on a capped hive: %s, want skipped", status)
	}
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	if len(h.fake.calls["dispatch"]) != 0 || len(h.fake.calls["final-synthesis"]) != 1 {
		t.Fatalf("model calls on a capped hive = %v, want final-synthesis alone", h.fake.calls)
	}
	if p := h.fake.calls["final-synthesis"][0].prompt; !strings.Contains(p, "0 of them in this") {
		t.Fatalf("final-synthesis prompt on a capped hive does not count 0 passes in this run:\n%s", p)
	}
}

// A small model that ignores the format is rejected and repaired, never
// passed as prose. A dispatch that answers "command not found" in prose on
// every call (its answer, the finalize call and the repair) is rejected: the
// pass is never completed and no synthesis is written.
// The dispatch agent's shell does not move the hive's loop. A scan run there
// would count a second pass inside the first, which complete-iteration
// refuses. chb hive next refuses its run's database in a process the run's
// model drives, so the shell call returns the refusal, the count stays where
// the pass's scan left it, and the run completes.
func TestHiveWorkflow_ADispatchThatRunsHiveNextLeavesTheCount(t *testing.T) {
	h := newOfflineHive(t, wellFormedHiveReplies)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	h.fake.readFirst = false
	h.fake.shellFirst = fmt.Sprintf("'%s' hive next --project %s", exe, offlineProject)
	res, err := h.run(t, 1)
	if err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	if got := h.iteration(t); got != 1 {
		t.Errorf("after one pass the iteration is %d, want 1", got)
	}
	if got := h.nodeStatus(t, res.RunID, "complete-iteration"); got != "completed" {
		t.Errorf("complete-iteration is %s, want completed", got)
	}
	calls := h.fake.calls["dispatch"]
	if len(calls) < 2 || !strings.Contains(calls[1].toolResult, "HIVE_AGENT_DB") {
		t.Errorf("the dispatch's shell call did not return the refusal naming HIVE_AGENT_DB; its requests: %+v", calls)
	}
}

// A dispatch whose model calls a tool on every turn it is offered one gets a
// wrap-up call once its turns are used, with no tools and the node's schema
// set, and its answer there completes the pass.
func TestHiveWorkflow_ADispatchOutOfTurnsAnswersOnItsWrapUp(t *testing.T) {
	h := newOfflineHive(t, wellFormedHiveReplies)
	h.fake.readFirst = false
	h.fake.toolsEveryTurn = true
	res, err := h.run(t, 1)
	if err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	if got := h.nodeStatus(t, res.RunID, "complete-iteration"); got != "completed" {
		t.Errorf("complete-iteration is %s, want completed", got)
	}
	calls := h.fake.calls["dispatch"]
	if n := len(calls); n != 31 || calls[n-1].offeredTools || !calls[n-1].withSchema {
		t.Fatalf("the dispatch made %d calls, the last offering tools %v and the schema %v; want 31, the last with no tools and the schema", n, n > 0 && calls[n-1].offeredTools, n > 0 && calls[n-1].withSchema)
	}
}

func TestHiveWorkflow_ProseDispatchIsRejected(t *testing.T) {
	const prose = "I looked at the plan. chb: command not found. Nothing was written."
	h := newOfflineHive(t, map[string]string{"dispatch": prose, "final-synthesis": wellFormedHiveReplies["final-synthesis"]})
	res, err := h.run(t, 2)
	if err == nil {
		t.Fatal("Run succeeded with a dispatch that answered in prose")
	}
	if status := h.nodeStatus(t, res.RunID, "dispatch"); status != "rejected" {
		t.Fatalf("dispatch = %s, want rejected", status)
	}
	for _, node := range []string{"complete-iteration", "report", "write-synthesis"} {
		if invs := commandInvocations(t, h.store, res.RunID, node); len(invs) != 0 {
			t.Fatalf("%s ran after a rejected dispatch: %+v", node, invs)
		}
	}
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	// The dispatch's read_file call and its answer, its finalize call, the
	// repair and the repair's finalize.
	if got := len(h.fake.calls["dispatch"]); got != 5 {
		t.Fatalf("dispatch calls = %d, want 5: read, answer, finalize, repair, finalize", got)
	}
	// The repair keeps the dispatch's tools and is sent the task again, with
	// the prose answer; after a malformed answer it is told to run no
	// command, since the plan's writes would repeat.
	repair := h.fake.calls["dispatch"][3].prompt
	if !strings.Contains(repair, dispatchMarker) || !strings.Contains(repair, prose) || !strings.Contains(repair, "run no command") {
		t.Fatalf("dispatch repair prompt = %q, want the task, the prose answer and the word to run no command", repair)
	}
	if len(h.fake.calls["final-synthesis"]) != 0 {
		t.Fatal("final-synthesis was called after a rejected dispatch")
	}
	run, err := h.store.Workflows().GetWorkflowRun(res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(run.StateJSON, "command not found") {
		t.Fatalf("the prose reached state: %s", run.StateJSON)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "workspace", offlineProject, "final-synthesis.md")); !os.IsNotExist(err) {
		t.Fatalf("final-synthesis.md exists after a failed run (%v)", err)
	}
}

// A synthesis answered in prose is rejected after its repair, and no file
// is written: chb writes the file from the answer, and there is none.
func TestHiveWorkflow_ProseSynthesisWritesNoFile(t *testing.T) {
	h := newOfflineHive(t, map[string]string{"final-synthesis": "Here is the synthesis: the hive found nothing."})
	// A hive already at its cap goes straight to the synthesis.
	if _, err := h.store.WriteDB.Exec(`INSERT INTO hive_state (project, iteration) VALUES (?, 1)`, offlineProject); err != nil {
		t.Fatal(err)
	}
	res, err := h.run(t, 1)
	if err == nil {
		t.Fatal("Run succeeded with a synthesis in prose")
	}
	if status := h.nodeStatus(t, res.RunID, "final-synthesis"); status != "rejected" {
		t.Fatalf("final-synthesis = %s, want rejected", status)
	}
	if invs := commandInvocations(t, h.store, res.RunID, "write-synthesis"); len(invs) != 0 {
		t.Fatalf("write-synthesis ran: %+v", invs)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "workspace", offlineProject, "final-synthesis.md")); !os.IsNotExist(err) {
		t.Fatalf("final-synthesis.md exists (%v)", err)
	}
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	if got := len(h.fake.calls["final-synthesis"]); got != 2 {
		t.Fatalf("final-synthesis calls = %d, want 2: the answer and one repair", got)
	}
}

// A dispatch that runs no tool did none of its actions, however well-formed
// its report. min_tool_calls sends it back to its repair with the count, the
// answer and the task; a repair that runs no tool either is rejected. The
// pass is never completed, nothing either call said reaches state, and no
// synthesis is written.
func TestHiveWorkflow_ADispatchThatRunsNoToolIsSentBackThenRejected(t *testing.T) {
	h := newOfflineHive(t, wellFormedHiveReplies)
	h.fake.readFirst = false
	res, err := h.run(t, 2)
	if err == nil {
		t.Fatal("Run succeeded with a dispatch that ran no tool")
	}
	if status := h.nodeStatus(t, res.RunID, "dispatch"); status != "rejected" {
		t.Fatalf("dispatch = %s, want rejected", status)
	}
	for _, node := range []string{"complete-iteration", "report", "write-synthesis"} {
		if invs := commandInvocations(t, h.store, res.RunID, node); len(invs) != 0 {
			t.Fatalf("%s ran after a dispatch that ran no tool: %+v", node, invs)
		}
	}
	run, err := h.store.Workflows().GetWorkflowRun(res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(run.StateJSON, "dispatch_results") {
		t.Fatalf("the report reached state: %s", run.StateJSON)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "workspace", offlineProject, "final-synthesis.md")); !os.IsNotExist(err) {
		t.Fatalf("final-synthesis.md exists after a failed run (%v)", err)
	}
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	if got := len(h.fake.calls["dispatch"]); got != 2 {
		t.Fatalf("dispatch calls = %d, want 2: the answer and one repair, and no retry", got)
	}
	gapRef := `"gap_id":` + strconv.FormatInt(h.gapID, 10)
	repair := h.fake.calls["dispatch"][1]
	if !strings.Contains(repair.prompt, dispatchRepairMarker) || !strings.Contains(repair.prompt, "tool call") ||
		!strings.Contains(repair.prompt, wellFormedHiveReplies["dispatch"]) || !strings.Contains(repair.prompt, gapRef) || !repair.offeredTools {
		t.Fatalf("dispatch repair = %+v, want the shortfall named, the answer, the plan with %s and tools offered", repair, gapRef)
	}
}

// A dispatch that runs no tool and whose repair then does the work: the
// repair's tool call counts for the node, the node completes with the
// repair's report, and the pass goes on to its end. This is the live
// failure of 2026-09-30, repaired.
func TestHiveWorkflow_ADispatchThatDoesTheWorkOnItsRepair(t *testing.T) {
	const iterations = 2
	h := newOfflineHive(t, wellFormedHiveReplies)
	h.fake.readFirst = false
	h.fake.readOnRepair = true
	res, err := h.run(t, iterations)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if status := h.nodeStatus(t, res.RunID, "dispatch"); status != "completed" {
		t.Fatalf("dispatch = %s, want completed", status)
	}
	if got := h.iteration(t); got != iterations {
		t.Fatalf("iteration = %d, want %d", got, iterations)
	}
	if invs := commandInvocations(t, h.store, res.RunID, "complete-iteration"); len(invs) != iterations {
		t.Fatalf("complete-iteration ran %d time(s), want %d", len(invs), iterations)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "workspace", offlineProject, "final-synthesis.md")); err != nil {
		t.Fatalf("final-synthesis.md: %v", err)
	}
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	// Each pass: the answer with no tool call, the repair's tool turn and
	// the repair's answer.
	calls := h.fake.calls["dispatch"]
	if len(calls) != 3*iterations {
		t.Fatalf("dispatch calls = %d, want %d", len(calls), 3*iterations)
	}
	reads := 0
	for _, c := range calls {
		if strings.Contains(c.toolResult, strings.TrimSpace(fanNote)) {
			reads++
		}
	}
	var rows int
	if err := h.store.ReadDB.QueryRow(
		`SELECT COUNT(*) FROM tool_invocations WHERE run_id=? AND node_name='dispatch' AND tool='read_file'`, res.RunID,
	).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if reads == 0 || rows != reads {
		t.Fatalf("%d read_file rows on dispatch, want the repairs' %d", rows, reads)
	}
	var passed, repairs int
	if err := h.store.ReadDB.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(accept_passed),0) FROM workflow_repairs WHERE run_id=? AND node_name='dispatch'`, res.RunID,
	).Scan(&repairs, &passed); err != nil {
		t.Fatal(err)
	}
	if repairs != iterations || passed != iterations {
		t.Fatalf("%d dispatch repairs, %d passed, want %d of each", repairs, passed, iterations)
	}
}

// A hive whose gate stays blocked stops on the stall rule, not at its cap.
// With no critical gap open, each scan asks for the gate: chb validates the
// wave's sources, the evaluator scores it and names a gap, and `guard
// --eval-stdin` derives NEEDS_MORE_WORK, records the gap and keeps the gate
// shut, exiting 1, which the gate node takes as an answer. The next passes
// research that gap and gate again, and the evaluator names it again: it is
// not written twice, and the repeated verdict is no progress. So the two
// passes after the first change nothing, and the loop stops at pass 3.
func TestHiveWorkflow_ABlockedGateStalls(t *testing.T) {
	const gap = "Which release set the fan's worker count?"
	eval, _ := json.Marshal(map[string]any{"gaps": []string{gap}, "coverage": 5, "depth": 5, "sources": 5, "actionability": 5})
	replies := map[string]string{"evaluate": string(eval)}
	for k, v := range wellFormedHiveReplies {
		replies[k] = v
	}
	h := newOfflineHive(t, replies)
	if _, err := h.store.WriteDB.Exec(`UPDATE gaps SET resolved_by_wave = 1`); err != nil {
		t.Fatal(err)
	}
	zero := 0
	if _, err := h.store.Findings().AddFinding(&db.Finding{Wave: 1, Agent: "seed", MSSLabel: "assumption",
		Finding: "The fan runs 4 workers at once.", D1: &zero, D2: &zero, D3: &zero, D4: &zero}); err != nil {
		t.Fatal(err)
	}
	const limit, stallAt = 5, 3
	res, err := h.run(t, limit)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	run, err := h.store.Workflows().GetWorkflowRun(res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal([]byte(run.StateJSON), &st); err != nil {
		t.Fatal(err)
	}
	if got := h.iteration(t); got != stallAt {
		t.Fatalf("iteration = %d, want %d: one pass that changed the state, then two that did not", got, stallAt)
	}
	if why, _ := st["stop_reason"].(string); !strings.HasPrefix(why, "stalled") {
		t.Fatalf("stop_reason = %q, want the stall", why)
	}
	for _, node := range []string{"validate-sources", "gate-state", "gate"} {
		if invs := commandInvocations(t, h.store, res.RunID, node); len(invs) != stallAt {
			t.Fatalf("%s ran %d time(s), want once per pass", node, len(invs))
		}
	}
	for _, inv := range commandInvocations(t, h.store, res.RunID, "gate") {
		var out map[string]any
		if err := json.Unmarshal([]byte(inv.stdout), &out); err != nil {
			t.Fatalf("gate printed %q: %v", inv.stdout, err)
		}
		if inv.exitCode != 1 || out["opened"] != false || out["verdict"] != "NEEDS_MORE_WORK" {
			t.Fatalf("gate = exit %d, %v; want exit 1, shut, NEEDS_MORE_WORK", inv.exitCode, out)
		}
	}
	var gaps, evals int
	if err := h.store.ReadDB.QueryRow(`SELECT COUNT(*) FROM gaps WHERE description = ? AND resolved_by_wave IS NULL`, gap).Scan(&gaps); err != nil {
		t.Fatal(err)
	}
	if err := h.store.ReadDB.QueryRow(`SELECT COUNT(*) FROM evaluations WHERE wave = 1 AND verdict = 'NEEDS_MORE_WORK'`).Scan(&evals); err != nil {
		t.Fatal(err)
	}
	if gaps != 1 || evals != stallAt {
		t.Fatalf("%d open copies of the evaluator's gap and %d evaluations, want 1 and %d", gaps, evals, stallAt)
	}
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	// The evaluator's gap is researched from the second pass on: two
	// dispatches, each a read and an answer.
	if got := len(h.fake.calls["dispatch"]); got != 2*(stallAt-1) {
		t.Fatalf("dispatch calls = %d, want %d", got, 2*(stallAt-1))
	}
	if got := len(h.fake.calls["evaluate"]); got != stallAt {
		t.Fatalf("evaluate calls = %d, want %d", got, stallAt)
	}
	for i, c := range h.fake.calls["evaluate"] {
		if c.offeredTools || !c.withSchema {
			t.Fatalf("evaluate call %d = %+v, want the schema and no tools", i+1, c)
		}
	}
}

type offlineInvocation struct {
	argv     []string
	exitCode int
	stdout   string
}

func commandInvocations(t *testing.T, s *db.Store, runID int64, node string) []offlineInvocation {
	t.Helper()
	rows, err := s.ReadDB.Query(
		`SELECT input, output FROM tool_invocations WHERE run_id=? AND node_name=? AND tool='command' ORDER BY id`,
		runID, node)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []offlineInvocation
	for rows.Next() {
		var input, output string
		if err := rows.Scan(&input, &output); err != nil {
			t.Fatal(err)
		}
		var inv offlineInvocation
		var o struct {
			ExitCode int    `json:"exit_code"`
			Stdout   string `json:"stdout"`
		}
		if err := json.Unmarshal([]byte(input), &inv.argv); err != nil {
			t.Fatalf("%s input %q: %v", node, input, err)
		}
		if err := json.Unmarshal([]byte(output), &o); err != nil {
			t.Fatalf("%s output %q: %v", node, output, err)
		}
		inv.exitCode, inv.stdout = o.ExitCode, o.Stdout
		out = append(out, inv)
	}
	return out
}

func nodeTokens(t *testing.T, s *db.Store, runID int64, node string) (in, out int64) {
	t.Helper()
	if err := s.ReadDB.QueryRow(
		`SELECT tokens_in, tokens_out FROM workflow_node_states WHERE run_id=? AND node_name=?`, runID, node,
	).Scan(&in, &out); err != nil {
		t.Fatalf("%s: %v", node, err)
	}
	return in, out
}
