package db

import (
	"database/sql"
	"strings"
	"testing"
)

// TestQueryConflicts_HappyPathAll verifies that conflicts inserted via AddConflict
// are returned by QueryConflicts with unresolvedOnly=false.
func TestQueryConflicts_HappyPathAll(t *testing.T) {
	s := newTestStore(t)
	repo := s.Conflicts()

	idA := addTestFinding(t, s, "definition", "finding A", nil)
	idB := addTestFinding(t, s, "definition", "finding B", nil)

	if err := repo.AddConflict(1, idA, idB, "they disagree"); err != nil {
		t.Fatalf("AddConflict: %v", err)
	}

	conflicts, err := repo.QueryConflicts(false)
	if err != nil {
		t.Fatalf("QueryConflicts(false): %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(conflicts))
	}
	c := conflicts[0]
	if c.Wave != 1 {
		t.Errorf("expected wave=1, got %d", c.Wave)
	}
	if c.FindingAID != idA {
		t.Errorf("expected FindingAID=%d, got %d", idA, c.FindingAID)
	}
	if c.FindingBID != idB {
		t.Errorf("expected FindingBID=%d, got %d", idB, c.FindingBID)
	}
	if c.Description != "they disagree" {
		t.Errorf("expected description=%q, got %q", "they disagree", c.Description)
	}
	if c.Resolution != nil {
		t.Errorf("expected Resolution=nil on a fresh conflict, got %v", c.Resolution)
	}
}

// TestQueryConflicts_UnresolvedOnly_ExcludesResolved verifies that resolved
// conflicts are excluded when unresolvedOnly=true.
func TestQueryConflicts_UnresolvedOnly_ExcludesResolved(t *testing.T) {
	s := newTestStore(t)
	repo := s.Conflicts()

	idA := addTestFinding(t, s, "definition", "finding A", nil)
	idB := addTestFinding(t, s, "definition", "finding B", nil)
	idC := addTestFinding(t, s, "definition", "finding C", nil)

	// Conflict 1 — will stay unresolved.
	if err := repo.AddConflict(1, idA, idB, "open conflict"); err != nil {
		t.Fatalf("AddConflict 1: %v", err)
	}
	// Conflict 2 — will be manually resolved.
	if err := repo.AddConflict(1, idA, idC, "resolved conflict"); err != nil {
		t.Fatalf("AddConflict 2: %v", err)
	}
	if _, err := s.WriteDB.Exec(
		"UPDATE conflicts SET resolution='A wins', resolved_by_wave=2 WHERE description='resolved conflict'",
	); err != nil {
		t.Fatalf("manual resolve: %v", err)
	}

	// unresolvedOnly=true should return only conflict 1.
	conflicts, err := repo.QueryConflicts(true)
	if err != nil {
		t.Fatalf("QueryConflicts(true): %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 unresolved conflict, got %d", len(conflicts))
	}
	if conflicts[0].Description != "open conflict" {
		t.Errorf("expected 'open conflict', got %q", conflicts[0].Description)
	}
}

// TestQueryConflicts_UnresolvedOnly_IncludesAll verifies that all conflicts
// (resolved + unresolved) are returned when unresolvedOnly=false.
func TestQueryConflicts_UnresolvedOnly_IncludesAll(t *testing.T) {
	s := newTestStore(t)
	repo := s.Conflicts()

	idA := addTestFinding(t, s, "definition", "A", nil)
	idB := addTestFinding(t, s, "definition", "B", nil)
	idC := addTestFinding(t, s, "definition", "C", nil)

	if err := repo.AddConflict(1, idA, idB, "open"); err != nil {
		t.Fatalf("AddConflict open: %v", err)
	}
	if err := repo.AddConflict(1, idA, idC, "closed"); err != nil {
		t.Fatalf("AddConflict closed: %v", err)
	}
	if _, err := s.WriteDB.Exec(
		"UPDATE conflicts SET resolution='resolved' WHERE description='closed'",
	); err != nil {
		t.Fatalf("manual resolve: %v", err)
	}

	conflicts, err := repo.QueryConflicts(false)
	if err != nil {
		t.Fatalf("QueryConflicts(false): %v", err)
	}
	if len(conflicts) != 2 {
		t.Errorf("expected 2 conflicts, got %d", len(conflicts))
	}
}

// TestQueryConflicts_EmptyTable verifies that an empty conflicts table
// returns an empty slice without error.
func TestQueryConflicts_EmptyTable(t *testing.T) {
	s := newTestStore(t)
	repo := s.Conflicts()

	for _, unresolvedOnly := range []bool{false, true} {
		conflicts, err := repo.QueryConflicts(unresolvedOnly)
		if err != nil {
			t.Fatalf("QueryConflicts(%v) on empty table: %v", unresolvedOnly, err)
		}
		if len(conflicts) != 0 {
			t.Errorf("expected 0 conflicts, got %d", len(conflicts))
		}
	}
}

// TestAddConflict_TableDriven exercises AddConflict's happy path and error
// path (FK violation) in a table-driven style.
func TestAddConflict_TableDriven(t *testing.T) {
	s := newTestStore(t)
	repo := s.Conflicts()

	idA := addTestFinding(t, s, "definition", "finding A", nil)
	idB := addTestFinding(t, s, "definition", "finding B", nil)

	tests := []struct {
		name        string
		wave        int
		findingAID  int64
		findingBID  int64
		description string
		wantErr     bool
	}{
		{
			name:        "happy path valid findings",
			wave:        1,
			findingAID:  idA,
			findingBID:  idB,
			description: "they disagree",
			wantErr:     false,
		},
		{
			name:        "error path nonexistent finding_a_id",
			wave:        2,
			findingAID:  999999,
			findingBID:  idB,
			description: "bad ref A",
			wantErr:     true,
		},
		{
			name:        "error path nonexistent finding_b_id",
			wave:        2,
			findingAID:  idA,
			findingBID:  999998,
			description: "bad ref B",
			wantErr:     true,
		},
		{
			name:        "error path both finding IDs nonexistent",
			wave:        2,
			findingAID:  999997,
			findingBID:  999996,
			description: "both refs bad",
			wantErr:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := repo.AddConflict(tc.wave, tc.findingAID, tc.findingBID, tc.description)
			if tc.wantErr && err == nil {
				t.Errorf("expected error for %q, got nil", tc.name)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error for %q: %v", tc.name, err)
			}
		})
	}
}

