package calibration

import (
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// seedMixedLedger writes a ledger that reaches every predictor kind: finding
// outcomes by a forager and by an agent that is not one, lens verdicts,
// synthesis verdicts with and without a fired ∇, some with a confidence.
func seedMixedLedger(t *testing.T, s *db.Store) {
	t.Helper()
	d := addFinding(t, s, "definition", ip(1), ip(0), "skeptic")
	g := addFinding(t, s, "guarantee", ip(1), ip(0), "skeptic", d)
	a := addFinding(t, s, "assumption", ip(2), nil, "wave-agent")
	mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: d, Resolution: Confirmed, Source: SourceHuman, StatedConfidence: ip(95)})
	mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: g, Resolution: Partial, Source: SourceExternal, StatedConfidence: ip(70)})
	mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: a, Resolution: Refuted, Source: SourceHuman})
	nabla := addRun(t, s)
	if _, err := s.ForagerBonds().RecordFired(nabla, "skeptic", "optimist", db.BondResonates, 1.0, nil); err != nil {
		t.Fatal(err)
	}
	plain := addRun(t, s)
	mustRecord(t, s, Outcome{SubjectKind: SubjectSynthesisVerdict, RunID: nabla, Resolution: Confirmed, Source: SourceHuman, StatedConfidence: ip(80), D1: ip(1)})
	mustRecord(t, s, Outcome{SubjectKind: SubjectSynthesisVerdict, RunID: plain, Resolution: Refuted, Source: SourceHuman, D1: ip(1)})
	mustRecord(t, s, Outcome{SubjectKind: SubjectLensVerdict, Lens: "optimist", RunID: nabla, Resolution: Confirmed, Source: SourceHuman, StatedConfidence: ip(60)})
	mustRecord(t, s, Outcome{SubjectKind: SubjectLensVerdict, Lens: "skeptic", RunID: plain, Resolution: Refuted, Source: SourceHuman})
}

var lenses = Options{Foragers: []string{"skeptic", "optimist", "steward"}}

// Two rebuilds over the same ledger write a byte-identical table, add no
// revision the second time, and leave every row's updated tick at the first.
func TestRecompute_RebuildTwiceByteIdentical(t *testing.T) {
	s := newStore(t)
	seedMixedLedger(t, s)
	opts := lenses
	opts.Rebuild = true

	first := mustRecompute(t, s, opts)
	if first.Skipped || first.TickID == 0 || len(first.Changed) == 0 {
		t.Fatalf("first rebuild = %+v", first)
	}
	dump1 := scoresDump(t, s)
	revs1, _ := s.Calibration().CountRevisions()
	if revs1 != len(first.Changed) {
		t.Fatalf("revisions = %d, changed = %d", revs1, len(first.Changed))
	}

	second := mustRecompute(t, s, opts)
	if second.Skipped || second.TickID == first.TickID {
		t.Fatalf("second rebuild = %+v", second)
	}
	if len(second.Changed) != 0 || len(second.Removed) != 0 {
		t.Fatalf("second rebuild changed %d rows and removed %d", len(second.Changed), len(second.Removed))
	}
	if dump2 := scoresDump(t, s); dump2 != dump1 {
		t.Fatalf("rebuild is not deterministic:\n%s\n---\n%s", dump1, dump2)
	}
	if revs2, _ := s.Calibration().CountRevisions(); revs2 != revs1 {
		t.Fatalf("second rebuild added revisions: %d -> %d", revs1, revs2)
	}
	for _, r := range second.Scores {
		if r.UpdatedTickID.Int64 != first.TickID {
			t.Fatalf("%v updated_tick_id = %d, want the first tick %d", r.ScoreKey, r.UpdatedTickID.Int64, first.TickID)
		}
	}
	if countTicks(t, s, db.TickCalibrate) != 2 {
		t.Fatalf("calibrate ticks = %d, want 2", countTicks(t, s, db.TickCalibrate))
	}
}

