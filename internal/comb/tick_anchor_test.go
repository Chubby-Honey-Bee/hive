package comb_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/comb"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// A revision written while a tick is open is anchored to it, which is what
// AtTick reads for `comb at --tick`.
func TestCombRevision_AnchoredToOpenTick(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tick.db")
	store, err := db.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}

	tickID, err := store.TimeWheel().Begin("run-1", db.TickSwarm, 0, 0, "test") // run_id 0 → NULL; the FK needs a real run
	if err != nil {
		t.Fatal(err)
	}
	if err := comb.BuildForagerVantage(context.Background(), store, 0, "optimist", `{"verdict":"support"}`,
		map[string]any{"verdict": "support", "recommendation": "ship"}); err != nil {
		t.Fatal(err)
	}

	rev, err := store.CombRevisions().AtTick("forager:optimist", tickID)
	if err != nil {
		t.Fatal(err)
	}
	if rev == nil {
		t.Fatal("no revision anchored to the open tick — `comb at --tick` would find nothing")
	}
	if !rev.TickID.Valid || rev.TickID.Int64 != tickID {
		t.Fatalf("revision tick_id = %+v, want %d", rev.TickID, tickID)
	}

	// AtTick's documented fallback (no anchored revision → latest revision
	// at or before the tick's start) needs the two timestamp formats to be
	// comparable: time_wheel uses SQLite's CURRENT_TIMESTAMP, comb_revisions
	// uses RFC3339, and 'T' sorts above ' '.
	empty, err := store.TimeWheel().Begin("run-empty", db.TickSwarm, 0, 0, "no revisions inside")
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := store.CombRevisions().AtTick("forager:optimist", empty)
	if err != nil {
		t.Fatal(err)
	}
	if fallback == nil {
		t.Fatal("fallback found nothing: the earlier revision predates this tick and should be returned")
	}

	ticks, err := store.TimeWheel().Recent(db.TickSwarm, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ticks) != 2 {
		t.Fatalf("comb wheel would list %d swarm ticks, want the 2 this test opened", len(ticks))
	}
}

// Two runs with open ticks: a verdict from the first run anchors to the first
// run's tick, not to the newer tick the second run opened.
func TestCombRevision_AnchoredToOwnRunTick(t *testing.T) {
	store, err := db.NewStore(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	var runs, ticks [2]int64
	for i := range runs {
		runs[i], err = store.Workflows().CreateWorkflowRun("swarm", 1, "name: swarm\nnodes: {}\n", "{}", nil)
		if err != nil {
			t.Fatal(err)
		}
		ticks[i], err = store.TimeWheel().Begin(fmt.Sprintf("run-%d", runs[i]), db.TickSwarm, runs[i], 0, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := comb.BuildForagerVantage(context.Background(), store, runs[0], "optimist", "",
		map[string]any{"verdict": "support", "recommendation": "ship"}); err != nil {
		t.Fatal(err)
	}
	rev, err := store.CombRevisions().AtTick("forager:optimist", ticks[0])
	if err != nil {
		t.Fatal(err)
	}
	if rev == nil || !rev.TickID.Valid || rev.TickID.Int64 != ticks[0] {
		t.Fatalf("revision = %+v, want tick_id %d (run %d's tick)", rev, ticks[0], runs[0])
	}
}
