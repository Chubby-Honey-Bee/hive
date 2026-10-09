package hive

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

type scanRecord struct {
	iteration, totalFindings, unresolvedGaps, batchSize int
	phase                                               string
	signals, caps, passes                               int
	signalsThrough                                      int64
}

func readScanRecord(t *testing.T, s *db.Store, project string) scanRecord {
	t.Helper()
	var r scanRecord
	if err := s.ReadDB.QueryRow(
		`SELECT iteration, total_findings, unresolved_gaps, batch_size, phase, signals_through FROM hive_state WHERE project=?`, project,
	).Scan(&r.iteration, &r.totalFindings, &r.unresolvedGaps, &r.batchSize, &r.phase, &r.signalsThrough); err != nil {
		t.Fatal(err)
	}
	for q, dest := range map[string]*int{
		`SELECT COUNT(*) FROM signals`:         &r.signals,
		`SELECT COUNT(*) FROM capped_findings`: &r.caps,
		`SELECT COUNT(*) FROM hive_iterations`: &r.passes,
	} {
		if err := s.ReadDB.QueryRow(q).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func scanOf(t *testing.T, s *db.Store, project string) *State {
	t.Helper()
	state, err := ScanState(s, project)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// A scan whose second signal cannot be written records nothing: not the
// metrics, not the first signal, not the iteration. The writes share one
// transaction, so a failed scan neither moves the baseline nor counts.
func TestRecordScan_AFailedWriteRecordsNothing(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "p")
	addFinding(t, s, "definition", "a", 0)
	addFinding(t, s, "assumption", "b", 1)
	before := readScanRecord(t, s, "p")
	state := scanOf(t, s, "p")
	if state.TotalFindings == before.totalFindings {
		t.Fatalf("setup: scanned findings %d equal the stored baseline, so a rollback would not show", state.TotalFindings)
	}

	fired := []Signal{
		{SignalType: "waggle_dance", Payload: map[string]any{"reason": "ok"}},
		{SignalType: "not_a_signal_type"},
	}
	if _, err := RecordScan(s, Scan{Project: "p", State: state, Fired: fired}); err == nil {
		t.Fatal("RecordScan succeeded with a signal the CHECK refuses")
	}
	if after := readScanRecord(t, s, "p"); after != before {
		t.Fatalf("after a failed scan = %+v, want it unchanged from %+v", after, before)
	}
}

// applySeed is a hive whose plan holds every action --apply runs: a
// laundering guarantee (g rests on the unknown u), so qmp plans fix_mss; a
// conflict between a and b with a named the winner, so its alarm plans the
// cascade_revert of b, on which the guarantee h rests; and, added to the
// plan by hand, a batch-size raise and a cap of the finding f.
type applySeed struct {
	u, g, a, b, h, f, conflict int64
	plan                       []Action
	fired                      []Signal
}

func seedApply(t *testing.T, s *db.Store, batch int) applySeed {
	t.Helper()
	var x applySeed
	x.u = addFinding(t, s, "unknown", "nobody confirmed the page size", 1)
	x.g = addFinding(t, s, "guarantee", "the page size is 4096", 1)
	x.a = addFinding(t, s, "assumption", "the fan runs 4 workers", 2)
	x.b = addFinding(t, s, "assumption", "the fan runs 8 workers", 2)
	x.h = addFinding(t, s, "guarantee", "two batches take 8 workers", 3)
	x.f = addFinding(t, s, "assumption", "converged", 4)
	for id, deps := range map[int64]int64{x.g: x.u, x.h: x.b} {
		if _, err := s.WriteDB.Exec(`UPDATE findings SET depends_on_ids=? WHERE id=?`, fmt.Sprintf("[%d]", deps), id); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Conflicts().AddConflict(1, x.a, x.b, "[numeric] workers"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReadDB.QueryRow(`SELECT id FROM conflicts`).Scan(&x.conflict); err != nil {
		t.Fatal(err)
	}
	if err := s.Conflicts().SetWinner(x.conflict, x.a); err != nil {
		t.Fatal(err)
	}
	state := scanOf(t, s, "p")
	x.fired = EvaluateSignals(s, state)
	plan, err := GeneratePlan(state, x.fired)
	if err != nil {
		t.Fatal(err)
	}
	x.plan = append(plan,
		Action{ID: "raise", Type: "adjust_params", Params: map[string]any{"batch_size": float64(batch + 3)}},
		Action{ID: "cap", Type: "cap_finding", FindingID: x.f},
	)
	return x
}

type applyRecord struct {
	scan                   scanRecord
	gLabel, gDeps, hLabel  string
	gaps                   int
	conflictResolution     sql.NullString
	conflictResolvedByWave sql.NullInt64
}

func readApplyRecord(t *testing.T, s *db.Store, x applySeed) applyRecord {
	t.Helper()
	r := applyRecord{scan: readScanRecord(t, s, "p")}
	var gDeps sql.NullString
	if err := s.ReadDB.QueryRow(`SELECT mss_label, depends_on_ids FROM findings WHERE id=?`, x.g).Scan(&r.gLabel, &gDeps); err != nil {
		t.Fatal(err)
	}
	r.gDeps = gDeps.String
	if err := s.ReadDB.QueryRow(`SELECT mss_label FROM findings WHERE id=?`, x.h).Scan(&r.hLabel); err != nil {
		t.Fatal(err)
	}
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM gaps`).Scan(&r.gaps); err != nil {
		t.Fatal(err)
	}
	if err := s.ReadDB.QueryRow(`SELECT resolution, resolved_by_wave FROM conflicts WHERE id=?`, x.conflict).Scan(&r.conflictResolution, &r.conflictResolvedByWave); err != nil {
		t.Fatal(err)
	}
	return r
}

// --apply's writes are part of the scan's transaction: the settle pass for
// fix_mss, the cascade and the conflict's close for cascade_revert, the cap
// and the batch-size raise. Each case refuses one of those writes, or the
// scan's last write, the iteration bump, when every other has been made. The
// scan fails and all of it rolls back, the signals and the pass's start with
// it, so `hive next --apply` that errors counts no iteration and running it
// again counts the pass once. A step moved out of the transaction, before it
// or after its commit, leaves a change behind in one of these cases.
func TestRecordScan_AFailedApplyRecordsNothing(t *testing.T) {
	for _, c := range []struct{ step, trigger string }{
		{"settle", `BEFORE UPDATE OF mss_label ON findings WHEN NEW.mss_label = 'assumption'`},
		{"cascade", `BEFORE UPDATE OF mss_label ON findings WHEN NEW.mss_label = 'unknown'`},
		{"conflict close", `BEFORE UPDATE OF resolution ON conflicts`},
		{"cap", `BEFORE INSERT ON capped_findings`},
		{"batch size", `BEFORE UPDATE OF batch_size ON hive_state`},
		{"iteration bump", `BEFORE UPDATE OF iteration ON hive_state`},
	} {
		t.Run(c.step, func(t *testing.T) {
			s := newTestStore(t)
			initProject(t, s, "p")
			x := seedApply(t, s, readScanRecord(t, s, "p").batchSize)
			kinds := map[string]bool{}
			for _, a := range x.plan {
				kinds[a.Type] = true
			}
			for _, k := range []string{"fix_mss", "cascade_revert", "adjust_params", "cap_finding"} {
				if !kinds[k] {
					t.Fatalf("setup: the plan holds no %s: %+v", k, x.plan)
				}
			}
			if _, err := s.WriteDB.Exec(`CREATE TRIGGER refuse ` + c.trigger + ` BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
				t.Fatal(err)
			}
			before := readApplyRecord(t, s, x)
			scan := func() (ScanResult, error) {
				return RecordScan(s, Scan{Project: "p", State: scanOf(t, s, "p"), Fired: x.fired, Apply: true, Plan: x.plan})
			}
			if _, err := scan(); err == nil {
				t.Fatalf("RecordScan succeeded with a %s the database refuses", c.step)
			}
			if after := readApplyRecord(t, s, x); after != before {
				t.Fatalf("after a failed --apply = %+v, want it unchanged from %+v", after, before)
			}

			// With the write allowed, the same scan records all of it at once.
			if _, err := s.WriteDB.Exec(`DROP TRIGGER refuse`); err != nil {
				t.Fatal(err)
			}
			res, err := scan()
			if err != nil {
				t.Fatal(err)
			}
			var cascade Action
			for _, a := range x.plan {
				if a.Type == "cascade_revert" {
					cascade = a
				}
			}
			after := readApplyRecord(t, s, x)
			want := before
			want.scan.iteration, want.scan.batchSize, want.scan.phase = before.scan.iteration+1, before.scan.batchSize+3, "dispatching"
			want.scan.totalFindings, want.scan.unresolvedGaps = after.scan.totalFindings, after.scan.unresolvedGaps
			want.scan.signalsThrough = after.scan.signalsThrough
			want.scan.caps, want.scan.passes = before.scan.caps+1, before.scan.passes+1
			// The fired signals, settle's alarm for g, and the cascade's alarm for h.
			want.scan.signals = before.scan.signals + len(x.fired) + 2
			want.gLabel, want.gDeps, want.hLabel = "assumption", "[]", "unknown"
			want.gaps = before.gaps + 1 // the cascade's gap for h
			want.conflictResolution = sql.NullString{String: fmt.Sprintf("loser %d reverted", x.b), Valid: true}
			want.conflictResolvedByWave = sql.NullInt64{Int64: int64(cascade.Wave), Valid: true}
			if after != want {
				t.Fatalf("after = %+v, want %+v", after, want)
			}
			wantCascades := []CascadeResult{{FindingID: x.b, Reverted: []int64{x.h}, ConflictID: x.conflict}}
			if res.Iteration != want.scan.iteration || !reflect.DeepEqual(res.MSSFixed, []int64{x.g}) ||
				!reflect.DeepEqual(res.Cascades, wantCascades) || len(res.Caps) != 1 || !res.Caps[0].Capped || len(res.ParamsApplied) != 1 {
				t.Fatalf("result = %+v, want iteration %d, g demoted, %+v, one cap and one param change", res, want.scan.iteration, wantCascades)
			}
		})
	}
}

