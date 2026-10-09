package db

import "fmt"

// RecordDecision persists a decision-node evaluation outcome.
func (r *WorkflowsRepo) RecordDecision(runID int64, nodeName, condition, evaluatedValue, branchTaken, evalErr string) error {
	var bt, ee any
	if branchTaken != "" {
		bt = branchTaken
	}
	if evalErr != "" {
		ee = evalErr
	}
	if _, err := r.writeDB.Exec(
		`INSERT INTO workflow_decisions (run_id, node_name, condition, evaluated_value, branch_taken, eval_error)
		 VALUES (?,?,?,?,?,?)`,
		runID, nodeName, condition, evaluatedValue, bt, ee,
	); err != nil {
		return fmt.Errorf("record decision %s/%s: %w", nodeName, condition, err)
	}
	return nil
}
