package workflow

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// resolveDecisionNodes evaluates every pending decision that can be, in
// name order. A resolved decision can ready another, so the scan repeats
// until one resolves nothing. No node runs meanwhile, so a decision resolves
// again only when a loop that ran no node returned it to pending. One such
// pass is allowed. A decision that comes round a third time ends the
// resolution with an error naming it, so a loop with no node to run, a
// decision whose branch is itself or a cycle of decisions, cannot spin.
func resolveDecisionNodes(repo Store, runID int64, defn map[string]any, state map[string]any, nodeStates map[string]map[string]any, incoming map[string]map[string]bool, outgoing map[string][]Edge, loops loopSet) error {
	v := &runView{repo: repo, runID: runID, defn: defn, state: state, nodeStates: nodeStates, incoming: incoming, outgoing: outgoing, loops: loops}
	decisions := v.decisionNames()
	resolutions := map[string]int{}
	for {
		resolved := v.resolvePendingDecisions(decisions)
		if len(resolved) == 0 {
			return nil
		}
		if again := countResolutions(resolutions, resolved); len(again) > 0 {
			return fmt.Errorf("run %d: a loop that runs no node came back to %s a third time in one call; one such pass is allowed, not two", runID, strings.Join(again, ", "))
		}
	}
}

// countResolutions counts one more resolution of each decision of resolved,
// and returns those resolved a third time.
func countResolutions(counts map[string]int, resolved []string) []string {
	var third []string
	for _, name := range resolved {
		counts[name]++
		if counts[name] > 2 {
			third = append(third, name)
		}
	}
	return third
}

// decisionNames is the definition's decision nodes, in name order.
func (v *runView) decisionNames() []string {
	var names []string
	for _, name := range slices.Sorted(maps.Keys(v.nodes())) {
		if isDecisionNode(v.nodes()[name]) {
			names = append(names, name)
		}
	}
	return names
}

// resolvePendingDecisions makes one scan over the pending decisions, in name
// order, and returns those it resolved.
func (v *runView) resolvePendingDecisions(decisions []string) []string {
	var resolved []string
	for _, name := range decisions {
		if v.nodeStates[name]["status"] == "pending" && evaluateDecision(v.repo, v.runID, name, v.defn, v.state, v.nodeStates, v.incoming, v.outgoing, v.loops) {
			resolved = append(resolved, name)
		}
	}
	return resolved
}

// evaluateDecision checks whether a single pending decision node is ready to
// fire, evaluates its condition, marks it completed, skips the not-taken
// branch, and persists the outcome. Returns true if the node was resolved so
// the outer loop knows to re-scan for newly-unblocked decisions.
//
// A decision waits until every predecessor is completed or skipped. It is
// readied like any other node: when every incoming edge is false it is
// skipped, and neither branch runs. Otherwise its condition chooses its
// branch.
func evaluateDecision(repo Store, runID int64, name string, defn map[string]any, state map[string]any, nodeStates map[string]map[string]any, incoming map[string]map[string]bool, outgoing map[string][]Edge, loops loopSet) bool {
	v := &runView{repo: repo, runID: runID, defn: defn, state: state, nodeStates: nodeStates, incoming: incoming, outgoing: outgoing, loops: loops}
	if !v.predecessorsFinished(name) {
		return false
	}
	node, _ := v.nodes()[name].(map[string]any)
	if !edgesFire(name, incoming[name], v.nodes(), state, outgoing, nodeStates) {
		v.skipDecision(name, node)
		return true
	}
	return v.decide(name, node)
}

func (v *runView) predecessorsFinished(name string) bool {
	for p := range v.incoming[name] {
		if !completedOrSkipped(statusOf(v.nodeStates[p])) {
			return false
		}
	}
	return true
}

// skipDecision marks a decision no incoming edge fires for skipped, and
// skips both of its branches: it chose neither.
func (v *runView) skipDecision(name string, node map[string]any) {
	v.nodeStates[name]["status"] = "skipped"
	if err := v.repo.MarkNodeSkipped(v.runID, name, timestamp()); err != nil {
		fmt.Fprintf(os.Stderr,
			"[workflow] run %d decision %q: failed to persist skip: %v\n",
			v.runID, name, err,
		)
	}
	for _, branch := range []string{stringField(node, "true_edge"), stringField(node, "false_edge")} {
		if branch != "" {
			skipBranch(v.repo, v.runID, branch, name, v.nodeStates, v.incoming)
		}
	}
}

// decide evaluates a ready decision's condition (true when it has none),
// completes the decision, follows the branch the condition chose and records
// the outcome. A condition that cannot be evaluated leaves the decision
// pending.
func (v *runView) decide(name string, node map[string]any) bool {
	condition := stringField(node, "condition")
	if condition == "" {
		condition = "true"
	}
	result, err := SafeEval(condition, v.state)
	if err != nil {
		v.recordEvalError(name, condition, err)
		return false
	}
	v.completeDecision(name, node)
	took := toBool(result)
	v.recordDecision(name, condition, took, v.takeBranch(name, node, took))
	return true
}