// A scan that succeeds records the metrics, every fired signal, one more
// iteration, the pass's start and the watermark of the pending signals it
// read.
func TestRecordScan_RecordsTheScan(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "p")
	addFinding(t, s, "definition", "a", 0)
	if _, err := s.WriteDB.Exec(`INSERT INTO signals (signal_type, acted_on) VALUES ('stop_signal', 0)`); err != nil {
		t.Fatal(err)
	}
	before := readScanRecord(t, s, "p")
	state := scanOf(t, s, "p")
	fired := []Signal{{SignalType: "waggle_dance"}, {SignalType: "tremble_dance"}}
	res, err := RecordScan(s, Scan{Project: "p", State: state, Fired: fired})
	if err != nil {
		t.Fatal(err)
	}
	after := readScanRecord(t, s, "p")
	want := scanRecord{
		iteration:      before.iteration + 1,
		totalFindings:  state.TotalFindings,
		unresolvedGaps: len(state.UnresolvedGaps),
		batchSize:      before.batchSize,
		phase:          "dispatching",
		signals:        before.signals + len(fired),
		passes:         before.passes + 1,
		signalsThrough: state.SignalsThrough(),
	}
	if after != want {
		t.Fatalf("after = %+v, want %+v", after, want)
	}
	if res.Iteration != want.iteration || len(res.SignalIDs) != len(fired) {
		t.Fatalf("result = %+v, want iteration %d and one id per fired signal", res, want.iteration)
	}
	for i, id := range res.SignalIDs {
		var typ string
		var acted int
		if err := s.ReadDB.QueryRow(`SELECT signal_type, acted_on FROM signals WHERE id=?`, id).Scan(&typ, &acted); err != nil {
			t.Fatal(err)
		}
		if typ != fired[i].SignalType || acted != 1 {
			t.Fatalf("signal %d = %s acted_on=%d, want %s recorded acted on", id, typ, acted, fired[i].SignalType)
		}
	}
}