// Every kind is scored from the mixed ledger, with the attribution the
// design fixes.
func TestRecompute_Attribution(t *testing.T) {
	s := newStore(t)
	seedMixedLedger(t, s)
	res := mustRecompute(t, s, lenses)

	// label: subject_label at record time.
	if r := findScore(res.Scores, KindLabel, "definition", ""); r == nil || r.NConfirmed != 1 || r.NResolved != 1 {
		t.Fatalf("label/definition = %+v", r)
	}
	if r := findScore(res.Scores, KindLabel, "guarantee", ""); r == nil || r.NPartial != 1 || r.HitRate != 0.5 {
		t.Fatalf("label/guarantee = %+v", r)
	}
	if r := findScore(res.Scores, KindLabel, "assumption", ""); r == nil || r.NRefuted != 1 || r.HitRate != 0 {
		t.Fatalf("label/assumption = %+v", r)
	}
	// lens: lens verdicts, plus finding outcomes whose agent is a forager.
	if r := findScore(res.Scores, KindLens, "skeptic", ""); r == nil || r.NResolved != 3 || r.NConfirmed != 1 || r.NPartial != 1 || r.NRefuted != 1 {
		t.Fatalf("lens/skeptic = %+v, want the two findings and the verdict", r)
	}
	if r := findScore(res.Scores, KindLens, "optimist", ""); r == nil || r.NResolved != 1 || r.NConfirmed != 1 {
		t.Fatalf("lens/optimist = %+v", r)
	}
	if r := findScore(res.Scores, KindLens, "wave-agent", ""); r != nil {
		t.Fatalf("an agent that is not a forager was scored as a lens: %+v", r)
	}
	// synthesizer: every synthesis verdict under queen.
	if r := findScore(res.Scores, KindSynthesizer, KeyQueen, ""); r == nil || r.NResolved != 2 || r.NConfirmed != 1 || r.NRefuted != 1 {
		t.Fatalf("synthesizer/queen = %+v", r)
	}
	// convergence: the ∇ run's verdict only.
	if r := findScore(res.Scores, KindConvergence, KeyNabla, ""); r == nil || r.NResolved != 1 || r.NConfirmed != 1 {
		t.Fatalf("convergence/nabla = %+v", r)
	}
	// Brier: only outcomes that stated a confidence.
	if r := findScore(res.Scores, KindLens, "skeptic", ""); r.BrierN != 2 || !r.BrierScore.Valid ||
		!near(r.BrierScore.Float64, (BrierTerm(95, Confirmed)+BrierTerm(70, Partial))/2) {
		t.Fatalf("lens/skeptic brier = %+v", r)
	}
	if r := findScore(res.Scores, KindLabel, "assumption", ""); r.BrierN != 0 || r.BrierScore.Valid {
		t.Fatalf("label/assumption brier = %+v, want none", r)
	}
	// Reference rates: a label against its target, a lens against the pool.
	if r := findScore(res.Scores, KindLabel, "definition", ""); !near(r.Weight, Formula(Counts{Confirmed: 1}, LabelTargets["definition"]).W) {
		t.Fatalf("label/definition weight = %v", r.Weight)
	}
	pool := Counts{Confirmed: 2, Partial: 1, Refuted: 1}
	if r := findScore(res.Scores, KindLens, "optimist", ""); !near(r.Weight, Formula(Counts{Confirmed: 1}, pool.PHat()).W) {
		t.Fatalf("lens/optimist weight = %v, want it against the pooled rate %v", r.Weight, pool.PHat())
	}
	// Seven outcomes in all: no prefix reaches the floor, so only the global
	// scope is scored.
	for _, r := range res.Scores {
		if r.ScopeKey != ScopeGlobal {
			t.Fatalf("a prefix with fewer than %d outcomes was scored: %v", NFloor, r.ScoreKey)
		}
		if r.Calibrated {
			t.Fatalf("%v calibrated at n=%d", r.ScoreKey, r.NResolved)
		}
	}
}

