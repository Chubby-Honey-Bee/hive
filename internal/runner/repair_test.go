package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// callableBackend is a LLMBackend whose per-call behaviour is driven by a
// slice of functions. Each invocation pops the next function; once the
// slice is exhausted, the last function is reused.
type callableBackend struct {
	mu    sync.Mutex
	calls int
	fns   []func(req RunRequest) (*RunResult, error)
}

func (b *callableBackend) Run(_ context.Context, req RunRequest) (*RunResult, error) {
	b.mu.Lock()
	idx := b.calls
	if idx >= len(b.fns) {
		idx = len(b.fns) - 1
	}
	b.calls++
	b.mu.Unlock()
	return b.fns[idx](req)
}

func successResult(text string) func(RunRequest) (*RunResult, error) {
	return func(_ RunRequest) (*RunResult, error) {
		return &RunResult{
			FinalText:    text,
			Turns:        1,
			InputTokens:  5,
			OutputTokens: 3,
		}, nil
	}
}

func errorResult(msg string) func(RunRequest) (*RunResult, error) {
	return func(_ RunRequest) (*RunResult, error) {
		return nil, fmt.Errorf("%s", msg)
	}
}

// buildRepairRC builds a minimal runtimeContext for tryRepair unit tests.
func buildRepairRC(t *testing.T, store *db.Store, runID int64, defn map[string]any, backend LLMBackend) *runtimeContext {
	t.Helper()
	return &runtimeContext{
		ctx:        context.Background(),
		cfg:        Config{ProjectDir: t.TempDir()},
		store:      store,
		backend:    backend,
		runID:      runID,
		logf:       makeLogf(&bytes.Buffer{}),
		parsedDefn: defn,
		res:        &Result{},
	}
}

// createRepairRun creates a workflow run with yamlStr stored as the definition
// and nodeName seeded as a pending agent node. Returns the run ID.
func createRepairRun(t *testing.T, store *db.Store, nodeName, yamlStr string) int64 {
	t.Helper()
	runID, err := store.Workflows().CreateWorkflowRun(
		"repair-unit-test", 1, yamlStr, "{}",
		[]db.NodeSeed{{Name: nodeName, Type: "agent"}},
	)
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	return runID
}

// makeRejection builds an AcceptRejection for nodeName.
func makeRejection(nodeName string) *workflow.AcceptRejection {
	return &workflow.AcceptRejection{
		NodeName:       nodeName,
		Predicate:      "outputs.compile_ok == true",
		EvaluatedValue: "false",
		Outputs:        map[string]any{"compile_ok": false},
	}
}

// ---------------------------------------------------------------------------
// Early-exit branches — no DB round-trips happen before the return.
// ---------------------------------------------------------------------------

func TestTryRepair_NoOnRejectBlock(t *testing.T) {
	const nodeName = "fix-1"
	yamlStr := fmt.Sprintf(`
name: test-fix
nodes:
  %s:
    type: agent
    model: haiku
    prompt: fix the code
`, nodeName)

	store := newTempStore(t)
	defn, _ := workflow.LoadYAMLString(yamlStr)
	rc := buildRepairRC(t, store, 0, defn, &stubBackend{})
	node := workflow.DispatchNode{Node: nodeName, Model: "haiku"}

	got, _, _, ok := rc.tryRepair(node, nil, makeRejection(nodeName))
	if ok || got != nil {
		t.Errorf("expected nil,false for node without on_reject; got %v,%v", got, ok)
	}
}

func TestTryRepair_ZeroMaxAttempts(t *testing.T) {
	const nodeName = "fix-zero"
	yamlStr := fmt.Sprintf(`
name: test-fix
nodes:
  %s:
    type: agent
    model: haiku
    prompt: fix the code
    on_reject:
      max_repair_iterations: 0
`, nodeName)

	store := newTempStore(t)
	defn, _ := workflow.LoadYAMLString(yamlStr)
	rc := buildRepairRC(t, store, 0, defn, &stubBackend{})
	node := workflow.DispatchNode{Node: nodeName, Model: "haiku"}

	got, _, _, ok := rc.tryRepair(node, nil, makeRejection(nodeName))
	if ok || got != nil {
		t.Errorf("expected nil,false for max_repair_iterations=0; got %v,%v", got, ok)
	}
}

