package runner

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// buildAgentNodeRC builds a minimal runtimeContext for executeAgentNode tests.
func buildAgentNodeRC(t *testing.T, store *db.Store, runID int64, defn map[string]any, backend LLMBackend, cfg Config) *runtimeContext {
	t.Helper()
	if cfg.ProjectDir == "" {
		cfg.ProjectDir = t.TempDir()
	}
	return &runtimeContext{
		ctx:        context.Background(),
		cfg:        cfg,
		store:      store,
		backend:    backend,
		gc:         &GitCommitter{Enabled: false},
		runID:      runID,
		logf:       makeLogf(&bytes.Buffer{}),
		parsedDefn: defn,
		res:        &Result{},
		resMu:      sync.Mutex{},
		// events nil → emit() is a no-op
	}
}

// seedAgentRun creates a workflow run and seeds one agent node.
func seedAgentRun(t *testing.T, store *db.Store, nodeName, yamlStr string) int64 {
	t.Helper()
	runID, err := store.Workflows().CreateWorkflowRun(
		"agent-node-test", 1, yamlStr, "{}",
		[]db.NodeSeed{{Name: nodeName, Type: "agent"}},
	)
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	return runID
}

// ---------------------------------------------------------------------------
// DryRun — node is marked completed without calling the LLM backend.
// ---------------------------------------------------------------------------

func TestExecuteAgentNode_DryRun(t *testing.T) {
	const nodeName = "w1-dry"
	yamlStr := fmt.Sprintf(`
name: test-agent-dryrun
nodes:
  %s:
    type: agent
    model: haiku
    prompt: do something
`, nodeName)

	store := newTempStore(t)
	runID := seedAgentRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)

	cfg := Config{DryRun: true, ProjectDir: t.TempDir()}
	rc := buildAgentNodeRC(t, store, runID, defn, &stubBackend{}, cfg)
	node := workflow.DispatchNode{Node: nodeName, Type: "agent", Model: "haiku"}

	stop := rc.executeAgentNode(node)

	if stop {
		t.Errorf("expected stop=false in dry-run, got true")
	}
	// Backend must not have been called.
	if b, ok := rc.backend.(*stubBackend); ok && len(b.submitted) > 0 {
		t.Errorf("dry-run must not call backend; got %d calls", len(b.submitted))
	}
	// Node must be marked completed in the DB.
	states, err := store.Workflows().GetWorkflowNodeStates(runID)
	if err != nil {
		t.Fatalf("GetWorkflowNodeStates: %v", err)
	}
	var found bool
	for _, s := range states {
		if s.NodeName == nodeName && s.Status == "completed" {
			found = true
		}
	}
	if !found {
		t.Errorf("node %q not marked completed after dry-run; states=%v", nodeName, states)
	}
	// NodesRun must NOT be incremented in dry-run.
	if rc.res.NodesRun != 0 {
		t.Errorf("NodesRun=%d after dry-run, want 0", rc.res.NodesRun)
	}
}

// ---------------------------------------------------------------------------
// Happy path — backend succeeds, accept passes, NodesRun incremented.
// ---------------------------------------------------------------------------

func TestExecuteAgentNode_HappyPath(t *testing.T) {
	const nodeName = "w1-happy"
	yamlStr := fmt.Sprintf(`
name: test-agent-happy
nodes:
  %s:
    type: agent
    model: haiku
    prompt: do something
    outputs: [result]
`, nodeName)

	store := newTempStore(t)
	runID := seedAgentRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)

	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		successResult(`{"result":"done"}`),
	}}
	cfg := Config{ProjectDir: t.TempDir()}
	rc := buildAgentNodeRC(t, store, runID, defn, backend, cfg)
	node := workflow.DispatchNode{Node: nodeName, Type: "agent", Model: "haiku"}

	stop := rc.executeAgentNode(node)

	if stop {
		t.Errorf("expected stop=false on happy path, got true")
	}
	if rc.res.NodesRun != 1 {
		t.Errorf("NodesRun=%d, want 1", rc.res.NodesRun)
	}
	// Node should be completed in the DB.
	states, err := store.Workflows().GetWorkflowNodeStates(runID)
	if err != nil {
		t.Fatalf("GetWorkflowNodeStates: %v", err)
	}
	var found bool
	for _, s := range states {
		if s.NodeName == nodeName && s.Status == "completed" {
			found = true
		}
	}
	if !found {
		t.Errorf("node %q not completed after happy path; states=%v", nodeName, states)
	}
}

// ---------------------------------------------------------------------------
// Backend error — runErr != nil, node is failed, NodesRun unchanged.
// ---------------------------------------------------------------------------

