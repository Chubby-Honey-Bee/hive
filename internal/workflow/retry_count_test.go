package workflow

import (
	"fmt"
	"testing"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// MarkNodeRunning bumps `attempt` before the first run, and a node is
// retried up to max_retries times, as the spec says: an `attempt <
// max_retries` check would retry it N−1 times.
func TestFailNode_RetriesExactlyMaxRetriesTimes(t *testing.T) {
	store := newWFStore(t)
	repo := store.Workflows()
	const defn = "name: t\nversion: 1\nnodes:\n  a:\n    type: agent\n    prompt: x\n    max_retries: 1\n"
	runID, err := repo.CreateWorkflowRun("t", 1, defn, "{}", []db.NodeSeed{{Name: "a", Type: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	status := func() string {
		nodes, err := repo.GetWorkflowNodeStates(runID)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range nodes {
			if n.NodeName == "a" {
				return n.Status
			}
		}
		t.Fatal("node a missing")
		return ""
	}
	now := time.Now().UTC().Format(time.RFC3339)

	// First run fails → one retry is owed.
	if err := repo.MarkNodeRunning(runID, "a", now); err != nil {
		t.Fatal(err)
	}
	if err := FailNode(repo, runID, "a", "boom"); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != "pending" {
		t.Fatalf("after first failure with max_retries=1: status %q, want pending (a retry)", got)
	}
	// The retry fails → terminal.
	if err := repo.MarkNodeRunning(runID, "a", now); err != nil {
		t.Fatal(err)
	}
	if err := FailNode(repo, runID, "a", "boom again"); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != "failed" {
		t.Fatalf("after the retry also failed: status %q, want failed", got)
	}
}

// A caller can fail a node it never marked running (`chb workflow fail` on a
// pending node), and that failure counts, so such failures cannot send the
// node back to pending without end.
func TestFailNode_CountsAFailureOnANodeNeverMarkedRunning(t *testing.T) {
	for maxRetries := 0; maxRetries <= 2; maxRetries++ {
		t.Run(fmt.Sprintf("max_retries=%d", maxRetries), func(t *testing.T) {
			repo := newWFStore(t).Workflows()
			defn := fmt.Sprintf("name: t\nversion: 1\nnodes:\n  a:\n    type: agent\n    prompt: x\n    max_retries: %d\n", maxRetries)
			runID, err := repo.CreateWorkflowRun("t", 1, defn, "{}", []db.NodeSeed{{Name: "a", Type: "agent"}})
			if err != nil {
				t.Fatal(err)
			}
			want := maxRetries + 1
			failures := 0
			for nodeStatus(t, repo, runID, "a") != "failed" {
				if failures > want {
					t.Fatalf("still %q after %d failures, want failed after %d", nodeStatus(t, repo, runID, "a"), failures, want)
				}
				if err := FailNode(repo, runID, "a", "boom"); err != nil {
					t.Fatal(err)
				}
				failures++
			}
			if failures != want {
				t.Errorf("failed after %d failures, want %d", failures, want)
			}
		})
	}
}