// ---------------------------------------------------------------------------
// RecordRepairAttempt fails (FK violation on non-existent run) → nil, false.
// ---------------------------------------------------------------------------

func TestTryRepair_RecordAttemptFails(t *testing.T) {
	const nodeName = "fix-bad-run"
	yamlStr := fmt.Sprintf(`
name: test-fix
nodes:
  %s:
    type: agent
    model: haiku
    prompt: fix the code
    on_reject:
      max_repair_iterations: 1
`, nodeName)

	store := newTempStore(t)
	defn, _ := workflow.LoadYAMLString(yamlStr)

	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		successResult(`{"ok": true}`),
	}}

	// runID 99999 does not exist → workflow_repairs FK constraint will reject INSERT.
	rc := buildRepairRC(t, store, 99999, defn, backend)
	node := workflow.DispatchNode{Node: nodeName, Model: "haiku"}

	got, _, _, ok := rc.tryRepair(node, nil, makeRejection(nodeName))
	if ok || got != nil {
		t.Errorf("expected nil,false on RecordRepairAttempt failure; got %v,%v", got, ok)
	}
	// Backend must NOT have been called — we return before reaching backend.Run.
	if backend.calls != 0 {
		t.Errorf("expected 0 backend calls before RecordRepairAttempt failure; got %d", backend.calls)
	}
}

// ---------------------------------------------------------------------------
// Happy path: backend succeeds, CompleteNode accepts (no accept: predicates).
// ---------------------------------------------------------------------------

func TestTryRepair_HappyPath(t *testing.T) {
	const nodeName = "fix-happy"
	yamlStr := fmt.Sprintf(`
name: test-fix
nodes:
  %s:
    type: agent
    model: haiku
    prompt: fix the code
    outputs: [compile_ok]
    on_reject:
      max_repair_iterations: 3
      model: haiku
`, nodeName)

	store := newTempStore(t)
	runID := createRepairRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)

	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		successResult(`{"compile_ok": true}`),
	}}

	rc := buildRepairRC(t, store, runID, defn, backend)
	node := workflow.DispatchNode{Node: nodeName, Model: "haiku"}

	outputs, _, _, ok := rc.tryRepair(node, nil, makeRejection(nodeName))
	if !ok {
		t.Fatal("expected ok=true on happy path")
	}
	if outputs == nil {
		t.Fatal("expected non-nil outputs on happy path")
	}
	if v, _ := outputs["compile_ok"].(bool); !v {
		t.Errorf("outputs.compile_ok should be true; got %v", outputs["compile_ok"])
	}
}

// ---------------------------------------------------------------------------
// Backend always errors: repair exhausts max attempts → nil, false.
// ---------------------------------------------------------------------------

func TestTryRepair_BackendError_Exhausts(t *testing.T) {
	const nodeName = "fix-backend-err"
	const maxAttempts = 2
	yamlStr := fmt.Sprintf(`
name: test-fix
nodes:
  %s:
    type: agent
    model: haiku
    prompt: fix the code
    on_reject:
      max_repair_iterations: %d
      model: haiku
`, nodeName, maxAttempts)

	store := newTempStore(t)
	runID := createRepairRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)

	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		errorResult("backend boom 1"),
		errorResult("backend boom 2"),
	}}

	rc := buildRepairRC(t, store, runID, defn, backend)
	node := workflow.DispatchNode{Node: nodeName, Model: "haiku"}

	outputs, _, _, ok := rc.tryRepair(node, nil, makeRejection(nodeName))
	if ok || outputs != nil {
		t.Errorf("expected nil,false after backend exhaustion; got %v,%v", outputs, ok)
	}
	if backend.calls != maxAttempts {
		t.Errorf("expected %d backend calls, got %d", maxAttempts, backend.calls)
	}
}

// ---------------------------------------------------------------------------
// Backend succeeds but accept: predicate still fails: exhausts → nil, false.
// ---------------------------------------------------------------------------