func TestExecuteAgentNode_BackendError(t *testing.T) {
	const nodeName = "w1-be-err"
	yamlStr := fmt.Sprintf(`
name: test-agent-be-err
nodes:
  %s:
    type: agent
    model: haiku
    prompt: do something
`, nodeName)

	store := newTempStore(t)
	runID := seedAgentRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)

	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		errorResult("simulated backend failure"),
	}}
	cfg := Config{ProjectDir: t.TempDir()}
	rc := buildAgentNodeRC(t, store, runID, defn, backend, cfg)
	node := workflow.DispatchNode{Node: nodeName, Type: "agent", Model: "haiku"}

	stop := rc.executeAgentNode(node)

	if stop {
		t.Errorf("expected stop=false after backend error, got true")
	}
	if rc.res.NodesRun != 0 {
		t.Errorf("NodesRun=%d after backend error, want 0", rc.res.NodesRun)
	}
	// Node should be marked failed.
	states, err := store.Workflows().GetWorkflowNodeStates(runID)
	if err != nil {
		t.Fatalf("GetWorkflowNodeStates: %v", err)
	}
	var found bool
	for _, s := range states {
		if s.NodeName == nodeName && s.Status == "failed" {
			found = true
		}
	}
	if !found {
		t.Errorf("node %q not marked failed after backend error; states=%v", nodeName, states)
	}
}

// ---------------------------------------------------------------------------
// Accept predicate fails — completeOrRepair returns !accepted, stop=false.
// ---------------------------------------------------------------------------

func TestExecuteAgentNode_AcceptRejected(t *testing.T) {
	const nodeName = "w1-rejected"
	yamlStr := fmt.Sprintf(`
name: test-agent-rej
nodes:
  %s:
    type: agent
    model: haiku
    prompt: do something
    outputs: [compile_ok]
    accept:
      - "outputs.compile_ok == true"
`, nodeName)

	store := newTempStore(t)
	runID := seedAgentRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)

	// Backend returns compile_ok=false → predicate fails → node rejected.
	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		successResult(`{"compile_ok": false}`),
	}}
	cfg := Config{ProjectDir: t.TempDir()}
	rc := buildAgentNodeRC(t, store, runID, defn, backend, cfg)
	node := workflow.DispatchNode{Node: nodeName, Type: "agent", Model: "haiku"}

	stop := rc.executeAgentNode(node)

	if stop {
		t.Errorf("expected stop=false after rejection, got true")
	}
	if rc.res.NodesRun != 0 {
		t.Errorf("NodesRun=%d after rejection, want 0", rc.res.NodesRun)
	}
}

// ---------------------------------------------------------------------------
// OnlyWave match — node name matches OnlyWave, stop=true returned.
// ---------------------------------------------------------------------------

func TestExecuteAgentNode_OnlyWaveStop(t *testing.T) {
	const nodeName = "w2-step"
	yamlStr := fmt.Sprintf(`
name: test-agent-onlywave
nodes:
  %s:
    type: agent
    model: haiku
    prompt: do something
`, nodeName)

	store := newTempStore(t)
	runID := seedAgentRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)

	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		successResult(`{"result":"ok"}`),
	}}
	// OnlyWave=2 and node is "w2-step" → detectWave returns 2 → stop=true.
	cfg := Config{ProjectDir: t.TempDir(), OnlyWave: 2}
	rc := buildAgentNodeRC(t, store, runID, defn, backend, cfg)
	node := workflow.DispatchNode{Node: nodeName, Type: "agent", Model: "haiku"}

	stop := rc.executeAgentNode(node)

	if !stop {
		t.Errorf("expected stop=true when OnlyWave matches node wave, got false")
	}
	if rc.res.NodesRun != 1 {
		t.Errorf("NodesRun=%d after OnlyWave stop, want 1", rc.res.NodesRun)
	}
}

// ---------------------------------------------------------------------------
// OnlyWave set but node belongs to a different wave — stop=false.
// ---------------------------------------------------------------------------

func TestExecuteAgentNode_OnlyWaveNoStop(t *testing.T) {
	const nodeName = "w1-step"
	yamlStr := fmt.Sprintf(`
name: test-agent-onlywave-nomatch
nodes:
  %s:
    type: agent
    model: haiku
    prompt: do something
`, nodeName)

	store := newTempStore(t)
	runID := seedAgentRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)

	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		successResult(`{"result":"ok"}`),
	}}
	// OnlyWave=2 but node is "w1-step" → detectWave returns 1 → stop=false.
	cfg := Config{ProjectDir: t.TempDir(), OnlyWave: 2}
	rc := buildAgentNodeRC(t, store, runID, defn, backend, cfg)
	node := workflow.DispatchNode{Node: nodeName, Type: "agent", Model: "haiku"}

	stop := rc.executeAgentNode(node)

	if stop {
		t.Errorf("expected stop=false when OnlyWave does not match node wave, got true")
	}
}

