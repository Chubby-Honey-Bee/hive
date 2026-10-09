package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Chubby-Honey-Bee/hive/internal/design"
)

// designPolicy is what the fake makes of one task on one arm: the files
// its plan tells the executor to write (none: the executor changes
// nothing), the deviations and additions the executor then reports, and
// whether the design's claims hold a guarantee with no premise.
type designPolicy struct {
	files                 map[string]string
	deviations, additions int
	dishonest             bool
}

// designFake is an OpenAI-compatible server for the design case. Lenses
// and Queen answer with fixed verdicts; the plan call answers with a
// document whose plan writes the policy's files; the executor lists its
// tree, writes those files through write_file, and reports what the
// policy says. It records the plan prompts by arm, the executor's prompts
// and the tree listings the executor saw.
type designFake struct {
	tasks  []design.Task
	policy func(task, arm string) designPolicy
	served []string
	// downAfterRuns takes the server down once it has answered that many
	// runs' last calls, the executor's report: the listener closes and
	// later requests are dropped.
	downAfterRuns int
	listener      net.Listener

	mu          sync.Mutex
	planPrompts map[string]map[string]string // arm → task → prompt
	execPrompts []string
	listings    []string
	reports     int
	down        bool
	unmatched   int
	lensCalls   int
}

var (
	fakeDesignRe = regexp.MustCompile(`FAKE-DESIGN task=(\S+) arm=(\S+) deviations=(\d+) additions=(\d+)`)
	fenceRe      = regexp.MustCompile("(?s)```\n(.*?)\n```")
)

