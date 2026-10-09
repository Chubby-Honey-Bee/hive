package calibration

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

func ni(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }

func TestScopesFor_MostSpecificFirst(t *testing.T) {
	cases := []struct {
		d1, d2 sql.NullInt64
		want   []string
	}{
		{ni(2), ni(0), []string{"d1=2;d2=0", "d1=2", ""}},
		{ni(2), sql.NullInt64{}, []string{"d1=2", ""}},
		{sql.NullInt64{}, ni(3), []string{""}},
		{sql.NullInt64{}, sql.NullInt64{}, []string{""}},
	}
	for _, c := range cases {
		if got := ScopesFor(c.d1, c.d2); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ScopesFor(%v, %v) = %v, want %v", c.d1, c.d2, got, c.want)
		}
	}
}

func row(kind, key, scope string, n int, hit, w float64) *db.ScoreRow {
	return &db.ScoreRow{ScoreKey: db.ScoreKey{PredictorKind: kind, PredictorKey: key, ScopeKey: scope},
		NResolved: n, HitRate: hit, Weight: w, Calibrated: n >= NFloor}
}

// The governing label score is the most specific calibrated one: an
// uncalibrated row in the exact scope is skipped for a calibrated coarser
// one, the global scope is the last resort, and nothing governs when every
// scope is under the floor.
func TestLabelScore_MostSpecificCalibrated(t *testing.T) {
	s := ScoresFrom([]*db.ScoreRow{
		row(KindLabel, "guarantee", "d1=2;d2=0", 4, 0.5, 1),
		row(KindLabel, "guarantee", "d1=2", 12, 10.0/12, 0.9),
		row(KindLabel, "guarantee", "", 40, 0.95, 1),
		row(KindLabel, "assumption", "", 9, 0.4, 1),
		row(KindLens, "guarantee", "d1=2;d2=0", 30, 0.9, 1.2),
	})
	scopes := ScopesFor(ni(2), ni(0))
	if got := s.LabelScore("guarantee", scopes); got == nil || got.ScopeKey != "d1=2" {
		t.Fatalf("guarantee at d1=2;d2=0 governed by %+v, want the calibrated d1=2 row", got)
	}
	if got := s.LabelScore("guarantee", ScopesFor(ni(7), sql.NullInt64{})); got == nil || got.ScopeKey != "" {
		t.Fatalf("guarantee at d1=7 governed by %+v, want the global row", got)
	}
	if got := s.LabelScore("assumption", scopes); got != nil {
		t.Fatalf("an assumption with 9 outcomes is governed by %+v, want nothing", got)
	}
	if got := s.LabelScore("definition", scopes); got != nil {
		t.Fatalf("a label with no row is governed by %+v", got)
	}
}

