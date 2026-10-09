package dreamer

import (
	"context"
	"fmt"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/gate"
)

// passContradict re-runs DetectConflicts across *every* wave (not just
// the latest) so wave-N evidence that falsifies a wave-1 guarantee is
// surfaced. A conflict already recorded — the same finding pair with the
// same description, resolved or not — is not recorded again; the pass
// touches only the new ones.
//
// Dry-run mode counts the new conflicts without writing rows.
//
// DetectConflicts takes no context, so the per-pass deadline is checked
// only before it starts, and the max-rows cap does not apply: the pass
// examines every finding and records every conflict it finds.
func passContradict(ctx context.Context, store *db.Store, opts Options) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{Status: "failed"}, err
	}
	report, err := gate.DetectConflicts(store, nil, opts.DryRun)
	if err != nil {
		return Result{Status: "failed"}, fmt.Errorf("detect conflicts: %w", err)
	}
	status := "completed"
	if opts.DryRun {
		status = "dry_run"
	}
	notes := map[string]any{
		"total":     report.Total,
		"new":       report.New,
		"numeric":   report.Numeric,
		"mss_label": report.MSSLabel,
		"negation":  report.Negation,
	}
	return Result{
		Status:  status,
		Touched: report.New,
		Notes:   notes,
	}, nil
}
