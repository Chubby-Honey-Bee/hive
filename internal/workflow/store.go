package workflow

import "github.com/Chubby-Honey-Bee/hive/internal/db"

// Store is where the engine keeps a run: its workflow_runs row, its
// workflow_node_states rows and the decisions it records. It holds the
// methods the engine calls and no others. The binaries pass the
// *db.WorkflowsRepo a store's Workflows returns.
type Store interface {
	CreateWorkflowRun(name string, version int, defnYAML, inputsJSON string, nodes []db.NodeSeed) (int64, error)
	GetWorkflowRun(runID int64) (*db.WorkflowRun, error)
	GetWorkflowNodeStates(runID int64) ([]db.WorkflowNodeState, error)

	MarkNodeRunning(runID int64, nodeName, startedAt string) error
	// ReleaseNode returns a running node to pending and takes back the
	// attempt its claim counted.
	ReleaseNode(runID int64, nodeName string) error
	MarkNodeCompleted(runID int64, nodeName, outputsJSON, completedAt string) error
	MarkNodeFailed(runID int64, nodeName, errMsg, completedAt string, retryable bool) error
	MarkNodeSkipped(runID int64, nodeName, completedAt string) error
	ResetNodeForLoop(runID int64, nodeName string) error
	MarkNodeWaitingHuman(runID int64, nodeName, startedAt string) error
	FindWaitingHumanNode(runID int64) (string, error)

	// FinishNodeInTx writes a node's final status, completed or failed, with
	// the outputs and run state merge derives from the run, which it re-reads
	// inside the same write transaction. When merge returns an error nothing
	// is written.
	FinishNodeInTx(runID int64, nodeName, status string, merge func(run *db.WorkflowRun) (outputsJSON, stateJSON string, err error)) error

	// RecordDecision persists a decision-node evaluation. evalErr is "" when
	// SafeEval succeeded; non-empty when it failed (the engine still records
	// the row so post-hoc audit can see *why* the branch never fired).
	RecordDecision(runID int64, nodeName, condition, evaluatedValue, branchTaken, evalErr string) error

	MarkRunRunning(runID int64) error
	MarkRunPaused(runID int64) error
	MarkRunCompleted(runID int64, completedAt string) error
	MarkRunFailed(runID int64, completedAt string) error
}

var _ Store = (*db.WorkflowsRepo)(nil)
