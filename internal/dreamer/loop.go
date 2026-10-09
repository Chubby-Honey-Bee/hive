package dreamer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// LoopOptions are the orchestrator-level knobs (separate from per-pass
// Options). The CLI maps its flags onto both.
type LoopOptions struct {
	MaxPasses  int      // hard cap on total pass count; default 5 (one round)
	OnlyPasses []string // empty ⇒ run all
	Pass       Options  // per-pass options
}

// LoopResult carries the per-pass outcome plus aggregates.
type LoopResult struct {
	Passes        []PassOutcome
	TotalTouched  int
	TotalCostX10K int64
	Halted        bool
	HaltReason    string
}

// PassOutcome is one row of the run summary.
type PassOutcome struct {
	Name        string
	Status      string
	Touched     int
	CostX10K    int64
	StartedAt   time.Time
	CompletedAt time.Time
	Notes       map[string]any
	RipenLogID  int64
	HaltOnError error
}

// ErrHalted is returned by Run when the loop halts due to QMP detection.
// The LoopResult still carries every pass that
// completed before the halt.
var ErrHalted = errors.New("ripen halted")

// Run executes the configured passes in order against `store`. Each
// pass is wrapped with QMP pre-check + ripen_log row + per-pass
// deadline. Returns the LoopResult and a non-nil error only on
// terminal failure; ErrHalted means "stopped cleanly per policy".
//
// Pass.GapAgeMin and Pass.JaccardMin are taken as given, 0 included, so
// `chb ripen --gap-age-days 0` reaches every open gap; DefaultOptions and
// the CLI flags carry their defaults.
func Run(ctx context.Context, store *db.Store, lo LoopOptions) (*LoopResult, error) {
	lo = withDefaults(lo)
	all := AllPasses()
	if len(lo.OnlyPasses) > 0 {
		all = filterPasses(all, lo.OnlyPasses)
	}

	out := &LoopResult{}
	settle := findPass(all, "settle")
	for _, p := range all[:min(len(all), lo.MaxPasses)] {
		if err := gateAndRun(ctx, store, lo, p, settle, out); err != nil {
			return out, err
		}
	}
	return out, nil
}

// withDefaults fills the loop's unset knobs: at most one round of the five
// passes, and the per-pass defaults.
func withDefaults(lo LoopOptions) LoopOptions {
	if lo.MaxPasses <= 0 {
		lo.MaxPasses = 5
	}
	lo.Pass = passDefaults(lo.Pass)
	return lo
}