// A terminal scan records the phase and its reason and does not count an
// iteration.
func TestRecordScan_Terminal(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "p")
	before := readScanRecord(t, s, "p")
	const reason = "all conditions hold"
	if _, err := RecordScan(s, Scan{Project: "p", State: scanOf(t, s, "p"), Terminal: true, TerminalReason: reason}); err != nil {
		t.Fatal(err)
	}
	after := readScanRecord(t, s, "p")
	var stored string
	if err := s.ReadDB.QueryRow(`SELECT terminal_reason FROM hive_state WHERE project='p'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if after.phase != "terminal" || stored != reason || after.iteration != before.iteration || after.signals != before.signals || after.passes != before.passes {
		t.Fatalf("after = %+v reason %q, want terminal, %q, iteration %d, no signals, no pass", after, stored, reason, before.iteration)
	}
}

// A hive at its cap is not scanned: nothing is recorded, and the result
// says so. Below the cap the same scan counts.
func TestRecordScan_AtTheCapRecordsNothing(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "p")
	addFinding(t, s, "assumption", "a", 0)
	const limit = 3
	if _, err := s.WriteDB.Exec(`UPDATE hive_state SET iteration=? WHERE project='p'`, limit); err != nil {
		t.Fatal(err)
	}
	before := readScanRecord(t, s, "p")
	res, err := RecordScan(s, Scan{Project: "p", State: scanOf(t, s, "p"), Fired: []Signal{{SignalType: "waggle_dance"}}, MaxIterations: limit})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Capped || res.Iteration != limit {
		t.Fatalf("result = %+v, want capped at %d", res, limit)
	}
	if after := readScanRecord(t, s, "p"); after != before {
		t.Fatalf("a capped scan changed the hive: %+v, want %+v", after, before)
	}
	res, err = RecordScan(s, Scan{Project: "p", State: scanOf(t, s, "p"), MaxIterations: limit + 1})
	if err != nil || res.Capped || res.Iteration != limit+1 {
		t.Fatalf("below the cap: %+v, %v; want iteration %d", res, err, limit+1)
	}
}

// Completing a pass whose count moved refuses and writes nothing; with the
// scan's count it consumes the plan's signals and records the pass's end.
func TestCompleteIteration_ExpectIteration(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "p")
	if _, err := s.WriteDB.Exec(`INSERT INTO signals (signal_type, acted_on) VALUES ('stop_signal', 0)`); err != nil {
		t.Fatal(err)
	}
	first, err := RecordScan(s, Scan{Project: "p", State: scanOf(t, s, "p")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RecordScan(s, Scan{Project: "p", State: scanOf(t, s, "p")}); err != nil {
		t.Fatal(err)
	}
	before := readScanRecord(t, s, "p")
	var pending int
	countPending := func() int {
		t.Helper()
		if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM signals WHERE acted_on=0`).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		return pending
	}
	pendingBefore := countPending()
	_, err = CompleteIteration(s, Completion{Project: "p", ExpectIteration: first.Iteration})
	if !errors.Is(err, ErrIterationMoved) {
		t.Fatalf("complete = %v, want ErrIterationMoved", err)
	}
	if after := readScanRecord(t, s, "p"); after != before || countPending() != pendingBefore {
		t.Fatalf("a refused completion changed the hive: %+v (%d pending), want %+v (%d)", after, pending, before, pendingBefore)
	}

	done, err := CompleteIteration(s, Completion{Project: "p", ExpectIteration: before.iteration})
	if err != nil {
		t.Fatal(err)
	}
	if done.Iteration != before.iteration || done.SignalsConsumed != int64(pendingBefore) || countPending() != 0 {
		t.Fatalf("completion = %+v, want iteration %d and %d signals consumed", done, before.iteration, pendingBefore)
	}
	if done.Start == nil || *done.Start != *done.End {
		t.Fatalf("pass = %+v → %+v, want a recorded start equal to its end: nothing changed", done.Start, done.End)
	}
}

