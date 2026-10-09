package calibration

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// Confidences from this set give Brier terms that are exact binary
// fractions, so a sum of them is the same float in any order and the
// equalities below hold byte for byte.
var dyadic = []int{0, 25, 50, 75, 100}

// seedLedgerA and seedLedgerB are two workspaces' ledgers. Together they
// cross the floor in scope d1=1 and for the skeptic lens, which neither
// does alone, and they hold ∇ and no-∇ synthesis verdicts.
func seedLedgerA(t *testing.T, s *db.Store) {
	t.Helper()
	d := addFinding(t, s, "definition", ip(1), ip(0), "skeptic")
	for i := 0; i < 6; i++ {
		g := addFinding(t, s, "guarantee", ip(1), ip(0), "skeptic", d)
		res := Confirmed
		if i == 5 {
			res = Refuted
		}
		mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: g, Resolution: res, Source: SourceHuman, StatedConfidence: ip(dyadic[i%5])})
	}
	nabla := addRun(t, s)
	if _, err := s.ForagerBonds().RecordFired(nabla, "skeptic", "optimist", db.BondResonates, 1.0, nil); err != nil {
		t.Fatal(err)
	}
	mustRecord(t, s, Outcome{SubjectKind: SubjectSynthesisVerdict, RunID: nabla, Resolution: Confirmed, Source: SourceHuman, D1: ip(1)})
	plain := addRun(t, s)
	mustRecord(t, s, Outcome{SubjectKind: SubjectSynthesisVerdict, RunID: plain, Resolution: Partial, Source: SourceHuman, D1: ip(1), StatedConfidence: ip(75)})
	mustRecord(t, s, Outcome{SubjectKind: SubjectLensVerdict, Lens: "optimist", RunID: plain, Resolution: Confirmed, Source: SourceHuman})
}

func seedLedgerB(t *testing.T, s *db.Store) {
	t.Helper()
	a := addFinding(t, s, "assumption", ip(1), nil, "optimist")
	for i := 0; i < 5; i++ {
		res := Confirmed
		if i%2 == 1 {
			res = Refuted
		}
		mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: a, Resolution: res, Source: SourceExternal, StatedConfidence: ip(dyadic[(i+2)%5])})
	}
	run := addRun(t, s)
	for i := 0; i < 7; i++ {
		res := Confirmed
		if i >= 5 {
			res = Partial
		}
		mustRecord(t, s, Outcome{SubjectKind: SubjectLensVerdict, Lens: "skeptic", RunID: run, Resolution: res, Source: SourceHuman, D1: ip(1)})
	}
	mustRecord(t, s, Outcome{SubjectKind: SubjectSynthesisVerdict, RunID: run, Resolution: Refuted, Source: SourceHuman, D1: ip(2)})
	mustRecord(t, s, Outcome{SubjectKind: SubjectLensVerdict, Lens: "steward", RunID: run, Resolution: Confirmed, Source: SourceHuman, D1: ip(2)})
}

// rowsDump renders score rows as scoresDump renders the table, the tick
// left out, so a merge and a recompute compare byte for byte.
func rowsDump(rows []*db.ScoreRow) string {
	var b strings.Builder
	for _, r := range rows {
		bscore := -1.0
		if r.BrierScore.Valid {
			bscore = r.BrierScore.Float64
		}
		fmt.Fprintf(&b, "%s/%s@%q n=%d c=%d r=%d p=%d hit=%.17g bsum=%.17g bn=%d bscore=%.17g w=%.17g cal=%v\n",
			r.PredictorKind, r.PredictorKey, r.ScopeKey, r.NResolved, r.NConfirmed, r.NRefuted, r.NPartial,
			r.HitRate, r.BrierSum, r.BrierN, bscore, r.Weight, r.Calibrated)
	}
	return b.String()
}

