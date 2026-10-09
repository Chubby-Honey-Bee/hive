package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/comb"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/dreamer"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// dreamerLoopOutcome is the runner-side projection of the dreamer's
// LoopResult. We don't expose the dreamer's internal types to the
// swarm layer; this struct carries just what the workflow node
// needs to record outputs + rationale. Unexported — runner-internal.
type dreamerLoopOutcome struct {
	PassNames    []string
	TotalTouched int
	Halted       bool
	HaltReason   string
}

// dispatchDreamer invokes the Dreamer forager's full pass list against
// the live Comb. Pure consolidation — runs prune → reprove → contradict
// → hypothesize → settle in order, recommend-only by default. Caller
// (the runner) is responsible for surfacing outcomes via workflow
// outputs + rationale + Comb forager-vantage write.
func dispatchDreamer(ctx context.Context, store *db.Store) (*dreamerLoopOutcome, error) {
	loop, err := dreamer.Run(ctx, store, dreamer.LoopOptions{
		MaxPasses: 5,
		Pass:      dreamer.DefaultOptions(),
	})
	out := dreamerOutcome(loop)
	// Halted-by-policy is not fatal — it's how the dreamer stops
	// cleanly on QMP. Surface as outcome.HaltReason rather than as a
	// Go error. Use errors.Is so wrapped sentinels still match.
	if errors.Is(err, dreamer.ErrHalted) {
		err = nil
	}
	return out, err
}

// dreamerOutcome projects the dreamer's LoopResult: an empty outcome for
// none.
func dreamerOutcome(loop *dreamer.LoopResult) *dreamerLoopOutcome {
	out := &dreamerLoopOutcome{}
	if loop == nil {
		return out
	}
	out.Halted, out.HaltReason = loop.Halted, loop.HaltReason
	for _, p := range loop.Passes {
		out.PassNames = append(out.PassNames, p.Name)
		out.TotalTouched += p.Touched
	}
	return out
}

// executeDreamerNode runs a dreamer-archetype forager node. Skips the
// LLM backend; dispatches through internal/dreamer.Run with the
// archetype's standard pass list. The resulting LoopResult is recorded
// as the node's outputs + rationale, so the rationale shows what the
// dreamer touched.
func (rc *runtimeContext) executeDreamerNode(node workflow.DispatchNode) (stop bool) {
	rc.logf("dreamer dispatch: %s (passes: prune→reprove→contradict→hypothesize→settle)", node.Node)

	if rc.cfg.DryRun {
		rc.completeDryRun(node, `{"dry_run":true,"archetype":"dreamer"}`)
		return false
	}

	loopResult, err := dispatchDreamer(rc.ctx, rc.store)
	if err != nil {
		rc.failDreamerNode(node, err)
		return false
	}
	rc.completeDreamerNode(node, loopResult)
	return false
}

// failDreamerNode settles a dreamer node whose loop failed. The run's own
// cancellation is not the node's failure: the node goes back to pending
// (releaseNode).
func (rc *runtimeContext) failDreamerNode(node workflow.DispatchNode, err error) {
	if rc.ctx.Err() != nil {
		rc.releaseNode(node)
		return
	}
	rc.logf("dreamer node %s FAILED: %v", node.Node, err)
	_ = workflow.FailNode(rc.store.Workflows(), rc.runID, node.Node, err.Error())
}

// completeDreamerNode completes a dreamer node with what its loop reports,
// under the loop's own names.
func (rc *runtimeContext) completeDreamerNode(node workflow.DispatchNode, loopResult *dreamerLoopOutcome) {
	outputs := map[string]any{
		"passes":  loopResult.PassNames,
		"touched": loopResult.TotalTouched,
		"halted":  loopResult.Halted,
	}
	// Write the forager vantage BEFORE completing the node, the same
	// ordering the agent path uses and for the same reason: a downstream
	// `cites:` forager can tick the moment the node flips to completed, and
	// it must not find the Comb row missing.
	if node.ForagerName != "" {
		rc.writeDreamerVantage(node, loopResult)
	}

	if err := workflow.CompleteNode(rc.store.Workflows(), rc.runID, node.Node, outputs); err != nil {
		rc.logf("complete dreamer node: %v", err)
		return
	}
	rc.countNodeRun()
	_ = rc.store.Workflows().UpdateNodeRationale(
		rc.runID, node.Node, truncate(dreamerRationale(loopResult), 4000),
	)
}

// writeDreamerVantage writes a dreamer node's verdict to the Comb. The
// verdict is abstain: the dreamer takes no position on the question, so the
// ∇ quorum sensor never counts a dreamer bonded by resonates (scholar,
// timekeeper) as agreeing with a lens, even when the loop halted on an
// integrity violation. A write failure is logged.
func (rc *runtimeContext) writeDreamerVantage(node workflow.DispatchNode, loopResult *dreamerLoopOutcome) {
	if err := comb.BuildForagerVantage(rc.ctx, rc.store, rc.runID, node.ForagerName, "", map[string]any{
		"verdict":        "abstain",
		"recommendation": dreamerRecommendation(loopResult),
		"key_points":     loopResult.PassNames,
		"evidence":       []any{},
		"uncertainties":  []any{},
	}); err != nil {
		rc.logf("comb forager write (%s): %v", node.ForagerName, err)
	}
}

// dreamerRecommendation is the dreamer's verdict's recommendation: the
// passes it ran and the items it touched, and its halt.
func dreamerRecommendation(loopResult *dreamerLoopOutcome) string {
	if loopResult.Halted {
		return fmt.Sprintf("halted (%s) after %d passes; touched %d items", loopResult.HaltReason, len(loopResult.PassNames), loopResult.TotalTouched)
	}
	return fmt.Sprintf("ran %d passes; touched %d items", len(loopResult.PassNames), loopResult.TotalTouched)
}

// dreamerRationale is a dreamer node's rationale: the passes it ran, one a
// line, and why it halted.
func dreamerRationale(loopResult *dreamerLoopOutcome) string {
	var b strings.Builder
	fmt.Fprintf(&b, "dreamer ran %d passes\n", len(loopResult.PassNames))
	for _, p := range loopResult.PassNames {
		b.WriteString("  • " + p + "\n")
	}
	if loopResult.HaltReason != "" {
		b.WriteString("halted: " + loopResult.HaltReason + "\n")
	}
	return b.String()
}
