package runner

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// callsMade is how many model calls answered r's run: the turns its backend
// counted, else 1 when a backend that counts none returned text or tokens,
// else 0. A run whose first call failed or was never sent, and a missing
// result, made none.
func callsMade(r *RunResult) int {
	switch {
	case r == nil:
		return 0
	case r.Turns > 0:
		return r.Turns
	case runResultAnswered(r):
		return 1
	}
	return 0
}

// runResultAnswered reports whether a run returned text or counted tokens.
func runResultAnswered(r *RunResult) bool {
	return r.FinalText != "" || r.InputTokens > 0 || r.OutputTokens > 0
}

// spendParts lists the runs in res that are priced one by one: its
// ItemResults, each broken down the same way, or res itself when it has
// none.
func spendParts(res *RunResult) []*RunResult {
	if len(res.ItemResults) == 0 {
		return []*RunResult{res}
	}
	var parts []*RunResult
	for _, r := range res.ItemResults {
		parts = append(parts, spendParts(r)...)
	}
	return parts
}

// recordSpend adds one backend run's tokens and cost to the node's
// workflow_node_states row, records its provider and endpoint, and adds its
// cost and Copilot credits to the run totals. The run made callsMade(res)
// model calls; one that made none records nothing and returns 0, 0. A
// parallel_fan's items and the attempts a rate-limit retry folded in
// (res.ItemResults, and theirs in turn) are priced one by one, each as its
// own call. A run whose backend names the models it called (ByModel) is
// priced model by model. Calls whose cost is unknown, to a model with no
// models-config entry off this machine or on a backend that reports no token
// counts (UsageUnreported), are counted as unmetered, not as $0; a call on
// this machine costs nothing (onThisMachine). Its calls cut off
// at the output cap (res.CutOffCalls) add to the node's cutoff_calls. Tokens
// reach the run totals elsewhere (recordInvocations, or the repair loop).
// Returns the run's cost and credits. Best-effort: a DB write failure is
// logged, never fatal.
func (rc *runtimeContext) recordSpend(node workflow.DispatchNode, model, provider, baseURL string, res *RunResult) (costX10000, creditsX1000 int64) {
	if callsMade(res) == 0 {
		return 0, 0
	}
	tally := tallySpend(spendParts(res), model, provider, baseURL)
	// Copilot premium-request credits, x1000 to preserve the 0.33×/0.05×
	// fractions as integers. One credit-multiplier per call (Copilot bills
	// per request, not per token).
	creditsX1000 = int64(copilotMultiplier(model) * 1000)
	rc.writeNodeSpend(node, provider, baseURL, res, tally)
	for _, line := range rc.addRunSpend(node, model, tally, creditsX1000) {
		rc.logf("%s", line)
	}
	return tally.cost, creditsX1000
}

// spendTally is what recordSpend sums over the parts of a run it prices:
// the metered calls' cost, the metered and unmetered calls, why each
// unmetered part's calls are, and the models the backends report calling,
// in the order first seen.
type spendTally struct {
	cost               int64
	metered, unmetered int64
	reasons            []string
	used               []string
}

// tallySpend prices each part's calls (spendTally.add); unpriced calls are
// priced at the node's model on provider, whose calls go to baseURL.
func tallySpend(parts []*RunResult, model, provider, baseURL string) spendTally {
	var t spendTally
	for _, r := range parts {
		t.add(r, model, provider, baseURL)
	}
	return t
}

// add prices one part's calls. Calls whose usage the backend does not
// report, and calls to a model with no price off this machine, are
// unmetered: part of the cost is unknown, so none of it counts, and
// cost_usd_x10000 stays the metered calls' sum. A model with no price on
// this machine costs nothing (costKnown).
func (t *spendTally) add(r *RunResult, model, provider, baseURL string) {
	calls := int64(callsMade(r))
	if calls == 0 {
		return
	}
	usage := partUsage(r, model)
	t.noteModels(r.ByModel)
	cost, priced, unpriced := usageCostUSDx10000(usage)
	switch {
	case r.UsageUnreported:
		t.unmeter(calls, unreportedUsageReason(r, model, provider))
	case !costKnown(priced, unpriced, provider, baseURL):
		t.unmeter(calls, fmt.Sprintf("model %q has no price in the models config", unpriced))
	default:
		t.metered += calls
		t.cost += cost
	}
}

// noteModels adds the models a backend names that the tally has not seen.
func (t *spendTally) noteModels(usage []ModelUsage) {
	for _, u := range usage {
		if !slices.Contains(t.used, u.Model) {
			t.used = append(t.used, u.Model)
		}
	}
}

// unmeter counts calls as unmetered, for reason.
func (t *spendTally) unmeter(calls int64, reason string) {
	t.reasons = append(t.reasons, reason)
	t.unmetered += calls
}

// partUsage is what a part is priced on: the models its backend names it
// called (ByModel), else its tokens at the node's model.
func partUsage(r *RunResult, model string) []ModelUsage {
	if len(r.ByModel) > 0 {
		return r.ByModel
	}
	return []ModelUsage{{Model: model, InputTokens: r.InputTokens, CachedInputTokens: r.CachedInputTokens,
		CacheWriteTokens: r.CacheWriteTokens, CacheWrite1hTokens: r.CacheWrite1hTokens, OutputTokens: r.OutputTokens}}
}

