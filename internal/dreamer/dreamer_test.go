package dreamer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

func freshStore(t *testing.T) *db.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dreamer.db")
	store, err := db.NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Dimensions().AddDimension("dim1", "test", `["0","1","2"]`); err != nil {
		t.Fatalf("AddDimension: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func intP(v int) *int { return &v }

func mustAddFinding(t *testing.T, store *db.Store, label, text string, deps []int64) int64 {
	t.Helper()
	f := &db.Finding{
		Wave:     1,
		Agent:    "test",
		MSSLabel: label,
		Finding:  text,
		D1:       intP(0),
	}
	if len(deps) > 0 {
		b, _ := json.Marshal(deps)
		s := string(b)
		f.DependsOnIDs = &s
	}
	id, err := store.Findings().AddFinding(f)
	if err != nil {
		t.Fatalf("AddFinding(%s): %v", label, err)
	}
	return id
}

func mustAddGap(t *testing.T, store *db.Store, desc, priority string) {
	t.Helper()
	if err := store.Gaps().AddGap(1, "test", desc, priority,
		intP(0), nil, nil, nil); err != nil {
		t.Fatalf("AddGap: %v", err)
	}
}

func countSignals(t *testing.T, store *db.Store, kind string) int {
	t.Helper()
	var n int
	if err := store.ReadDB.QueryRow(
		`SELECT COUNT(*) FROM signals WHERE signal_type = ?`, kind).Scan(&n); err != nil {
		t.Fatalf("count signals: %v", err)
	}
	return n
}

func countDreamLogs(t *testing.T, store *db.Store) int {
	t.Helper()
	var n int
	if err := store.ReadDB.QueryRow(`SELECT COUNT(*) FROM ripen_log`).Scan(&n); err != nil {
		t.Fatalf("count ripen_log: %v", err)
	}
	return n
}

func TestPrune_NearDuplicateAssumptionsEmitStopSignal(t *testing.T) {
	store := freshStore(t)
	// Each phrase has 10 content tokens (length > 3 after stripping
	// non-alpha). Nine overlap; one differs (condition vs conditions).
	// Jaccard = 9/11 ≈ 0.818, well above the 0.7 threshold.
	mustAddFinding(t, store, "assumption", "the unit draws zero point three watts under normal load condition", nil)
	mustAddFinding(t, store, "assumption", "the unit draws zero point three watts under normal load conditions", nil)

	res, err := passPrune(context.Background(), store, DefaultOptions())
	if err != nil {
		t.Fatalf("passPrune: %v", err)
	}
	if res.Status != "completed" {
		t.Fatalf("expected status=completed, got %q", res.Status)
	}
	if res.Touched < 1 {
		t.Fatalf("expected at least one prune candidate, got %d", res.Touched)
	}
	if got := countSignals(t, store, "stop_signal"); got < 1 {
		t.Fatalf("expected at least one stop_signal, got %d", got)
	}
}

func TestPrune_DryRun_DoesNotEmitSignals(t *testing.T) {
	store := freshStore(t)
	mustAddFinding(t, store, "assumption", "the unit draws zero point three watts under normal load condition", nil)
	mustAddFinding(t, store, "assumption", "the unit draws zero point three watts under normal load conditions", nil)

	opts := DefaultOptions()
	opts.DryRun = true
	res, err := passPrune(context.Background(), store, opts)
	if err != nil {
		t.Fatalf("passPrune: %v", err)
	}
	if res.Status != "dry_run" {
		t.Fatalf("expected status=dry_run, got %q", res.Status)
	}
	if got := countSignals(t, store, "stop_signal"); got != 0 {
		t.Fatalf("dry-run should not emit signals, got %d", got)
	}
}

func TestReprove_DepModifiedAfterGuarantee(t *testing.T) {
	store := freshStore(t)
	depA := mustAddFinding(t, store, "definition", "the moon is grey", nil)
	depB := mustAddFinding(t, store, "definition", "tides come from the moon", nil)
	gid := mustAddFinding(t, store, "guarantee", "moon affects tides", []int64{depA, depB})
	_ = gid

	// SQLite CURRENT_TIMESTAMP has 1-second resolution. Sleep 1.5s so the
	// signal we're about to emit lands strictly after the guarantee's
	// created_at (no backdating required).
	time.Sleep(1500 * time.Millisecond)
	srcType := "finding"
	if _, err := store.Signals().EmitSignal(
		"stop_signal", &srcType, &depA,
		intP(0), nil, nil, nil,
		map[string]any{"reason": "test"}, nil,
	); err != nil {
		t.Fatalf("emit signal: %v", err)
	}

	res, err := passReprove(context.Background(), store, DefaultOptions())
	if err != nil {
		t.Fatalf("passReprove: %v", err)
	}
	if res.Touched < 1 {
		t.Fatalf("expected reprove to flag at least one guarantee, got %d", res.Touched)
	}
	if got := countSignals(t, store, "alarm"); got < 1 {
		t.Fatalf("expected an alarm signal, got %d", got)
	}
}

func TestContradict_DetectsCrossWaveConflicts(t *testing.T) {
	store := freshStore(t)
	mustAddFinding(t, store, "definition", "RTL-SDR V4 draws 0.3W", nil)
	mustAddFinding(t, store, "definition", "RTL-SDR V4 does not draw 0.3W", nil)

	res, err := passContradict(context.Background(), store, DefaultOptions())
	if err != nil {
		t.Fatalf("passContradict: %v", err)
	}
	// negation conflict at same coords should be detected
	if res.Touched < 1 {
		t.Fatalf("expected at least one cross-wave conflict, got %d", res.Touched)
	}
}

func TestHypothesize_OldGapBecomesFollowup(t *testing.T) {
	store := freshStore(t)
	mustAddGap(t, store, "We don't know shipping cost per pound", "important")

	// Backdate the gap so it's older than the cutoff.
	if _, err := store.WriteDB.Exec(
		`UPDATE gaps SET created_at = datetime('now', '-30 days')`,
	); err != nil {
		t.Fatalf("backdate gap: %v", err)
	}

	opts := DefaultOptions()
	opts.GapAgeMin = 24 * time.Hour
	opts.Now = time.Now()

	res, err := passHypothesize(context.Background(), store, opts)
	if err != nil {
		t.Fatalf("passHypothesize: %v", err)
	}
	if res.Touched != 1 {
		t.Fatalf("expected exactly 1 followup created, got %d", res.Touched)
	}

	// Idempotent: second run should add zero new followups.
	res2, err := passHypothesize(context.Background(), store, opts)
	if err != nil {
		t.Fatalf("second passHypothesize: %v", err)
	}
	if res2.Touched != 0 {
		t.Fatalf("hypothesize should be idempotent, second pass touched=%d", res2.Touched)
	}
}

func TestSettle_GuaranteeWithUnknownDep_FlagsAlarm(t *testing.T) {
	store := freshStore(t)
	depA := mustAddFinding(t, store, "definition", "fact A", nil)
	depUnknown := mustAddFinding(t, store, "unknown", "we don't know B", nil)

	// Build a guarantee depending on definitionA only, then update to add
	// the unknown after the fact via direct SQL (the validator forbids it
	// at write time, so we sneak it in to simulate the cascade-revert
	// scenario the settle pass is designed to catch).
	gid := mustAddFinding(t, store, "guarantee", "A and B implies C", []int64{depA})
	depsBoth, _ := json.Marshal([]int64{depA, depUnknown})
	if _, err := store.WriteDB.Exec(
		`UPDATE findings SET depends_on_ids = ? WHERE id = ?`, string(depsBoth), gid,
	); err != nil {
		t.Fatalf("backdoor inject unknown dep: %v", err)
	}

	res, err := passSettle(context.Background(), store, DefaultOptions())
	if err != nil {
		t.Fatalf("passSettle: %v", err)
	}
	if res.Touched < 1 {
		t.Fatalf("expected settle to flag at least one guarantee, got %d", res.Touched)
	}
	if got := countSignals(t, store, "alarm"); got < 1 {
		t.Fatalf("expected alarm signal, got %d", got)
	}

	// idempotent: second run finds the same guarantee already-flagged
	res2, err := passSettle(context.Background(), store, DefaultOptions())
	if err != nil {
		t.Fatalf("second passSettle: %v", err)
	}
	if res2.Touched != 0 {
		t.Fatalf("expected idempotent settle, second pass touched=%d", res2.Touched)
	}
}

func TestSettle_Apply_DemotesGuarantee(t *testing.T) {
	store := freshStore(t)
	depA := mustAddFinding(t, store, "definition", "fact A", nil)
	depUnknown := mustAddFinding(t, store, "unknown", "we don't know B", nil)
	gid := mustAddFinding(t, store, "guarantee", "C follows", []int64{depA})
	depsBoth, _ := json.Marshal([]int64{depA, depUnknown})
	if _, err := store.WriteDB.Exec(
		`UPDATE findings SET depends_on_ids = ? WHERE id = ?`, string(depsBoth), gid,
	); err != nil {
		t.Fatalf("inject unknown dep: %v", err)
	}

	opts := DefaultOptions()
	opts.Apply = true
	if _, err := passSettle(context.Background(), store, opts); err != nil {
		t.Fatalf("passSettle apply: %v", err)
	}

	var label string
	if err := store.ReadDB.QueryRow(
		`SELECT mss_label FROM findings WHERE id = ?`, gid).Scan(&label); err != nil {
		t.Fatalf("read label: %v", err)
	}
	if label != "assumption" {
		t.Fatalf("expected demoted to assumption, got %q", label)
	}
}

func TestLoop_FullPipeline_DryRun(t *testing.T) {
	store := freshStore(t)
	mustAddFinding(t, store, "assumption", "X happens", nil)
	mustAddFinding(t, store, "assumption", "X happens too", nil)

	lo := LoopOptions{
		MaxPasses: 5,
		Pass:      DefaultOptions(),
	}
	lo.Pass.DryRun = true

	res, err := Run(context.Background(), store, lo)
	if err != nil && !errors.Is(err, ErrHalted) {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Passes) != 5 {
		t.Fatalf("expected 5 passes in dry-run, got %d", len(res.Passes))
	}
	// Dry-run invariant: no DB writes. Status may be "completed" for
	// passes that found nothing (which is correct; dry-run vs apply only
	// matters when there's actual work).
	if got := countDreamLogs(t, store); got != 0 {
		t.Fatalf("dry-run should not write ripen_log rows, got %d", got)
	}
	if got := countSignals(t, store, "stop_signal"); got != 0 {
		t.Fatalf("dry-run should not emit signals, got %d stop_signal", got)
	}
}

func TestLoop_FullPipeline_WritesDreamLog(t *testing.T) {
	store := freshStore(t)
	mustAddFinding(t, store, "assumption", "X happens", nil)
	mustAddFinding(t, store, "assumption", "X happens too", nil)

	lo := LoopOptions{
		MaxPasses: 5,
		Pass:      DefaultOptions(),
	}
	res, err := Run(context.Background(), store, lo)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Passes) != 5 {
		t.Fatalf("expected 5 passes, got %d", len(res.Passes))
	}
	if got := countDreamLogs(t, store); got != 5 {
		t.Fatalf("expected 5 ripen_log rows, got %d", got)
	}
}

func TestLoop_OnlyPassesFilter(t *testing.T) {
	store := freshStore(t)
	lo := LoopOptions{
		MaxPasses:  5,
		Pass:       DefaultOptions(),
		OnlyPasses: []string{"prune", "settle"},
	}
	res, err := Run(context.Background(), store, lo)
	if err != nil && !errors.Is(err, ErrHalted) {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Passes) != 2 {
		t.Fatalf("expected 2 passes filtered, got %d (%v)", len(res.Passes), passNames(res.Passes))
	}
	if res.Passes[0].Name != "prune" || res.Passes[1].Name != "settle" {
		t.Fatalf("filter should preserve canonical order, got %v", passNames(res.Passes))
	}
}

func TestLoop_QMPHaltsOnPartitionViolation(t *testing.T) {
	store := freshStore(t)
	mustAddFinding(t, store, "definition", "x", nil)
	// Inject a partition violation by writing an invalid label directly.
	if _, err := store.WriteDB.Exec(
		`INSERT INTO findings (wave, agent, mss_label, finding, d1)
		 VALUES (1, 'test', 'definition', 'y', 0)`,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Now flip its label without going through the validator.
	if _, err := store.WriteDB.Exec(
		`UPDATE findings SET mss_label = 'totally_bogus' WHERE finding = 'y'`,
	); err != nil {
		// SQLite's CHECK constraint may block this — that's fine; the
		// partition pass still works for testing if we can land it.
		t.Skipf("CHECK constraint blocked test fixture (expected): %v", err)
	}

	lo := LoopOptions{MaxPasses: 5, Pass: DefaultOptions()}
	res, err := Run(context.Background(), store, lo)
	if !errors.Is(err, ErrHalted) {
		t.Fatalf("expected ErrHalted, got %v", err)
	}
	if !res.Halted {
		t.Fatalf("expected res.Halted=true")
	}
}

// TestLoop_DefaultsApplied exercises the zero-value guards Run keeps:
// MaxPasses, PassDeadline, Now and MaxPerPass. GapAgeMin and JaccardMin are
// taken as given (TestLoop_ZeroGapAgeIsHonoured).
func TestLoop_DefaultsApplied(t *testing.T) {
	store := freshStore(t)
	lo := LoopOptions{}
	res, err := Run(context.Background(), store, lo)
	if err != nil && !errors.Is(err, ErrHalted) {
		t.Fatalf("Run: %v", err)
	}
	// MaxPasses defaults to 5 → all five canonical passes must have run.
	if len(res.Passes) != 5 {
		t.Fatalf("expected 5 passes with default MaxPasses, got %d", len(res.Passes))
	}
}

// --gap-age-days 0 means every open gap, not the 7-day default: Run takes a
// zero GapAgeMin as given, so a 3-day-old gap is promoted.
func TestLoop_ZeroGapAgeIsHonoured(t *testing.T) {
	store := freshStore(t)
	mustAddGap(t, store, "open for three days", "critical")
	if _, err := store.WriteDB.Exec(`UPDATE gaps SET created_at = datetime('now', '-3 days')`); err != nil {
		t.Fatalf("backdate gap: %v", err)
	}
	opts := DefaultOptions()
	opts.GapAgeMin = 0
	res, err := Run(context.Background(), store, LoopOptions{MaxPasses: 1, OnlyPasses: []string{"hypothesize"}, Pass: opts})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := res.Passes[0].Notes["stale_gaps"]; got != 1 {
		t.Fatalf("stale_gaps = %v with a zero gap age, want the one open gap", got)
	}
}

// MaxPasses caps the passes a run makes: with a cap of 1, 2 or 3 of the five
// passes, the run records exactly that many.
func TestLoop_MaxPassesCap(t *testing.T) {
	for _, cap := range []int{1, 2, 3} {
		cap := cap
		t.Run(fmt.Sprintf("cap=%d", cap), func(t *testing.T) {
			store := freshStore(t)
			lo := LoopOptions{
				MaxPasses: cap,
				Pass:      DefaultOptions(),
			}
			res, err := Run(context.Background(), store, lo)
			if err != nil && !errors.Is(err, ErrHalted) {
				t.Fatalf("Run: %v", err)
			}
			if len(res.Passes) != cap {
				t.Fatalf("expected %d passes (MaxPasses=%d), got %d",
					cap, cap, len(res.Passes))
			}
		})
	}
}

func passNames(p []PassOutcome) []string {
	out := make([]string, len(p))
	for i, x := range p {
		out[i] = x.Name
	}
	return out
}

func TestRecordHaltedPass(t *testing.T) {
	tests := []struct {
		name     string
		passName string
		reason   string
		wantErr  bool
	}{
		{
			name:     "happy path writes halted row",
			passName: "prune",
			reason:   "qmp",
		},
		{
			name:     "empty passName still writes row",
			passName: "",
			reason:   "budget",
		},
		{
			name:     "arbitrary reason stored",
			passName: "settle",
			reason:   "partition_violation",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := freshStore(t)
			before := countDreamLogs(t, store)

			err := recordHaltedPass(store, tc.passName, tc.reason)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("recordHaltedPass(%q, %q): %v", tc.passName, tc.reason, err)
			}

			after := countDreamLogs(t, store)
			if after != before+1 {
				t.Fatalf("expected ripen_log count %d, got %d", before+1, after)
			}

			// Verify the row carries the expected status.
			var status string
			if err := store.ReadDB.QueryRow(
				`SELECT status FROM ripen_log ORDER BY id DESC LIMIT 1`,
			).Scan(&status); err != nil {
				t.Fatalf("read ripen_log status: %v", err)
			}
			if status != "halted" {
				t.Fatalf("expected status=halted, got %q", status)
			}
		})
	}
}