// Without --rebuild a run with nothing new changes nothing and opens no
// tick; a new outcome makes the next run recompute, and that incremental
// run equals a rebuild.
func TestRecompute_SkipsWhenNothingNew_IncrementalEqualsRebuild(t *testing.T) {
	s := newStore(t)
	seedMixedLedger(t, s)
	first := mustRecompute(t, s, lenses)
	if first.Skipped || first.SinceTickID != 0 || first.NewOutcomes != 7 {
		t.Fatalf("first run = %+v", first)
	}
	dump1 := scoresDump(t, s)

	again := mustRecompute(t, s, lenses)
	if !again.Skipped || again.TickID != 0 || again.SinceTickID != first.TickID || again.NewOutcomes != 0 {
		t.Fatalf("a run with nothing new = %+v", again)
	}
	if len(again.Scores) != len(first.Scores) {
		t.Fatalf("a skipped run reports %d scores, want the current %d", len(again.Scores), len(first.Scores))
	}
	if scoresDump(t, s) != dump1 || countTicks(t, s, db.TickCalibrate) != 1 {
		t.Fatal("a skipped run wrote something")
	}

	f := addFinding(t, s, "assumption", ip(2), nil, "steward")
	mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: f, Resolution: Confirmed, Source: SourceHuman})
	third := mustRecompute(t, s, lenses)
	if third.Skipped || third.SinceTickID != first.TickID || third.NewOutcomes != 1 {
		t.Fatalf("the run after a new outcome = %+v", third)
	}
	if r := findScore(third.Scores, KindLens, "steward", ""); r == nil || r.NConfirmed != 1 {
		t.Fatalf("lens/steward = %+v", r)
	}
	if r := findScore(third.Scores, KindLabel, "assumption", ""); r == nil || r.NResolved != 2 {
		t.Fatalf("label/assumption = %+v, want both outcomes", r)
	}
	dump3 := scoresDump(t, s)

	opts := lenses
	opts.Rebuild = true
	rebuilt := mustRecompute(t, s, opts)
	if len(rebuilt.Changed) != 0 {
		t.Fatalf("a rebuild after an incremental run changed %d rows", len(rebuilt.Changed))
	}
	if scoresDump(t, s) != dump3 {
		t.Fatalf("incremental and rebuild differ:\n%s\n---\n%s", dump3, scoresDump(t, s))
	}
}

// calibrated is 0 below NFloor and 1 from it, per predictor.
func TestRecompute_CalibratedAtTheFloor(t *testing.T) {
	s := newStore(t)
	run := addRun(t, s)
	for i := 0; i < NFloor-1; i++ {
		mustRecord(t, s, Outcome{SubjectKind: SubjectLensVerdict, Lens: "skeptic", RunID: run, Resolution: Confirmed, Source: SourceHuman})
	}
	res := mustRecompute(t, s, lenses)
	if r := findScore(res.Scores, KindLens, "skeptic", ""); r == nil || r.NResolved != NFloor-1 || r.Calibrated {
		t.Fatalf("at n=%d: %+v, want calibrated=0", NFloor-1, r)
	}
	if res.LowestCalibratedLens != "" {
		t.Fatalf("lowest calibrated lens = %q with no calibrated lens", res.LowestCalibratedLens)
	}
	mustRecord(t, s, Outcome{SubjectKind: SubjectLensVerdict, Lens: "skeptic", RunID: run, Resolution: Confirmed, Source: SourceHuman})
	res = mustRecompute(t, s, lenses)
	if r := findScore(res.Scores, KindLens, "skeptic", ""); r == nil || r.NResolved != NFloor || !r.Calibrated {
		t.Fatalf("at n=%d: %+v, want calibrated=1", NFloor, r)
	}
	if res.LowestCalibratedLens != "skeptic" {
		t.Fatalf("lowest calibrated lens = %q", res.LowestCalibratedLens)
	}
}

