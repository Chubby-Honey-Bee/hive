package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// CreateWorkflowRun inserts a workflow_runs row and seeds the
// workflow_node_states rows from the definition's nodes map. Returns
// the new run ID. The state_json starts as a copy of inputs_json (the
// engine's convention — every input becomes a top-level state var).
func (r *WorkflowsRepo) CreateWorkflowRun(name string, version int, defnYAML, inputsJSON string,
	nodes []NodeSeed,
) (int64, error) {
	tx, err := r.writeDB.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck — Rollback is no-op after Commit

	runID, err := createRunIn(tx, name, version, defnYAML, inputsJSON, nodes)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return runID, nil
}

// createRunIn inserts the run row and seeds its node states in tx.
func createRunIn(tx *sql.Tx, name string, version int, defnYAML, inputsJSON string, nodes []NodeSeed) (int64, error) {
	runID, err := insertRun(tx, name, version, defnYAML, inputsJSON)
	if err != nil {
		return 0, err
	}
	return runID, seedNodes(tx, runID, nodes)
}

// insertRun inserts the workflow_runs row, its state a copy of its inputs,
// and returns its id.
func insertRun(tx *sql.Tx, name string, version int, defnYAML, inputsJSON string) (int64, error) {
	res, err := tx.Exec(
		`INSERT INTO workflow_runs (workflow_name, workflow_version, definition_yaml, inputs_json, state_json)
		 VALUES (?,?,?,?,?)`,
		name, version, defnYAML, inputsJSON, inputsJSON,
	)
	if err != nil {
		return 0, fmt.Errorf("create workflow run: %w", err)
	}
	runID, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id: %w", err)
	}
	return runID, nil
}

// GetWorkflowRun returns the typed run row, or sql.ErrNoRows if the id
// doesn't exist.
func (r *WorkflowsRepo) GetWorkflowRun(runID int64) (*WorkflowRun, error) {
	out := &WorkflowRun{}
	err := r.readDB.QueryRow(
		`SELECT id, workflow_name, workflow_version, definition_yaml,
		        inputs_json, state_json, status, started_at, completed_at
		 FROM workflow_runs WHERE id=?`, runID,
	).Scan(&out.ID, &out.Name, &out.Version, &out.DefinitionYAML,
		&out.InputsJSON, &out.StateJSON, &out.Status, &out.StartedAt, &out.CompletedAt)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListWorkflowRuns returns the most recent runs (id-descending) up to
// limit.
func (r *WorkflowsRepo) ListWorkflowRuns(limit int) ([]WorkflowRun, error) {
	rows, err := r.readDB.Query(
		`SELECT id, workflow_name, workflow_version, '', '', '',
		        status, started_at, completed_at
		 FROM workflow_runs ORDER BY id DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list workflow runs: %w", err)
	}
	defer rows.Close()

	var out []WorkflowRun
	for rows.Next() {
		var row WorkflowRun
		if err := rows.Scan(&row.ID, &row.Name, &row.Version, &row.DefinitionYAML,
			&row.InputsJSON, &row.StateJSON, &row.Status, &row.StartedAt, &row.CompletedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// MarkRunCompleted sets status='completed' and completed_at on the run.
func (r *WorkflowsRepo) MarkRunCompleted(runID int64, completedAt string) error {
	if _, err := r.writeDB.Exec(
		"UPDATE workflow_runs SET status='completed', completed_at=? WHERE id=?",
		completedAt, runID,
	); err != nil {
		return fmt.Errorf("complete run %d: %w", runID, err)
	}
	return nil
}

// MarkRunFailed closes a run that finished with work it could not complete —
// a node that failed, or one whose accept: predicate rejected it after its
// repair budget ran out. Distinct from 'completed' so a caller can tell a
// clean finish from a salvaged one.
func (r *WorkflowsRepo) MarkRunFailed(runID int64, completedAt string) error {
	if _, err := r.writeDB.Exec(
		"UPDATE workflow_runs SET status='failed', completed_at=? WHERE id=?",
		completedAt, runID,
	); err != nil {
		return fmt.Errorf("fail run %d: %w", runID, err)
	}
	return nil
}

// MarkRunPaused parks the run itself so GetNextNodes stops handing out work
// and the dispatcher exits without declaring the run finished.
func (r *WorkflowsRepo) MarkRunPaused(runID int64) error {
	if _, err := r.writeDB.Exec("UPDATE workflow_runs SET status='paused' WHERE id=?", runID); err != nil {
		return fmt.Errorf("pause run %d: %w", runID, err)
	}
	return nil
}

// MarkRunRunning resets a paused run back to running (used by Resume).
func (r *WorkflowsRepo) MarkRunRunning(runID int64) error {
	if _, err := r.writeDB.Exec(
		"UPDATE workflow_runs SET status='running' WHERE id=?", runID,
	); err != nil {
		return fmt.Errorf("resume run %d: %w", runID, err)
	}
	return nil
}

// AddRunProbeUsage adds one constraint probe's usage to its run's row: a
// model call made for no node, which every run total adds to its nodes'.
func (r *WorkflowsRepo) AddRunProbeUsage(runID, tokensIn, tokensOut, costUSDx10000, meteredCalls, unmeteredCalls int64) error {
	if _, err := r.writeDB.Exec(
		`UPDATE workflow_runs
		 SET probe_tokens_in       = COALESCE(probe_tokens_in,       0) + ?,
		     probe_tokens_out      = COALESCE(probe_tokens_out,      0) + ?,
		     probe_cost_usd_x10000 = COALESCE(probe_cost_usd_x10000, 0) + ?,
		     probe_metered_calls   = COALESCE(probe_metered_calls,   0) + ?,
		     probe_unmetered_calls = COALESCE(probe_unmetered_calls, 0) + ?
		 WHERE id=?`,
		tokensIn, tokensOut, costUSDx10000, meteredCalls, unmeteredCalls, runID,
	); err != nil {
		return fmt.Errorf("add probe usage to run %d: %w", runID, err)
	}
	return nil
}

// ProbeUsage is a run's constraint-probe usage, from its workflow_runs row.
type ProbeUsage struct {
	TokensIn, TokensOut, CostUSDx10000, MeteredCalls, UnmeteredCalls int64
}

// ReadProbeUsage reads runID's constraint-probe usage from conn. A run with
// no row has none.
func ReadProbeUsage(conn *sql.DB, runID int64) (ProbeUsage, error) {
	var u ProbeUsage
	err := conn.QueryRow(
		`SELECT COALESCE(probe_tokens_in,0), COALESCE(probe_tokens_out,0), COALESCE(probe_cost_usd_x10000,0),
		        COALESCE(probe_metered_calls,0), COALESCE(probe_unmetered_calls,0)
		 FROM workflow_runs WHERE id=?`, runID,
	).Scan(&u.TokensIn, &u.TokensOut, &u.CostUSDx10000, &u.MeteredCalls, &u.UnmeteredCalls)
	if errors.Is(err, sql.ErrNoRows) {
		return ProbeUsage{}, nil
	}
	if err != nil {
		return ProbeUsage{}, fmt.Errorf("read probe usage of run %d: %w", runID, err)
	}
	return u, nil
}