// passDefaults fills the per-pass knobs the loop defaults: a five-minute
// deadline, the clock, and a cap of 200 rows.
func passDefaults(o Options) Options {
	if o.PassDeadline <= 0 {
		o.PassDeadline = 5 * time.Minute
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if o.MaxPerPass == 0 {
		o.MaxPerPass = 200
	}
	return o
}

// gateAndRun runs pass p once the QMP gate lets it, and returns ErrHalted
// when the gate halts the loop instead.
func gateAndRun(ctx context.Context, store *db.Store, lo LoopOptions, p Pass, settle *Pass, out *LoopResult) error {
	halted, err := qmpGate(ctx, store, lo, p, settle, out)
	if err != nil {
		return err
	}
	if halted {
		return ErrHalted
	}
	return runOnePass(ctx, store, lo, p, out)
}

// qmpGate is the Queen Mandibular Pheromone halt, run before every pass.
// Partition and cycle violations halt unconditionally: no pass repairs them.
// Laundering is different — settle exists to repair exactly that (a
// guarantee resting on an unknown is demoted), so halting on laundering
// before settle could run would make the repair pass unreachable. When
// laundering is found ahead of a non-settle pass, settle runs first as a
// repair; if laundering persists afterwards, the loop halts. This keeps the
// invariant the halt is for — no metabolising pass builds on a laundered
// comb — without deadlocking on the one pass that fixes it. settle is nil
// when --passes excluded it; then laundering halts the loop.
func qmpGate(ctx context.Context, store *db.Store, lo LoopOptions, p Pass, settle *Pass, out *LoopResult) (bool, error) {
	reason, err := haltReason(ctx, store, lo, p, settle, out)
	if err != nil || reason == "" {
		return false, err
	}
	if !lo.Pass.DryRun {
		_ = EmitQMP(store, reason)
		_ = recordHaltedPass(store, p.Name, reason)
	}
	out.Halted = true
	out.HaltReason = reason
	return true, nil
}

// haltReason is why the loop halts before pass p, empty when p may run.
// Laundering is left to launderingAfterRepair; every other violation halts.
func haltReason(ctx context.Context, store *db.Store, lo LoopOptions, p Pass, settle *Pass, out *LoopResult) (string, error) {
	halted, reason, err := LaunderingDetected(store)
	if err != nil {
		return "", fmt.Errorf("qmp precheck: %w", err)
	}
	if !halted {
		return "", nil
	}
	if reason != "laundering" {
		return reason, nil
	}
	return launderingAfterRepair(ctx, store, lo, p, settle, out)
}

// launderingAfterRepair is why laundering found ahead of pass p halts the
// loop, empty when p may run: p is settle itself, or settle ran first as the
// repair and the comb holds no violation afterwards. With settle excluded by
// --passes, laundering halts the loop.
func launderingAfterRepair(ctx context.Context, store *db.Store, lo LoopOptions, p Pass, settle *Pass, out *LoopResult) (string, error) {
	if p.Name == "settle" {
		return "", nil
	}
	if settle == nil {
		return "laundering", nil
	}
	if err := runOnePass(ctx, store, lo, *settle, out); err != nil {
		return "", err
	}
	return recheckAfterRepair(store)
}

// recheckAfterRepair is the violation that still halts the loop once
// settle's repair has run, empty when none does.
func recheckAfterRepair(store *db.Store) (string, error) {
	halted, reason, err := LaunderingDetected(store)
	if err != nil {
		return "", fmt.Errorf("qmp recheck: %w", err)
	}
	if !halted {
		return "", nil
	}
	return reason, nil
}

// runOnePass executes one ripen pass under its deadline and records the
// outcome on out and in ripen_log.
func runOnePass(ctx context.Context, store *db.Store, lo LoopOptions, p Pass, out *LoopResult) error {
	started := time.Now()
	ripenID, err := beginRipen(store, lo.Pass.DryRun, p.Name)
	if err != nil {
		return err
	}
	res, runErr := runPass(ctx, store, lo.Pass, p)
	out.add(PassOutcome{
		Name:        p.Name,
		Status:      res.Status,
		Touched:     res.Touched,
		CostX10K:    res.CostUSDx10000,
		StartedAt:   started,
		CompletedAt: time.Now(),
		Notes:       res.Notes,
		RipenLogID:  ripenID,
		HaltOnError: runErr,
	})
	// A dry run opens no ripen_log row, so its ripenID is 0.
	if ripenID > 0 {
		_ = store.Ripen().CompletePass(ripenID, res.Status, res.Touched, res.CostUSDx10000, res.Notes)
	}
	if runErr != nil {
		return fmt.Errorf("ripen pass %q: %w", p.Name, runErr)
	}
	return nil
}

// beginRipen opens pass name's ripen_log row and returns its id; a dry run
// opens none and returns 0.
func beginRipen(store *db.Store, dryRun bool, name string) (int64, error) {
	if dryRun {
		return 0, nil
	}
	id, err := store.Ripen().BeginPass(name)
	if err != nil {
		return 0, fmt.Errorf("begin ripen pass %q: %w", name, err)
	}
	return id, nil
}

// runPass runs p under the per-pass deadline. A pass that fails reports
// failed, and a dry run that did nothing reports dry_run, not completed.
func runPass(ctx context.Context, store *db.Store, opts Options, p Pass) (Result, error) {
	passCtx, cancel := context.WithTimeout(ctx, opts.PassDeadline)
	res, err := p.Run(passCtx, store, opts)
	cancel()
	if err != nil {
		res.Status = "failed"
	}
	if opts.DryRun && res.Status == "completed" {
		res.Status = "dry_run"
	}
	return res, err
}

// add appends o to the run summary and to its totals.
func (r *LoopResult) add(o PassOutcome) {
	r.Passes = append(r.Passes, o)
	r.TotalTouched += o.Touched
	r.TotalCostX10K += o.CostX10K
}

func findPass(all []Pass, name string) *Pass {
	for i := range all {
		if all[i].Name == name {
			return &all[i]
		}
	}
	return nil
}

// recordHaltedPass writes a ripen_log row marking the halt, so the QMP event
// is on record beside the passes.
func recordHaltedPass(store *db.Store, passName, reason string) error {
	id, err := store.Ripen().BeginPass(passName)
	if err != nil {
		return err
	}
	return store.Ripen().CompletePass(id, "halted", 0, 0, map[string]any{
		"reason": reason,
		"source": "dreamer.qmp",
	})
}

// filterPasses keeps only the named passes, in their original order.
func filterPasses(all []Pass, names []string) []Pass {
	keep := map[string]bool{}
	for _, n := range names {
		keep[n] = true
	}
	out := make([]Pass, 0, len(all))
	for _, p := range all {
		if keep[p.Name] {
			out = append(out, p)
		}
	}
	return out
}
