package db

import (
	"testing"
)

// TestGetSummaryEmptyDB verifies GetSummary returns zero counts on a fresh store.
func TestGetSummaryEmptyDB(t *testing.T) {
	s := newTestStore(t)
	sum, err := s.GetSummary()
	if err != nil {
		t.Fatalf("GetSummary on empty DB: %v", err)
	}
	if sum == nil {
		t.Fatal("expected non-nil Summary")
	}
	if sum.TotalFindings != 0 {
		t.Errorf("TotalFindings want 0, got %d", sum.TotalFindings)
	}
	if sum.TotalSources != 0 {
		t.Errorf("TotalSources want 0, got %d", sum.TotalSources)
	}
	if sum.PrimarySources != 0 {
		t.Errorf("PrimarySources want 0, got %d", sum.PrimarySources)
	}
	if sum.UnresolvedGaps != 0 {
		t.Errorf("UnresolvedGaps want 0, got %d", sum.UnresolvedGaps)
	}
	if sum.CriticalGaps != 0 {
		t.Errorf("CriticalGaps want 0, got %d", sum.CriticalGaps)
	}
	if sum.UnresolvedConflicts != 0 {
		t.Errorf("UnresolvedConflicts want 0, got %d", sum.UnresolvedConflicts)
	}
	if sum.UnansweredFollowups != 0 {
		t.Errorf("UnansweredFollowups want 0, got %d", sum.UnansweredFollowups)
	}
	if sum.TotalAgents != 0 {
		t.Errorf("TotalAgents want 0, got %d", sum.TotalAgents)
	}
	if sum.Dimensions != 0 {
		t.Errorf("Dimensions want 0, got %d", sum.Dimensions)
	}
	if sum.LatestEval != nil {
		t.Errorf("LatestEval want nil, got %+v", sum.LatestEval)
	}
	if sum.MSSLabels == nil {
		t.Error("MSSLabels map should be initialised (non-nil)")
	}
	if sum.Convergence == nil {
		t.Error("Convergence map should be initialised (non-nil)")
	}
	if sum.D1Coverage == nil {
		t.Error("D1Coverage map should be initialised (non-nil)")
	}
}

// TestGetSummaryWithFindings verifies counts update when data is present.
func TestGetSummaryWithFindings(t *testing.T) {
	s := newTestStore(t)

	// Add two findings with different MSS labels.
	addTestFinding(t, s, "assumption", "bet one", nil)
	addTestFinding(t, s, "definition", "choice one", nil)

	sum, err := s.GetSummary()
	if err != nil {
		t.Fatalf("GetSummary: %v", err)
	}
	if sum.TotalFindings != 2 {
		t.Errorf("TotalFindings want 2, got %d", sum.TotalFindings)
	}
	if sum.MSSLabels["assumption"] != 1 {
		t.Errorf("MSSLabels[assumption] want 1, got %d", sum.MSSLabels["assumption"])
	}
	if sum.MSSLabels["definition"] != 1 {
		t.Errorf("MSSLabels[definition] want 1, got %d", sum.MSSLabels["definition"])
	}
}

// TestGetSummaryLatestEval verifies LatestEval is populated when an evaluation exists.
func TestGetSummaryLatestEval(t *testing.T) {
	s := newTestStore(t)

	_, err := s.WriteDB.Exec(`INSERT INTO evaluations
		(wave, coverage_score, depth_score, source_score, actionability_score,
		 mss_integrity_score, verdict)
		VALUES (2, 3, 4, 3, 4, 2, 'COMPLETE')`)
	if err != nil {
		t.Fatalf("insert evaluation: %v", err)
	}

	sum, err := s.GetSummary()
	if err != nil {
		t.Fatalf("GetSummary: %v", err)
	}
	if sum.LatestEval == nil {
		t.Fatal("LatestEval want non-nil when evaluation row exists")
	}
	if sum.LatestEval.Wave != 2 {
		t.Errorf("LatestEval.Wave want 2, got %d", sum.LatestEval.Wave)
	}
	if sum.LatestEval.Verdict != "COMPLETE" {
		t.Errorf("LatestEval.Verdict want COMPLETE, got %s", sum.LatestEval.Verdict)
	}
	if sum.LatestEval.Coverage != 3 {
		t.Errorf("LatestEval.Coverage want 3, got %d", sum.LatestEval.Coverage)
	}
}

// TestGetSummaryGapCounts verifies gap-related counts.
func TestGetSummaryGapCounts(t *testing.T) {
	s := newTestStore(t)

	// Insert one critical unresolved gap and one minor unresolved gap.
	s.WriteDB.Exec(`INSERT INTO gaps (wave, agent, description, priority) VALUES (1, 'a', 'critical gap', 'critical')`)
	s.WriteDB.Exec(`INSERT INTO gaps (wave, agent, description, priority) VALUES (1, 'a', 'minor gap', 'minor')`)

	sum, err := s.GetSummary()
	if err != nil {
		t.Fatalf("GetSummary: %v", err)
	}
	if sum.UnresolvedGaps != 2 {
		t.Errorf("UnresolvedGaps want 2, got %d", sum.UnresolvedGaps)
	}
	if sum.CriticalGaps != 1 {
		t.Errorf("CriticalGaps want 1, got %d", sum.CriticalGaps)
	}
}

// TestGetSummaryD1Coverage verifies the D1 coverage map is populated.
func TestGetSummaryD1Coverage(t *testing.T) {
	s := newTestStore(t)

	// addTestFinding sets D1=0 for all inserted findings.
	addTestFinding(t, s, "assumption", "alpha", nil)
	addTestFinding(t, s, "assumption", "beta", nil)

	sum, err := s.GetSummary()
	if err != nil {
		t.Fatalf("GetSummary: %v", err)
	}
	if sum.D1Coverage[0] != 2 {
		t.Errorf("D1Coverage[0] want 2, got %d", sum.D1Coverage[0])
	}
}

// TestGetSummaryConvergencePopulated verifies that the Convergence map contains
// entries when findings with a convergence_level are present.
func TestGetSummaryConvergencePopulated(t *testing.T) {
	s := newTestStore(t)
	addTestFinding(t, s, "assumption", "finding one", nil)
	addTestFinding(t, s, "definition", "finding two", nil)

	sum, err := s.GetSummary()
	if err != nil {
		t.Fatalf("GetSummary: %v", err)
	}
	total := 0
	for _, cnt := range sum.Convergence {
		total += cnt
	}
	if total != 2 {
		t.Errorf("Convergence total want 2, got %d (map=%v)", total, sum.Convergence)
	}
}

// TestGetSummaryReadDBClosed verifies that GetSummary propagates the error
// returned when the read connection is closed (IO failure on count queries).
func TestGetSummaryReadDBClosed(t *testing.T) {
	s := newTestStore(t)
	if err := s.ReadDB.Close(); err != nil {
		t.Fatalf("close ReadDB: %v", err)
	}
	_, err := s.GetSummary()
	if err == nil {
		t.Fatal("expected error when ReadDB is closed, got nil")
	}
}
