package workflow

import (
	"fmt"
	"os"
	"slices"
)

// readiness is what findReadyNodes makes of one node.
type readiness int

const (
	// nodeWaits: the node is not handed out now: it is not pending, it is a
	// decision, or a predecessor has not finished.
	nodeWaits readiness = iota
	// nodeReady: the node can run.
	nodeReady
	// nodeBlocked: no incoming edge can fire, so the node is to be skipped.
	nodeBlocked
)

// findReadyNodes returns the pending nodes whose predecessors have all
// finished and at least one incoming edge fires, plus the pending nodes whose
// every incoming edge evaluated false (the caller marks those skipped), each
// list in name order.
func findReadyNodes(defn map[string]any, state map[string]any, nodeStates map[string]map[string]any, incoming map[string]map[string]bool, outgoing map[string][]Edge) ([]string, []string) {
	v := &runView{defn: defn, state: state, nodeStates: nodeStates, incoming: incoming, outgoing: outgoing}
	var ready, blocked []string
	for name, raw := range v.nodes() {
		switch v.readiness(name, raw) {
		case nodeReady:
			ready = append(ready, name)
		case nodeBlocked:
			blocked = append(blocked, name)
		}
	}
	slices.Sort(ready)
	slices.Sort(blocked)
	return ready, blocked
}

// readiness decides one node of the definition. A pending node that is not
// a decision, which resolveDecisionNodes resolves, is ready at once when it
// has no predecessor.
func (v *runView) readiness(name string, raw any) readiness {
	node, ok := raw.(map[string]any)
	if !ok || !v.awaitsDispatch(name, node) {
		return nodeWaits
	}
	preds := v.incoming[name]
	if len(preds) == 0 {
		return nodeReady
	}
	return v.joinReadiness(name, node, preds)
}

func (v *runView) awaitsDispatch(name string, node map[string]any) bool {
	t, _ := node["type"].(string)
	return v.nodeStates[name]["status"] == "pending" && t != "decision"
}

// joinReadiness decides a pending node with predecessors. It is ready only
// when every predecessor has finished — completed or skipped — so a join
// never dispatches before all its inputs exist, with unresolved
// {placeholders} in its prompt. A failed predecessor leaves the join
// pending; run-level termination decides the run. A node with `join:
// settled` also counts a failed or rejected predecessor as finished, and
// waits for at least one completed. A node whose every predecessor was
// skipped is blocked, whatever its join mode: nothing that ran reaches it.
// Otherwise a node whose predecessors have finished is ready when an
// incoming edge fires, and blocked when none does.
func (v *runView) joinReadiness(name string, node map[string]any, preds map[string]bool) readiness {
	t := v.tallyPredecessors(preds, joinsSettled(node))
	switch {
	case t.allSkipped:
		return nodeBlocked
	case !t.ready():
		return nodeWaits
	case edgesFire(name, preds, v.nodes(), v.state, v.outgoing, v.nodeStates):
		return nodeReady
	}
	return nodeBlocked
}

// joinTally is how a node's predecessors stand.
type joinTally struct {
	settled      bool // the node declares join: settled
	allFinished  bool // every predecessor finished, as the join counts it
	anyCompleted bool
	allSkipped   bool
}

func (v *runView) tallyPredecessors(preds map[string]bool, settled bool) joinTally {
	t := joinTally{settled: settled, allFinished: true, allSkipped: true}
	for p := range preds {
		t.add(statusOf(v.nodeStates[p]))
	}
	return t
}

func (t *joinTally) add(status string) {
	t.allSkipped = t.allSkipped && status == "skipped"
	t.anyCompleted = t.anyCompleted || status == "completed"
	t.allFinished = t.allFinished && t.finishes(status)
}

// finishes reports whether a predecessor with status has finished, as the
// join counts it: completed or skipped, and for a settled join failed or
// rejected too.
func (t joinTally) finishes(status string) bool {
	return completedOrSkipped(status) || (t.settled && failedOrRejected(status))
}

// ready reports whether the predecessors let the node run: all finished,
// and for a settled join at least one completed.
func (t joinTally) ready() bool {
	return t.allFinished && (!t.settled || t.anyCompleted)
}

func completedOrSkipped(status string) bool {
	return status == "completed" || status == "skipped"
}

func failedOrRejected(status string) bool {
	return status == "failed" || status == "rejected"
}

// edgesFire reports whether any incoming edge into `name` lets it run. An
// edge with no `condition:` always fires, as does any edge from a decision
// node (the decision already chose its branch and skipped the other, so
// re-evaluating its condition here would double-count). A node is blocked
// only when it has at least one conditioned edge and every incoming edge
// evaluates false. An edge from a skipped predecessor never fires: a skipped
// decision chose no branch, and a skipped node ran nothing to carry. So a
// node every one of whose predecessors was skipped is skipped too, however
// the skip cascade reached it; skipBranch stops at a node that still had a
// pending predecessor when it looked, and that node would otherwise run.
func edgesFire(name string, preds map[string]bool, nodes map[string]any, state map[string]any, outgoing map[string][]Edge, nodeStates map[string]map[string]any) bool {
	live := livePredecessors(preds, nodeStates)
	edges := edgesInto(name, live, outgoing)
	if len(edges) == 0 {
		// No edge to judge: a node with a live predecessor runs, and so
		// does a node with no predecessor at all, a start node.
		return len(live) > 0 || len(preds) == 0
	}
	return anyFires(edges, name, nodes, state)
}

// livePredecessors is the predecessors that were not skipped.
func livePredecessors(preds map[string]bool, nodeStates map[string]map[string]any) []string {
	var live []string
	for pred := range preds {
		if statusOf(nodeStates[pred]) != "skipped" {
			live = append(live, pred)
		}
	}
	return live
}

// predEdge is an edge with the node it leaves.
type predEdge struct {
	from string
	Edge
}

// edgesInto is the edges from preds into name, in order.
func edgesInto(name string, preds []string, outgoing map[string][]Edge) []predEdge {
	var edges []predEdge
	for _, p := range preds {
		for _, e := range outgoing[p] {
			if e.Target == name {
				edges = append(edges, predEdge{from: p, Edge: e})
			}
		}
	}
	return edges
}

// anyFires reports whether one of edges fires, judging them in order and
// stopping at the first that does.
func anyFires(edges []predEdge, name string, nodes, state map[string]any) bool {
	for _, e := range edges {
		if edgeFires(e, name, nodes, state) {
			return true
		}
	}
	return false
}

func edgeFires(e predEdge, name string, nodes, state map[string]any) bool {
	if e.Condition == "" || isDecisionNode(nodes[e.from]) {
		return true
	}
	return conditionFires(e.from, name, e.Condition, state)
}

// conditionFires evaluates an edge's condition. One that cannot be evaluated
// fails open, loudly: a stranded run is worse than an extra node run.
func conditionFires(from, to, cond string, state map[string]any) bool {
	v, err := SafeEval(cond, state)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"[workflow] edge %s→%s: condition %q failed to evaluate (%v) — letting the edge fire\n",
			from, to, cond, err)
		return true
	}
	return toBool(v)
}
