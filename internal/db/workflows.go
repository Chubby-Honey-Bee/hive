package db

import "database/sql"

// Workflow storage primitives. Each method is a focused SQL operation
// that the workflow engine composes. The engine knows no SQL strings;
// it asks the repo for a typed result.
//
// Naming convention: verbs match the engine's vocabulary (Create/
// Mark/Update) so call sites read like the workflow state-machine they
// implement.

// WorkflowsRepo owns the workflow_runs, workflow_node_states,
// workflow_decisions and workflow_repairs tables. Its methods live in one
// file per table, named after it; a method that writes two tables lives
// with the one it is about (a run's creation seeds its node states, and a
// node's finish merges the run's state).
type WorkflowsRepo struct {
	writeDB *sql.DB
	readDB  *sql.DB
}

// newWorkflowsRepo binds the two pools.
func newWorkflowsRepo(writeDB, readDB *sql.DB) *WorkflowsRepo {
	return &WorkflowsRepo{writeDB: writeDB, readDB: readDB}
}
