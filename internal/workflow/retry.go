package workflow

import (
	"fmt"
	"slices"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// FailNode marks a node as failed with automatic retry if under max_retries.
func FailNode(repo Store, runID int64, nodeName, errMsg string) error {
	run, err := repo.GetWorkflowRun(runID)
	if err != nil {
		// Can't read the run — write the failure as non-retryable so it
		// doesn't get stuck running forever.
		return repo.MarkNodeFailed(runID, nodeName, errMsg, timestamp(), false)
	}
	maxRetries := nodeMaxRetriesFromYAML(run.DefinitionYAML, nodeName)
	attempt, err := countedAttempt(repo, runID, nodeName)
	if err != nil {
		return err
	}
	// MarkNodeRunning bumps `attempt` before each run, so attempt counts runs
	// so far and retries so far = attempt - 1. Retry while that count is
	// below max_retries: exactly max_retries retries after the first attempt.
	retriesSoFar := max(attempt-1, 0)
	now := timestamp()
	if retriesSoFar < maxRetries {
		retryMsg := fmt.Sprintf("Attempt %d failed: %s", attempt, errMsg)
		return repo.MarkNodeFailed(runID, nodeName, retryMsg, now, true)
	}
	return repo.MarkNodeFailed(runID, nodeName, errMsg, now, false)
}

// countedAttempt is the node's attempt count with this failure counted; 0
// for a node the run does not hold.
func countedAttempt(repo Store, runID int64, nodeName string) (int, error) {
	nodes, err := repo.GetWorkflowNodeStates(runID)
	if err != nil {
		return 0, err
	}
	i := slices.IndexFunc(nodes, func(n db.WorkflowNodeState) bool { return n.NodeName == nodeName })
	if i < 0 {
		return 0, nil
	}
	return countFailure(repo, runID, nodes[i])
}

// countFailure counts a failure in the node's attempts. A caller can fail a
// node it never marked running (`chb workflow fail` on a pending node). That
// failure is still a run: it is claimed so it counts, or the node would go
// back to pending forever.
func countFailure(repo Store, runID int64, n db.WorkflowNodeState) (int, error) {
	if n.Status != "pending" {
		return n.Attempt, nil
	}
	if err := repo.MarkNodeRunning(runID, n.NodeName, timestamp()); err != nil {
		return 0, err
	}
	return n.Attempt + 1, nil
}

// ReleaseNode returns a node whose dispatch the run's own cancellation
// stopped to pending, with the attempt its claim counted taken back. The
// node did not fail, so FailNode's retry rule does not apply: it spends no
// retry, and the run's next driver (`chb agent-run --resume`) dispatches it
// again. Only a running node is released.
func ReleaseNode(repo Store, runID int64, nodeName string) error {
	return repo.ReleaseNode(runID, nodeName)
}

// nodeMaxRetriesFromYAML is a node's max_retries, 0 when the definition does
// not parse, the node is absent, or it sets none.
func nodeMaxRetriesFromYAML(defnYAML, nodeName string) int {
	defn, err := LoadYAMLString(defnYAML)
	if err != nil {
		return 0
	}
	nodes, _ := defn["nodes"].(map[string]any)
	node, _ := nodes[nodeName].(map[string]any)
	return int(toFloat(node["max_retries"]))
}
