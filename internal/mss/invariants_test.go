package mss

import (
	"database/sql"
	"errors"
	"testing"

	_ "modernc.org/sqlite"
)

// newDB returns an in-memory DB with a minimal findings table sufficient for
// MSS validation. It seeds the table with the provided rows in order.
func newDB(t *testing.T, rows []row) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(`CREATE TABLE findings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		mss_label TEXT NOT NULL,
		depends_on_ids TEXT
	)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, r := range rows {
		var deps any
		if r.deps != "" {
			deps = r.deps
		}
		if _, err := db.Exec("INSERT INTO findings(id, mss_label, depends_on_ids) VALUES(?,?,?)",
			r.id, r.label, deps); err != nil {
			t.Fatalf("insert id=%d: %v", r.id, err)
		}
	}
	return db
}

type row struct {
	id    int64
	label string
	deps  string // JSON; "" means NULL
}

func TestValidateGuaranteeDeps_EmptyJSON(t *testing.T) {
	db := newDB(t, nil)
	for _, deps := range []string{"", "[]", "null", "not-json"} {
		err := ValidateGuaranteeDeps(db, 1, deps)
		if !errors.Is(err, ErrMissingDeps) {
			t.Errorf("deps=%q: expected ErrMissingDeps, got %v", deps, err)
		}
	}
}

func TestValidateGuaranteeDeps_AllValid(t *testing.T) {
	db := newDB(t, []row{
		{id: 10, label: "definition"},
		{id: 11, label: "assumption"},
	})
	if err := ValidateGuaranteeDeps(db, 99, `[10, 11]`); err != nil {
		t.Errorf("expected valid, got %v", err)
	}
}

func TestValidateGuaranteeDeps_MissingDepID(t *testing.T) {
	db := newDB(t, []row{
		{id: 10, label: "definition"},
	})
	err := ValidateGuaranteeDeps(db, 99, `[10, 999]`)
	if !errors.Is(err, ErrMissingDeps) {
		t.Errorf("expected ErrMissingDeps for non-existent id, got %v", err)
	}
}

func TestValidateGuaranteeDeps_LaunderingFromUnknown(t *testing.T) {
	db := newDB(t, []row{
		{id: 10, label: "definition"},
		{id: 20, label: "unknown"},
	})
	err := ValidateGuaranteeDeps(db, 99, `[10, 20]`)
	if !errors.Is(err, ErrLaundering) {
		t.Fatalf("expected ErrLaundering, got %v", err)
	}
	var mssErr *MSSError
	if !errors.As(err, &mssErr) {
		t.Fatalf("expected *MSSError, got %T", err)
	}
	if mssErr.Label != Guarantee {
		t.Errorf("MSSError.Label = %s, want guarantee", mssErr.Label)
	}
}

func TestValidateGuaranteeDeps_LaunderingViaAssumption_IsAllowed(t *testing.T) {
	// Per the canonical MSS rule (no_laundering only forbids unknown deps);
	// guarantee→assumption chains are allowed (assumptions are honest bets).
	db := newDB(t, []row{
		{id: 10, label: "assumption"},
	})
	if err := ValidateGuaranteeDeps(db, 99, `[10]`); err != nil {
		t.Errorf("guarantee→assumption should be allowed, got %v", err)
	}
}

func TestCheckCycle_Acyclic(t *testing.T) {
	// 100 → 10 → 5 (no cycle)
	db := newDB(t, []row{
		{id: 5, label: "definition"},
		{id: 10, label: "guarantee", deps: `[5]`},
	})
	// Proposed: finding 100 depends on 10 — still acyclic.
	if err := CheckCycle(db, 100, Guarantee, []int64{10}); err != nil {
		t.Errorf("expected no cycle, got %v", err)
	}
}

func TestCheckCycle_DirectCycle(t *testing.T) {
	// 100 → 10, then update 10 to depend on 100 → cycle.
	db := newDB(t, []row{
		{id: 10, label: "guarantee", deps: `[100]`},
	})
	err := CheckCycle(db, 100, Guarantee, []int64{10})
	if !errors.Is(err, ErrCycleDetected) {
		t.Fatalf("expected ErrCycleDetected, got %v", err)
	}
}

func TestCheckCycle_TransitiveCycle(t *testing.T) {
	// 1 → 2 → 3, propose 3 → 1 (transitive cycle)
	db := newDB(t, []row{
		{id: 1, label: "guarantee", deps: `[2]`},
		{id: 2, label: "guarantee", deps: `[3]`},
	})
	err := CheckCycle(db, 3, Guarantee, []int64{1})
	if !errors.Is(err, ErrCycleDetected) {
		t.Errorf("expected transitive cycle to be detected, got %v", err)
	}
}

func TestCheckCycle_SelfReference(t *testing.T) {
	db := newDB(t, nil)
	if err := CheckCycle(db, 1, Guarantee, []int64{1}); !errors.Is(err, ErrCycleDetected) {
		t.Errorf("self-reference should be a cycle, got %v", err)
	}
}

func TestCheckCycle_NoDepsIsAcyclic(t *testing.T) {
	db := newDB(t, nil)
	if err := CheckCycle(db, 1, Guarantee, nil); err != nil {
		t.Errorf("empty deps must not produce a cycle, got %v", err)
	}
}
