package workflow

import (
	"testing"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// TestGetNextNodes_TerminalRunMarksCompleted exercises finalizeIfTerminal:
// every node is completed → finalizeIfTerminal flips the run to "completed"
// and returns no dispatches.
func TestGetNextNodes_TerminalRunMarksCompleted(t *testing.T) {
	store := newWFStore(t)
	yaml := `name: t
nodes:
  a:
    type: agent
    prompt: "go"
`
	id, err := store.Workflows().CreateWorkflowRun(
		"t", 1, yaml, `{}`, []db.NodeSeed{{Name: "a", Type: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := store.Workflows().MarkNodeCompleted(id, "a", `{"out":"v"}`, now); err != nil {
		t.Fatal(err)
	}

	got, err := GetNextNodes(store.Workflows(), id)
	if err != nil {
		t.Fatalf("GetNextNodes: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no dispatch (terminal); got %v", got)
	}

	// Run should have been finalized.
	run, err := store.Workflows().GetWorkflowRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "completed" {
		t.Errorf("status = %q; want completed", run.Status)
	}
}

// TestGetNextNodes_PendingNodeWithSkippedFinalizes covers the path where
// every reachable node is skipped/completed — run finalizes anyway.
func TestGetNextNodes_AllSkippedFinalizes(t *testing.T) {
	store := newWFStore(t)
	yaml := `name: t
nodes:
  a:
    type: agent
    prompt: "go"
`
	id, err := store.Workflows().CreateWorkflowRun(
		"t", 1, yaml, `{}`, []db.NodeSeed{{Name: "a", Type: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := store.Workflows().MarkNodeSkipped(id, "a", now); err != nil {
		t.Fatal(err)
	}

	if _, err := GetNextNodes(store.Workflows(), id); err != nil {
		t.Fatalf("GetNextNodes: %v", err)
	}

	run, _ := store.Workflows().GetWorkflowRun(id)
	if run.Status != "completed" {
		t.Errorf("status = %q; want completed", run.Status)
	}
}

// TestGetNextNodes_RunningPredecessorBlocksDispatch covers loadNodeStateMap
// when a pending node has a running predecessor — it must NOT be dispatched.
func TestGetNextNodes_RunningPredecessorBlocks(t *testing.T) {
	store := newWFStore(t)
	yaml := `name: t
nodes:
  a:
    type: agent
    prompt: "go"
  b:
    type: agent
    prompt: "go"
edges:
  - {from: a, to: b}
`
	id, err := store.Workflows().CreateWorkflowRun(
		"t", 1, yaml, `{}`, []db.NodeSeed{
			{Name: "a", Type: "agent"},
			{Name: "b", Type: "agent"},
		})
	if err != nil {
		t.Fatal(err)
	}

	// Mark 'a' running. 'b' should NOT be dispatched.
	if _, err := store.WriteDB.Exec(
		`UPDATE workflow_node_states SET status='running' WHERE run_id=? AND node_name='a'`,
		id,
	); err != nil {
		t.Fatal(err)
	}

	got, err := GetNextNodes(store.Workflows(), id)
	if err != nil {
		t.Fatalf("GetNextNodes: %v", err)
	}
	for _, n := range got {
		if n.Node == "b" {
			t.Error("b should not be ready while a is running")
		}
	}
}
