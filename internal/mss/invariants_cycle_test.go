package mss

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestCheckCycle_DBQueryError(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.Close() // closed → query fails
	err = CheckCycle(db, 1, Guarantee, []int64{2})
	if err == nil {
		t.Error("expected error from closed DB")
	}
}

func TestCheckCycle_NoExistingGraph(t *testing.T) {
	db := newDB(t, nil)
	if err := CheckCycle(db, 1, Guarantee, []int64{}); err != nil {
		t.Errorf("empty deps + empty graph: expected nil; got %v", err)
	}
}

func TestCheckCycle_MalformedDepsInGraphSkipped(t *testing.T) {
	db := newDB(t, []row{
		{id: 1, label: "guarantee", deps: "not-json"},
		{id: 2, label: "guarantee", deps: "[1]"},
	})
	if err := CheckCycle(db, 3, Guarantee, []int64{2}); err != nil {
		t.Errorf("malformed-deps row should be skipped; got %v", err)
	}
}

func TestCheckCycle_SelfReferenceExtra(t *testing.T) {
	db := newDB(t, nil)
	err := CheckCycle(db, 1, Guarantee, []int64{1})
	if err == nil {
		t.Error("expected cycle for self-reference")
	}
}

func TestCheckCycle_NoCycle(t *testing.T) {
	db := newDB(t, []row{
		{id: 1, label: "definition"},
		{id: 2, label: "guarantee", deps: "[1]"},
	})
	if err := CheckCycle(db, 3, Guarantee, []int64{2}); err != nil {
		t.Errorf("expected no cycle: 3 → 2 → 1; got %v", err)
	}
}
