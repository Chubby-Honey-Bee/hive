package workflow

// finalizeIfTerminal settles a run GetNextNodes found nothing ready in. A
// node waiting on a human pauses it; a running node leaves it open. Pending
// nodes with nothing running can never become ready, so the run has failed.
// Otherwise every node is final (completed, failed, skipped or rejected):
// failed if any node failed or was rejected and no `join: settled` node took
// its place (absorbed), completed if none did. It marks nothing skipped.
func finalizeIfTerminal(repo Store, runID int64, defn map[string]any, incoming map[string]map[string]bool, nodeStates map[string]map[string]any) error {
	switch outlookOf(nodeStates) {
	case runParked:
		// Parked for a human. Not terminal and not dispatchable: the run
		// is paused until `workflow resume` moves the node on.
		return repo.MarkRunPaused(runID)
	case runStuck:
		// Nothing is ready and nothing is running, so no pending node can
		// ever become ready: a predecessor failed or was rejected, or a
		// decision's condition cannot be evaluated. So the run fails rather
		// than stay `running` for good.
		return repo.MarkRunFailed(runID, timestamp())
	case runFinal:
		return settleRun(repo, runID, defn, incoming, nodeStates)
	}
	return nil
}

// runOutlook is what a run's node statuses say of it when nothing is ready.
type runOutlook int

const (
	// runOpen: a node is running, and the dispatcher re-polls when it
	// finishes; or the run has no node states yet.
	runOpen runOutlook = iota
	// runParked: a node waits on a human.
	runParked
	// runStuck: a node is pending, and nothing is running.
	runStuck
	// runFinal: every node is completed, failed, skipped or rejected.
	runFinal
)

// statusOutlooks is what one node's status says of its run. Any other
// status, running, leaves the run open.
var statusOutlooks = map[string]runOutlook{
	"waiting_human": runParked,
	"pending":       runStuck,
	"completed":     runFinal,
	"failed":        runFinal,
	"skipped":       runFinal,
	"rejected":      runFinal,
}

// outlookOf reads a run's node statuses. A run with none recorded yet has
// not initialized: this path is hit on a freshly-created run before its
// node rows are visible, so it is left open.
func outlookOf(nodeStates map[string]map[string]any) runOutlook {
	if len(nodeStates) == 0 {
		return runOpen
	}
	return statusesOutlook(nodeStates)
}

// statusesOutlook reads the statuses in turn: the first node running or
// waiting on a human decides; else a pending node leaves the run stuck;
// else it is final.
func statusesOutlook(nodeStates map[string]map[string]any) runOutlook {
	outlook := runFinal
	for _, ns := range nodeStates {
		switch o := statusOutlooks[statusOf(ns)]; o {
		case runOpen, runParked:
			return o
		case runStuck:
			outlook = runStuck
		}
	}
	return outlook
}

// settleRun gives a run whose every node is final its outcome. `rejected` is
// terminal for the node (its accept: predicate held after the repair budget
// ran out), so it is terminal for the run too: a run with a rejected node
// never stays in status='running' with nothing able to move it.
func settleRun(repo Store, runID int64, defn map[string]any, incoming map[string]map[string]bool, nodeStates map[string]map[string]any) error {
	now := timestamp()
	if unabsorbedFailure(defn, incoming, nodeStates) {
		return repo.MarkRunFailed(runID, now)
	}
	return repo.MarkRunCompleted(runID, now)
}

// unabsorbedFailure reports whether a node failed or was rejected and the
// nodes after it did not take its place (absorbed).
func unabsorbedFailure(defn map[string]any, incoming map[string]map[string]bool, nodeStates map[string]map[string]any) bool {
	for name, ns := range nodeStates {
		if failedOrRejected(statusOf(ns)) && !absorbed(name, defn, incoming, nodeStates) {
			return true
		}
	}
	return false
}

// absorbed reports whether a failed or rejected node's failure was taken
// over by the nodes after it: it has at least one successor over a forward
// edge, and every one declares `join: settled` and completed. Those nodes ran
// knowing it had no output, so the run's outcome rests on them.
func absorbed(name string, defn map[string]any, incoming map[string]map[string]bool, nodeStates map[string]map[string]any) bool {
	nodes, _ := defn["nodes"].(map[string]any)
	successors := 0
	for succ, preds := range incoming {
		if !preds[name] {
			continue
		}
		successors++
		if !tookOver(nodes[succ], nodeStates[succ]) {
			return false
		}
	}
	return successors > 0
}

// tookOver reports whether a successor ran in a failed predecessor's place:
// it declares join: settled and completed.
func tookOver(raw any, nodeState map[string]any) bool {
	node, _ := raw.(map[string]any)
	return joinsSettled(node) && statusOf(nodeState) == "completed"
}