func TestTryRepair_StillRejected_Exhausts(t *testing.T) {
	const nodeName = "fix-still-rej"
	const maxAttempts = 2
	// The node has an accept predicate requiring compile_ok==true; the backend
	// always returns compile_ok:false, so every CompleteNode call re-rejects.
	yamlStr := fmt.Sprintf(`
name: test-fix
nodes:
  %s:
    type: agent
    model: haiku
    prompt: fix the code
    outputs: [compile_ok]
    accept:
      - "outputs.compile_ok == true"
    on_reject:
      max_repair_iterations: %d
      model: haiku
`, nodeName, maxAttempts)

	store := newTempStore(t)
	runID := createRepairRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)

	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		successResult(`{"compile_ok": false}`),
		successResult(`{"compile_ok": false}`),
	}}

	rc := buildRepairRC(t, store, runID, defn, backend)
	node := workflow.DispatchNode{Node: nodeName, Model: "haiku"}

	outputs, _, _, ok := rc.tryRepair(node, nil, makeRejection(nodeName))
	if ok || outputs != nil {
		t.Errorf("expected nil,false after exhausting rejected attempts; got %v,%v", outputs, ok)
	}
	if backend.calls != maxAttempts {
		t.Errorf("expected %d backend calls, got %d", maxAttempts, backend.calls)
	}
}

// ---------------------------------------------------------------------------
// Default prompt template is injected when on_reject omits prompt_template.
// ---------------------------------------------------------------------------

func TestTryRepair_DefaultPromptTemplate(t *testing.T) {
	const nodeName = "fix-default-tmpl"
	// No prompt_template → the hardcoded default must be used.
	yamlStr := fmt.Sprintf(`
name: test-fix
nodes:
  %s:
    type: agent
    model: haiku
    prompt: original prompt text
    outputs: [result]
    on_reject:
      max_repair_iterations: 1
`, nodeName)

	store := newTempStore(t)
	runID := createRepairRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)

	var capturedPrompt string
	backend := &callableBackend{
		fns: []func(RunRequest) (*RunResult, error){
			func(req RunRequest) (*RunResult, error) {
				capturedPrompt = req.Prompt
				return &RunResult{FinalText: `{"result": "fixed"}`, Turns: 1}, nil
			},
		},
	}

	rc := buildRepairRC(t, store, runID, defn, backend)
	node := workflow.DispatchNode{
		Node:           nodeName,
		Model:          "haiku",
		ResolvedPrompt: "original prompt text",
	}

	// Result not the point here; we validate the prompt shape.
	rc.tryRepair(node, nil, makeRejection(nodeName))

	if capturedPrompt == "" {
		t.Fatal("backend was not called — prompt not captured")
	}
	// The default template must interpolate original prompt and fix advice.
	for _, want := range []string{"original prompt text", "Fix only what failed"} {
		if !strings.Contains(capturedPrompt, want) {
			t.Errorf("default prompt template missing %q; full prompt:\n%s", want, capturedPrompt)
		}
	}
}

// The repair prompt is filled in one pass: a placeholder inside the node's
// prompt, such as a context pack naming {outputs}, reaches the repair as
// written, and the template's own placeholders are filled.
func TestTryRepair_OriginalPromptStaysAsWritten(t *testing.T) {
	const nodeName = "fix-one-pass"
	yamlStr := fmt.Sprintf(`
name: test-fix
nodes:
  %s:
    type: agent
    model: haiku
    prompt: original
    outputs: [compile_ok]
    on_reject:
      max_repair_iterations: 1
`, nodeName)
	store := newTempStore(t)
	runID := createRepairRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)
	var captured string
	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		func(req RunRequest) (*RunResult, error) {
			captured = req.Prompt
			return &RunResult{FinalText: `{"compile_ok": true}`, Turns: 1}, nil
		},
	}}
	rc := buildRepairRC(t, store, runID, defn, backend)
	original := "The pack says: Queen reads {outputs} and {accept_failure}."
	rej := makeRejection(nodeName)
	rc.tryRepair(workflow.DispatchNode{Node: nodeName, Model: "haiku", ResolvedPrompt: original}, nil, rej)
	outputs, _ := json.Marshal(rej.Outputs)
	for _, want := range []string{"Original prompt:\n" + original + "\n", "Outputs that failed:\n" + string(outputs), rej.Error()} {
		if !strings.Contains(captured, want) {
			t.Errorf("repair prompt lacks %q:\n%s", want, captured)
		}
	}
}

