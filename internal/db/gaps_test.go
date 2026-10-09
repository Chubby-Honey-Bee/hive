package db

import (
	"database/sql"
	"strings"
	"testing"
)

// TestQueryGaps_HappyPath verifies that gaps inserted via AddGap are returned by QueryGaps.
func TestQueryGaps_HappyPath(t *testing.T) {
	s := newTestStore(t)
	repo := s.Gaps()

	if err := repo.AddGap(1, "agent-a", "missing data", "critical", intPtr(1), intPtr(2), nil, nil); err != nil {
		t.Fatalf("AddGap: %v", err)
	}
	if err := repo.AddGap(2, "agent-b", "minor gap", "minor", intPtr(1), nil, nil, nil); err != nil {
		t.Fatalf("AddGap: %v", err)
	}

	gaps, err := repo.QueryGaps(nil, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("QueryGaps: %v", err)
	}
	if len(gaps) != 2 {
		t.Errorf("expected 2 gaps, got %d", len(gaps))
	}
}

// TestQueryGaps_FilterByPriority verifies the priority filter.
func TestQueryGaps_FilterByPriority(t *testing.T) {
	s := newTestStore(t)
	repo := s.Gaps()

	if err := repo.AddGap(1, "agent-a", "critical gap", "critical", nil, nil, nil, nil); err != nil {
		t.Fatalf("AddGap: %v", err)
	}
	if err := repo.AddGap(1, "agent-b", "low gap", "minor", nil, nil, nil, nil); err != nil {
		t.Fatalf("AddGap: %v", err)
	}

	p := "critical"
	gaps, err := repo.QueryGaps(&p, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("QueryGaps: %v", err)
	}
	if len(gaps) != 1 {
		t.Errorf("expected 1 critical gap, got %d", len(gaps))
	}
	if gaps[0].Priority != "critical" {
		t.Errorf("expected priority=critical, got %q", gaps[0].Priority)
	}
}

// TestQueryGaps_UnresolvedOnly verifies that resolved gaps are excluded when unresolvedOnly=true.
func TestQueryGaps_UnresolvedOnly(t *testing.T) {
	s := newTestStore(t)
	repo := s.Gaps()

	// Insert one gap that will remain unresolved.
	if err := repo.AddGap(1, "agent-a", "open gap", "important", nil, nil, nil, nil); err != nil {
		t.Fatalf("AddGap open: %v", err)
	}
	// Insert a second gap then mark it resolved via raw SQL.
	if err := repo.AddGap(1, "agent-b", "resolved gap", "important", nil, nil, nil, nil); err != nil {
		t.Fatalf("AddGap resolved: %v", err)
	}
	if _, err := s.WriteDB.Exec("UPDATE gaps SET resolved_by_wave=2 WHERE agent='agent-b'"); err != nil {
		t.Fatalf("manual resolve: %v", err)
	}

	gaps, err := repo.QueryGaps(nil, true, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("QueryGaps unresolvedOnly: %v", err)
	}
	if len(gaps) != 1 {
		t.Errorf("expected 1 unresolved gap, got %d", len(gaps))
	}
	if gaps[0].Agent != "agent-a" {
		t.Errorf("expected agent-a, got %q", gaps[0].Agent)
	}
}

// TestQueryGaps_FilterByCoordinate verifies d1/d2/d3/d4 filters.
func TestQueryGaps_FilterByCoordinate(t *testing.T) {
	s := newTestStore(t)
	repo := s.Gaps()

	if err := repo.AddGap(1, "agent-a", "coord gap", "minor", intPtr(5), intPtr(7), nil, nil); err != nil {
		t.Fatalf("AddGap coord: %v", err)
	}
	if err := repo.AddGap(1, "agent-b", "other gap", "minor", intPtr(5), intPtr(9), nil, nil); err != nil {
		t.Fatalf("AddGap other: %v", err)
	}

	gaps, err := repo.QueryGaps(nil, false, intPtr(5), intPtr(7), nil, nil)
	if err != nil {
		t.Fatalf("QueryGaps coordinate: %v", err)
	}
	if len(gaps) != 1 {
		t.Errorf("expected 1 gap at d1=5,d2=7, got %d", len(gaps))
	}
}

// TestQueryGaps_EmptyResult verifies that an empty table returns a nil/empty slice without error.
func TestQueryGaps_EmptyResult(t *testing.T) {
	s := newTestStore(t)
	repo := s.Gaps()

	gaps, err := repo.QueryGaps(nil, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("QueryGaps empty: %v", err)
	}
	if len(gaps) != 0 {
		t.Errorf("expected 0 gaps, got %d", len(gaps))
	}
}

// TestQueryGaps_AllFilters combines priority + unresolvedOnly + coordinates.
func TestQueryGaps_AllFilters(t *testing.T) {
	s := newTestStore(t)
	repo := s.Gaps()

	// This gap matches all filters.
	if err := repo.AddGap(1, "agent-a", "match", "critical", intPtr(1), intPtr(2), intPtr(3), intPtr(4)); err != nil {
		t.Fatalf("AddGap match: %v", err)
	}
	// This gap has wrong priority.
	if err := repo.AddGap(1, "agent-b", "wrong prio", "minor", intPtr(1), intPtr(2), intPtr(3), intPtr(4)); err != nil {
		t.Fatalf("AddGap wrong prio: %v", err)
	}
	// This gap has wrong d1.
	if err := repo.AddGap(1, "agent-c", "wrong coord", "critical", intPtr(9), intPtr(2), intPtr(3), intPtr(4)); err != nil {
		t.Fatalf("AddGap wrong coord: %v", err)
	}

	p := "critical"
	gaps, err := repo.QueryGaps(&p, true, intPtr(1), intPtr(2), intPtr(3), intPtr(4))
	if err != nil {
		t.Fatalf("QueryGaps all filters: %v", err)
	}
	if len(gaps) != 1 {
		t.Errorf("expected 1 gap matching all filters, got %d", len(gaps))
	}
	if gaps[0].Agent != "agent-a" {
		t.Errorf("expected agent-a, got %q", gaps[0].Agent)
	}
}

// TestAddGap_ErrorPath covers the error branch in AddGap (db write failure).
func TestAddGap_ErrorPath(t *testing.T) {
	// Use a closed DB to force an IO error from Exec.
	badDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	badDB.Close() // close immediately so Exec fails

	repo := NewGapsRepo(badDB, badDB)
	err = repo.AddGap(1, "agent-x", "some gap", "critical", nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error from AddGap on closed DB, got nil")
	}
	if !strings.Contains(err.Error(), "add gap") {
		t.Errorf("expected error to contain 'add gap', got: %v", err)
	}
}
