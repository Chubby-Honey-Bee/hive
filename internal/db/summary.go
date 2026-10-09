package db

import (
	"database/sql"
	"fmt"
)

// Summary is a summary view of the DB state.
type Summary struct {
	TotalFindings       int            `json:"total_findings"`
	TotalSources        int            `json:"total_sources"`
	PrimarySources      int            `json:"primary_sources"`
	UnresolvedGaps      int            `json:"unresolved_gaps"`
	CriticalGaps        int            `json:"critical_gaps"`
	UnresolvedConflicts int            `json:"unresolved_conflicts"`
	UnansweredFollowups int            `json:"unanswered_followups"`
	TotalAgents         int            `json:"total_agents"`
	MSSLabels           map[string]int `json:"mss_labels"`
	Convergence         map[string]int `json:"convergence"`
	D1Coverage          map[int]int    `json:"d1_coverage"`
	Dimensions          int            `json:"dimensions"`
	LatestEval          *EvalSummary   `json:"latest_eval"`
}

// EvalSummary is a compact view of the latest evaluation.
type EvalSummary struct {
	Wave          int    `json:"wave"`
	Verdict       string `json:"verdict"`
	Coverage      int    `json:"coverage"`
	Depth         int    `json:"depth"`
	Sources       int    `json:"sources"`
	Actionability int    `json:"actionability"`
	MSSIntegrity  int    `json:"mss_integrity"`
	Laundering    int    `json:"laundering"`
	Untraceable   int    `json:"untraceable"`
}

// GetSummary returns the summary.
func (s *Store) GetSummary() (*Summary, error) {
	sum := &Summary{
		MSSLabels:   make(map[string]int),
		Convergence: make(map[string]int),
		D1Coverage:  make(map[int]int),
	}
	if err := s.readSummaryCounts(sum); err != nil {
		return nil, err
	}
	if err := s.readDistributions(sum); err != nil {
		return nil, err
	}
	sum.LatestEval = s.latestEvalSummary()
	return sum, nil
}

// readSummaryCounts reads the summary's totals.
func (s *Store) readSummaryCounts(sum *Summary) error {
	counts := []struct {
		dest  *int
		query string
	}{
		{&sum.TotalFindings, "SELECT COUNT(*) FROM findings"},
		{&sum.TotalSources, "SELECT COUNT(*) FROM sources"},
		{&sum.PrimarySources, "SELECT COUNT(*) FROM sources WHERE primary_source=1"},
		{&sum.UnresolvedGaps, "SELECT COUNT(*) FROM gaps WHERE resolved_by_wave IS NULL"},
		{&sum.CriticalGaps, "SELECT COUNT(*) FROM gaps WHERE priority='critical' AND resolved_by_wave IS NULL"},
		{&sum.UnresolvedConflicts, "SELECT COUNT(*) FROM conflicts WHERE resolution IS NULL"},
		{&sum.UnansweredFollowups, "SELECT COUNT(*) FROM followups WHERE answered=0"},
		{&sum.TotalAgents, "SELECT COUNT(*) FROM agent_runs"},
		{&sum.Dimensions, "SELECT COUNT(*) FROM dimensions"},
	}
	for _, c := range counts {
		if err := s.ReadDB.QueryRow(c.query).Scan(c.dest); err != nil {
			return fmt.Errorf("count query: %w", err)
		}
	}
	return nil
}

// readDistributions reads the MSS label and convergence distributions and
// the d1 coverage.
func (s *Store) readDistributions(sum *Summary) error {
	if err := groupCounts(s.ReadDB, "SELECT mss_label, COUNT(*) FROM findings GROUP BY mss_label", sum.MSSLabels); err != nil {
		return err
	}
	if err := groupCounts(s.ReadDB, "SELECT convergence_level, COUNT(*) FROM findings GROUP BY convergence_level", sum.Convergence); err != nil {
		return err
	}
	return groupCounts(s.ReadDB, "SELECT d1, COUNT(*) FROM findings WHERE d1 IS NOT NULL GROUP BY d1 ORDER BY d1", sum.D1Coverage)
}

// groupCounts runs a "SELECT key, COUNT(*)" query into dest.
func groupCounts[K comparable](rdb *sql.DB, query string, dest map[K]int) error {
	rows, err := rdb.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key K
		var cnt int
		if err := rows.Scan(&key, &cnt); err != nil {
			return err
		}
		dest[key] = cnt
	}
	return rows.Err()
}

// latestEvalSummary is the latest evaluation, or nil when there is none.
func (s *Store) latestEvalSummary() *EvalSummary {
	var ev EvalSummary
	err := s.ReadDB.QueryRow(
		"SELECT wave, verdict, coverage_score, depth_score, source_score, actionability_score, mss_integrity_score, laundering_violations, untraceable_guarantees FROM evaluations ORDER BY wave DESC LIMIT 1",
	).Scan(&ev.Wave, &ev.Verdict, &ev.Coverage, &ev.Depth, &ev.Sources, &ev.Actionability, &ev.MSSIntegrity, &ev.Laundering, &ev.Untraceable)
	if err != nil {
		return nil
	}
	return &ev
}