// recordEvalError reports a condition that failed to evaluate: swallowed, it
// would leave the decision node pending, then skipped by the terminal-state
// pass, with no sign of why its branch never fired. It is logged to stderr
// so the operator sees it, and persisted so post-hoc audit can show *why*
// the branch never fired. It is not raised: re-raising would abort the whole
// run.
func (v *runView) recordEvalError(name, condition string, err error) {
	fmt.Fprintf(os.Stderr,
		"[workflow] run %d decision %q: condition %q failed to evaluate (%v) — leaving pending\n",
		v.runID, name, condition, err,
	)
	if recErr := v.repo.RecordDecision(v.runID, name, condition, "<error>", "", err.Error()); recErr != nil {
		fmt.Fprintf(os.Stderr,
			"[workflow] run %d decision %q: failed to record eval error: %v\n",
			v.runID, name, recErr,
		)
	}
}

// completeDecision marks a decision completed with empty outputs: a decision
// produces a branch, not arbitrary outputs. A failure to persist it is
// logged, not raised.
func (v *runView) completeDecision(name string, node map[string]any) {
	v.nodeStates[name]["status"] = "completed"
	if err := v.persistDecision(name, node); err != nil {
		fmt.Fprintf(os.Stderr,
			"[workflow] run %d decision %q: failed to persist completion: %v\n",
			v.runID, name, err,
		)
	}
}

// persistDecision writes a decision's completion. Its state_updates merge
// into the run state in the same transaction, and into this pass's copy of
// state so the branch it takes can read them.
func (v *runView) persistDecision(name string, node map[string]any) error {
	if _, has := node["state_updates"]; !has {
		return v.repo.MarkNodeCompleted(v.runID, name, "{}", timestamp())
	}
	var merged map[string]any
	err := v.repo.FinishNodeInTx(v.runID, name, "completed", func(run *db.WorkflowRun) (string, string, error) {
		merged = decodeState(run.StateJSON)
		applyStateUpdatesEngine(v.defn, name, merged)
		st, _ := json.Marshal(merged)
		return "{}", string(st), nil
	})
	if err == nil {
		maps.Copy(v.state, merged)
	}
	return err
}

// takeBranch follows the branch a decision's condition chose and returns
// it. When the branch loops back, the loop runs again, this decision with
// it, and the other branch waits for a later pass; otherwise the branch not
// taken is skipped.
func (v *runView) takeBranch(name string, node map[string]any, took bool) string {
	taken, notTaken := stringField(node, "false_edge"), stringField(node, "true_edge")
	if took {
		taken, notTaken = notTaken, taken
	}
	if body, loop := v.loops[[2]string{name, taken}]; loop {
		v.resetLoop(name, body)
	} else if notTaken != "" {
		skipBranch(v.repo, v.runID, notTaken, name, v.nodeStates, v.incoming)
	}
	return taken
}

// resetLoop returns a loop's body to pending for its next pass. A skipped
// node the body reaches over forward edges through skipped nodes only, such
// as an inner decision's exit that an earlier pass skipped, may run on the
// next pass. Each one reset has a reset predecessor, so it waits for the
// next pass to reach it. A skipped node whose only way in is a finished node
// past the body, such as a side decision's untaken branch, stays skipped:
// nothing on the next pass chooses it again.
func (v *runView) resetLoop(decision string, body []string) {
	for _, n := range body {
		v.resetForLoop(decision, n)
	}
	queue := append([]string(nil), body...)
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		queue = append(queue, v.resetSkippedSuccessors(decision, n)...)
	}
}

// resetSkippedSuccessors resets each skipped node n reaches over a forward
// edge, and returns them.
func (v *runView) resetSkippedSuccessors(decision, n string) []string {
	var reset []string
	for _, e := range v.outgoing[n] {
		if !v.incoming[e.Target][n] || v.nodeStates[e.Target]["status"] != "skipped" {
			continue
		}
		v.resetForLoop(decision, e.Target)
		reset = append(reset, e.Target)
	}
	return reset
}

func (v *runView) resetForLoop(decision, n string) {
	v.nodeStates[n]["status"] = "pending"
	if err := v.repo.ResetNodeForLoop(v.runID, n); err != nil {
		fmt.Fprintf(os.Stderr,
			"[workflow] run %d decision %q: failed to reset %q for the next pass: %v\n",
			v.runID, decision, n, err,
		)
	}
}

// recordDecision persists the decision's outcome to workflow_decisions.
// Best-effort; a recording failure must not abort the run.
func (v *runView) recordDecision(name, condition string, took bool, branchTaken string) {
	if err := v.repo.RecordDecision(v.runID, name, condition, strconv.FormatBool(took), branchTaken, ""); err != nil {
		fmt.Fprintf(os.Stderr,
			"[workflow] run %d decision %q: failed to record outcome: %v\n",
			v.runID, name, err,
		)
	}
}
