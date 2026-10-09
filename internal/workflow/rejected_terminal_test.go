package workflow

import (
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// A rejected node ends the run: it is terminal, so a run with one, such as a
// swarm run whose queen her own accept: predicate rejects, does not sit in
// status='running' with nothing able to move it.
func TestFinalize_RejectedNodeEndsTheRun(t *testing.T) {
	store := newWFStore(t)
	repo := store.Workflows()
	const defn = "name: t\nversion: 1\nnodes:\n  a:\n    type: agent\n    prompt: x\n  b:\n    type: agent\n    prompt: y\n"
	runID, err := repo.CreateWorkflowRun("t", 1, defn, "{}", []db.NodeSeed{{Name: "a", Type: "agent"}, {Name: "b", Type: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := CompleteNode(repo, runID, "a", map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkNodeRejected(runID, "b", "accept rejected: verdict missing", "2026-09-16T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	// Nothing left to dispatch; the run must reach a terminal state.
	if _, err := GetNextNodes(repo, runID); err != nil {
		t.Fatal(err)
	}
	run, err := repo.GetWorkflowRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status == "running" {
		t.Fatal("a run whose only remaining node is rejected is stranded in 'running'")
	}
	// Salvaged, not clean: a rejected node means the run did not do what it
	// was asked, and a caller should be able to tell.
	if run.Status != "failed" {
		t.Errorf("run status = %q, want failed", run.Status)
	}
}

// A run where everything completed is still reported completed.
func TestFinalize_AllCompletedIsStillCompleted(t *testing.T) {
	store := newWFStore(t)
	repo := store.Workflows()
	const defn = "name: t\nversion: 1\nnodes:\n  a:\n    type: agent\n    prompt: x\n"
	runID, err := repo.CreateWorkflowRun("t", 1, defn, "{}", []db.NodeSeed{{Name: "a", Type: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := CompleteNode(repo, runID, "a", map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := GetNextNodes(repo, runID); err != nil {
		t.Fatal(err)
	}
	run, _ := repo.GetWorkflowRun(runID)
	if run.Status != "completed" {
		t.Errorf("run status = %q, want completed", run.Status)
	}
}
