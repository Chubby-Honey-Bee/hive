package runner

import (
	"encoding/json"
	"path/filepath"

	calib "github.com/Chubby-Honey-Bee/hive/internal/calibration"
	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// executeCalibrateNode runs a calibrate node: the calibration recompute in
// the run, with no prompt, no model and no tokens (workflow.md § The
// calibrate node). It writes calibration_drift_count, the drift reports in
// the node's scope (every scope when none is set), and
// lowest_calibrated_lens, the calibrated lens with the smallest weight
// there, and completes through CompleteNode, so accept: and state_updates
// apply. A rejected predicate is terminal, as on a command node: the same
// ledger gives the same scores. The rationale is the recompute's summary.
func (rc *runtimeContext) executeCalibrateNode(node workflow.DispatchNode) (stop bool) {
	if rc.cfg.DryRun {
		rc.logf("[dry-run] would recompute calibration at %s (rebuild=%v scope=%s)", node.Node, node.Rebuild, calibrateScopeLabel(node))
		rc.completeDryRun(node, `{"dry_run":true}`)
		return false
	}
	res, ok := rc.calibrateRecompute(node)
	if !ok {
		return false
	}
	return rc.completeCalibrateNode(node, res)
}

// calibrateRecompute runs the node's recompute, crediting finding outcomes
// to the forager tree's lenses, and reports whether it ran: a cancelled run
// releases the node instead, and a recompute that fails fails it.
func (rc *runtimeContext) calibrateRecompute(node workflow.DispatchNode) (*calib.Result, bool) {
	names, err := calibrateLenses(rc.cfg.ProjectDir)
	if err != nil {
		rc.logf("calibrate node %s: forager tree unread (%v); finding outcomes credit no lens", node.Node, err)
	}
	// Recompute takes no context, so a cancelled run is caught before it
	// starts; once started, it runs to the end.
	if rc.ctx.Err() != nil {
		rc.releaseNode(node)
		return nil, false
	}
	res, err := calib.Recompute(rc.store, calib.Options{Rebuild: node.Rebuild, Foragers: names})
	if err != nil {
		rc.failNode(node, "FAILED", err.Error())
		return nil, false
	}
	return res, true
}

// completeCalibrateNode completes a calibrate node with the recompute's
// outputs in its scope and keeps the recompute's summary as its rationale.
// A rejected accept: predicate rejects the node. Returns true if OnlyWave
// is reached.
func (rc *runtimeContext) completeCalibrateNode(node workflow.DispatchNode, res *calib.Result) (stop bool) {
	drift, lowest := calibrateScopeResults(node, res)
	outputs := map[string]any{
		workflow.CalibrateOutputs[0]: drift,
		workflow.CalibrateOutputs[1]: lowest,
	}
	if err := workflow.CompleteNode(rc.store.Workflows(), rc.runID, node.Node, outputs); err != nil {
		rc.refuseCompletion(node, err, func(rej *workflow.AcceptRejection) { rc.markAcceptRejected(node, rej) })
		return false
	}
	rc.countNodeRun()
	rationale, _ := json.Marshal(calibrateRationale(res, outputs))
	rc.recordCompletion(node, string(rationale))
	rc.logf("calibrate node %s: tick %d, %d new outcomes, %d rows changed, drift %d", node.Node, res.TickID, res.NewOutcomes, len(res.Changed), drift)
	return rc.onlyWaveReached(node)
}

// calibrateScopeResults is the drift count and the lowest calibrated lens
// in the node's scope, every scope's when none is set.
func calibrateScopeResults(node workflow.DispatchNode, res *calib.Result) (drift int, lowest string) {
	if !node.ScopeSet {
		return res.DriftCount, res.LowestCalibratedLens
	}
	for _, d := range res.Drift {
		if d.ScopeKey == node.Scope {
			drift++
		}
	}
	return drift, calib.LowestCalibratedLens(res.Scores, node.Scope)
}

// calibrateLenses names the lenses finding outcomes are credited to: the
// forager tree under the project's foragers/, else the working directory's,
// else the copy the binary carries (foragers.Resolve).
func calibrateLenses(projectDir string) ([]string, error) {
	all, err := foragers.Resolve(filepath.Join(projectDir, "foragers"), "foragers").Load()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(all))
	for _, f := range all {
		names = append(names, f.Name)
	}
	return names, nil
}

func calibrateScopeLabel(node workflow.DispatchNode) string {
	switch {
	case !node.ScopeSet:
		return "every scope"
	case node.Scope == "":
		return "global"
	}
	return node.Scope
}

// calibrateRationale is what the node row keeps of the recompute: enough
// to read what it found without the ledger.
func calibrateRationale(res *calib.Result, outputs map[string]any) map[string]any {
	drift := make([]map[string]any, 0, len(res.Drift))
	for _, d := range res.Drift {
		drift = append(drift, map[string]any{
			"predictor_kind": d.PredictorKind, "predictor_key": d.PredictorKey, "scope_key": d.ScopeKey,
			"reason": d.Reason, "hit_rate": d.HitRate, "previous": d.Previous,
		})
	}
	nabla := make([]map[string]any, 0, len(res.Nabla))
	for _, n := range res.Nabla {
		nabla = append(nabla, map[string]any{
			"scope": n.Scope, "n_nabla": n.NPos, "hit_rate_nabla": n.HitPos, "n_other": n.NNeg,
			"hit_rate_other": n.HitNeg, "weight": n.Weight, "calibrated": n.Calibrated, "predictive": n.Predictive,
		})
	}
	return map[string]any{
		"tick_id":        res.TickID,
		"skipped":        res.Skipped,
		"since_tick_id":  res.SinceTickID,
		"new_outcomes":   res.NewOutcomes,
		"scores_changed": len(res.Changed),
		"scores_removed": len(res.Removed),
		"drift":          drift,
		"nabla":          nabla,
		"outputs":        outputs,
		"note":           calib.CorrelationalNote,
	}
}
