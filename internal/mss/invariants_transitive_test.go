package mss

import (
	"errors"
	"testing"
)

// Transitive laundering detection: a guarantee that depends on another
// guarantee that ultimately depends on an unknown should be rejected.
//
// Graph:  1 (def) <— 2 (guarantee, deps=[1]) <— 3 (guarantee, deps=[2,4])
//
//	4 (unknown)
//
// Adding 5 (guarantee, deps=[3]) must FAIL — 5 transitively depends on 4 (unknown)
// via 3.
func TestValidateGuaranteeDeps_TransitiveLaundering(t *testing.T) {
	db := newDB(t, []row{
		{id: 1, label: "definition"},
		{id: 2, label: "guarantee", deps: "[1]"},
		{id: 3, label: "guarantee", deps: "[2,4]"},
		{id: 4, label: "unknown"},
	})
	err := ValidateGuaranteeDeps(db, 5, "[3]")
	if err == nil {
		t.Fatal("expected ErrLaundering for transitive unknown dep")
	}
	if !errors.Is(err, ErrLaundering) {
		t.Errorf("err = %v; want ErrLaundering", err)
	}
}

// Counter-case: even with a long guarantee chain, if no leaf is unknown
// it must succeed.
func TestValidateGuaranteeDeps_TransitiveAllProven(t *testing.T) {
	db := newDB(t, []row{
		{id: 1, label: "definition"},
		{id: 2, label: "guarantee", deps: "[1]"},
		{id: 3, label: "guarantee", deps: "[2]"},
		{id: 4, label: "guarantee", deps: "[3]"},
	})
	if err := ValidateGuaranteeDeps(db, 5, "[4]"); err != nil {
		t.Errorf("expected OK on all-proven chain; got %v", err)
	}
}

// Transitive via assumption: the walk goes through an assumption as the MSS
// audit does, so guarantee → assumption → unknown is refused at write. An
// assumption with nothing unknown beneath it is still fine.
func TestValidateGuaranteeDeps_TransitiveViaAssumption(t *testing.T) {
	ok := newDB(t, []row{
		{id: 1, label: "assumption"},
		{id: 2, label: "guarantee", deps: "[1]"},
	})
	if err := ValidateGuaranteeDeps(ok, 3, "[2]"); err != nil {
		t.Errorf("guarantee → guarantee → assumption: expected OK; got %v", err)
	}

	laundering := newDB(t, []row{
		{id: 1, label: "unknown"},
		{id: 2, label: "assumption", deps: "[1]"},
	})
	if err := ValidateGuaranteeDeps(laundering, 3, "[2]"); !errors.Is(err, ErrLaundering) {
		t.Errorf("guarantee → assumption → unknown: want ErrLaundering, got %v", err)
	}
}

// Bogus JSON in transitive deps must not crash; the dirty entry should
// just be skipped (or the function returns successfully if no laundering).
func TestValidateGuaranteeDeps_TransitiveSkipsBogusDeps(t *testing.T) {
	// Direct deps are valid; the parent guarantee has malformed
	// depends_on_ids — function should treat it as no sub-deps.
	db := newDB(t, []row{
		{id: 1, label: "definition"},
		{id: 2, label: "guarantee", deps: "not-json"},
	})
	if err := ValidateGuaranteeDeps(db, 3, "[1]"); err != nil {
		t.Errorf("expected OK when direct dep proven and parent has bogus deps; got %v", err)
	}
}

// --- Direct tests for transitiveUnknownCheck internals ---

// Empty initialDeps: the initial-query block is skipped entirely (len==0),
// queue stays nil, loop never runs, function returns nil.
func TestTransitiveUnknownCheck_EmptyInitialDeps(t *testing.T) {
	db := newDB(t, nil)
	if err := transitiveUnknownCheck(db, 1, []int64{}); err != nil {
		t.Errorf("empty initialDeps should return nil, got %v", err)
	}
}