func TestRecordHaltedPass_ClosedStore_ReturnsError(t *testing.T) {
	store := freshStore(t)
	// Close the store before calling to force an IO error.
	store.Close()

	err := recordHaltedPass(store, "prune", "qmp")
	if err == nil {
		t.Fatal("expected error from closed store, got nil")
	}
}

// TestLoop_QMPHalt_LaunderingNonDryRun: outside a dry run, a QMP halt emits
// the qmp signal, records the halted pass and returns ErrHalted.
// The partition-violation test always gets skipped by SQLite's CHECK constraint,
// so this test uses a laundering violation injected via raw SQL instead.
func TestLoop_QMPHalt_LaunderingNonDryRun(t *testing.T) {
	store := freshStore(t)
	// Inject guarantee→unknown laundering violation bypassing write-time check.
	unkID := mustAddFinding(t, store, "unknown", "gap we cannot resolve at all", nil)
	if _, err := store.WriteDB.Exec(
		`INSERT INTO findings(wave, agent, mss_label, finding, depends_on_ids) VALUES (1,'b','guarantee','launderer',?)`,
		fmt.Sprintf("[%d]", unkID),
	); err != nil {
		t.Fatalf("inject laundering: %v", err)
	}

	lo := LoopOptions{MaxPasses: 5, Pass: DefaultOptions()} // DryRun=false
	res, err := Run(context.Background(), store, lo)
	if !errors.Is(err, ErrHalted) {
		t.Fatalf("expected ErrHalted, got %v", err)
	}
	if !res.Halted {
		t.Fatal("expected res.Halted=true")
	}
	if res.HaltReason != "laundering" {
		t.Fatalf("expected HaltReason=%q, got %q", "laundering", res.HaltReason)
	}
	// EmitQMP must have written a qmp signal (non-dry-run path).
	if got := countSignals(t, store, "qmp"); got < 1 {
		t.Fatalf("expected at least 1 qmp signal, got %d", got)
	}
	// recordHaltedPass must have written a ripen_log row.
	if got := countDreamLogs(t, store); got < 1 {
		t.Fatalf("expected at least 1 ripen_log row, got %d", got)
	}
}