// A rejection's reason reaches every attempt's prompt, alone, before and
// after the failed outputs, including the attempt after a backend error.
// The attempt's failure_reason keeps the predicate after it.
func TestTryRepair_ReasonSurvivesABackendError(t *testing.T) {
	const nodeName = "fix-reason"
	yamlStr := fmt.Sprintf(`
name: test-fix
nodes:
  %s:
    type: agent
    model: haiku
    prompt: original
    outputs: [compile_ok]
    on_reject:
      max_repair_iterations: 2
`, nodeName)
	store := newTempStore(t)
	runID := createRepairRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)
	var prompts []string
	capture := func(answer func(RunRequest) (*RunResult, error)) func(RunRequest) (*RunResult, error) {
		return func(req RunRequest) (*RunResult, error) {
			prompts = append(prompts, req.Prompt)
			return answer(req)
		}
	}
	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		capture(errorResult("backend boom")),
		capture(successResult(`{"compile_ok": true}`)),
	}}
	rc := buildRepairRC(t, store, runID, defn, backend)
	rej := makeRejection(nodeName)
	rej.Reason = "compile_ok was false; make the build pass"
	rc.tryRepair(workflow.DispatchNode{Node: nodeName, Model: "haiku", ResolvedPrompt: "original"}, nil, rej)
	if len(prompts) != 2 {
		t.Fatalf("backend calls = %d, want 2", len(prompts))
	}
	outputs, _ := json.Marshal(rej.Outputs)
	for i, p := range prompts {
		before, after, ok := strings.Cut(p, "Outputs that failed:\n"+string(outputs))
		switch {
		case !ok:
			t.Errorf("attempt %d: the prompt lacks the failed outputs:\n%s", i+1, p)
		case !strings.Contains(before, rej.Reason) || !strings.Contains(after, rej.Reason):
			t.Errorf("attempt %d: the reason %q is not both before and after the failed outputs:\n%s", i+1, rej.Reason, p)
		case strings.Contains(p, rej.Predicate):
			t.Errorf("attempt %d: the prompt shows the predicate %q:\n%s", i+1, rej.Predicate, p)
		}
	}
	rows, err := store.ReadDB.Query(`SELECT failure_reason FROM workflow_repairs WHERE run_id=? AND node_name=? ORDER BY attempt`, runID, nodeName)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var failure string
		_ = rows.Scan(&failure)
		n++
		if !strings.HasPrefix(failure, rej.Reason+" (") || !strings.Contains(failure, rej.Predicate) {
			t.Errorf("attempt %d: failure_reason %q, want the reason, then the predicate", n, failure)
		}
	}
	if n != 2 {
		t.Errorf("repair rows = %d, want 2", n)
	}
}

// A run cancelled before a repair attempt starts makes no attempt: nothing
// is recorded and no call is made, and the caller puts the node back to
// pending.
func TestTryRepair_ACancelledRunMakesNoAttempt(t *testing.T) {
	const nodeName = "fix-cancelled"
	yamlStr := fmt.Sprintf(`
name: test-fix
nodes:
  %s:
    type: agent
    model: haiku
    prompt: fix the code
    on_reject:
      max_repair_iterations: 3
      model: haiku
`, nodeName)

	store := newTempStore(t)
	runID := createRepairRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)
	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		errorResult("context canceled"),
	}}
	rc := buildRepairRC(t, store, runID, defn, backend)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rc.ctx = ctx

	outputs, _, _, ok := rc.tryRepair(workflow.DispatchNode{Node: nodeName, Model: "haiku"}, nil, makeRejection(nodeName))
	if ok || outputs != nil {
		t.Errorf("tryRepair in a cancelled run returned %v,%v; want nil,false", outputs, ok)
	}
	if backend.calls != 0 {
		t.Errorf("tryRepair in a cancelled run made %d calls; want 0", backend.calls)
	}
	var recorded int
	if err := store.ReadDB.QueryRow(`SELECT COUNT(*) FROM workflow_repairs WHERE run_id = ?`, runID).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 0 {
		t.Errorf("tryRepair in a cancelled run recorded %d attempts; want 0", recorded)
	}
}