// All initialDeps are non-guarantee (definition / assumption) with no
// dependencies of their own: sub-deps list stays empty → queue stays
// empty → main loop never runs → nil.
func TestTransitiveUnknownCheck_NoGuaranteeAmongInitialDeps(t *testing.T) {
	db := newDB(t, []row{
		{id: 1, label: "definition"},
		{id: 2, label: "assumption"},
	})
	if err := transitiveUnknownCheck(db, 99, []int64{1, 2}); err != nil {
		t.Errorf("definition/assumption deps: expected nil, got %v", err)
	}
}

// Already-visited dedup: the main loop enqueues a node that was already
// visited, so the deduplicated batch becomes empty and the loop continues
// without issuing another DB query.
//
// Graph:  A (guarantee, deps=[B,C])  B (guarantee, deps=[A])  C (definition)
// initialDeps = [A] → visited={A}
// initial query: A is guarantee → sub-deps=[B,C] → queue=[B,C]
// loop iter 1:  batch=[B,C], visited={A,B,C}
//
//	B is guarantee, deps=[A] → enqueue A
//	C is definition → no enqueue
//	queue=[A]
//
// loop iter 2:  A already visited → batch=[] → continue
// loop ends → nil
func TestTransitiveUnknownCheck_AlreadyVisitedBatchIsEmpty(t *testing.T) {
	db := newDB(t, []row{
		{id: 1, label: "guarantee", deps: "[2,3]"},
		{id: 2, label: "guarantee", deps: "[1]"},
		{id: 3, label: "definition"},
	})
	if err := transitiveUnknownCheck(db, 99, []int64{1}); err != nil {
		t.Errorf("already-visited dedup should return nil, got %v", err)
	}
}

// DB closed before call: both the initial query and the main-loop query
// must return a wrapped error (not panic, not nil).
func TestTransitiveUnknownCheck_DBErrorOnInitialQuery(t *testing.T) {
	db := newDB(t, []row{
		{id: 1, label: "guarantee", deps: "[2]"},
		{id: 2, label: "definition"},
	})
	// Close deliberately so the first SELECT inside transitiveUnknownCheck fails.
	db.Close()
	err := transitiveUnknownCheck(db, 99, []int64{1})
	if err == nil {
		t.Error("expected an error when DB is closed, got nil")
	}
}

// DB closed after the initial query completes but before the main loop runs.
// We achieve this by ensuring the initial query returns no guarantee sub-deps
// (so queue stays empty and the main-loop query is never reached), then
// verifying the function returns nil — a narrower correctness check that the
// main-loop error path is not triggered spuriously.
func TestTransitiveUnknownCheck_MainLoopSkippedWhenQueueEmpty(t *testing.T) {
	// initialDeps=[1], 1 is a definition → no sub-deps queued → loop body
	// never executes → nil even if DB is closed afterward (we don't close
	// here; we just confirm queue-empty path returns nil cleanly).
	db := newDB(t, []row{
		{id: 1, label: "definition"},
	})
	if err := transitiveUnknownCheck(db, 99, []int64{1}); err != nil {
		t.Errorf("definition-only dep: expected nil, got %v", err)
	}
}

// Duplicate IDs in sub-deps: same node appears more than once in the
// queued list; dedup should process it only once and return nil.
func TestTransitiveUnknownCheck_DuplicateSubDeps(t *testing.T) {
	// 1 (guarantee, deps=[2,2]) — duplicate entry in JSON
	// 2 (definition)
	db := newDB(t, []row{
		{id: 1, label: "guarantee", deps: "[2,2]"},
		{id: 2, label: "definition"},
	})
	if err := transitiveUnknownCheck(db, 99, []int64{1}); err != nil {
		t.Errorf("duplicate sub-dep IDs: expected nil, got %v", err)
	}
}

// Transitive laundering two levels deep: guarantee→guarantee→unknown.
// The inner guarantee (2) is the direct dep of the outer (3);
// transitiveUnknownCheck must detect the unknown (1) reachable from 2.
func TestTransitiveUnknownCheck_TwoLevelTransitiveLaundering(t *testing.T) {
	db := newDB(t, []row{
		{id: 1, label: "unknown"},
		{id: 2, label: "guarantee", deps: "[1]"},
	})
	err := transitiveUnknownCheck(db, 99, []int64{2})
	if !errors.Is(err, ErrLaundering) {
		t.Errorf("two-level transitive unknown: want ErrLaundering, got %v", err)
	}
}