// The stall rule: two passes in a row that end as they began stop the loop
// below the cap. A pass that adds a finding, or one with no recorded end,
// is not a stalled pass.
func TestLoopDecision_Stall(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "p")
	const limit = 10
	pass := func(change, complete bool) (bool, string) {
		t.Helper()
		res, err := RecordScan(s, Scan{Project: "p", State: scanOf(t, s, "p")})
		if err != nil {
			t.Fatal(err)
		}
		if change {
			addFinding(t, s, "assumption", "new", res.Iteration)
		}
		if complete {
			if _, err := CompleteIteration(s, Completion{Project: "p", ExpectIteration: res.Iteration}); err != nil {
				t.Fatal(err)
			}
		}
		more, why, err := LoopDecision(s, "p", res.Iteration, limit)
		if err != nil {
			t.Fatal(err)
		}
		return more, why
	}
	steps := []struct {
		change, complete, more bool
	}{
		{change: false, complete: true, more: true},  // one unchanged pass is not a stall
		{change: true, complete: true, more: true},   // a finding breaks the run
		{change: false, complete: false, more: true}, // no recorded end
		{change: false, complete: true, more: true},  // the pass before had no end
		{change: false, complete: true, more: false}, // two unchanged passes
	}
	for i, st := range steps {
		more, why := pass(st.change, st.complete)
		if more != st.more || (more == (why != "")) {
			t.Fatalf("pass %d (%+v): should_continue %v, reason %q", i+1, st, more, why)
		}
	}
}

// An evaluation is progress only when its verdict differs from its wave's
// previous one, so a hive that gates a blocked wave every pass, recording
// the same verdict each time, stalls rather than running to its cap.
func TestLoopDecision_ARepeatedVerdictIsNoProgress(t *testing.T) {
	s := newTestStore(t)
	initProject(t, s, "p")
	addFinding(t, s, "assumption", "a", 0)
	const limit = 10
	steps := []struct {
		wave    int
		verdict string
		more    bool
	}{
		{1, "NEEDS_MORE_WORK", true}, // the wave's first verdict
		{1, "NEEDS_MORE_WORK", true}, // one unchanged pass
		{1, "COMPLETE", true},        // the verdict changed
		{2, "COMPLETE", true},        // another wave's first verdict
		{2, "COMPLETE", true},        // one unchanged pass
		{1, "COMPLETE", false},       // two unchanged passes
	}
	for i, st := range steps {
		res, err := RecordScan(s, Scan{Project: "p", State: scanOf(t, s, "p")})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Evaluations().AddEvaluation(st.wave, 5, 5, 5, 5, 5, st.verdict, 0, 0, 0, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := CompleteIteration(s, Completion{Project: "p", ExpectIteration: res.Iteration}); err != nil {
			t.Fatal(err)
		}
		more, why, err := LoopDecision(s, "p", res.Iteration, limit)
		if err != nil {
			t.Fatal(err)
		}
		if more != st.more || more == (why != "") {
			t.Fatalf("pass %d (%+v): should_continue %v, reason %q", i+1, st, more, why)
		}
	}
}
