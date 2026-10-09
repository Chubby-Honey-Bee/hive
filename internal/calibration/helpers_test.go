package calibration

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

func newStore(t *testing.T) *db.Store {
	t.Helper()
	s, err := db.NewStore(filepath.Join(t.TempDir(), "calibration.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func ip(v int) *int { return &v }

// addFinding writes one finding by agent at (d1, d2); a guarantee names deps.
func addFinding(t *testing.T, s *db.Store, label string, d1, d2 *int, agent string, deps ...int64) int64 {
	t.Helper()
	f := &db.Finding{Wave: 1, Agent: agent, MSSLabel: label, Finding: fmt.Sprintf("%s by %s", label, agent), D1: d1, D2: d2}
	if label == "assumption" || label == "guarantee" {
		src := "https://example.com/" + label
		f.SourceURLs = &src
	}
	if len(deps) > 0 {
		b, _ := json.Marshal(deps)
		str := string(b)
		f.DependsOnIDs = &str
	}
	id, err := s.Findings().AddFinding(f)
	if err != nil {
		t.Fatalf("add %s: %v", label, err)
	}
	return id
}

func addRun(t *testing.T, s *db.Store) int64 {
	t.Helper()
	id, err := s.Workflows().CreateWorkflowRun("calibration-test", 1, "name: t", "{}", nil)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func labelOf(t *testing.T, s *db.Store, id int64) string {
	t.Helper()
	var label string
	if err := s.ReadDB.QueryRow(`SELECT mss_label FROM findings WHERE id = ?`, id).Scan(&label); err != nil {
		t.Fatal(err)
	}
	return label
}

// labelsDump is every finding's label, in id order, as one string.
func labelsDump(t *testing.T, s *db.Store) string {
	t.Helper()
	rows, err := s.ReadDB.Query(`SELECT id, mss_label, COALESCE(depends_on_ids, '') FROM findings ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id int64
		var label, deps string
		if err := rows.Scan(&id, &label, &deps); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%d %s %s\n", id, label, deps)
	}
	return b.String()
}

// scoresDump is the calibration_scores table, every column, in key order.
func scoresDump(t *testing.T, s *db.Store) string {
	t.Helper()
	rows, err := s.ReadDB.Query(`SELECT predictor_kind, predictor_key, scope_key, n_resolved, n_confirmed, n_refuted, n_partial,
		hit_rate, brier_sum, brier_n, COALESCE(brier_score, -1), weight, calibrated, COALESCE(updated_tick_id, 0)
		FROM calibration_scores ORDER BY predictor_kind, predictor_key, scope_key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var kind, key, scope string
		var n, c, r, p, bn, cal int
		var hit, bsum, bscore, w float64
		var tick int64
		if err := rows.Scan(&kind, &key, &scope, &n, &c, &r, &p, &hit, &bsum, &bn, &bscore, &w, &cal, &tick); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s/%s@%q n=%d c=%d r=%d p=%d hit=%.17g bsum=%.17g bn=%d bscore=%.17g w=%.17g cal=%d tick=%d\n",
			kind, key, scope, n, c, r, p, hit, bsum, bn, bscore, w, cal, tick)
	}
	return b.String()
}

func mustRecord(t *testing.T, s *db.Store, o Outcome) Recorded {
	t.Helper()
	rec, err := Record(s, o)
	if err != nil {
		t.Fatalf("record %+v: %v", o, err)
	}
	return rec
}

func mustRecompute(t *testing.T, s *db.Store, opts Options) *Result {
	t.Helper()
	res, err := Recompute(s, opts)
	if err != nil {
		t.Fatalf("recompute: %v", err)
	}
	return res
}

func findScore(rows []*db.ScoreRow, kind, key, scope string) *db.ScoreRow {
	for _, r := range rows {
		if r.PredictorKind == kind && r.PredictorKey == key && r.ScopeKey == scope {
			return r
		}
	}
	return nil
}

func countTicks(t *testing.T, s *db.Store, kind db.TickKind) int {
	t.Helper()
	counts, err := s.TimeWheel().CountByKind()
	if err != nil {
		t.Fatal(err)
	}
	return counts[kind]
}