// unreportedUsageReason says why a part's calls are unmetered when its
// backend reports no token counts.
func unreportedUsageReason(r *RunResult, model, provider string) string {
	why := fmt.Sprintf("provider %s reports no token counts for model %q", provider, model)
	if r.UsageUnreportedWhy != "" {
		why += " (" + r.UsageUnreportedWhy + ")"
	}
	return why
}

// writeNodeSpend adds a run's tokens and cost to the node's row, with its
// provider and endpoint, and its calls cut off at the output cap.
// Best-effort: a write failure is logged.
func (rc *runtimeContext) writeNodeSpend(node workflow.DispatchNode, provider, baseURL string, res *RunResult, t spendTally) {
	if err := rc.store.Workflows().UpdateNodeMetrics(
		rc.runID, node.Node, res.InputTokens, res.OutputTokens, t.cost, provider, baseURL, t.metered, t.unmetered,
	); err != nil {
		rc.logf("update metrics (run %d node %s): %v", rc.runID, node.Node, err)
	}
	if res.CutOffCalls > 0 {
		if err := rc.store.Workflows().AddNodeCutoffCalls(rc.runID, node.Node, int64(res.CutOffCalls)); err != nil {
			rc.logf("record cut-off calls (run %d node %s): %v", rc.runID, node.Node, err)
		}
	}
}

// addRunSpend adds a tally and its credits to the run totals, and returns
// the lines to log: each unmetered reason the run has not logged yet, and
// the models the calls used where they differ from the node's.
func (rc *runtimeContext) addRunSpend(node workflow.DispatchNode, model string, t spendTally, creditsX1000 int64) []string {
	rc.resMu.Lock()
	defer rc.resMu.Unlock()
	rc.res.CostUSDx10000 += t.cost
	rc.res.CopilotCreditsX1000 += creditsX1000
	rc.res.MeteredCalls += int(t.metered)
	rc.res.UnmeteredCalls += int(t.unmetered)
	lines := rc.newUnmeteredLines(t.reasons)
	if line := usedModelsLine(node, model, t.used); line != "" && firstSpendLine(&rc.usedModels, line) {
		lines = append(lines, line)
	}
	return lines
}

// newUnmeteredLines is the line for each unmetered reason the run has not
// logged yet. Callers hold resMu.
func (rc *runtimeContext) newUnmeteredLines(reasons []string) []string {
	var lines []string
	for _, why := range reasons {
		if firstSpendLine(&rc.unmetered, why) {
			lines = append(lines, why+": its calls are reported unmetered, and --max-cost-usd does not count them")
		}
	}
	return lines
}

// usedModelsLine names the models a node's calls used, each priced at its
// own prices, where they differ from the node's model, which the row's
// resolved_model names; "" where they do not.
func usedModelsLine(node workflow.DispatchNode, model string, used []string) string {
	if len(used) == 0 || len(used) == 1 && used[0] == model {
		return ""
	}
	slices.Sort(used)
	return fmt.Sprintf("node %s: its calls used %s, each priced at its own prices; the node names %q", node.Node, strings.Join(used, ", "), model)
}

// firstSpendLine reports whether line is not yet in seen, which it then
// holds, made when nil. Callers hold resMu.
func firstSpendLine(seen *map[string]bool, line string) bool {
	if *seen == nil {
		*seen = map[string]bool{}
	}
	if (*seen)[line] {
		return false
	}
	(*seen)[line] = true
	return true
}

// runConstraintProbes sends the constraint probes (ProbeConstraints), logs
// each, and charges each answered probe's tokens and cost to the run, so
// --max-cost-usd sees them. No node row carries them: the run's row does,
// and every persisted run total adds it. It returns the probes by
// (endpoint, model, reasoning).
func (rc *runtimeContext) runConstraintProbes() map[probeKey]ConstraintProbe {
	probes := map[probeKey]ConstraintProbe{}
	for _, p := range ProbeConstraints(rc.ctx, rc.cfg, rc.parsedDefn, rc.logf) {
		probes[p.key()] = p
		rc.logf("constraint probe: %s", p.Summary())
		if p.Answered {
			rc.chargeProbe(p)
		}
	}
	return probes
}

// chargeProbe charges an answered probe's tokens and cost to the run
// totals and the run's row. Best-effort: a write failure is logged.
func (rc *runtimeContext) chargeProbe(p ConstraintProbe) {
	cost := ComputeCostUSDx10000(p.Model, p.InputTokens, p.OutputTokens)
	metered, unmetered := probeCallMetering(p)
	rc.resMu.Lock()
	rc.res.InputTokens += p.InputTokens
	rc.res.OutputTokens += p.OutputTokens
	rc.res.CostUSDx10000 += cost
	rc.res.MeteredCalls += int(metered)
	rc.res.UnmeteredCalls += int(unmetered)
	rc.resMu.Unlock()
	if err := rc.store.Workflows().AddRunProbeUsage(rc.runID, p.InputTokens, p.OutputTokens, cost, metered, unmetered); err != nil {
		rc.logf("record constraint probe usage (run %d): %v", rc.runID, err)
	}
}

// probeCallMetering counts a probe's call as metered, or as unmetered when
// its cost is unknown (costKnown): its model has no price, and it was sent
// off this machine.
func probeCallMetering(p ConstraintProbe) (metered, unmetered int64) {
	if !costKnown(isMetered(p.Model), p.Model, string(p.Provider), p.BaseURL) {
		return 0, 1
	}
	return 1, 0
}
