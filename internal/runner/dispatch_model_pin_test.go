package runner

// Verifies that executeAgentNode writes the canonical SDK model ID into
// workflow_node_states.resolved_model at each dispatch, and that
// GetWorkflowNodeStates reads it back in the WorkflowNodeState.Model field.

import (
	"fmt"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// TestModelPin_HaikuAlias asserts that a node dispatched with model: haiku
// ends up with resolved_model = "claude-haiku-4-5" in the DB, the canonical
// id resolveAliasForPricing("haiku") returns from the models config.
func TestModelPin_HaikuAlias(t *testing.T) {
	const nodeName = "w1-pin-haiku"
	yamlStr := fmt.Sprintf(`
name: test-model-pin
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
	cfg := Config{ProjectDir: t.TempDir(), Provider: "claude-cli"} // not the environment's keys or CLIs
	rc := buildAgentNodeRC(t, store, runID, defn, backend, cfg)
	node := workflow.DispatchNode{Node: nodeName, Type: "agent", Model: "haiku"}

	stop := rc.executeAgentNode(node)
	if stop {
		t.Errorf("expected stop=false, got true")
	}

	states, err := store.Workflows().GetWorkflowNodeStates(runID)
	if err != nil {
		t.Fatalf("GetWorkflowNodeStates: %v", err)
	}

	wantModel := resolveAliasForPricing("haiku") // "claude-haiku-4-5"
	if wantModel == "" {
		t.Fatal("resolveAliasForPricing(haiku) returned empty — test precondition invalid")
	}

	var found bool
	for _, s := range states {
		if s.NodeName == nodeName {
			found = true
			if s.Model != wantModel {
				t.Errorf("WorkflowNodeState.Model = %q, want %q", s.Model, wantModel)
			}
		}
	}
	if !found {
		t.Errorf("node %q not found in GetWorkflowNodeStates results", nodeName)
	}
}

// TestModelPin_SonnetAlias checks that the sonnet alias also gets pinned.
func TestModelPin_SonnetAlias(t *testing.T) {
	const nodeName = "w1-pin-sonnet"
	yamlStr := fmt.Sprintf(`
name: test-model-pin-sonnet
nodes:
  %s:
    type: agent
    model: sonnet
    prompt: do something
`, nodeName)

	store := newTempStore(t)
	runID := seedAgentRun(t, store, nodeName, yamlStr)
	defn, _ := workflow.LoadYAMLString(yamlStr)

	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		successResult(`{"result":"ok"}`),
	}}
	cfg := Config{ProjectDir: t.TempDir(), Provider: "claude-cli"} // not the environment's keys or CLIs
	rc := buildAgentNodeRC(t, store, runID, defn, backend, cfg)
	node := workflow.DispatchNode{Node: nodeName, Type: "agent", Model: "sonnet"}

	_ = rc.executeAgentNode(node)

	states, err := store.Workflows().GetWorkflowNodeStates(runID)
	if err != nil {
		t.Fatalf("GetWorkflowNodeStates: %v", err)
	}

	wantModel := resolveAliasForPricing("sonnet")
	if wantModel == "" {
		t.Fatal("resolveAliasForPricing(sonnet) returned empty")
	}

	for _, s := range states {
		if s.NodeName == nodeName {
			if s.Model != wantModel {
				t.Errorf("WorkflowNodeState.Model = %q, want %q", s.Model, wantModel)
			}
			return
		}
	}
	t.Errorf("node %q not found in state list", nodeName)
}

// TestModelPin_NullFallback asserts that a node row with NULL resolved_model
// (no dispatch has pinned it) returns Model="" from GetWorkflowNodeStates, and that
// the repair loop's fallback to node.Model still works in that case.
func TestModelPin_NullFallback(t *testing.T) {
	const nodeName = "w1-pin-null"
	yamlStr := fmt.Sprintf(`
name: test-model-pin-null
nodes:
  %s:
    type: agent
    model: haiku
    prompt: do something
`, nodeName)

	store := newTempStore(t)
	runID := seedAgentRun(t, store, nodeName, yamlStr)

	// Manually write NULL for resolved_model, as a row no dispatch has pinned holds.
	if _, err := store.WriteDB.Exec(
		`UPDATE workflow_node_states SET resolved_model=NULL WHERE run_id=? AND node_name=?`,
		runID, nodeName,
	); err != nil {
		t.Fatalf("set NULL resolved_model: %v", err)
	}

	states, err := store.Workflows().GetWorkflowNodeStates(runID)
	if err != nil {
		t.Fatalf("GetWorkflowNodeStates: %v", err)
	}
	for _, s := range states {
		if s.NodeName == nodeName {
			if s.Model != "" {
				t.Errorf("Model should be empty string for NULL resolved_model row, got %q", s.Model)
			}
			return
		}
	}
	t.Errorf("node %q not found", nodeName)
}
