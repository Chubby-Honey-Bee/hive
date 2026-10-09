// Package dreamer implements the dreamer archetype's ripening loop over the
// Comb.
// Five deterministic passes that operate on the existing CDE/MSS tables:
//
//	prune        Surface near-duplicate assumptions (Jaccard ≥ threshold).
//	reprove      Mark guarantees with edited deps as stale (alarm).
//	contradict   Re-detect conflicts across every wave (not just current).
//	hypothesize  Convert old open gaps into followups so they get worked.
//	settle       Demote guarantees whose deps have flipped to unknown.
//
// Each pass writes one ripen_log row and zero or more bee-colony signals.
// The orchestrator loop respects a budget cap, halts on QMP (MSS
// laundering detected at any boundary), and a per-pass deadline of 5min.
package dreamer

import (
	"context"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// Result is the per-pass return.
type Result struct {
	Touched       int            // findings/gaps/etc affected
	CostUSDx10000 int64          // 0 for deterministic passes
	Notes         map[string]any // freeform; surfaced in ripen_log.notes_json
	Halt          bool           // set by QMP halt
	Status        string         // completed|skipped|halted|dry_run|failed
}

// Options carry per-invocation flags from the CLI.
type Options struct {
	Apply        bool          // when true, prune/settle write changes; otherwise emit signals only
	DryRun       bool          // no DB writes, no signals; just plan + report
	Now          time.Time     // injectable clock for tests
	GapAgeMin    time.Duration // hypothesize threshold; default 168h (7 days)
	JaccardMin   float64       // prune threshold; default 0.7 (matches mss.IndependenceAudit)
	MaxPerPass   int           // safety cap on rows written per pass; default 200
	PassDeadline time.Duration // per-pass deadline; default 5 min
}

// DefaultOptions returns reasonable defaults so callers don't need to
// fill every field.
func DefaultOptions() Options {
	return Options{
		Now:          time.Now(),
		GapAgeMin:    7 * 24 * time.Hour,
		JaccardMin:   0.7,
		MaxPerPass:   200,
		PassDeadline: 5 * time.Minute,
	}
}

// Pass is a single named consolidation step.
type Pass struct {
	Name string
	// Run executes the pass. The store is the live DB; opts carries flags.
	// The function returns a Result with Touched/Notes; the orchestrator
	// handles BeginPass/CompletePass and signal emission.
	Run func(ctx context.Context, store *db.Store, opts Options) (Result, error)
}

// AllPasses returns the five canonical passes in execution order.
// Tests can subset via the --passes flag on the CLI.
func AllPasses() []Pass {
	return []Pass{
		{Name: "prune", Run: passPrune},
		{Name: "reprove", Run: passReprove},
		{Name: "contradict", Run: passContradict},
		{Name: "hypothesize", Run: passHypothesize},
		{Name: "settle", Run: passSettle},
	}
}

// admit reports whether a pass that has acted on acted rows may act on
// another: not once it reaches maxPerPass, and not past its deadline, whose
// error it then returns. The per-pass deadline is a context; checking it per
// row is what makes it bind. A cut pass reports the rows it already acted on.
func admit(ctx context.Context, acted, maxPerPass int) (bool, error) {
	if acted >= maxPerPass {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return true, nil
}

// passStatus is the status of a pass that ran to its end: dry_run on a dry
// run, completed otherwise.
func passStatus(opts Options) string {
	if opts.DryRun {
		return "dry_run"
	}
	return "completed"
}
