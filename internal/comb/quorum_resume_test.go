package comb

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

func verdictEvent(runID int64, forager, verdict string) Event {
	return Event{
		Kind:        EventVantageWritten,
		VantageKey:  "forager:" + forager,
		VantageKind: string(db.VantageForager),
		RunID:       runID,
		Payload:     map[string]any{"forager": forager, "verdict": verdict},
	}
}

// firedByPair counts a run's fired resonates bonds per pair, either way round.
func firedByPair(t *testing.T, store *db.Store, runID int64) map[string]int {
	t.Helper()
	bonds, err := store.ForagerBonds().ListByRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	n := map[string]int{}
	for _, b := range bonds {
		if b.Kind == db.BondResonates && b.Fired {
			n[pairKey(b.From, b.To)]++
		}
	}
	return n
}

// Start's channel closes once the sensor has drained, so a caller that waits
// on it finds every ∇ its buffered verdicts completed already recorded.
func TestQuorumSensor_DoneClosesAfterTheDrain(t *testing.T) {
	store := quorumStore(t)
	bus := NewEventBus()
	q := NewQuorumSensor(store, bus)
	q.Register("optimist", "pragmatist")
	ctx, cancel := context.WithCancel(context.Background())
	done := q.Start(ctx)
	for _, f := range []string{"optimist", "pragmatist"} {
		bus.Publish(context.Background(), verdictEvent(0, f, "support"))
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sensor never finished")
	}
	var n int
	if err := store.ReadDB.QueryRow(`SELECT COUNT(*) FROM forager_bonds WHERE fired = 1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d fired bonds once the sensor was done, want 1", n)
	}
}

// A resumed run's sensor starts from the verdicts its foragers wrote before
// the interruption, and from the bonds that already fired in it, so a pair
// whose verdicts straddle the interruption fires, and a pair that fired
// before it does not fire twice.
func TestQuorumSensor_ResumeSeedsVerdictsAndFiredBonds(t *testing.T) {
	store := quorumStore(t)
	var runs [2]int64
	for i := range runs {
		id, err := store.Workflows().CreateWorkflowRun("swarm", 1, "name: swarm\nnodes: {}\n", "{}", nil)
		if err != nil {
			t.Fatal(err)
		}
		runs[i] = id
		if _, err := store.TimeWheel().Begin(fmt.Sprintf("run-%d", id), db.TickSwarm, id, 0, ""); err != nil {
			t.Fatal(err)
		}
	}
	run, other := runs[0], runs[1]
	write := func(runID int64, forager, verdict string) {
		t.Helper()
		raw := "```json\n{\"verdict\":\"" + verdict + "\"}\n```"
		if err := BuildForagerVantage(context.Background(), store, runID, forager, raw, map[string]any{"verdict": verdict}); err != nil {
			t.Fatal(err)
		}
	}
	// Before the interruption: optimist supports in this run. Another run's
	// later verdict for optimist must not leak into this run's state.
	write(run, "optimist", "support")
	write(other, "optimist", "oppose")
	// c and d already converged and fired in this run.
	if _, err := store.ForagerBonds().RecordFired(run, "c", "d", db.BondResonates, 1, nil); err != nil {
		t.Fatal(err)
	}

	bus := NewEventBus()
	q := NewQuorumSensor(store, bus)
	q.SetRunID(run)
	q.Register("optimist", "pragmatist")
	q.Register("c", "d")
	if err := q.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := q.Start(ctx)
	// After the resume: pragmatist matches optimist; c and d converge again.
	latest := map[string]string{"optimist": "support", "pragmatist": "support", "c": "support", "d": "support"}
	for _, f := range []string{"pragmatist", "c", "d"} {
		bus.Publish(context.Background(), verdictEvent(run, f, latest[f]))
	}
	cancel()
	<-done

	// Each converging pair fires exactly once in the run: the pair that
	// straddles the resume once, the pair that had fired not again. A total
	// count let the two errors cancel.
	fired := firedByPair(t, store, run)
	for _, pair := range [][2]string{{"optimist", "pragmatist"}, {"c", "d"}} {
		a, b := latest[pair[0]], latest[pair[1]]
		want := 0
		if a == b && a != "" && a != "abstain" {
			want = 1
		}
		if got := fired[pairKey(pair[0], pair[1])]; got != want {
			t.Errorf("%s-%s fired %d time(s) in run %d, want %d", pair[0], pair[1], got, run, want)
		}
	}
}