func (f *designFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	down := f.down
	f.mu.Unlock()
	if down {
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			_ = conn.Close()
		}
		return
	}
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
		data := []map[string]string{}
		for _, id := range f.served {
			data = append(data, map[string]string{"id": id, "object": "model"})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
		return
	}
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		ResponseFormat struct {
			JSONSchema struct {
				Name string `json:"name"`
			} `json:"json_schema"`
		} `json:"response_format"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	reply := func(msg map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"finish_reason": "stop", "message": msg}},
			"usage":   map[string]any{"prompt_tokens": 20, "completion_tokens": 10},
		})
	}
	if body.ResponseFormat.JSONSchema.Name == "hive_constraint_probe" {
		reply(map[string]any{"role": "assistant", "content": "It is sunny."})
		return
	}
	var prompt string
	var toolResults []string
	for _, m := range body.Messages {
		switch m.Role {
		case "user":
			prompt += m.Content
		case "tool":
			toolResults = append(toolResults, m.Content)
		}
	}
	switch {
	case foragerKey.MatchString(prompt):
		f.mu.Lock()
		f.lensCalls++
		f.mu.Unlock()
		reply(map[string]any{"role": "assistant", "content": mustJSON(map[string]any{
			"forager": foragerKey.FindStringSubmatch(prompt)[1], "verdict": "conditional",
			"key_points": []string{"keep the public API the statement fixes"}, "evidence": []string{"the pack"},
			"uncertainties": []string{"none"}, "recommendation": "Implement the statement's contract.",
		})})
	case strings.Contains(prompt, "Forager verdicts"):
		reply(map[string]any{"role": "assistant", "content": mustJSON(map[string]any{
			"report":  "## Swarm Verdict: design\n\nRESEARCH-MARK the lenses agree: keep the API the statement fixes.",
			"verdict": "conditional", "convergence": "high", "recommendation": "Follow the statement.", "coverage": 3, "gaps": []string{},
			"dissent_from_plurality": "none",
		})})
	case strings.Contains(prompt, "design document and an execution plan"):
		f.planCall(prompt, reply)
	case strings.Contains(prompt, "Carry out this plan"):
		f.executorCall(prompt, toolResults, reply)
	default:
		f.mu.Lock()
		f.unmatched++
		f.mu.Unlock()
		http.Error(w, "the fake does not know this prompt", 400)
	}
}

// planCall answers the plan prompt: the task is the one whose statement
// the pack holds, the arm is told by the research block.
func (f *designFake) planCall(prompt string, reply func(map[string]any)) {
	arm := design.ArmControl
	if strings.Contains(prompt, "## Research") {
		arm = design.ArmDesigner
	}
	var task design.Task
	for _, t := range f.tasks {
		if strings.Contains(prompt, t.Statement) {
			task = t
		}
	}
	f.mu.Lock()
	if f.planPrompts == nil {
		f.planPrompts = map[string]map[string]string{}
	}
	if f.planPrompts[arm] == nil {
		f.planPrompts[arm] = map[string]string{}
	}
	f.planPrompts[arm][task.Name] = prompt
	f.mu.Unlock()
	p := f.policy(task.Name, arm)
	var plan []map[string]any
	paths := make([]string, 0, len(p.files))
	for path := range p.files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for i, path := range paths {
		plan = append(plan, map[string]any{"step": i + 1, "action": "Write " + path + " with exactly this content:\n```\n" + p.files[path] + "\n```", "files": []string{path}})
	}
	if len(plan) == 0 {
		plan = append(plan, map[string]any{"step": 1, "action": "Read the tree and leave it as it is.", "files": []string{}})
	}
	claims := []map[string]any{
		{"label": "definition", "claim": "The public API is the statement's.", "rests_on": []int{}},
		{"label": "assumption", "claim": "The hidden tests call that API.", "rests_on": []int{}},
		{"label": "guarantee", "claim": "The plan builds.", "rests_on": []int{0, 1}},
	}
	if p.dishonest {
		claims[2]["rests_on"] = []int{}
	}
	reply(map[string]any{"role": "assistant", "content": mustJSON(map[string]any{
		"design": fmt.Sprintf("FAKE-DESIGN task=%s arm=%s deviations=%d additions=%d", task.Name, arm, p.deviations, p.additions),
		"claims": claims, "plan": plan,
	})})
}

// executorCall drives the executor's turns: first list the tree, then
// write every file the plan carries, then report.
func (f *designFake) executorCall(prompt string, toolResults []string, reply func(map[string]any)) {
	f.mu.Lock()
	if len(toolResults) == 0 {
		f.execPrompts = append(f.execPrompts, prompt)
	}
	if len(toolResults) == 1 {
		f.listings = append(f.listings, toolResults[0])
	}
	f.mu.Unlock()
	switch len(toolResults) {
	case 0:
		reply(map[string]any{"role": "assistant", "content": "", "tool_calls": []map[string]any{{"id": "call_ls", "type": "function",
			"function": map[string]any{"name": "shell", "arguments": mustJSON(map[string]string{"command": "find . -type f | sort; pwd"})}}}})
	case 1:
		var calls []map[string]any
		for i, step := range planSteps(prompt) {
			m := fenceRe.FindStringSubmatch(step.Action)
			if m == nil || len(step.Files) == 0 {
				continue
			}
			calls = append(calls, map[string]any{"id": fmt.Sprintf("call_w%d", i), "type": "function",
				"function": map[string]any{"name": "write_file", "arguments": mustJSON(map[string]string{"path": step.Files[0], "content": m[1]})}})
		}
		if len(calls) == 0 {
			calls = append(calls, map[string]any{"id": "call_noop", "type": "function",
				"function": map[string]any{"name": "shell", "arguments": mustJSON(map[string]string{"command": "true"})}})
		}
		reply(map[string]any{"role": "assistant", "content": "", "tool_calls": calls})
	default:
		m := fakeDesignRe.FindStringSubmatch(prompt)
		dev, add := 0, 0
		if m != nil {
			fmt.Sscan(m[3], &dev)
			fmt.Sscan(m[4], &add)
		}
		var deviations []map[string]any
		for i := 0; i < dev; i++ {
			deviations = append(deviations, map[string]any{"step": i + 1, "why": "done otherwise"})
		}
		additions := []string{}
		for i := 0; i < add; i++ {
			additions = append(additions, fmt.Sprintf("extra %d", i+1))
		}
		if deviations == nil {
			deviations = []map[string]any{}
		}
		// Bare JSON: prose around it would break the schema and cost a
		// finalize call, which an outage test must not have to survive.
		reply(map[string]any{"role": "assistant", "content": mustJSON(map[string]any{
			"steps_done": []int{1}, "deviations": deviations, "additions": additions, "summary": "the fake executor wrote the plan's files",
		})})
		f.mu.Lock()
		f.reports++
		if f.downAfterRuns > 0 && f.reports >= f.downAfterRuns {
			f.down = true
			_ = f.listener.Close()
		}
		f.mu.Unlock()
	}
}

// planSteps reads the plan JSON out of the executor's prompt.
func planSteps(prompt string) []design.Step {
	_, rest, ok := strings.Cut(prompt, "## Plan\n\n")
	if !ok {
		return nil
	}
	raw, _, _ := strings.Cut(rest, "\n\nEnd your answer")
	var steps []design.Step
	_ = json.Unmarshal([]byte(raw), &steps)
	return steps
}

// referenceFiles reads a task's reference solution as path → content.
func referenceFiles(t *testing.T, task design.Task) map[string]string {
	t.Helper()
	files, err := design.TreeFiles(task.Reference())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(task.Reference(), filepath.FromSlash(f)))
		if err != nil {
			t.Fatal(err)
		}
		out[f] = string(b)
	}
	return out
}

// expectedGrade recomputes what the hidden tests say of a tree with files
// written over it, by go test -v and a count of its top-level PASS lines,
// independent of the harness's -json reader; the total is the count of
// TestHidden functions in the hidden files.
func expectedGrade(t *testing.T, task design.Task, files map[string]string) (passed, total int) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "tree")
	if err := design.CopyTree(task, dir); err != nil {
		t.Fatal(err)
	}
	for path, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hidden, err := design.TreeFiles(task.Hidden())
	if err != nil {
		t.Fatal(err)
	}
	funcRe := regexp.MustCompile(`(?m)^func (TestHidden\w*)\(`)
	for _, h := range hidden {
		b, err := os.ReadFile(filepath.Join(task.Hidden(), filepath.FromSlash(h)))
		if err != nil {
			t.Fatal(err)
		}
		total += len(funcRe.FindAllIndex(b, -1))
		p := filepath.Join(dir, filepath.FromSlash(h))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "test", "-v", "-count=1", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=")
	out, _ := cmd.CombinedOutput()
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "--- PASS: TestHidden") {
			passed++
		}
	}
	return passed, total
}

// designHarness points a harness at the fake and a one-case design suite
// over the named shipped tasks, running chb from the repository root.
func designHarness(t *testing.T, chb string, fake *designFake, taskNames []string) (*agentHarness, string) {
	t.Helper()
	lensModel, queenModel := "fake-lens", "fake-queen"
	fake.served = []string{lensModel, queenModel}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	fake.listener = srv.Listener
	t.Chdir(filepath.Join("..", ".."))
	// The default database is under the working directory, the checkout
	// here; a test keeps its own.
	t.Setenv("HIVE_DB_PATH", filepath.Join(t.TempDir(), "hive.db"))
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "fake-key-for-the-test-server")
	t.Setenv("HIVE_RPM_OPENAI", "600000")
	t.Setenv("HIVE_FORAGERS_DIR", "")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	t.Setenv("HIVE_PROFILE", "")
	tasks, err := design.LoadTasks(defaultTasksRoot, taskNames)
	if err != nil {
		t.Fatal(err)
	}
	fake.tasks = tasks

	c := harnessCase{Name: "design", Kind: "design", Foragers: "minimal", Tasks: defaultTasksRoot, TaskNames: taskNames, ExecutorMinutes: 3}
	raw, err := yaml.Marshal(harnessSuite{Cases: []harnessCase{c}})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	suite := filepath.Join(dir, "suite.yaml")
	if err := os.WriteFile(suite, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { devnull.Close() })
	ws := filepath.Join(dir, "ws")
	return &agentHarness{Options: Options{
		Self: chb, Stdout: devnull, Suite: suite, Workspace: ws, BudgetMode: "cheap", Model: "haiku",
		KeepGoing: true, Provider: "openai", LensModel: lensModel, QueenModel: queenModel, Reps: 1, Config: "fake",
		Foragers: resolveForagers, LoadForagers: loadForagers,
	}}, filepath.Join(ws, "design", "results.jsonl")
}

func readDesignResults(t *testing.T, path string) []design.Row {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := design.ReadRows(f)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func rowOf(rows []design.Row, task, arm string) (design.Row, bool) {
	for _, r := range rows {
		if r.Task == task && r.Arm == arm {
			return r, true
		}
	}
	return design.Row{}, false
}

// The design case end to end: the real harness drives chb ask and two
// agent-runs per task and arm against the fake, whose executor applies the
// plan's files; the rows are then checked against an independent go test
// of the same edits, the fake's own fidelity counts, and the audit.
func TestDesignCase_AgainstAFakeModel(t *testing.T) {
	chb := buildChb(t, t.TempDir())
	t.Run("rows graded from the executor's edits", func(t *testing.T) { designRowsGraded(t, chb) })
	t.Run("an endpoint that goes down mid-case", func(t *testing.T) { designOutage(t, chb) })
}

func designRowsGraded(t *testing.T, chb string) {
	names := []string{"intervals", "slug-ascii", "slug-unicode"}
	fake := &designFake{}
	h, results := designHarness(t, chb, fake, names)
	byName := map[string]design.Task{}
	for _, task := range fake.tasks {
		byName[task.Name] = task
	}
	// The designer writes each task's reference; the control writes nothing
	// on slug-ascii, the twin's solution on slug-unicode, and the reference
	// on intervals, where the designer reports a deviation and an addition
	// and the control's design carries a guarantee with no premise.
	policies := map[string]map[string]designPolicy{
		"slug-ascii":   {design.ArmDesigner: {files: referenceFiles(t, byName["slug-ascii"])}, design.ArmControl: {}},
		"slug-unicode": {design.ArmDesigner: {files: referenceFiles(t, byName["slug-unicode"])}, design.ArmControl: {files: referenceFiles(t, byName["slug-ascii"])}},
		"intervals":    {design.ArmDesigner: {files: referenceFiles(t, byName["intervals"]), deviations: 1, additions: 1}, design.ArmControl: {files: referenceFiles(t, byName["intervals"]), dishonest: true}},
	}
	fake.policy = func(task, arm string) designPolicy { return policies[task][arm] }
	if err := h.run(context.Background()); err != nil {
		var failed []string
		for name, ck := range caseChecks(t, h.Workspace) {
			if !ck.OK {
				failed = append(failed, name+": "+ck.Detail)
			}
		}
		t.Fatalf("the case failed: %v\n%s", err, strings.Join(failed, "\n"))
	}
	if fake.unmatched > 0 {
		t.Errorf("%d prompts the fake did not know", fake.unmatched)
	}
	rows := readDesignResults(t, results)
	if len(rows) != 6 {
		t.Fatalf("%d rows, want 6", len(rows))
	}
	for _, name := range names {
		for _, arm := range []string{design.ArmDesigner, design.ArmControl} {
			row, ok := rowOf(rows, name, arm)
			if !ok {
				t.Errorf("no row for %s %s", name, arm)
				continue
			}
			p := policies[name][arm]
			passed, total := expectedGrade(t, byName[name], p.files)
			if row.TestsPassed != passed || row.TestsTotal != total || !row.Completed || !row.Designed || !row.Executed {
				t.Errorf("%s %s: %d of %d, completed %v designed %v executed %v; want %d of %d, all true (error %q)", name, arm, row.TestsPassed, row.TestsTotal, row.Completed, row.Designed, row.Executed, passed, total, row.Error)
			}
			steps := len(p.files)
			if steps == 0 {
				steps = 1
			}
			wantRate := float64(p.deviations+p.additions) / float64(steps)
			if row.Steps != steps || row.Deviations != p.deviations || row.Additions != p.additions || row.Fidelity.Rate == nil || *row.Fidelity.Rate != wantRate {
				t.Errorf("%s %s: fidelity %+v (rate %v); want steps %d, deviations %d, additions %d, rate %v", name, arm, row.Fidelity, row.Fidelity.Rate, steps, p.deviations, p.additions, wantRate)
			}
			if len(p.files) > 0 {
				if row.Changed != len(p.files) || row.Named != len(p.files) || row.Coverage.Rate == nil || *row.Coverage.Rate != 1 {
					t.Errorf("%s %s: coverage %+v; want every one of %d changed files named", name, arm, row.Coverage, len(p.files))
				}
			} else if row.Changed != 0 || row.Coverage.Rate != nil {
				t.Errorf("%s %s: coverage %+v with nothing written", name, arm, row.Coverage)
			}
			refs, err := design.ReferenceChanges(byName[name])
			if err != nil {
				t.Fatal(err)
			}
			wantNamed := 0
			if len(p.files) > 0 {
				wantNamed = len(refs) // the plan names the files it writes, the reference's
			}
			if row.RefFiles != len(refs) || row.RefNamed != wantNamed || row.Completeness.Rate == nil || *row.Completeness.Rate != float64(wantNamed)/float64(len(refs)) {
				t.Errorf("%s %s: completeness %+v (rate %v); want %d of %d reference files named", name, arm, row.Completeness, row.Completeness.Rate, wantNamed, len(refs))
			}
			wantPremises := 1
			if p.dishonest {
				wantPremises = 0
			}
			if row.Claims != 3 || row.Labelled != 3 || row.Guarantees != 1 || row.GuaranteesWithPremises != wantPremises || row.AuditPass == p.dishonest {
				t.Errorf("%s %s: labels %+v; want 3 claims labelled, 1 guarantee with %d premises, audit pass %v", name, arm, row.Labels, wantPremises, !p.dishonest)
			}
			if !row.TreeOK || row.TokensIn == 0 || len(row.Nodes) == 0 || row.Family != byName[name].Family || row.Twin != byName[name].Twin {
				t.Errorf("%s %s: tree_ok %v tokens %d nodes %d family %q twin %q", name, arm, row.TreeOK, row.TokensIn, len(row.Nodes), row.Family, row.Twin)
			}
			hasSwarm := false
			for _, n := range row.Nodes {
				hasSwarm = hasSwarm || strings.HasPrefix(n.Node, "swarm/")
			}
			if hasSwarm != (arm == design.ArmDesigner) {
				t.Errorf("%s %s: swarm nodes recorded %v", name, arm, hasSwarm)
			}
		}
	}
	// The decision follows from the rows: the designer wins the two slug
	// tasks on tests and ties intervals; its plan names the reference file
	// on slug-ascii where the control's names none, so completeness is not
	// below; the verdict is worth it. Fidelity, reported only, is below:
	// the designer recorded deviations on intervals. The cost ratio is the
	// rows' tokens.
	d := design.Decide(rows, design.DefaultAlpha)
	var desTokens, conTokens int64
	for _, r := range rows {
		if r.Arm == design.ArmDesigner {
			desTokens += r.TokensIn + r.TokensOut
		} else {
			conTokens += r.TokensIn + r.TokensOut
		}
	}
	if d.Tests.Outcome != design.NotBelow || d.Tests.Wins != 2 || d.Tests.Losses != 0 || d.Completeness.Outcome != design.NotBelow || d.Completeness.Wins != 1 ||
		d.Fidelity.Outcome != design.Below || d.Fidelity.Losses != 1 || d.Verdict != design.VerdictWorthIt || d.CostRatio == nil || *d.CostRatio != float64(desTokens)/float64(conTokens) {
		t.Errorf("decision tests %+v completeness %+v fidelity %+v verdict %s ratio %v", d.Tests, d.Completeness, d.Fidelity, d.Verdict, d.CostRatio)
	}
	report, err := os.ReadFile(filepath.Join(h.Workspace, "REPORT.md"))
	if err != nil || !strings.Contains(string(report), "**Verdict: "+d.Verdict+"**") || !strings.Contains(string(report), fmt.Sprintf("token cost designer ÷ control = %.2f×", *d.CostRatio)) {
		t.Errorf("REPORT.md lacks the verdict or the cost ratio: %v\n%s", err, report)
	}

	// The executor saw the tree alone: no hidden test, note, reference, nor
	// any file of the designer's run; and its prompt carried neither the
	// statement nor the research.
	if len(fake.listings) != 6 {
		t.Fatalf("%d tree listings, want one per run", len(fake.listings))
	}
	for _, l := range fake.listings {
		for _, leak := range []string{"hidden", "note.md", "reference", "_test.go", "design.json", "swarm.db", "artifact.json", "pack.md"} {
			if strings.Contains(l, leak) {
				t.Errorf("the executor's tree holds %q:\n%s", leak, l)
			}
		}
		if !strings.Contains(l, "./go.mod") {
			t.Errorf("the executor's tree lacks go.mod:\n%s", l)
		}
	}
	for _, p := range fake.execPrompts {
		if strings.Contains(p, "RESEARCH-MARK") || strings.Contains(p, "# Fix Merge") || strings.Contains(p, "# Implement Slugify") {
			t.Errorf("the executor's prompt carries the research or the statement:\n%s", clip(p, 400))
		}
		if !strings.Contains(p, "FAKE-DESIGN") || !strings.Contains(p, "## Plan") {
			t.Errorf("the executor's prompt lacks the design or the plan:\n%s", clip(p, 400))
		}
	}
	// The arms' inputs: the designer's plan prompt with its research block
	// removed is the control's.
	for _, name := range names {
		des, con := fake.planPrompts[design.ArmDesigner][name], fake.planPrompts[design.ArmControl][name]
		if des == "" || con == "" {
			t.Errorf("%s: plan prompts missing (designer %d bytes, control %d bytes)", name, len(des), len(con))
			continue
		}
		i, j := strings.Index(des, "\n## Research\n"), strings.Index(des, "\n## What to return")
		if i < 0 || j < i || !strings.Contains(des[i:j], "RESEARCH-MARK") {
			t.Errorf("%s: the designer's prompt holds no research block with the swarm's report", name)
			continue
		}
		if stripped := des[:i] + des[j:]; stripped != con {
			t.Errorf("%s: the arms' prompts differ beyond the research block:\n--- designer minus research\n%s\n--- control\n%s", name, clip(stripped, 600), clip(con, 600))
		}
		if !strings.Contains(con, byName[name].Statement) || strings.Contains(con, byName[name].Note) {
			t.Errorf("%s: the control's prompt lacks the statement or leaks the note", name)
		}
	}
	// Every run's directory is in the workspace with its artifacts.
	for _, name := range names {
		for _, arm := range []string{design.ArmDesigner, design.ArmControl} {
			for _, f := range []string{"design.json", "go-test.log", "exec", "graded"} {
				if _, err := os.Stat(filepath.Join(h.Workspace, "design", name, arm, f)); err != nil {
					t.Errorf("%s %s: %s missing from the workspace: %v", name, arm, f, err)
				}
			}
		}
	}
}

// A server that goes down after it has answered the first task's two runs
// stops the case at the second task's first run, whose swarm fails its
// model preflight and reaches no model; the rows so far are written marked
// as not a measurement, which chb design report refuses.
func designOutage(t *testing.T, chb string) {
	names := []string{"intervals", "retry"}
	fake := &designFake{downAfterRuns: 2}
	h, results := designHarness(t, chb, fake, names)
	ref := referenceFiles(t, fake.tasks[0])
	fake.policy = func(string, string) designPolicy { return designPolicy{files: ref} }
	if err := h.run(context.Background()); err == nil {
		t.Fatal("the case passed with the endpoint down after its first task")
	}
	checks := caseChecks(t, h.Workspace)
	stop := checks[stopCheck]
	head := "stopped at run 3 of 4 (retry designer, swarm stage), which reached no model"
	if stop.OK || !strings.HasPrefix(stop.Detail, head) || !strings.Contains(detailSuffix(stop.Detail), "connection refused") {
		t.Errorf("stop check %+v; want it failed, starting %q, naming the refused connection", stop, head)
	}
	raw, err := os.ReadFile(results)
	if err != nil {
		t.Fatal(err)
	}
	_, err = design.ReadRows(strings.NewReader(string(raw)))
	if !errors.Is(err, design.ErrNotMeasured) || !strings.Contains(err.Error(), head) {
		t.Errorf("ReadRows = %v; want it refused as not a measurement, saying %q", err, head)
	}
	if n := strings.Count(strings.TrimSpace(string(raw)), "\n"); n != 3 {
		t.Errorf("results.jsonl has %d lines after the mark, want 3 rows", n)
	}
	for _, run := range []string{"intervals/designer", "intervals/control", "retry/designer"} {
		if _, err := os.Stat(filepath.Join(h.Workspace, "design", run)); err != nil {
			t.Errorf("%s did not run: %v", run, err)
		}
	}
	if _, err := os.Stat(filepath.Join(h.Workspace, "design", "retry", "control")); err == nil {
		t.Error("retry control ran after the stop")
	}
	out, err := exec.Command(chb, "design", "report", results).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "not a measurement") {
		t.Errorf("chb design report accepted the file: %v\n%s", err, out)
	}
}

// The plan and executor workflows are held to their terms: the plan node
// has no tools, the document schema and one repair; the executor runs as
// the coder with the shell and the file tools, the report schema, one
// repair and at least one tool call; the two plan workflows differ only in
// the research block.
func TestDesignWorkflows_HeldToTheirTerms(t *testing.T) {
	for _, withResearch := range []bool{false, true} {
		var wf struct {
			Nodes map[string]struct {
				Tools        []string       `yaml:"tools"`
				Role         string         `yaml:"role"`
				Model        string         `yaml:"model"`
				Agent        string         `yaml:"agent"`
				Prompt       string         `yaml:"prompt"`
				MinToolCalls int            `yaml:"min_tool_calls"`
				Schema       map[string]any `yaml:"output_schema"`
				OnReject     map[string]any `yaml:"on_reject"`
			} `yaml:"nodes"`
		}
		if err := yaml.Unmarshal([]byte(planWorkflow("m", "none", withResearch)), &wf); err != nil {
			t.Fatal(err)
		}
		n := wf.Nodes["plan"]
		if n.Tools == nil || len(n.Tools) != 0 || n.Role != "implement-plan" || n.Model != "m" || n.OnReject["max_repair_iterations"] != 1 {
			t.Errorf("plan node %+v", n)
		}
		req, _ := n.Schema["required"].([]any)
		if len(req) != 3 || strings.Contains(n.Prompt, "## Research") != withResearch {
			t.Errorf("plan schema required %v, research %v", req, strings.Contains(n.Prompt, "## Research"))
		}
		if err := yaml.Unmarshal([]byte(executorWorkflow("m", "")), &wf); err != nil {
			t.Fatal(err)
		}
		e := wf.Nodes["execute"]
		if e.Agent != "coder" || e.Role != "implement-fix" || e.MinToolCalls != 1 || strings.Join(e.Tools, ",") != "shell,read_file,write_file,edit_file,glob,grep" || e.OnReject["max_repair_iterations"] != 1 {
			t.Errorf("executor node %+v", e)
		}
		if strings.Contains(e.Prompt, "{pack}") || strings.Contains(e.Prompt, "{research}") || !strings.Contains(e.Prompt, "{design}") || !strings.Contains(e.Prompt, "{plan}") {
			t.Errorf("executor prompt placeholders wrong:\n%s", e.Prompt)
		}
	}
	a, b := planWorkflow("m", "", false), planWorkflow("m", "", true)
	i, j := strings.Index(b, "\n      ## Research"), strings.Index(b, "\n      ## What to return")
	if i < 0 || j < i || b[:i]+b[j:] != a {
		t.Error("the two plan workflows differ beyond the research block")
	}
}
