package workflow

import (
	"sync"
	"testing"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// barrierRepo holds a driver's first claim until every driver has read the
// pending set: the interleaving two drivers of one run produce when both
// read before either claims.
type barrierRepo struct {
	*db.WorkflowsRepo
	arrived *sync.WaitGroup
	first   sync.Once
}

func (r *barrierRepo) MarkNodeRunning(runID int64, nodeName, startedAt string) error {
	r.first.Do(func() {
		r.arrived.Done()
		done := make(chan struct{})
		go func() { r.arrived.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	})
	return r.WorkflowsRepo.MarkNodeRunning(runID, nodeName, startedAt)
}

// Two drivers of one run (two `agent-run --resume N`, or agent-run beside
// `chb workflow next`) that read the same pending nodes claim each one once:
// the claim is taken only from pending, so the second driver's claim of a
// node the first has taken changes nothing and it does not dispatch it.
func TestGetNextNodes_TwoDriversClaimEachNodeOnce(t *testing.T) {
	store := newWFStore(t)
	const yaml = `name: t
nodes:
  a:
    type: agent
    prompt: a
  b:
    type: agent
    prompt: b
`
	id, err := store.Workflows().CreateWorkflowRun("t", 1, yaml, `{}`,
		[]db.NodeSeed{{Name: "a", Type: "agent"}, {Name: "b", Type: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	var arrived sync.WaitGroup
	arrived.Add(2)
	got := make([][]DispatchNode, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], errs[i] = GetNextNodes(&barrierRepo{WorkflowsRepo: store.Workflows(), arrived: &arrived}, id)
		}()
	}
	wg.Wait()
	claimed := map[string]int{}
	for i := range 2 {
		if errs[i] != nil {
			t.Fatalf("driver %d: %v", i, errs[i])
		}
		for _, d := range got[i] {
			claimed[d.Node]++
		}
	}
	for _, name := range []string{"a", "b"} {
		if claimed[name] != 1 {
			t.Errorf("node %s was handed out %d times across the two drivers, want once", name, claimed[name])
		}
	}
	states, err := store.Workflows().GetWorkflowNodeStates(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range states {
		if s.Status != "running" || s.Attempt != 1 {
			t.Errorf("node %s is %s at attempt %d, want running at attempt 1", s.NodeName, s.Status, s.Attempt)
		}
	}
}