// A QMP pre-check that cannot read the comb fails the run with an error, not
// ErrHalted.
func TestLoop_QMPPrecheckError(t *testing.T) {
	store := freshStore(t)
	// Close the store so MSSAudit (called inside LaunderingDetected) fails.
	store.Close()

	lo := LoopOptions{MaxPasses: 1, Pass: DefaultOptions()}
	_, err := Run(context.Background(), store, lo)
	if err == nil {
		t.Fatal("expected error from closed store, got nil")
	}
	if errors.Is(err, ErrHalted) {
		t.Fatalf("expected a non-ErrHalted error (qmp precheck), got ErrHalted")
	}
}

// A ripen_log row that cannot be opened fails the run with an error, not
// ErrHalted. MSSAudit only queries the findings table, so dropping ripen_log
// lets the QMP pre-check pass while BeginPass fails on the missing table.
func TestLoop_BeginPassError(t *testing.T) {
	store := freshStore(t)
	if _, err := store.WriteDB.Exec(`DROP TABLE IF EXISTS ripen_log`); err != nil {
		t.Fatalf("drop ripen_log: %v", err)
	}

	lo := LoopOptions{MaxPasses: 1, Pass: DefaultOptions()} // DryRun=false
	_, err := Run(context.Background(), store, lo)
	if err == nil {
		t.Fatal("expected error after BeginPass failure, got nil")
	}
	if errors.Is(err, ErrHalted) {
		t.Fatalf("expected non-ErrHalted error, got ErrHalted")
	}
}