// TestQueryConflicts_ReadDBError verifies that a closed readDB causes QueryConflicts
// to return a non-nil error (covers the rows.Query error branch).
func TestQueryConflicts_ReadDBError(t *testing.T) {
	badDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	badDB.Close() // force all queries to fail

	repo := NewConflictsRepo(badDB, badDB)
	for _, unresolvedOnly := range []bool{false, true} {
		_, err := repo.QueryConflicts(unresolvedOnly)
		if err == nil {
			t.Errorf("QueryConflicts(%v) with closed DB: expected error, got nil", unresolvedOnly)
		}
	}
}

// Resolve closes a conflict once, with a stated resolution, and names why it
// refuses.
func TestResolveConflict_ClosesOnceWithAResolution(t *testing.T) {
	s := newTestStore(t)
	repo := s.Conflicts()
	idA := addTestFinding(t, s, "definition", "finding A", nil)
	idB := addTestFinding(t, s, "definition", "finding B", nil)
	if err := repo.AddConflict(1, idA, idB, "[negation] they disagree"); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := s.ReadDB.QueryRow("SELECT id FROM conflicts").Scan(&id); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name       string
		conflict   int64
		resolution string
		errHas     string
	}{
		{"unknown conflict", id + 1, "settled", "no such conflict"},
		{"empty resolution", id, "  ", "resolution is required"},
	} {
		if err := repo.Resolve(tc.conflict, 2, tc.resolution); err == nil || !strings.Contains(err.Error(), tc.errHas) {
			t.Errorf("%s: err = %v, want one containing %q", tc.name, err, tc.errHas)
		}
	}

	const resolution = "finding A survives"
	if err := repo.Resolve(id, 2, resolution); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	open, err := repo.QueryConflicts(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 0 {
		t.Errorf("%d conflicts still unresolved, want 0", len(open))
	}
	all, err := repo.QueryConflicts(false)
	if err != nil {
		t.Fatal(err)
	}
	if got := all[0]; got.Resolution == nil || *got.Resolution != resolution || got.ResolvedByWave == nil || *got.ResolvedByWave != 2 {
		t.Errorf("resolution=%v resolved_by_wave=%v, want %q and 2", got.Resolution, got.ResolvedByWave, resolution)
	}

	if err := repo.Resolve(id, 3, "again"); err == nil || !strings.Contains(err.Error(), "already resolved") {
		t.Errorf("second Resolve: err = %v, want already resolved", err)
	}
}
