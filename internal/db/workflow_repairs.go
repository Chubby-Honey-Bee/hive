package db

import "fmt"

// RecordRepairAttempt inserts the start-of-repair row, with the model and
// provider the attempt runs on and the trigger that started it.
func (r *WorkflowsRepo) RecordRepairAttempt(runID int64, nodeName string, attempt int, failureReason, repairPrompt, model, provider, trigger string) (int64, error) {
	res, err := r.writeDB.Exec(
		`INSERT INTO workflow_repairs (run_id, node_name, attempt, failure_reason, repair_prompt, model, provider, trigger)
		 VALUES (?,?,?,?,?,?,?,?)`,
		runID, nodeName, attempt, failureReason, repairPrompt, model, provider, trigger,
	)
	if err != nil {
		return 0, fmt.Errorf("record repair %s/%d: %w", nodeName, attempt, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("repair last-insert-id: %w", err)
	}
	return id, nil
}

// MarkRepairCompleted records a single repair attempt's terminal state.
func (r *WorkflowsRepo) MarkRepairCompleted(repairID int64, outputsJSON string, acceptPassed bool, tokensIn, tokensOut, costUSDx10000 int64, completedAt string) error {
	passed := 0
	if acceptPassed {
		passed = 1
	}
	if _, err := r.writeDB.Exec(
		`UPDATE workflow_repairs
		 SET repair_outputs_json=?, accept_passed=?, tokens_in=?, tokens_out=?, cost_usd_x10000=?, completed_at=?
		 WHERE id=?`,
		outputsJSON, passed, tokensIn, tokensOut, costUSDx10000, completedAt, repairID,
	); err != nil {
		return fmt.Errorf("complete repair %d: %w", repairID, err)
	}
	return nil
}
