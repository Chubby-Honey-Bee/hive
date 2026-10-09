package workflow

import (
	"fmt"
	"os"
)

// skipBranch BFS-marks startNode and its descendants as 'skipped'. Both
// startNode and descendants are skipped only when no live predecessor exists
// *outside the dead branch we're walking* — i.e. nodes reachable from both
// the dead branch and a surviving sibling stay pending and will be
// dispatched once their surviving parent completes. The calling decision
// node `decisionNode` is the dead branch's predecessor by construction and
// always 'completed' by the time we reach this function, so it (and only it)
// is left out when checking startNode for other live predecessors: otherwise
// the not-taken branch's first hop would always be skipped, killing any node
// reachable via two different decision branches (e.g. a shared synthesize
// step downstream of both a wave1-COMPLETE shortcut and a wave2 follow-up
// path).
//
// For descendants (not the startNode) the calling decision is not their
// direct predecessor, so the predecessor scan considers every incoming edge.
func skipBranch(repo Store, runID int64, startNode, decisionNode string, nodeStates map[string]map[string]any, incoming map[string]map[string]bool) {
	w := &branchSkip{repo: repo, runID: runID, start: startNode, decision: decisionNode, nodeStates: nodeStates, incoming: incoming, visited: map[string]bool{}}
	queue := []string{startNode}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		queue = append(queue, w.visit(node)...)
	}
}

// branchSkip is skipBranch's walk: the branch's start and the decision that
// left it, and the nodes visited so far.
type branchSkip struct {
	repo       Store
	runID      int64
	start      string
	decision   string
	nodeStates map[string]map[string]any
	incoming   map[string]map[string]bool
	visited    map[string]bool
}

// visit skips node when it is pending with no other live predecessor, and
// returns its children not yet visited: they may need skipping now that one
// of their predecessors is dead. It returns none for a node visited before
// or left as it is.
func (w *branchSkip) visit(node string) []string {
	if w.visited[node] {
		return nil
	}
	w.visited[node] = true
	if statusOf(w.nodeStates[node]) != "pending" || w.hasOtherLive(node) {
		return nil
	}
	w.skip(node)
	return w.unvisitedChildren(node)
}

// hasOtherLive reports whether a predecessor that could still reach node
// exists. For the start node the calling decision is left out, since it is
// always completed by construction; for descendants every incoming edge
// counts.
func (w *branchSkip) hasOtherLive(node string) bool {
	for pred := range w.incoming[node] {
		if !w.isCaller(node, pred) && w.isLive(pred) {
			return true
		}
	}
	return false
}

// isCaller reports whether pred is the calling decision, as a predecessor
// of the start node.
func (w *branchSkip) isCaller(node, pred string) bool {
	return node == w.start && pred == w.decision
}

// isLive reports whether a predecessor gives a path to live execution from
// somewhere other than this walk: completed and not already visited by it,
// or still running or pending.
func (w *branchSkip) isLive(pred string) bool {
	switch statusOf(w.nodeStates[pred]) {
	case "completed":
		return !w.visited[pred]
	case "running", "pending":
		return true
	}
	return false
}

func (w *branchSkip) skip(node string) {
	w.nodeStates[node]["status"] = "skipped"
	if err := w.repo.MarkNodeSkipped(w.runID, node, timestamp()); err != nil {
		fmt.Fprintf(os.Stderr,
			"[workflow] run %d skipBranch %q: failed to persist skip: %v\n",
			w.runID, node, err,
		)
	}
}

func (w *branchSkip) unvisitedChildren(node string) []string {
	var children []string
	for child, preds := range w.incoming {
		if preds[node] && !w.visited[child] {
			children = append(children, child)
		}
	}
	return children
}
