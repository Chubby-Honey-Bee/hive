package runner

// Tests for a min_tool_calls shortfall sent back to the node's on_reject:
// repair: a repair that does the work completes the node with its tool
// calls counted, one that still does nothing is rejected with the reason in
// its prompt, the dispatch's and the attempts' calls count together, a
// node with no repair block fails and keeps the reply, and a backend that
// reports no tool calls is not checked. Every server is an httptest fake;
// no model is called.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// shortRepairYAML is a node that must make one tool call, with one repair
// whose template shows the reason, the answer and the task.
const shortRepairYAML = `name: short
nodes:
  work:
    type: agent
    model: local-model
    prompt: "do the work"
    outputs: [said]
    min_tool_calls: 1
    on_reject:
      max_repair_iterations: 1
      prompt_template: "Sent back: {accept_failure}\nYou said: {outputs}\nThe task: {original_prompt}"
`

// shortRepairMarker opens the repair prompt of shortRepairYAML.
const shortRepairMarker = "Sent back:"

// firstUserMessage is the first user message of a captured chat body.
func firstUserMessage(body map[string]any) string {
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		msg, _ := m.(map[string]any)
		if msg["role"] == "user" {
			s, _ := msg["content"].(string)
			return s
		}
	}
	return ""
}

// carriesToolResult reports whether a captured chat body holds a tool
// message: the turn after a tool call.
func carriesToolResult(body map[string]any) bool {
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		msg, _ := m.(map[string]any)
		if msg["role"] == "tool" {
			return true
		}
	}
	return false
}

// readFileReply renders a one-choice chat completion that calls read_file
// on path.
func readFileReply(path string, in, out int) string {
	args, _ := json.Marshal(map[string]string{"path": path})
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{
			"content": "",
			"tool_calls": []any{map[string]any{
				"id": "call_1", "type": "function",
				"function": map[string]any{"name": "read_file", "arguments": string(args)},
			}},
		}}},
		"usage": map[string]any{"prompt_tokens": in, "completion_tokens": out},
	})
	return string(b)
}

// shortReason is the plain-words reason the runner gives a node whose calls
// made fewer tool calls than its minimum, checked to name both counts.
func shortReason(t *testing.T, made, minimum int) string {
	t.Helper()
	reason := toolCallsShort(workflow.DispatchNode{Node: "work", MinToolCalls: minimum}, made, nil).Reason
	if !strings.Contains(reason, fmt.Sprintf("%d tool call", made)) || !strings.Contains(reason, fmt.Sprintf("at least %d", minimum)) {
		t.Fatalf("reason = %q, want it to name %d made and at least %d", reason, made, minimum)
	}
	return reason
}

