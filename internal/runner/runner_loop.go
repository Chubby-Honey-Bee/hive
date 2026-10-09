package runner

import (
	"fmt"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// dispatchLoop is the bounded outer loop that pulls dispatchable nodes
// from the workflow engine and fans them out via dispatchWave. Returns
// nil on terminal-state OR OnlyWave-stop; only propagates real errors.
//
// Two soft termination signals beyond MaxIterations:
//   - cfg.MaxCostUSDx10000 > 0 and rc.res.CostUSDx10000 ≥ that — exits cleanly
//     and logs "max-cost reached"; the workflow is left in its current state
//     so the user can resume after raising the cap.
//   - dispatchWave reports OnlyWave-stop (--only-wave smoke runs).
//
// A cancelled run stops before its next wave and returns the cancellation:
// the nodes it stopped went back to pending (releaseNode), so the run stays
// running for --resume rather than recorded as failed.
func (rc *runtimeContext) dispatchLoop() error {
	for iter := 0; iter < rc.cfg.MaxIterations; iter++ {
		done, err := rc.dispatchIteration()
		if done || err != nil {
			return err
		}
	}
	return rc.iterationsSpent()
}

// dispatchIteration is one iteration of the dispatch loop: a cancelled run
// and a spent --max-cost-usd stop it before it pulls more nodes, and
// otherwise it dispatches the next wave. It reports whether the loop ends,
// and with what.
func (rc *runtimeContext) dispatchIteration() (bool, error) {
	if err := rc.ctx.Err(); err != nil {
		return true, err
	}
	rc.res.Iterations++
	if rc.costCapReached() {
		return true, nil
	}
	return rc.dispatchNextWave()
}

// costCapReached reports whether the run's spend has reached
// --max-cost-usd, and logs that the run exits when it has.
func (rc *runtimeContext) costCapReached() bool {
	if rc.cfg.MaxCostUSDx10000 <= 0 || rc.res.CostUSDx10000 < rc.cfg.MaxCostUSDx10000 {
		return false
	}
	rc.logf("max-cost reached: cum=$%s ≥ cap=$%s — exiting cleanly",
		formatUSDx10000(rc.res.CostUSDx10000),
		formatUSDx10000(rc.cfg.MaxCostUSDx10000),
	)
	return true
}

// dispatchNextWave dispatches the nodes the engine has ready, and reports
// whether the loop ends there: the run has nothing left to dispatch, or
// its wave asked to stop (waveStopped).
func (rc *runtimeContext) dispatchNextWave() (bool, error) {
	dispatch, err := workflow.GetNextNodes(rc.store.Workflows(), rc.runID)
	if err != nil {
		return true, fmt.Errorf("get next nodes: %w", err)
	}
	if len(dispatch) == 0 {
		return true, rc.settled()
	}
	if rc.dispatchWave(dispatch) {
		return true, rc.waveStopped()
	}
	rc.logIteration()
	return false, nil
}

// waveStopped reports how a run ends whose wave asked to stop: a
// human_review node stops the wave too, and settled reports the pause with
// both commands that continue the run; else --only-wave reached its wave.
func (rc *runtimeContext) waveStopped() error {
	run, err := rc.store.Workflows().GetWorkflowRun(rc.runID)
	if err != nil {
		return fmt.Errorf("read run %d: %w", rc.runID, err)
	}
	if run.Status == "paused" {
		return rc.settled()
	}
	if rc.cfg.OnlyWave > 0 {
		rc.logf("OnlyWave=%d reached — stopping after this batch", rc.cfg.OnlyWave)
	}
	return nil
}

// logIteration logs the per-iteration cost meter, after the wave, so the
// first line is not all zeros. The credit meter only appears when the user
// has opted into Copilot billing; direct-API + CLI users see only the
// dollar cost.
func (rc *runtimeContext) logIteration() {
	rc.resMu.Lock()
	cost := formatCost(rc.res.CostUSDx10000, rc.res.MeteredCalls, rc.res.UnmeteredCalls)
	rc.resMu.Unlock()
	if isCopilotBilling() {
		rc.logf("iter=%d provider=%s tokens=in=%d/out=%d cost=%s credits=%.2f",
			rc.res.Iterations,
			providerLabel(rc.cfg),
			rc.res.InputTokens, rc.res.OutputTokens,
			cost,
			float64(rc.res.CopilotCreditsX1000)/1000.0,
		)
		return
	}
	rc.logf("iter=%d provider=%s tokens=in=%d/out=%d cost=%s",
		rc.res.Iterations,
		providerLabel(rc.cfg),
		rc.res.InputTokens, rc.res.OutputTokens,
		cost,
	)
}

// iterationsSpent reports how a run ends that spent its MaxIterations. The
// budget is spent, but the last wave may have settled every node. The
// engine finalizes a run only when asked for more work, so ask once more
// and report the run as usual. Only unfinished work is an error.
func (rc *runtimeContext) iterationsSpent() error {
	remains, err := rc.workRemains()
	if err != nil {
		return err
	}
	if remains {
		return fmt.Errorf("run %d: MaxIterations=%d reached with nodes still to run; continue with --resume %d", rc.runID, rc.cfg.MaxIterations, rc.runID)
	}
	if _, err := workflow.GetNextNodes(rc.store.Workflows(), rc.runID); err != nil {
		return fmt.Errorf("get next nodes: %w", err)
	}
	return rc.settled()
}

// workRemains reports whether any node of the run is not final (completed,
// failed, skipped or rejected). A run with no node rows has not started, so
// its work remains, as the engine's own finalization treats it.
func (rc *runtimeContext) workRemains() (bool, error) {
	states, err := rc.store.Workflows().GetWorkflowNodeStates(rc.runID)
	if err != nil {
		return false, fmt.Errorf("read node states of run %d: %w", rc.runID, err)
	}
	return len(states) == 0 || anyNodeUnfinished(states), nil
}

// anyNodeUnfinished reports whether any of the node states is not final.
func anyNodeUnfinished(states []db.WorkflowNodeState) bool {
	for _, s := range states {
		if !nodeStatusFinal(s.Status) {
			return true
		}
	}
	return false
}

// nodeStatusFinal reports whether a node status is final: completed,
// failed, skipped or rejected.
func nodeStatusFinal(status string) bool {
	switch status {
	case "completed", "failed", "skipped", "rejected":
		return true
	}
	return false
}

// settled reports how a run with nothing left to dispatch ended: nil for a
// completed or a paused run, else settledUnfinished's error, so agent-run
// exits non-zero on a failed run.
func (rc *runtimeContext) settled() error {
	run, err := rc.store.Workflows().GetWorkflowRun(rc.runID)
	if err != nil {
		return fmt.Errorf("read run %d: %w", rc.runID, err)
	}
	switch run.Status {
	case "completed":
		rc.logf("no more dispatchable nodes — workflow terminal")
		return nil
	case "paused":
		rc.logf("paused for human review — answer with `chb workflow resume %d`, then continue with --resume %d", rc.runID, rc.runID)
		return nil
	}
	return rc.settledUnfinished(run.Status)
}

// settledUnfinished reports a run that settled neither completed nor
// paused: a failed node, a rejected one, or nothing left that can run.
// A dry run produces no outputs, so it stops at the first decision or
// conditioned edge that reads one. That is the dry run's limit, not a
// failure of the workflow.
func (rc *runtimeContext) settledUnfinished(status string) error {
	if rc.cfg.DryRun {
		rc.logf("[dry-run] run %d stopped with nodes still to run: a decision or edge needs outputs a dry run does not produce (chb workflow status %d)", rc.runID, rc.runID)
		return nil
	}
	return fmt.Errorf("run %d %s: a node failed or was rejected, or nothing left can run (chb workflow status %d)", rc.runID, status, rc.runID)
}

// formatUSDx10000 renders a 1/10000-USD integer as a $-prefixed string
// rounded to two decimals. 14_201 → "1.42".
func formatUSDx10000(x10000 int64) string {
	dollars := x10000 / 10000
	cents := x10000 % 10000 / 100
	return fmt.Sprintf("%d.%02d", dollars, cents)
}