// A prefix is scored once it holds NFloor outcomes of any kind; an outcome
// without coordinates counts toward the global scope only.
func TestRecompute_ScopeFloor(t *testing.T) {
	s := newStore(t)
	run := addRun(t, s)
	for i := 0; i < NFloor-1; i++ {
		mustRecord(t, s, Outcome{SubjectKind: SubjectLensVerdict, Lens: "skeptic", RunID: run, Resolution: Confirmed, Source: SourceHuman, D1: ip(3), D2: ip(1)})
	}
	mustRecord(t, s, Outcome{SubjectKind: SubjectSynthesisVerdict, RunID: run, Resolution: Confirmed, Source: SourceHuman})
	res := mustRecompute(t, s, lenses)
	for _, r := range res.Scores {
		if r.ScopeKey != ScopeGlobal {
			t.Fatalf("scope %q scored with %d outcomes", r.ScopeKey, NFloor-1)
		}
	}
	// The tenth outcome in the prefix is a synthesis verdict: the scope's
	// floor counts every kind, and the lens row there still has nine.
	mustRecord(t, s, Outcome{SubjectKind: SubjectSynthesisVerdict, RunID: run, Resolution: Refuted, Source: SourceHuman, D1: ip(3), D2: ip(1)})
	res = mustRecompute(t, s, lenses)
	for _, scope := range []string{"d1=3", "d1=3;d2=1"} {
		lens := findScore(res.Scores, KindLens, "skeptic", scope)
		if lens == nil || lens.NResolved != NFloor-1 || lens.Calibrated {
			t.Fatalf("lens/skeptic @%s = %+v, want 9 uncalibrated", scope, lens)
		}
		if q := findScore(res.Scores, KindSynthesizer, KeyQueen, scope); q == nil || q.NResolved != 1 {
			t.Fatalf("synthesizer/queen @%s = %+v", scope, q)
		}
	}
	if q := findScore(res.Scores, KindSynthesizer, KeyQueen, ScopeGlobal); q == nil || q.NResolved != 2 {
		t.Fatalf("synthesizer/queen @global = %+v, want both verdicts", q)
	}
	if r := findScore(res.Scores, KindLens, "skeptic", "d1=3;d2=2"); r != nil {
		t.Fatalf("a prefix with no outcome was scored: %+v", r)
	}
}

// A finding the cascade reverts after its outcome was recorded still scores
// under the label it had then.
func TestRecompute_LabelRecordedAtOutcomeTime(t *testing.T) {
	s := newStore(t)
	a := addFinding(t, s, "assumption", ip(1), nil, "test")
	g := addFinding(t, s, "guarantee", ip(1), nil, "test", a)
	mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: g, Resolution: Confirmed, Source: SourceHuman})
	rec := mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: a, Resolution: Refuted, Source: SourceHuman})
	if len(rec.Reverted) != 1 || labelOf(t, s, g) != "unknown" {
		t.Fatalf("the cascade did not revert G: %+v, %s", rec, labelOf(t, s, g))
	}
	res := mustRecompute(t, s, lenses)
	if r := findScore(res.Scores, KindLabel, "guarantee", ""); r == nil || r.NConfirmed != 1 {
		t.Fatalf("label/guarantee = %+v, want G's outcome under guarantee", r)
	}
	if r := findScore(res.Scores, KindLabel, "unknown", ""); r != nil {
		t.Fatalf("label/unknown = %+v, want none", r)
	}
	if r := findScore(res.Scores, KindLabel, "assumption", ""); r == nil || r.NRefuted != 1 {
		t.Fatalf("label/assumption = %+v", r)
	}
}

// Guarantee drift: 12 guarantees in d1=2 at a hit rate of 10/12 report one
// drift there; 12 confirmed report none.
func TestRecompute_GuaranteeDrift(t *testing.T) {
	for _, refuted := range []int{2, 0} {
		s := newStore(t)
		d := addFinding(t, s, "definition", ip(2), nil, "test")
		for i := 0; i < 12; i++ {
			g := addFinding(t, s, "guarantee", ip(2), nil, "test", d)
			resolution := Confirmed
			if i < refuted {
				resolution = Refuted
			}
			mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: g, Resolution: resolution, Source: SourceHuman})
		}
		res := mustRecompute(t, s, lenses)
		inScope := 0
		for _, dr := range res.Drift {
			if dr.PredictorKind != KindLabel || dr.PredictorKey != "guarantee" || dr.Reason != "guarantee_floor" {
				t.Fatalf("unexpected drift %+v", dr)
			}
			if dr.ScopeKey == "d1=2" {
				inScope++
				if !near(dr.HitRate, 10.0/12.0) {
					t.Fatalf("drift hit rate = %v", dr.HitRate)
				}
			}
		}
		if refuted == 2 && (inScope != 1 || res.DriftCount != len(res.Drift)) {
			t.Fatalf("drift in d1=2 = %d (all: %+v), want exactly one", inScope, res.Drift)
		}
		if refuted == 0 && res.DriftCount != 0 {
			t.Fatalf("drift above the floor: %+v", res.Drift)
		}
	}
}