// A pass that returns an error is recorded as failed, and the run returns
// the error, not ErrHalted. Near-duplicate assumptions make passPrune emit a
// signal, and with the signals table dropped EmitSignal fails inside it.
func TestLoop_PassError(t *testing.T) {
	store := freshStore(t)
	// Near-duplicate assumptions: Jaccard ≈ 0.818, above the 0.7 threshold.
	mustAddFinding(t, store, "assumption", "the unit draws zero point three watts under normal load condition", nil)
	mustAddFinding(t, store, "assumption", "the unit draws zero point three watts under normal load conditions", nil)

	// Drop signals so EmitSignal inside passPrune fails.
	if _, err := store.WriteDB.Exec(`DROP TABLE IF EXISTS signals`); err != nil {
		t.Fatalf("drop signals: %v", err)
	}

	lo := LoopOptions{
		MaxPasses:  1,
		OnlyPasses: []string{"prune"},
		Pass:       DefaultOptions(), // DryRun=false
	}
	res, err := Run(context.Background(), store, lo)
	if err == nil {
		t.Fatal("expected error after pass failure, got nil")
	}
	if errors.Is(err, ErrHalted) {
		t.Fatalf("expected non-ErrHalted error, got ErrHalted")
	}
	if len(res.Passes) != 1 {
		t.Fatalf("expected 1 pass recorded even on failure, got %d", len(res.Passes))
	}
	if res.Passes[0].Status != "failed" {
		t.Fatalf("expected pass status=failed, got %q", res.Passes[0].Status)
	}
}

