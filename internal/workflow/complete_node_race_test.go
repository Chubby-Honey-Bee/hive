package workflow

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// Two sibling nodes completing at the same time each keep their outputs in
// workflow_runs.state_json: completion is one write transaction that
// re-reads state under the lock, so neither UPDATE drops the other's
// outputs.
func TestCompleteNode_ParallelSiblingsKeepBothOutputs(t *testing.T) {
	store := newWFStore(t)
	repo := store.Workflows()
	const defn = "name: t\nversion: 1\nnodes:\n  a:\n    type: agent\n    prompt: x\n  b:\n    type: agent\n    prompt: y\n"
	for i := 0; i < 100; i++ {
		runID, err := repo.CreateWorkflowRun("t", 1, defn, "{}", []db.NodeSeed{{Name: "a", Type: "agent"}, {Name: "b", Type: "agent"}})
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := CompleteNode(repo, runID, "a", map[string]any{"a": i}); err != nil {
				t.Errorf("complete a: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := CompleteNode(repo, runID, "b", map[string]any{"b": i}); err != nil {
				t.Errorf("complete b: %v", err)
			}
		}()
		wg.Wait()
		run, err := repo.GetWorkflowRun(runID)
		if err != nil {
			t.Fatal(err)
		}
		var state map[string]any
		_ = json.Unmarshal([]byte(run.StateJSON), &state)
		if _, ok := state["a"]; !ok {
			t.Fatalf("iteration %d: node a's output was lost: %s", i, run.StateJSON)
		}
		if _, ok := state["b"]; !ok {
			t.Fatalf("iteration %d: node b's output was lost: %s", i, run.StateJSON)
		}
	}
}