// ---------------------------------------------------------------------------
// Per-node provider: unknown kind — canonicalKind returns "" so the override
// block is skipped and the run-default backend is used.
// ---------------------------------------------------------------------------

func TestExecuteAgentNode_PerNodeProvider_UnknownKind(t *testing.T) {
	const nodeName = "w1-pnp-unknown"
	yamlStr := fmt.Sprintf(`
name: test-agent-pnp-unknown
nodes:
  %s:
    type: agent
    model: haiku
    prompt: do something
    outputs: [result]
`, nodeName)

	store := newTempStore(t)
	runID := seedAgentRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)

	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		successResult(`{"result":"done"}`),
	}}
	cfg := Config{ProjectDir: t.TempDir()}
	rc := buildAgentNodeRC(t, store, runID, defn, backend, cfg)
	// "bogus-xyz" is not a recognised provider kind; canonicalKind returns "".
	// The per-node override block must be skipped and rc.backend (callableBackend)
	// must be used instead.
	node := workflow.DispatchNode{
		Node:     nodeName,
		Type:     "agent",
		Model:    "haiku",
		Provider: "bogus-xyz",
	}

	stop := rc.executeAgentNode(node)

	if stop {
		t.Errorf("expected stop=false with unknown provider kind, got true")
	}
	if rc.res.NodesRun != 1 {
		t.Errorf("NodesRun=%d, want 1", rc.res.NodesRun)
	}
	if backend.calls != 1 {
		t.Errorf("callableBackend.calls=%d, want 1 (run-default must be used for unknown provider kind)", backend.calls)
	}
}

// ---------------------------------------------------------------------------
// Per-node provider: valid kind but NewBackend fails — falls back to the
// run-default backend and the node completes normally.
// ---------------------------------------------------------------------------

func TestExecuteAgentNode_PerNodeProvider_InitFailed_FallsBackToDefault(t *testing.T) {
	const nodeName = "w1-pnp-fallback"
	yamlStr := fmt.Sprintf(`
name: test-agent-pnp-fallback
nodes:
  %s:
    type: agent
    model: haiku
    prompt: do something
    outputs: [result]
`, nodeName)

	store := newTempStore(t)
	runID := seedAgentRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)

	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		successResult(`{"result":"done"}`),
	}}
	cfg := Config{ProjectDir: t.TempDir()}
	rc := buildAgentNodeRC(t, store, runID, defn, backend, cfg)
	// "openai" is a recognised kind, but NewOpenAIBackend requires OPENAI_API_KEY.
	// Clear the var so NewBackend returns an error → the code must log and fall
	// back to the run-default backend (callableBackend).
	t.Setenv("OPENAI_API_KEY", "")
	node := workflow.DispatchNode{
		Node:     nodeName,
		Type:     "agent",
		Model:    "haiku",
		Provider: "openai",
	}

	stop := rc.executeAgentNode(node)

	if stop {
		t.Errorf("expected stop=false after provider-init fallback, got true")
	}
	if rc.res.NodesRun != 1 {
		t.Errorf("NodesRun=%d, want 1", rc.res.NodesRun)
	}
	// Run-default backend must have been used (fallback path).
	if backend.calls != 1 {
		t.Errorf("callableBackend.calls=%d, want 1 (run-default must be used after provider init failure)", backend.calls)
	}
}

// ---------------------------------------------------------------------------
// ForagerName set — Comb forager vantage write is attempted (best-effort,
// must not panic or error-out the node).
// ---------------------------------------------------------------------------

func TestExecuteAgentNode_ForagerName(t *testing.T) {
	const nodeName = "w1-forager"
	yamlStr := fmt.Sprintf(`
name: test-agent-forager
nodes:
  %s:
    type: agent
    model: haiku
    prompt: do something
`, nodeName)

	store := newTempStore(t)
	runID := seedAgentRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)

	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		successResult(`{"result":"forager-done"}`),
	}}
	cfg := Config{ProjectDir: t.TempDir()}
	rc := buildAgentNodeRC(t, store, runID, defn, backend, cfg)
	node := workflow.DispatchNode{
		Node:        nodeName,
		Type:        "agent",
		Model:       "haiku",
		ForagerName: "optimist",
	}

	stop := rc.executeAgentNode(node)

	if stop {
		t.Errorf("expected stop=false with ForagerName set, got true")
	}
	if rc.res.NodesRun != 1 {
		t.Errorf("NodesRun=%d with ForagerName set, want 1", rc.res.NodesRun)
	}
}