// A second ripen over an unchanged comb emits nothing new, and the cap does
// not strand pairs: with room for one signal per run, the second run reaches
// the second pair instead of re-emitting the first.
func TestPrune_StandingSignalsAreNotReEmitted(t *testing.T) {
	store := freshStore(t)
	mustAddFinding(t, store, "assumption", "the unit draws zero point three watts under normal load condition", nil)
	mustAddFinding(t, store, "assumption", "the unit draws zero point three watts under normal load conditions", nil)
	mustAddFinding(t, store, "assumption", "orders ship from the eastern warehouse every tuesday morning before noon", nil)
	mustAddFinding(t, store, "assumption", "orders ship from the eastern warehouse every tuesday morning before noon today", nil)

	opts := DefaultOptions()
	opts.MaxPerPass = 1
	for run, want := range []int{1, 2, 2} {
		if _, err := passPrune(context.Background(), store, opts); err != nil {
			t.Fatalf("run %d: %v", run+1, err)
		}
		if got := countSignals(t, store, "stop_signal"); got != want {
			t.Fatalf("after run %d: %d stop_signals, want %d", run+1, got, want)
		}
	}
}

// reprove flags a guarantee once per change to its dependencies, not once per
// ripen.
func TestReprove_FlagsOncePerChange(t *testing.T) {
	store := freshStore(t)
	dep := mustAddFinding(t, store, "definition", "the moon is grey", nil)
	mustAddFinding(t, store, "guarantee", "moon affects tides", []int64{dep})
	srcType := "finding"
	emit := func() {
		t.Helper()
		// One-second timestamps: land strictly after the previous event.
		time.Sleep(1100 * time.Millisecond)
		if _, err := store.Signals().EmitSignal("stop_signal", &srcType, &dep,
			intP(0), nil, nil, nil, map[string]any{"reason": "test"}, nil); err != nil {
			t.Fatalf("emit signal: %v", err)
		}
	}
	reprove := func(want int) {
		t.Helper()
		if _, err := passReprove(context.Background(), store, DefaultOptions()); err != nil {
			t.Fatalf("passReprove: %v", err)
		}
		if got := countSignals(t, store, "alarm"); got != want {
			t.Fatalf("%d alarm signals, want %d", got, want)
		}
	}
	emit()
	reprove(1)
	reprove(1)
	emit()
	reprove(2)
}

// With room for one followup per run, the second run reaches the second stale
// gap rather than re-reading the first and stopping.
func TestHypothesize_CapDoesNotStrandLaterGaps(t *testing.T) {
	store := freshStore(t)
	mustAddGap(t, store, "first open question", "critical")
	mustAddGap(t, store, "second open question", "critical")
	opts := DefaultOptions()
	opts.Now = time.Now().Add(30 * 24 * time.Hour)
	opts.MaxPerPass = 1
	for run := 1; run <= 2; run++ {
		if _, err := passHypothesize(context.Background(), store, opts); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
	}
	var n int
	if err := store.ReadDB.QueryRow(`SELECT COUNT(*) FROM followups`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("%d followups after two runs, want 2", n)
	}
}