// Hit-rate drift: a calibrated score whose hit rate falls by DriftDelta
// since its previous revision is reported; an uncalibrated swing is not.
func TestRecompute_HitRateDropDrift(t *testing.T) {
	s := newStore(t)
	run := addRun(t, s)
	for i := 0; i < NFloor; i++ {
		mustRecord(t, s, Outcome{SubjectKind: SubjectLensVerdict, Lens: "skeptic", RunID: run, Resolution: Confirmed, Source: SourceHuman})
	}
	mustRecord(t, s, Outcome{SubjectKind: SubjectLensVerdict, Lens: "optimist", RunID: run, Resolution: Confirmed, Source: SourceHuman})
	if res := mustRecompute(t, s, lenses); res.DriftCount != 0 {
		t.Fatalf("drift on the first recompute: %+v", res.Drift)
	}
	for i := 0; i < NFloor; i++ {
		mustRecord(t, s, Outcome{SubjectKind: SubjectLensVerdict, Lens: "skeptic", RunID: run, Resolution: Refuted, Source: SourceHuman})
	}
	mustRecord(t, s, Outcome{SubjectKind: SubjectLensVerdict, Lens: "optimist", RunID: run, Resolution: Refuted, Source: SourceHuman})
	res := mustRecompute(t, s, lenses)
	if res.DriftCount != 1 {
		t.Fatalf("drift = %+v, want one", res.Drift)
	}
	d := res.Drift[0]
	if d.PredictorKey != "skeptic" || d.Reason != "hit_rate_drop" || d.Previous != 1.0 || d.HitRate != 0.5 {
		t.Fatalf("drift = %+v", d)
	}
}

// The ∇ report: predictive when calibrated with a weight above 1, and said
// to be not predictive otherwise.
func TestRecompute_NablaReport(t *testing.T) {
	s := newStore(t)
	if res := mustRecompute(t, s, Options{Rebuild: true}); len(res.Nabla) != 0 {
		t.Fatalf("∇ report on an empty ledger: %+v", res.Nabla)
	}
	for i := 0; i < NFloor; i++ {
		nabla := addRun(t, s)
		if _, err := s.ForagerBonds().RecordFired(nabla, "a", "b", db.BondResonates, 1.0, nil); err != nil {
			t.Fatal(err)
		}
		mustRecord(t, s, Outcome{SubjectKind: SubjectSynthesisVerdict, RunID: nabla, Resolution: Confirmed, Source: SourceHuman})
		plain := addRun(t, s)
		mustRecord(t, s, Outcome{SubjectKind: SubjectSynthesisVerdict, RunID: plain, Resolution: Refuted, Source: SourceHuman})
		// An unfired bond makes no ∇-positive run.
		if _, err := s.WriteDB.Exec(`INSERT INTO forager_bonds (run_id, from_forager, to_forager, bond_kind, fired) VALUES (?, 'a', 'b', 'resonates', 0)`, plain); err != nil {
			t.Fatal(err)
		}
	}
	res := mustRecompute(t, s, lenses)
	if len(res.Nabla) != 1 {
		t.Fatalf("∇ report = %+v", res.Nabla)
	}
	n := res.Nabla[0]
	want := Formula(Counts{Confirmed: NFloor}, Counts{Refuted: NFloor}.PHat())
	if n.NPos != NFloor || n.HitPos != 1 || n.NNeg != NFloor || n.HitNeg != 0 || !n.Calibrated || !n.Predictive || !near(n.Weight, want.W) {
		t.Fatalf("∇ report = %+v, want weight %v predictive", n, want.W)
	}
	if want.W != 1.5 {
		t.Fatalf("the fixture's ∇ weight should be exactly 1.5 (clamped ratio 2, shrunk by 10/20): %v", want.W)
	}

	// The reverse: ∇ runs refuted, others confirmed. Calibrated, weight
	// below 1, so not predictive.
	s2 := newStore(t)
	for i := 0; i < NFloor; i++ {
		nabla := addRun(t, s2)
		if _, err := s2.ForagerBonds().RecordFired(nabla, "a", "b", db.BondResonates, 1.0, nil); err != nil {
			t.Fatal(err)
		}
		mustRecord(t, s2, Outcome{SubjectKind: SubjectSynthesisVerdict, RunID: nabla, Resolution: Refuted, Source: SourceHuman})
		plain := addRun(t, s2)
		mustRecord(t, s2, Outcome{SubjectKind: SubjectSynthesisVerdict, RunID: plain, Resolution: Confirmed, Source: SourceHuman})
	}
	res = mustRecompute(t, s2, lenses)
	if len(res.Nabla) != 1 || !res.Nabla[0].Calibrated || res.Nabla[0].Predictive || res.Nabla[0].Weight >= 1 {
		t.Fatalf("∇ report = %+v, want calibrated and not predictive", res.Nabla)
	}

	// One ∇ run: uncalibrated, so not predictive whatever its hit rate.
	s3 := newStore(t)
	nabla := addRun(t, s3)
	if _, err := s3.ForagerBonds().RecordFired(nabla, "a", "b", db.BondResonates, 1.0, nil); err != nil {
		t.Fatal(err)
	}
	mustRecord(t, s3, Outcome{SubjectKind: SubjectSynthesisVerdict, RunID: nabla, Resolution: Confirmed, Source: SourceHuman})
	res = mustRecompute(t, s3, lenses)
	if len(res.Nabla) != 1 || res.Nabla[0].Calibrated || res.Nabla[0].Predictive || res.Nabla[0].NNeg != 0 {
		t.Fatalf("∇ report = %+v, want one uncalibrated, not predictive entry", res.Nabla)
	}
}