func mustExport(t *testing.T, s *db.Store, source string) *Bundle {
	t.Helper()
	b, err := Export(s, source, lenses.Foragers)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The export carries counts and provenance and never a derived value; it
// holds every key, the scopes under the floor included, and each scope's
// total; it opens no tick.
func TestExport_CountsNeverWeights(t *testing.T) {
	s := newStore(t)
	seedLedgerA(t, s)
	b := mustExport(t, s, "a.db")
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"weight"`, `"hit_rate"`, `"calibrated"`, `"brier_score"`, `"n_resolved"`} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("the export carries %s:\n%s", forbidden, raw)
		}
	}
	if b.Format != BundleFormat || b.Source != "a.db" || b.Outcomes != 9 || b.ThroughOutcomeID != 9 || b.ExportedAt == "" {
		t.Fatalf("bundle header = %+v", b)
	}
	// Nine outcomes: d1=1 and d1=1;d2=0 are under the floor and still here.
	if b.ScopeTotals[""] != 9 || b.ScopeTotals["d1=1"] != 8 || b.ScopeTotals["d1=1;d2=0"] != 6 {
		t.Fatalf("scope totals = %v", b.ScopeTotals)
	}
	var guarantee, skepticLens *CountRow
	for i := range b.Counts {
		r := &b.Counts[i]
		if r.PredictorKind == KindLabel && r.PredictorKey == "guarantee" && r.ScopeKey == "d1=1;d2=0" {
			guarantee = r
		}
		if r.PredictorKind == KindLens && r.PredictorKey == "skeptic" && r.ScopeKey == "" {
			skepticLens = r
		}
	}
	if guarantee == nil || guarantee.Confirmed != 5 || guarantee.Refuted != 1 || guarantee.BrierN != 6 {
		t.Fatalf("label/guarantee@d1=1;d2=0 = %+v", guarantee)
	}
	if skepticLens == nil || skepticLens.Confirmed != 5 || skepticLens.Refuted != 1 {
		t.Fatalf("lens/skeptic@'' = %+v (finding outcomes credit the forager that wrote them)", skepticLens)
	}
	for i := 1; i < len(b.Counts); i++ {
		if !lessKey(b.Counts[i-1].key(), b.Counts[i].key()) {
			t.Fatalf("counts are not sorted at %d: %+v", i, b.Counts[i-1:i+1])
		}
	}
	if countTicks(t, s, db.TickCalibrate) != 0 {
		t.Fatal("the export opened a tick")
	}
}

// merge(A, B) == merge(B, A); merge(A) equals A's own recompute; and
// merging the two exports equals recomputing over the union of the two
// ledgers, the eligibility of a scope decided over the union.
func TestMerge_CommutesAndEqualsTheUnion(t *testing.T) {
	a := newStore(t)
	seedLedgerA(t, a)
	b := newStore(t)
	seedLedgerB(t, b)
	union := newStore(t)
	seedLedgerA(t, union)
	seedLedgerB(t, union)

	ba, bb := mustExport(t, a, "a.db"), mustExport(t, b, "b.db")
	ab, err := Merge(ba, bb)
	if err != nil {
		t.Fatal(err)
	}
	baRows, err := Merge(bb, ba)
	if err != nil {
		t.Fatal(err)
	}
	if rowsDump(ab) != rowsDump(baRows) {
		t.Fatalf("merge is not commutative:\n%s\n---\n%s", rowsDump(ab), rowsDump(baRows))
	}

	own := mustRecompute(t, a, lenses)
	one, err := Merge(ba)
	if err != nil {
		t.Fatal(err)
	}
	if rowsDump(one) != rowsDump(own.Scores) {
		t.Fatalf("merge of one bundle differs from its recompute:\n%s\n---\n%s", rowsDump(one), rowsDump(own.Scores))
	}

	whole := mustRecompute(t, union, lenses)
	if rowsDump(ab) != rowsDump(whole.Scores) {
		t.Fatalf("merge differs from the recompute over the union:\n%s\n---\n%s", rowsDump(ab), rowsDump(whole.Scores))
	}
	// The union crosses the floor where neither part does: d1=1 holds 8 + 8
	// outcomes, and skeptic has 6 + 7.
	if findScore(ab, KindLabel, "guarantee", "d1=1") == nil || findScore(own.Scores, KindLabel, "guarantee", "d1=1") != nil {
		t.Fatalf("scope d1=1 is not scored over the union alone:\n%s", rowsDump(ab))
	}
	if r := findScore(ab, KindLens, "skeptic", ""); r == nil || !r.Calibrated || r.NResolved != 13 {
		t.Fatalf("lens/skeptic over the union = %+v, want 13 outcomes, calibrated", r)
	}
	if r := findScore(ab, KindConvergence, KeyNabla, ""); r == nil || r.NResolved != 1 {
		t.Fatalf("convergence/nabla over the union = %+v", r)
	}
}

// A merge refuses what it cannot sum: another format, a kind outside the
// four, a key a bundle lists twice, and a ∇ count past its synthesis count.
func TestMerge_Refuses(t *testing.T) {
	s := newStore(t)
	seedLedgerA(t, s)
	good := mustExport(t, s, "a.db")

	other := *good
	other.Format = "hive-calibration-weights/1"
	if _, err := Merge(good, &other); err == nil || !strings.Contains(err.Error(), "format") {
		t.Fatalf("another format merged: %v", err)
	}

	dup := *good
	dup.Counts = append(append([]CountRow(nil), good.Counts...), good.Counts[0])
	if _, err := Merge(&dup); err == nil || !strings.Contains(err.Error(), "listed twice") {
		t.Fatalf("a duplicate key merged: %v", err)
	}

	kind := *good
	kind.Counts = append([]CountRow(nil), good.Counts...)
	kind.Counts[0].PredictorKind = "oracle"
	if _, err := Merge(&kind); err == nil || !strings.Contains(err.Error(), "oracle") {
		t.Fatalf("an unknown kind merged: %v", err)
	}

	skew := *good
	skew.Counts = append([]CountRow(nil), good.Counts...)
	for i := range skew.Counts {
		if skew.Counts[i].PredictorKind == KindConvergence && skew.Counts[i].ScopeKey == "" {
			skew.Counts[i].Confirmed = 5
		}
	}
	if _, err := Merge(&skew); err == nil || !strings.Contains(err.Error(), "more ∇") {
		t.Fatalf("a ∇ count past the synthesis count merged: %v", err)
	}
	if _, err := Merge(nil); err == nil {
		t.Fatal("a nil bundle merged")
	}
}
