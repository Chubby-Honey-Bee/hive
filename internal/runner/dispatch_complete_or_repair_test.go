package runner

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// buildCompleteRC builds a minimal runtimeContext for completeOrRepair tests.
func buildCompleteRC(t *testing.T, store *db.Store, runID int64, defn map[string]any, backend LLMBackend) *runtimeContext {
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
		// events is nil → emit() is a safe no-op
	}
}

// createCompleteRun creates a workflow run and seeds a single agent node.
func createCompleteRun(t *testing.T, store *db.Store, nodeName, yamlStr string) int64 {
	t.Helper()
	runID, err := store.Workflows().CreateWorkflowRun(
		"complete-unit-test", 1, yamlStr, "{}",
		[]db.NodeSeed{{Name: nodeName, Type: "agent"}},
	)
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	return runID
}

// ---------------------------------------------------------------------------
// Happy path — no accept: predicates, CompleteNode succeeds.
// ---------------------------------------------------------------------------

func TestCompleteOrRepair_HappyPath(t *testing.T) {
	const nodeName = "step-happy"
	yamlStr := fmt.Sprintf(`
name: test-complete
nodes:
  %s:
    type: agent
    model: haiku
    prompt: do the thing
    outputs: [result]
`, nodeName)

	store := newTempStore(t)
	runID := createCompleteRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)

	rc := buildCompleteRC(t, store, runID, defn, &stubBackend{})
	node := workflow.DispatchNode{Node: nodeName, Model: "haiku"}
	runResult := &RunResult{FinalText: `{"result": "done"}`, Turns: 1}

	outputs, _, _, accepted := rc.completeOrRepair(node, runResult, nil)

	if !accepted {
		t.Fatal("expected accepted=true on happy path")
	}
	if outputs == nil {
		t.Fatal("expected non-nil outputs on happy path")
	}
	if v, _ := outputs["result"].(string); v != "done" {
		t.Errorf("outputs[result] = %q, want %q", v, "done")
	}
}

// ---------------------------------------------------------------------------
// Happy path — no JSON in FinalText; workflow.ExtractJSONOutput returns empty map.
// ---------------------------------------------------------------------------

func TestCompleteOrRepair_NoJSONInOutput(t *testing.T) {
	const nodeName = "step-nojson"
	yamlStr := fmt.Sprintf(`
name: test-complete
nodes:
  %s:
    type: agent
    model: haiku
    prompt: do the thing
`, nodeName)

	store := newTempStore(t)
	runID := createCompleteRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)

	rc := buildCompleteRC(t, store, runID, defn, &stubBackend{})
	node := workflow.DispatchNode{Node: nodeName, Model: "haiku"}
	runResult := &RunResult{FinalText: "plain text, no JSON here", Turns: 1}

	outputs, _, _, accepted := rc.completeOrRepair(node, runResult, nil)

	if !accepted {
		t.Fatal("expected accepted=true when no accept predicates exist")
	}
	// outputs may be empty but must not be nil (workflow.ExtractJSONOutput returns an empty map)
	if outputs == nil {
		t.Error("expected non-nil outputs (at minimum empty map)")
	}
}

// ---------------------------------------------------------------------------
// AcceptRejection without on_reject — tryRepair returns (nil,false),
// completeOrRepair marks the node rejected and returns (nil, false).
// ---------------------------------------------------------------------------

func TestCompleteOrRepair_AcceptRejected_NoRepair(t *testing.T) {
	const nodeName = "step-rej-noreplace"
	yamlStr := fmt.Sprintf(`
name: test-complete
nodes:
  %s:
    type: agent
    model: haiku
    prompt: do the thing
    outputs: [compile_ok]
    accept:
      - "outputs.compile_ok == true"
`, nodeName)

	store := newTempStore(t)
	runID := createCompleteRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)

	rc := buildCompleteRC(t, store, runID, defn, &stubBackend{})
	node := workflow.DispatchNode{Node: nodeName, Model: "haiku"}
	// compile_ok=false → predicate fails → AcceptRejection
	runResult := &RunResult{FinalText: `{"compile_ok": false}`, Turns: 1}

	outputs, _, _, accepted := rc.completeOrRepair(node, runResult, nil)

	if accepted {
		t.Error("expected accepted=false when accept predicate fails and no on_reject")
	}
	if outputs != nil {
		t.Errorf("expected nil outputs on rejection; got %v", outputs)
	}
}

// ---------------------------------------------------------------------------
// AcceptRejection with on_reject and a backend that now passes the predicate.
// completeOrRepair should return (repaired_outputs, true).
// ---------------------------------------------------------------------------

func TestCompleteOrRepair_AcceptRejected_RepairSucceeds(t *testing.T) {
	const nodeName = "step-rej-repair"
	yamlStr := fmt.Sprintf(`
name: test-complete
nodes:
  %s:
    type: agent
    model: haiku
    prompt: do the thing
    outputs: [compile_ok]
    accept:
      - "outputs.compile_ok == true"
    on_reject:
      max_repair_iterations: 1
      model: haiku
`, nodeName)

	store := newTempStore(t)
	runID := createCompleteRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)

	// Repair backend returns compile_ok=true → predicate passes.
	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		successResult(`{"compile_ok": true}`),
	}}

	rc := buildCompleteRC(t, store, runID, defn, backend)
	node := workflow.DispatchNode{Node: nodeName, Model: "haiku"}
	// Initial result fails the predicate.
	runResult := &RunResult{FinalText: `{"compile_ok": false}`, Turns: 1}

	outputs, _, _, accepted := rc.completeOrRepair(node, runResult, nil)

	if !accepted {
		t.Fatal("expected accepted=true after successful repair")
	}
	if outputs == nil {
		t.Fatal("expected non-nil outputs after successful repair")
	}
	if v, _ := outputs["compile_ok"].(bool); !v {
		t.Errorf("repaired outputs.compile_ok should be true; got %v", outputs["compile_ok"])
	}
	if backend.calls != 1 {
		t.Errorf("expected 1 backend call for the repair; got %d", backend.calls)
	}
}

// ---------------------------------------------------------------------------
// Generic (non-AcceptRejection) error from CompleteNode — run not found.
// completeOrRepair must return (nil, false).
// ---------------------------------------------------------------------------

func TestCompleteOrRepair_GenericError(t *testing.T) {
	const nodeName = "step-generic-err"
	yamlStr := fmt.Sprintf(`
name: test-complete
nodes:
  %s:
    type: agent
    model: haiku
    prompt: do the thing
`, nodeName)

	store := newTempStore(t)
	defn, _ := workflow.LoadYAMLString(yamlStr)

	// runID 99999 does not exist → GetWorkflowRun fails with a generic error.
	rc := buildCompleteRC(t, store, 99999, defn, &stubBackend{})
	node := workflow.DispatchNode{Node: nodeName, Model: "haiku"}
	runResult := &RunResult{FinalText: `{"result": "ok"}`, Turns: 1}

	outputs, _, _, accepted := rc.completeOrRepair(node, runResult, nil)

	if accepted {
		t.Error("expected accepted=false on generic CompleteNode error")
	}
	if outputs != nil {
		t.Errorf("expected nil outputs on generic error; got %v", outputs)
	}
}