// clamp(confidence × hit_rate / target, 0, 100), rounded; nothing for a
// nil or uncalibrated score or a label with no target.
func TestCalibratedConfidence(t *testing.T) {
	cases := []struct {
		name       string
		confidence int
		label      string
		score      *db.ScoreRow
		want       int
		ok         bool
	}{
		{"guarantee 10/12", 80, "guarantee", row(KindLabel, "guarantee", "", 12, 10.0/12, 0.9), 67, true},
		{"exact", 100, "guarantee", row(KindLabel, "guarantee", "", 12, 10.0/12, 0.9), 83, true},
		{"assumption above target clamps", 80, "assumption", row(KindLabel, "assumption", "", 20, 0.9, 1.4), 100, true},
		{"definition", 50, "definition", row(KindLabel, "definition", "", 10, 0.5, 0.6), 26, true},
		{"unknown target", 50, "unknown", row(KindLabel, "unknown", "", 10, 0.5, 1), 50, true},
		{"zero hit rate", 90, "guarantee", row(KindLabel, "guarantee", "", 10, 0, 0.5), 0, true},
		{"uncalibrated", 80, "guarantee", row(KindLabel, "guarantee", "", 9, 0.5, 1), 0, false},
		{"no score", 80, "guarantee", nil, 0, false},
		{"no such label", 80, "fact", row(KindLabel, "fact", "", 12, 0.5, 1), 0, false},
	}
	for _, c := range cases {
		got, ok := CalibratedConfidence(c.confidence, c.label, c.score)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: CalibratedConfidence = %d, %v; want %d, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

// The token renders nothing when no lens is calibrated; otherwise one line
// per lens in name order, with weight, hit rate and n for a calibrated
// lens and `uncalibrated` for one under the floor or with no outcome, after
// the instruction and the correlational note.
func TestLensTrackRecord(t *testing.T) {
	s := ScoresFrom([]*db.ScoreRow{
		row(KindLens, "skeptic", "", 22, 0.7272727, 1.23),
		row(KindLens, "optimist", "", 4, 1, 1.05),
		row(KindLens, "steward", "d1=2", 30, 0.9, 1.4),
	})
	got := LensTrackRecord(s, []string{"steward", "skeptic", "optimist"})
	want := "\n\nLens track records, from the outcomes ledger. " + CorrelationalNote +
		" Weight each lens's agreement in Consensus by its weight; an uncalibrated lens weighs 1:\n" +
		"  optimist: uncalibrated (n=4 < 10)\n" +
		"  skeptic: weight 1.23 (hit rate 0.73, n=22)\n" +
		"  steward: uncalibrated (no outcomes)"
	if got != want {
		t.Fatalf("LensTrackRecord =\n%q\nwant\n%q", got, want)
	}
	if strings.Contains(got, "optimist: weight") || strings.Contains(got, "steward: weight") {
		t.Fatalf("an uncalibrated lens got a weight:\n%s", got)
	}
	if got := LensTrackRecord(s, []string{"optimist", "steward"}); got != "" {
		t.Fatalf("no calibrated lens, yet the token rendered %q", got)
	}
	if got := LensTrackRecord(s, nil); got != "" {
		t.Fatalf("no lens, yet the token rendered %q", got)
	}
}

// At the helpers every consumer reads through, with 9 outcomes the lens is
// not rendered and the label governs nothing; the tenth outcome turns both
// on.
func TestConsumers_NeutralUnderTheFloor(t *testing.T) {
	s := newStore(t)
	run := addRun(t, s)
	d := addFinding(t, s, "definition", ip(2), nil, "test")
	for i := 0; i < 9; i++ {
		mustRecord(t, s, Outcome{SubjectKind: SubjectLensVerdict, Lens: "skeptic", RunID: run, Resolution: Confirmed, Source: SourceHuman})
		g := addFinding(t, s, "guarantee", ip(2), nil, "test", d)
		mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: g, Resolution: Confirmed, Source: SourceHuman})
	}
	res := mustRecompute(t, s, lenses)
	scores := ScoresFrom(res.Scores)
	if got := LensTrackRecord(scores, []string{"skeptic"}); got != "" {
		t.Fatalf("9 outcomes rendered %q", got)
	}
	if sc := scores.LabelScore("guarantee", ScopesFor(ni(2), sql.NullInt64{})); sc != nil {
		t.Fatalf("9 outcomes govern: %+v", sc)
	}
	if _, ok := CalibratedConfidence(80, "guarantee", scores[db.ScoreKey{PredictorKind: KindLabel, PredictorKey: "guarantee", ScopeKey: "d1=2"}]); ok {
		t.Fatal("9 outcomes rescaled a confidence")
	}

	mustRecord(t, s, Outcome{SubjectKind: SubjectLensVerdict, Lens: "skeptic", RunID: run, Resolution: Refuted, Source: SourceHuman})
	g := addFinding(t, s, "guarantee", ip(2), nil, "test", d)
	mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: g, Resolution: Refuted, Source: SourceHuman})
	res = mustRecompute(t, s, lenses)
	scores = ScoresFrom(res.Scores)
	if got := LensTrackRecord(scores, []string{"skeptic"}); !strings.Contains(got, "skeptic: weight 1.00 (hit rate 0.90, n=10)") {
		t.Fatalf("10 outcomes rendered %q", got)
	}
	sc := scores.LabelScore("guarantee", ScopesFor(ni(2), sql.NullInt64{}))
	if sc == nil || sc.ScopeKey != "d1=2" {
		t.Fatalf("10 outcomes govern %+v, want the d1=2 row", sc)
	}
	if v, ok := CalibratedConfidence(80, "guarantee", sc); !ok || v != 72 {
		t.Fatalf("CalibratedConfidence(80) = %d, %v; want 72 (80 × 0.9)", v, ok)
	}
}
