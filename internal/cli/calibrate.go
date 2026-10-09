package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/calibration"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
	"github.com/spf13/cobra"
)

// newCalibrateCmd returns `chb calibrate`: the recompute over the outcomes
// ledger. No model call; no finding label changes (CALIB-1).
func newCalibrateCmd() *cobra.Command {
	var (
		rebuild bool
		scope   string
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use:   "calibrate",
		Short: "Score every predictor against the outcomes ledger",
		Long: `Roll the outcomes ledger into calibration_scores: one row per predictor
(a lens, an MSS label, the ∇ signal, the Queen) and scope (global, or a
d1 / d1;d2 prefix with at least 10 outcomes). The recompute reads the
whole ledger, so two runs over the same rows write the same scores. It
opens a calibrate tick, snapshots the rows that changed, and reports
drift: a calibrated guarantee hit rate under 0.95, or a calibrated hit
rate down 0.20 or more since its previous revision. Without --rebuild,
a run with no outcome recorded since the last calibrate tick changes
nothing. It writes no finding and calls no model.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			names, err := foragerNames()
			if err != nil {
				return err
			}
			res, err := calibration.Recompute(store, calibration.Options{Rebuild: rebuild, Foragers: names})
			if err != nil {
				return err
			}
			if asJSON {
				return jsonPrint(calibrateJSON(res, scope))
			}
			printCalibrate(res, scope)
			return nil
		},
	}
	cmd.Flags().BoolVar(&rebuild, "rebuild", false, "recompute even when no outcome was recorded since the last calibrate tick")
	cmd.Flags().StringVar(&scope, "scope", "", "print only this scope's rows ('' is the global scope; 'd1=2' or 'd1=2;d2=0' a prefix)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print one JSON object")
	return cmd
}

// foragerNames lists the lenses the recompute credits finding outcomes to:
// the forager tree `chb ask` reads.
func foragerNames() ([]string, error) {
	all, err := foragers.Resolve(foragerDirs()...).Load()
	if err != nil {
		return nil, fmt.Errorf("load foragers: %w", err)
	}
	names := make([]string, 0, len(all))
	for _, f := range all {
		names = append(names, f.Name)
	}
	return names, nil
}

func scoresJSON(rows []*db.ScoreRow, scope string, filter bool) []calibration.ScoreView {
	out := []calibration.ScoreView{}
	for _, r := range rows {
		if filter && r.ScopeKey != scope {
			continue
		}
		out = append(out, calibration.View(r))
	}
	return out
}

func calibrateJSON(res *calibration.Result, scope string) map[string]any {
	filter := scope != ""
	return map[string]any{
		"skipped":                res.Skipped,
		"since_tick_id":          res.SinceTickID,
		"new_outcomes":           res.NewOutcomes,
		"tick_id":                res.TickID,
		"scores":                 scoresJSON(res.Scores, scope, filter),
		"changed":                scoresJSON(res.Changed, scope, filter),
		"removed":                calibrateRemovedJSON(res.Removed),
		"drift":                  calibrateDriftJSON(res.Drift, scope, filter),
		"drift_count":            res.DriftCount,
		"lowest_calibrated_lens": res.LowestCalibratedLens,
		"nabla":                  calibrateNablaJSON(calibrateNablaInScope(res.Nabla, scope, filter)),
		"note":                   calibration.CorrelationalNote,
	}
}

// calibrateDriftJSON is the drift rows the --scope filter shows, as JSON
// objects.
func calibrateDriftJSON(drift []calibration.Drift, scope string, filter bool) []map[string]any {
	out := []map[string]any{}
	for _, d := range drift {
		if filter && d.ScopeKey != scope {
			continue
		}
		out = append(out, map[string]any{
			"predictor_kind": d.PredictorKind, "predictor_key": d.PredictorKey, "scope_key": d.ScopeKey,
			"reason": d.Reason, "hit_rate": d.HitRate, "previous": d.Previous,
		})
	}
	return out
}

// calibrateNablaJSON is the ∇ report's scopes as JSON objects.
func calibrateNablaJSON(nabla []calibration.NablaScope) []map[string]any {
	out := []map[string]any{}
	for _, n := range nabla {
		out = append(out, map[string]any{
			"scope": n.Scope, "n_nabla": n.NPos, "hit_rate_nabla": n.HitPos,
			"n_other": n.NNeg, "hit_rate_other": n.HitNeg, "weight": n.Weight,
			"calibrated": n.Calibrated, "predictive": n.Predictive,
		})
	}
	return out
}

// calibrateRemovedJSON is the removed score keys as JSON objects.
func calibrateRemovedJSON(removed []db.ScoreKey) []map[string]any {
	out := []map[string]any{}
	for _, k := range removed {
		out = append(out, map[string]any{"predictor_kind": k.PredictorKind, "predictor_key": k.PredictorKey, "scope_key": k.ScopeKey})
	}
	return out
}

// calibrateNablaInScope is the ∇ report's scopes the --scope filter shows.
func calibrateNablaInScope(nabla []calibration.NablaScope, scope string, filter bool) []calibration.NablaScope {
	var out []calibration.NablaScope
	for _, n := range nabla {
		if filter && n.Scope != scope {
			continue
		}
		out = append(out, n)
	}
	return out
}

func printCalibrate(res *calibration.Result, scope string) {
	if res.Skipped {
		fmt.Printf("calibrate: no outcome recorded since calibrate tick #%d; nothing recomputed (--rebuild recomputes anyway)\n", res.SinceTickID)
		return
	}
	filter := scope != ""
	printCalibrateTick(res)
	fmt.Printf("scores changed: %d, removed: %d\n", len(res.Changed), len(res.Removed))
	printCalibrateChanged(res.Changed, scope, filter)
	fmt.Printf("drift: %d\n", res.DriftCount)
	printCalibrateDrift(res.Drift, scope, filter)
	if res.LowestCalibratedLens != "" {
		fmt.Printf("lowest calibrated lens: %s\n", res.LowestCalibratedLens)
	}
	printCalibrateNabla(calibrateNablaInScope(res.Nabla, scope, filter))
	fmt.Println(calibration.CorrelationalNote)
}

// printCalibrateTick prints the tick the recompute opened and the outcomes
// it read.
func printCalibrateTick(res *calibration.Result) {
	if res.SinceTickID == 0 {
		fmt.Printf("calibrate: tick #%d over %d outcomes\n", res.TickID, res.NewOutcomes)
		return
	}
	fmt.Printf("calibrate: tick #%d, %d new outcomes since tick #%d\n", res.TickID, res.NewOutcomes, res.SinceTickID)
}

// printCalibrateChanged prints the changed scores the --scope filter shows.
func printCalibrateChanged(changed []*db.ScoreRow, scope string, filter bool) {
	for _, r := range changed {
		if filter && r.ScopeKey != scope {
			continue
		}
		fmt.Printf("  %s\n", formatScore(r))
	}
}

// printCalibrateDrift prints the drift rows the --scope filter shows.
func printCalibrateDrift(drift []calibration.Drift, scope string, filter bool) {
	for _, d := range drift {
		if filter && d.ScopeKey != scope {
			continue
		}
		fmt.Printf("  %s\n", calibrateDriftLine(d))
	}
}

// calibrateDriftLine describes one drift: a guarantee hit rate under the
// floor, or a hit rate down since the previous revision.
func calibrateDriftLine(d calibration.Drift) string {
	switch d.Reason {
	case "guarantee_floor":
		return fmt.Sprintf("%s/%s @%s: hit rate %.2f below %.2f", d.PredictorKind, d.PredictorKey, scopeName(d.ScopeKey), d.HitRate, calibration.GuaranteeConfirmFloor)
	default:
		return fmt.Sprintf("%s/%s @%s: hit rate %.2f, down from %.2f", d.PredictorKind, d.PredictorKey, scopeName(d.ScopeKey), d.HitRate, d.Previous)
	}
}

// printCalibrateNabla prints the ∇ report over the scopes shown, or says
// that ∇ is predictive nowhere yet.
func printCalibrateNabla(shown []calibration.NablaScope) {
	if len(shown) == 0 {
		fmt.Println("∇ report: no ∇-positive run has a synthesis outcome in the scopes shown; ∇ is not predictive anywhere yet")
		return
	}
	fmt.Println("∇ report:")
	for _, n := range shown {
		fmt.Printf("  %s\n", formatNabla(n))
	}
}

func scopeName(scope string) string {
	if scope == "" {
		return "global"
	}
	return scope
}

func formatScore(r *db.ScoreRow) string {
	cal := "calibrated"
	if !r.Calibrated {
		cal = fmt.Sprintf("uncalibrated (n<%d)", calibration.NFloor)
	}
	brier := "-"
	if r.BrierScore.Valid {
		brier = fmt.Sprintf("%.3f", r.BrierScore.Float64)
	}
	return fmt.Sprintf("%s/%s @%s n=%d hit=%.2f w=%.3f brier=%s %s",
		r.PredictorKind, r.PredictorKey, scopeName(r.ScopeKey), r.NResolved, r.HitRate, r.Weight, brier, cal)
}

func formatNabla(n calibration.NablaScope) string {
	base := fmt.Sprintf("%s: ∇ runs %d hit %.2f; other runs %d hit %.2f; weight %.3f",
		scopeName(n.Scope), n.NPos, n.HitPos, n.NNeg, n.HitNeg, n.Weight)
	switch {
	case n.Predictive:
		return base + " — ∇ predicts here"
	case !n.Calibrated:
		return base + fmt.Sprintf(" — not predictive (n=%d < %d)", n.NPos, calibration.NFloor)
	default:
		return base + " — not predictive (weight not above 1)"
	}
}

// newReadCalibrationCmd returns `chb db-read calibration`: the scores as
// they stand, with uncalibrated rows marked.
func newReadCalibrationCmd() *cobra.Command {
	var (
		kind   string
		scope  string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "calibration",
		Short: "List calibration scores per predictor and scope",
		RunE: func(cmd *cobra.Command, args []string) error {
			var scopePtr *string
			if cmd.Flags().Changed("scope") {
				scopePtr = &scope
			}
			return runReadCalibration(kind, scopePtr, asJSON)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "predictor kind (lens|label|convergence|synthesizer)")
	cmd.Flags().StringVar(&scope, "scope", "", "one scope ('' is the global scope, 'd1=2' a prefix); unset lists every scope")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print one JSON object")
	return cmd
}

// runReadCalibration lists the scores of one predictor kind, or every kind
// when kind is empty, in one scope, or every scope when scope is nil.
func runReadCalibration(kind string, scope *string, asJSON bool) error {
	if err := flagOneOf("--kind", kind, calibration.KindLens, calibration.KindLabel, calibration.KindConvergence, calibration.KindSynthesizer); err != nil {
		return err
	}
	rows, err := store.Calibration().ListScores(kind, scope)
	if err != nil {
		return err
	}
	if asJSON {
		return jsonPrint(map[string]any{"scores": scoresJSON(rows, "", false), "note": calibration.CorrelationalNote})
	}
	printCalibrationScores(rows)
	return nil
}

// printCalibrationScores prints the scores, one per line, then the
// correlational note.
func printCalibrationScores(rows []*db.ScoreRow) {
	fmt.Printf("calibration scores: %d\n", len(rows))
	for _, r := range rows {
		fmt.Printf("  %s\n", formatScore(r))
	}
	fmt.Println(calibration.CorrelationalNote)
}

func recordOutcomes(inputs []calibration.Input, source string) error {
	for _, in := range inputs {
		rec, err := calibration.Record(store, in.Outcome(source))
		if err != nil {
			return err
		}
		fmt.Println(describeRecorded(in, source, rec))
	}
	return nil
}

func describeRecorded(in calibration.Input, source string, rec calibration.Recorded) string {
	line := fmt.Sprintf("outcome #%d: %s %s (%s)", rec.ID, outcomeSubject(in), in.Resolution, source)
	if in.Resolution == calibration.Refuted && in.SubjectKind == calibration.SubjectFinding {
		line += outcomeCascadeNote(source, rec.Reverted)
	}
	return line
}

// outcomeSubject names what an outcome resolves: a finding, a lens's verdict
// in a run, or a run's synthesis.
func outcomeSubject(in calibration.Input) string {
	switch in.SubjectKind {
	case calibration.SubjectFinding:
		return fmt.Sprintf("finding %d", in.FindingID)
	case calibration.SubjectLensVerdict:
		return fmt.Sprintf("lens %s in run %d", in.Lens, in.RunID)
	default:
		return fmt.Sprintf("synthesis of run %d", in.RunID)
	}
}

// outcomeCascadeNote says what a refuted finding's alarm cascade did: none
// ran for a downstream-run outcome, whose adjudication arms it; otherwise it
// reverted nothing or the findings listed.
func outcomeCascadeNote(source string, reverted []int64) string {
	if source == calibration.SourceDownstreamRun {
		return "; no cascade (the adjudication arms it)"
	}
	if len(reverted) == 0 {
		return "; cascade reverted nothing"
	}
	ids := make([]string, len(reverted))
	for i, id := range reverted {
		ids[i] = fmt.Sprint(id)
	}
	return "; cascade reverted findings " + strings.Join(ids, ", ") + " to unknown"
}

// newOutcomeRecordCmd returns `chb outcome-record <json>`: one or more
// outcomes from a human.
func newOutcomeRecordCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "outcome-record <json>",
		Short: "Record whether a finding, lens verdict or synthesis verdict held (source: human)",
		Long: `Append one outcome (or an array of them) to the ledger with source
"human". A finding outcome names finding_id; its coordinates and label are
copied from the finding. A lens_verdict names lens and run_id; a
synthesis_verdict names run_id. resolution is confirmed, refuted or
partial. A refuted finding runs the alarm cascade over what depends on it;
no label on the finding itself changes.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			inputs, err := calibration.DecodeInputs([]byte(args[0]))
			if err != nil {
				return err
			}
			return recordOutcomes(inputs, calibration.SourceHuman)
		},
	}
}

// newOutcomeImportCmd returns `chb outcome-import <path.json>`: outcomes
// from an external source.
func newOutcomeImportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "outcome-import <path.json>",
		Short: "Import outcomes from a JSON file (source: external)",
		Long: `Append every outcome in the file, an object or an array in the shape
outcome-record takes, with source "external". A refuted finding runs the
alarm cascade over what depends on it; no label on the finding itself
changes.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			inputs, err := calibration.DecodeInputs(data)
			if err != nil {
				return err
			}
			return recordOutcomes(inputs, calibration.SourceExternal)
		},
	}
}
