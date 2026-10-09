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

// seedExecuteNodeRun creates a workflow run seeded with a single node of the
// given type. The YAML definition uses `type: agent` as a placeholder so
// workflow.LoadYAMLString parses it without complaint; the node type stored in
// the DB (nodeType) is what executeNode branches on via the DispatchNode it
// receives.
func seedExecuteNodeRun(t *testing.T, store *db.Store, nodeName, nodeType, yamlStr string) int64 {
	t.Helper()
	runID, err := store.Workflows().CreateWorkflowRun(
		"execute-node-test", 1, yamlStr, "{}",
		[]db.NodeSeed{{Name: nodeName, Type: nodeType}},
	)
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	return runID
}

// buildExecuteNodeRC builds a minimal runtimeContext for executeNode tests.
func buildExecuteNodeRC(t *testing.T, store *db.Store, runID int64, defn map[string]any, backend LLMBackend) *runtimeContext {
	t.Helper()
	return &runtimeContext{
		ctx:        context.Background(),
		cfg:        Config{ProjectDir: t.TempDir()},
		store:      store,
		backend:    backend,
		gc:         &GitCommitter{Enabled: false},
		runID:      runID,
		logf:       makeLogf(&bytes.Buffer{}),
		parsedDefn: defn,
		res:        &Result{},
		resMu:      sync.Mutex{},
		// events nil → emit() is a safe no-op
	}
}

// ---------------------------------------------------------------------------
// "agent" type — executeNode routes to executeAgentNode (happy path).
// ---------------------------------------------------------------------------

func TestExecuteNode_AgentType(t *testing.T) {
	const nodeName = "en-agent"
	yamlStr := fmt.Sprintf(`
name: test-execute-node-agent
nodes:
  %s:
    type: agent
    model: haiku
    prompt: do something
    outputs: [result]
`, nodeName)

	store := newTempStore(t)
	runID := seedExecuteNodeRun(t, store, nodeName, "agent", yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)

	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		successResult(`{"result":"done"}`),
	}}
	rc := buildExecuteNodeRC(t, store, runID, defn, backend)
	node := workflow.DispatchNode{Node: nodeName, Type: "agent", Model: "haiku"}

	stop := rc.executeNode(node)

	if stop {
		t.Errorf("expected stop=false for agent node, got true")
	}
	if rc.res.NodesRun != 1 {
		t.Errorf("NodesRun=%d, want 1", rc.res.NodesRun)
	}
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
		t.Errorf("node %q not completed after executeNode(agent); states=%v", nodeName, states)
	}
}

// ---------------------------------------------------------------------------
// "parallel_fan" type — executeNode routes to executeAgentNode.
// ---------------------------------------------------------------------------

func TestExecuteNode_ParallelFanType(t *testing.T) {
	const nodeName = "en-pfan"
	yamlStr := fmt.Sprintf(`
name: test-execute-node-pfan
nodes:
  %s:
    type: parallel_fan
    model: haiku
    prompt: do something
    outputs: [result]
`, nodeName)

	store := newTempStore(t)
	runID := seedExecuteNodeRun(t, store, nodeName, "parallel_fan", yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)

	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		successResult(`{"result":"fan-done"}`),
	}}
	rc := buildExecuteNodeRC(t, store, runID, defn, backend)
	node := workflow.DispatchNode{Node: nodeName, Type: "parallel_fan", Model: "haiku"}

	stop := rc.executeNode(node)

	if stop {
		t.Errorf("expected stop=false for parallel_fan node, got true")
	}
	if rc.res.NodesRun != 1 {
		t.Errorf("NodesRun=%d, want 1", rc.res.NodesRun)
	}
}

// ---------------------------------------------------------------------------
// default branch — non-LLM node types are auto-completed without calling LLM.
// ---------------------------------------------------------------------------

func TestExecuteNode_DefaultType_AutoComplete(t *testing.T) {
	// human_review is deliberately absent: it parks the run for a human
	// rather than completing. Its own contract is
	// TestExecuteNode_HumanReview_PausesInsteadOfCompleting below.
	cases := []string{"decision", "unknown_type"}
	for _, nodeType := range cases {
		nodeType := nodeType
		t.Run(nodeType, func(t *testing.T) {
			nodeName := "en-" + nodeType
			yamlStr := fmt.Sprintf(`
name: test-execute-node-default
nodes:
  %s:
    type: %s
`, nodeName, nodeType)

			store := newTempStore(t)
			runID := seedExecuteNodeRun(t, store, nodeName, nodeType, yamlStr)
			defn, _ := workflow.LoadYAMLString(yamlStr)

			backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
				errorResult("backend must not be called for non-LLM node"),
			}}
			rc := buildExecuteNodeRC(t, store, runID, defn, backend)
			node := workflow.DispatchNode{Node: nodeName, Type: nodeType}

			stop := rc.executeNode(node)

			if stop {
				t.Errorf("expected stop=false for %s node, got true", nodeType)
			}
			// NodesRun must NOT be incremented for auto-completed nodes.
			if rc.res.NodesRun != 0 {
				t.Errorf("NodesRun=%d for %s node, want 0", rc.res.NodesRun, nodeType)
			}
			// Backend must not have been called.
			if backend.calls != 0 {
				t.Errorf("backend was called %d time(s) for non-LLM node type %s, want 0", backend.calls, nodeType)
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
				t.Errorf("node %q not auto-completed for type %s; states=%v", nodeName, nodeType, states)
			}
		})
	}
}

// A human_review node parks: no backend call, node waiting_human, run
// paused, and the dispatcher stops.
func TestExecuteNode_HumanReview_PausesInsteadOfCompleting(t *testing.T) {
	const nodeName = "en-human_review"
	yamlStr := `
name: test-execute-node-human
nodes:
  ` + nodeName + `:
    type: human_review
`
	store := newTempStore(t)
	runID := seedExecuteNodeRun(t, store, nodeName, "human_review", yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)
	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		errorResult("backend must not be called for a human_review node"),
	}}
	rc := buildExecuteNodeRC(t, store, runID, defn, backend)

	stop := rc.executeNode(workflow.DispatchNode{Node: nodeName, Type: "human_review"})

	if !stop {
		t.Error("executeNode must stop the dispatch loop at a human checkpoint")
	}
	if backend.calls != 0 {
		t.Errorf("backend called %d time(s) for a human_review node", backend.calls)
	}
	states, err := store.Workflows().GetWorkflowNodeStates(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range states {
		if s.NodeName == nodeName && s.Status != "waiting_human" {
			t.Errorf("node status = %q, want waiting_human", s.Status)
		}
	}
	run, err := store.Workflows().GetWorkflowRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "paused" {
		t.Errorf("run status = %q, want paused", run.Status)
	}
}