// The lowest calibrated lens is the calibrated global lens with the
// smallest weight.
func TestRecompute_LowestCalibratedLens(t *testing.T) {
	s := newStore(t)
	run := addRun(t, s)
	for i := 0; i < NFloor; i++ {
		mustRecord(t, s, Outcome{SubjectKind: SubjectLensVerdict, Lens: "skeptic", RunID: run, Resolution: Confirmed, Source: SourceHuman})
		resolution := Confirmed
		if i%2 == 0 {
			resolution = Refuted
		}
		mustRecord(t, s, Outcome{SubjectKind: SubjectLensVerdict, Lens: "optimist", RunID: run, Resolution: resolution, Source: SourceHuman})
	}
	mustRecord(t, s, Outcome{SubjectKind: SubjectLensVerdict, Lens: "steward", RunID: run, Resolution: Refuted, Source: SourceHuman})
	res := mustRecompute(t, s, lenses)
	if res.LowestCalibratedLens != "optimist" {
		t.Fatalf("lowest calibrated lens = %q, want optimist (steward has one outcome and is not calibrated)", res.LowestCalibratedLens)
	}
}

// A row whose key has no outcome left is removed: here a lens that is not
// among the foragers.
func TestRecompute_RemovesStaleRows(t *testing.T) {
	s := newStore(t)
	f := addFinding(t, s, "assumption", nil, nil, "skeptic")
	mustRecord(t, s, Outcome{SubjectKind: SubjectFinding, FindingID: f, Resolution: Confirmed, Source: SourceHuman})
	res := mustRecompute(t, s, Options{Foragers: []string{"skeptic"}})
	if findScore(res.Scores, KindLens, "skeptic", "") == nil {
		t.Fatal("lens/skeptic not scored")
	}
	res = mustRecompute(t, s, Options{Rebuild: true})
	if len(res.Removed) != 1 || res.Removed[0].PredictorKey != "skeptic" {
		t.Fatalf("removed = %+v", res.Removed)
	}
	if got, _ := s.Calibration().GetScore(db.ScoreKey{PredictorKind: KindLens, PredictorKey: "skeptic"}); got != nil {
		t.Fatalf("stale row survives: %+v", got)
	}
	if findScore(res.Scores, KindLabel, "assumption", "") == nil {
		t.Fatal("label/assumption gone")
	}
}