// nodeToolRows counts the node's tool_invocations rows for one tool.
func nodeToolRows(t *testing.T, store *db.Store, runID int64, node, tool string) int {
	t.Helper()
	var n int
	if err := store.ReadDB.QueryRow(
		`SELECT COUNT(*) FROM tool_invocations WHERE run_id=? AND node_name=? AND tool=?`, runID, node, tool,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// repairRow is the one workflow_repairs row of a node, or fails when the
// node has none or several.
type repairRow struct {
	prompt, failure, outputs string
	passed                   int
}

func readRepairRows(t *testing.T, store *db.Store, runID int64, node string) []repairRow {
	t.Helper()
	rows, err := store.ReadDB.Query(
		`SELECT repair_prompt, failure_reason, COALESCE(repair_outputs_json,''), COALESCE(accept_passed,0)
		 FROM workflow_repairs WHERE run_id=? AND node_name=? ORDER BY attempt`, runID, node)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []repairRow
	for rows.Next() {
		var r repairRow
		if err := rows.Scan(&r.prompt, &r.failure, &r.outputs, &r.passed); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// stateHas reports whether the run's state holds key.
func stateHas(t *testing.T, store *db.Store, runID int64, key string) bool {
	t.Helper()
	run, err := store.Workflows().GetWorkflowRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal([]byte(run.StateJSON), &st); err != nil {
		t.Fatal(err)
	}
	_, has := st[key]
	return has
}

// TestMinToolCalls_ARepairThatDoesTheWorkCompletesTheNode: a dispatch that
// answers its report with no tool call is sent to its repair with the
// count; the repair makes the call and answers, and the node completes
// with the repair's answer and the repair's tool calls on its audit trail.
func TestMinToolCalls_ARepairThatDoesTheWorkCompletesTheNode(t *testing.T) {
	const report = `{"said":"done"}`
	var reads atomic.Int64 // the tool calls the fake asked for
	f := &fakeOpenAI{models: []string{"local-model"}}
	f.reply = func(body map[string]any) string {
		if strings.HasPrefix(firstUserMessage(body), shortRepairMarker) && !carriesToolResult(body) {
			reads.Add(1)
			return readFileReply("wf.yaml", 1, 1)
		}
		return chatReply(report, "stop", 1, 1)
	}
	srv := httptest.NewServer(f)
	defer srv.Close()
	res, store, _, err := localRun(t, srv, shortRepairYAML, Config{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if reads.Load() == 0 {
		t.Fatal("precondition: the fake served no tool call")
	}
	row := readLocalNodeRow(t, store, "work")
	if row.status != "completed" || row.rationale != report {
		t.Fatalf("node = %s with rationale %q, want completed with the repair's answer", row.status, row.rationale)
	}
	// The dispatch's answer, then each tool turn and the repair's answer.
	if n, want := f.chats.Load(), 2+reads.Load(); n != want {
		t.Fatalf("%d chat calls, want %d", n, want)
	}
	if got := nodeToolRows(t, store, res.RunID, "work", "read_file"); got != int(reads.Load()) {
		t.Fatalf("%d read_file rows on the node, want the repair's %d", got, reads.Load())
	}
	repairs := readRepairRows(t, store, res.RunID, "work")
	if len(repairs) != 1 || repairs[0].passed != 1 {
		t.Fatalf("repairs = %+v, want one that passed", repairs)
	}
	reason := shortReason(t, 0, 1)
	r := repairs[0]
	if !strings.Contains(r.prompt, shortRepairMarker+" "+reason) || !strings.Contains(r.prompt, `"said":"done"`) || !strings.Contains(r.prompt, "The task: do the work") {
		t.Fatalf("repair prompt = %q, want the reason, the answer and the task", r.prompt)
	}
	if !strings.HasPrefix(r.failure, reason) || !strings.Contains(r.failure, workflow.MinToolCallsPredicate) {
		t.Fatalf("failure_reason = %q, want the reason first and min_tool_calls after it", r.failure)
	}
	if !stateHas(t, store, res.RunID, "said") {
		t.Fatal("the repair's answer did not reach state")
	}
}

// TestMinToolCalls_ARepairThatStillDoesNothingIsRejected: a repair that
// answers with no tool call either is rejected, after the one attempt, and
// both replies are kept: the dispatch's in the repair prompt, the repair's
// in its row's outputs.
func TestMinToolCalls_ARepairThatStillDoesNothingIsRejected(t *testing.T) {
	const report = `{"said":"done"}`
	f := &fakeOpenAI{models: []string{"local-model"}, reply: func(map[string]any) string { return chatReply(report, "stop", 1, 1) }}
	srv := httptest.NewServer(f)
	defer srv.Close()
	res, store, log, err := localRun(t, srv, shortRepairYAML, Config{})
	if err == nil {
		t.Fatal("Run succeeded with a node whose calls never ran a tool")
	}
	row := readLocalNodeRow(t, store, "work")
	if want := "accept rejected: " + workflow.MinToolCallsPredicate + " = 0"; row.status != "rejected" || row.rationale != want {
		t.Fatalf("node = %s with rationale %q, want rejected with %q", row.status, row.rationale, want)
	}
	// The dispatch and the one repair attempt, each one call.
	if n := f.chats.Load(); n != 2 {
		t.Fatalf("%d chat calls, want the dispatch and one repair", n)
	}
	if got := nodeToolRows(t, store, res.RunID, "work", "read_file"); got != 0 {
		t.Fatalf("%d read_file rows, want none: the fake made no tool call", got)
	}
	repairs := readRepairRows(t, store, res.RunID, "work")
	if len(repairs) != 1 || repairs[0].passed != 0 {
		t.Fatalf("repairs = %+v, want one that did not pass", repairs)
	}
	reason := shortReason(t, 0, 1)
	r := repairs[0]
	if !strings.Contains(r.prompt, shortRepairMarker+" "+reason) || !strings.Contains(r.prompt, `"said":"done"`) {
		t.Fatalf("repair prompt = %q, want the reason and the dispatch's answer", r.prompt)
	}
	if !strings.Contains(r.outputs, `"said":"done"`) {
		t.Fatalf("repair_outputs_json = %q, want the repair's answer", r.outputs)
	}
	if !strings.Contains(log, "repair work attempt 1 still rejected") || !strings.Contains(log, reason) {
		t.Fatalf("log lacks the repair's rejection with its reason:\n%s", log)
	}
	if stateHas(t, store, res.RunID, "said") {
		t.Fatal("an answer with no work behind it reached state")
	}
}

// TestMinToolCalls_CountsTheDispatchAndItsRepairsTogether: a dispatch that
// made its tool call and then failed accept: is repaired by an attempt that
// runs nothing, since the node's calls together made the minimum. The
// hive's dispatch repairs a malformed report this way.
func TestMinToolCalls_CountsTheDispatchAndItsRepairsTogether(t *testing.T) {
	const yaml = `name: together
nodes:
  work:
    type: agent
    model: local-model
    prompt: "answer"
    outputs: [ok]
    min_tool_calls: 1
    accept:
      - "outputs.ok == true"
    on_reject:
      max_repair_iterations: 1
`
	var reads atomic.Int64
	f := &fakeOpenAI{models: []string{"local-model"}}
	f.reply = func(body map[string]any) string {
		switch {
		case firstUserMessage(body) != "answer":
			return chatReply(`{"ok": true}`, "stop", 1, 1)
		case carriesToolResult(body):
			return chatReply(`{"ok": false}`, "stop", 1, 1)
		}
		reads.Add(1)
		return readFileReply("wf.yaml", 1, 1)
	}
	srv := httptest.NewServer(f)
	defer srv.Close()
	res, store, _, err := localRun(t, srv, yaml, Config{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if row := readLocalNodeRow(t, store, "work"); row.status != "completed" {
		t.Fatalf("node = %s, want completed", row.status)
	}
	if n, want := f.chats.Load(), 2+reads.Load(); n != want {
		t.Fatalf("%d chat calls, want %d: the dispatch's tool turn and answer, then one repair", n, want)
	}
	if got := nodeToolRows(t, store, res.RunID, "work", "read_file"); got != int(reads.Load()) {
		t.Fatalf("%d read_file rows, want the dispatch's %d", got, reads.Load())
	}
	repairs := readRepairRows(t, store, res.RunID, "work")
	if len(repairs) != 1 || repairs[0].passed != 1 || strings.Contains(repairs[0].failure, workflow.MinToolCallsPredicate) {
		t.Fatalf("repairs = %+v, want one that passed, started by accept: and not by min_tool_calls", repairs)
	}
}

// TestMinToolCalls_NoRepairBlockFailsAndKeepsTheReply: with no on_reject:, a
// short call fails the node, and the reply it answered with is kept as the
// node's rationale, since no repair row holds it.
func TestMinToolCalls_NoRepairBlockFailsAndKeepsTheReply(t *testing.T) {
	const yaml = `name: short
nodes:
  work:
    type: agent
    model: local-model
    prompt: "do the work"
    outputs: [said]
    min_tool_calls: 1
`
	const report = `{"said":"I ran everything"}`
	f := &fakeOpenAI{models: []string{"local-model"}, reply: func(map[string]any) string { return chatReply(report, "stop", 1, 1) }}
	srv := httptest.NewServer(f)
	defer srv.Close()
	res, store, _, err := localRun(t, srv, yaml, Config{})
	if err == nil {
		t.Fatal("Run succeeded with a node whose call ran no tool")
	}
	row := readLocalNodeRow(t, store, "work")
	if row.status != "failed" || !strings.Contains(row.errMsg, "0 tool call") || !strings.Contains(row.errMsg, workflow.MinToolCallsPredicate) {
		t.Fatalf("node = %s with error %q, want failed naming the count and min_tool_calls", row.status, row.errMsg)
	}
	if row.rationale != report {
		t.Fatalf("rationale = %q, want the reply %q", row.rationale, report)
	}
	if n := f.chats.Load(); n != 1 {
		t.Fatalf("%d chat calls, want the dispatch alone", n)
	}
	if repairs := readRepairRows(t, store, res.RunID, "work"); len(repairs) != 0 {
		t.Fatalf("repairs = %+v, want none without on_reject", repairs)
	}
	if stateHas(t, store, res.RunID, "said") {
		t.Fatal("an answer with no work behind it reached state")
	}
}

// TestMinToolCalls_UnreportedCallsAreNotChecked: a backend that runs its own
// tool loop and reports none of its calls cannot be checked, so a node with
// on_reject: completes on its answer, with no repair, and the log says the
// minimum was not checked.
func TestMinToolCalls_UnreportedCallsAreNotChecked(t *testing.T) {
	const yaml = `name: t
version: 1
nodes:
  work:
    type: agent
    prompt: do the work
    outputs: [said]
    min_tool_calls: 1
    on_reject:
      max_repair_iterations: 1
edges: []
`
	dir := t.TempDir()
	path := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	store := newTempStore(t)
	backend := &toolCountBackend{calls: []toolCountCall{{0, true}}}
	var log strings.Builder
	res, err := Run(context.Background(), store, Config{
		WorkflowYAML: path, ProjectDir: dir, MaxIterations: 10, Backend: backend, Log: io.MultiWriter(&log, io.Discard),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if row := readLocalNodeRow(t, store, "work"); row.status != "completed" || backend.n != 1 {
		t.Fatalf("node = %s after %d call(s), want completed after 1", row.status, backend.n)
	}
	if repairs := readRepairRows(t, store, res.RunID, "work"); len(repairs) != 0 {
		t.Fatalf("repairs = %+v, want none", repairs)
	}
	if !strings.Contains(log.String(), "min_tool_calls 1 not checked") {
		t.Fatalf("log does not say the minimum was not checked:\n%s", log.String())
	}
}
