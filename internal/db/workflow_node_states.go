package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// seedNodes inserts one pending workflow_node_states row per node.
func seedNodes(tx *sql.Tx, runID int64, nodes []NodeSeed) error {
	for _, n := range nodes {
		if _, err := tx.Exec(
			"INSERT INTO workflow_node_states (run_id, node_name, node_type, status) VALUES (?,?,?,'pending')",
			runID, n.Name, n.Type,
		); err != nil {
			return fmt.Errorf("seed node %q: %w", n.Name, err)
		}
	}
	return nil
}

// NodeSeed is the minimum information the engine needs to give the
// store about each node when seeding the run. Kept narrow so the engine
// can build the slice from its parsed YAML without leaking maps into
// the storage layer.
type NodeSeed struct {
	Name string
	Type string
}

// GetWorkflowNodeStates returns every node state for a run, ordered by
// id (which preserves the seed order from CreateWorkflowRun).
func (r *WorkflowsRepo) GetWorkflowNodeStates(runID int64) ([]WorkflowNodeState, error) {
	rows, err := r.readDB.Query(
		`SELECT id, run_id, node_name, node_type, status,
		        outputs_json, error, attempt, started_at, completed_at,
		        COALESCE(resolved_model, ''), COALESCE(schema_enforcement, '')
		 FROM workflow_node_states WHERE run_id=? ORDER BY id`, runID,
	)
	if err != nil {
		return nil, fmt.Errorf("get node states: %w", err)
	}
	defer rows.Close()

	var out []WorkflowNodeState
	for rows.Next() {
		var n WorkflowNodeState
		if err := rows.Scan(&n.ID, &n.RunID, &n.NodeName, &n.NodeType, &n.Status,
			&n.OutputsJSON, &n.Error, &n.Attempt, &n.StartedAt, &n.CompletedAt,
			&n.Model, &n.SchemaEnforcement); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ErrNodeNotPending is MarkNodeRunning's error for a node it did not claim
// because the node is not pending: another driver of the run claimed it
// first, or it has finished.
var ErrNodeNotPending = errors.New("node is not pending")

// MarkNodeRunning claims a pending node: it marks it running, bumps the
// attempt counter and sets started_at. The claim is taken only from
// pending, in the one UPDATE, so of two drivers of a run that read the same
// pending node one claims it; for the other the node is left as it is and
// the error is ErrNodeNotPending.
func (r *WorkflowsRepo) MarkNodeRunning(runID int64, nodeName, startedAt string) error {
	res, err := r.writeDB.Exec(
		"UPDATE workflow_node_states SET status='running', started_at=?, attempt=attempt+1 WHERE run_id=? AND node_name=? AND status='pending'",
		startedAt, runID, nodeName,
	)
	if err != nil {
		return fmt.Errorf("mark node %q running: %w", nodeName, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark node %q running: %w", nodeName, err)
	}
	if n == 0 {
		return fmt.Errorf("mark node %q running: %w", nodeName, ErrNodeNotPending)
	}
	return nil
}

// ReleaseNode returns a running node to pending and takes back the attempt
// its claim counted: a dispatch the run's own cancellation stopped is not a
// failure, so it spends no retry. A node that is not running is left as it
// is.
func (r *WorkflowsRepo) ReleaseNode(runID int64, nodeName string) error {
	if _, err := r.writeDB.Exec(
		"UPDATE workflow_node_states SET status='pending', attempt=attempt-1 WHERE run_id=? AND node_name=? AND status='running'",
		runID, nodeName,
	); err != nil {
		return fmt.Errorf("release node %q: %w", nodeName, err)
	}
	return nil
}

// MarkNodeCompleted records outputs and the completion timestamp.
func (r *WorkflowsRepo) MarkNodeCompleted(runID int64, nodeName, outputsJSON, completedAt string) error {
	if _, err := r.writeDB.Exec(
		"UPDATE workflow_node_states SET status='completed', outputs_json=?, completed_at=? WHERE run_id=? AND node_name=?",
		outputsJSON, completedAt, runID, nodeName,
	); err != nil {
		return fmt.Errorf("complete node %q: %w", nodeName, err)
	}
	return nil
}

// MarkNodeFailed records the error message and completion timestamp.
// If retryable is true (attempt <= max_retries — caller's check), uses
// 'pending' instead of 'failed' so the next GetNextNodes pass picks it
// back up.
func (r *WorkflowsRepo) MarkNodeFailed(runID int64, nodeName, errMsg, completedAt string, retryable bool) error {
	status := "failed"
	if retryable {
		status = "pending"
	}
	if _, err := r.writeDB.Exec(
		"UPDATE workflow_node_states SET status=?, error=?, completed_at=? WHERE run_id=? AND node_name=?",
		status, errMsg, nullableTime(completedAt, retryable), runID, nodeName,
	); err != nil {
		return fmt.Errorf("fail node %q: %w", nodeName, err)
	}
	return nil
}

// nullableTime returns sql.NullString — empty when the node is being
// retried (so completed_at stays NULL until terminal).
func nullableTime(t string, retryable bool) sql.NullString {
	if retryable {
		return sql.NullString{}
	}
	return sql.NullString{String: t, Valid: true}
}

// ResetNodeForLoop returns a node a decision loops back over to pending, as
// if not yet run: no completion time, no error, attempt 0 so max_retries
// counts afresh each pass. Tokens, cost and rationale are kept — they
// accumulate across passes like repair spend does.
func (r *WorkflowsRepo) ResetNodeForLoop(runID int64, nodeName string) error {
	if _, err := r.writeDB.Exec(
		"UPDATE workflow_node_states SET status='pending', completed_at=NULL, error=NULL, attempt=0 WHERE run_id=? AND node_name=?",
		runID, nodeName,
	); err != nil {
		return fmt.Errorf("reset node %q for loop: %w", nodeName, err)
	}
	return nil
}

// MarkNodeSkipped sets status='skipped' with completion timestamp.
func (r *WorkflowsRepo) MarkNodeSkipped(runID int64, nodeName, completedAt string) error {
	if _, err := r.writeDB.Exec(
		"UPDATE workflow_node_states SET status='skipped', completed_at=? WHERE run_id=? AND node_name=?",
		completedAt, runID, nodeName,
	); err != nil {
		return fmt.Errorf("skip node %q: %w", nodeName, err)
	}
	return nil
}

// FinishNodeInTx finishes a node as one write transaction, with the final
// status the caller chooses: `completed`, or `failed` when a reviewer rejects
// a human_review node. The run row is re-read inside the transaction, merge
// derives the node's outputs and the new run state from that fresh copy, and
// both rows are updated before COMMIT, so two parallel completions cannot
// read the same state_json and lose one node's outputs. When merge returns an
// error nothing is written; accept: rejections take that path.
func (r *WorkflowsRepo) FinishNodeInTx(runID int64, nodeName, status string, merge func(run *WorkflowRun) (outputsJSON, stateJSON string, err error)) error {
	if !finishStatus(status) {
		return fmt.Errorf("invalid status %q (want completed|failed)", status)
	}
	ctx := context.Background()
	conn, err := beginImmediateConn(ctx, r.writeDB)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := finishNodeOn(ctx, conn, runID, nodeName, status, merge); err != nil {
		_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		return err
	}
	return nil
}

// finishStatus reports whether status is one a node may finish with:
// completed or failed.
func finishStatus(status string) bool {
	return status == "completed" || status == "failed"
}

// beginImmediateConn pins a connection of the pool and begins an IMMEDIATE
// transaction on it. The caller closes the connection.
func beginImmediateConn(ctx context.Context, pool *sql.DB) (*sql.Conn, error) {
	conn, err := pool.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("pin connection: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		conn.Close()
		return nil, fmt.Errorf("begin: %w", err)
	}
	return conn, nil
}

// finishNodeOn is FinishNodeInTx's transaction body: it re-reads the run,
// merges, writes both rows and commits. The caller rolls back on an error.
func finishNodeOn(ctx context.Context, conn *sql.Conn, runID int64, nodeName, status string, merge func(run *WorkflowRun) (outputsJSON, stateJSON string, err error)) error {
	run, err := readRunOn(ctx, conn, runID)
	if err != nil {
		return err
	}
	outputsJSON, stateJSON, err := merge(run)
	if err != nil {
		return err
	}
	return commitNodeFinish(ctx, conn, runID, nodeName, status, outputsJSON, stateJSON)
}

// readRunOn reads the run row inside the transaction.
func readRunOn(ctx context.Context, conn *sql.Conn, runID int64) (*WorkflowRun, error) {
	run := &WorkflowRun{}
	if err := conn.QueryRowContext(ctx,
		`SELECT id, workflow_name, workflow_version, definition_yaml,
		        inputs_json, state_json, status, started_at, completed_at
		 FROM workflow_runs WHERE id=?`, runID,
	).Scan(&run.ID, &run.Name, &run.Version, &run.DefinitionYAML,
		&run.InputsJSON, &run.StateJSON, &run.Status, &run.StartedAt, &run.CompletedAt); err != nil {
		return nil, fmt.Errorf("run %d not found: %w", runID, err)
	}
	return run, nil
}

// commitNodeFinish writes the node's final status and outputs and the run's
// merged state, and commits.
func commitNodeFinish(ctx context.Context, conn *sql.Conn, runID int64, nodeName, status, outputsJSON, stateJSON string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := conn.ExecContext(ctx,
		"UPDATE workflow_node_states SET status=?, outputs_json=?, completed_at=? WHERE run_id=? AND node_name=?",
		status, outputsJSON, now, runID, nodeName); err != nil {
		return fmt.Errorf("finish node %q: %w", nodeName, err)
	}
	if _, err := conn.ExecContext(ctx, "UPDATE workflow_runs SET state_json=? WHERE id=?", stateJSON, runID); err != nil {
		return fmt.Errorf("update run state %d: %w", runID, err)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// MarkNodeWaitingHuman parks a node for human review and leaves completed_at
// NULL: the node has not finished, it is waiting. `chb workflow resume` is
// what moves it on.
func (r *WorkflowsRepo) MarkNodeWaitingHuman(runID int64, nodeName, startedAt string) error {
	if _, err := r.writeDB.Exec(
		"UPDATE workflow_node_states SET status='waiting_human', started_at=COALESCE(started_at, ?) WHERE run_id=? AND node_name=?",
		startedAt, runID, nodeName,
	); err != nil {
		return fmt.Errorf("mark node %q waiting_human: %w", nodeName, err)
	}
	return nil
}

// FindWaitingHumanNode returns the first node in `runID` whose status
// is 'waiting_human', or sql.ErrNoRows if none exists. Used by Resume
// handler to know which node to advance.
func (r *WorkflowsRepo) FindWaitingHumanNode(runID int64) (string, error) {
	var name string
	err := r.readDB.QueryRow(
		"SELECT node_name FROM workflow_node_states WHERE run_id=? AND status='waiting_human'",
		runID,
	).Scan(&name)
	return name, err
}

// UpdateNodeRationale stores the agent's truncated final text on a node row.
func (r *WorkflowsRepo) UpdateNodeRationale(runID int64, nodeName, rationale string) error {
	if _, err := r.writeDB.Exec(
		`UPDATE workflow_node_states SET rationale=? WHERE run_id=? AND node_name=?`,
		rationale, runID, nodeName,
	); err != nil {
		return fmt.Errorf("update rationale %s: %w", nodeName, err)
	}
	return nil
}

// UpdateNodeMetrics adds to the per-node tokens and cost columns. We use
// COALESCE+addition so partial-completion repair attempts accumulate
// rather than overwrite (each repair is its own workflow_repairs row,
// but the parent node aggregates totals here). meteredCalls and
// unmeteredCalls add to the node's call counts.
func (r *WorkflowsRepo) UpdateNodeMetrics(runID int64, nodeName string, tokensIn, tokensOut, costUSDx10000 int64, provider, baseURL string, meteredCalls, unmeteredCalls int64) error {
	var prov, base any
	if provider != "" {
		prov = provider
	}
	if baseURL != "" {
		base = baseURL
	}
	if _, err := r.writeDB.Exec(
		`UPDATE workflow_node_states
		 SET tokens_in       = COALESCE(tokens_in,       0) + ?,
		     tokens_out      = COALESCE(tokens_out,      0) + ?,
		     cost_usd_x10000 = COALESCE(cost_usd_x10000, 0) + ?,
		     provider        = COALESCE(?, provider),
		     base_url        = COALESCE(?, base_url),
		     metered_calls   = COALESCE(metered_calls,   0) + ?,
		     unmetered_calls = COALESCE(unmetered_calls, 0) + ?
		 WHERE run_id=? AND node_name=?`,
		tokensIn, tokensOut, costUSDx10000, prov, base, meteredCalls, unmeteredCalls, runID, nodeName,
	); err != nil {
		return fmt.Errorf("update metrics %s: %w", nodeName, err)
	}
	return nil
}

// UpdateNodeResolvedModel persists the canonical SDK model ID a dispatch
// resolved. Every dispatch writes it, so it names the latest; the repair
// loop reads it back via GetWorkflowNodeStates so a repair uses the exact
// model of the attempt it repairs even after an Anthropic alias version bump.
// Best-effort callers: if this errors, log but do not fail dispatch.
func (r *WorkflowsRepo) UpdateNodeResolvedModel(runID int64, nodeName, model string) error {
	if _, err := r.writeDB.Exec(
		`UPDATE workflow_node_states SET resolved_model=? WHERE run_id=? AND node_name=?`,
		model, runID, nodeName,
	); err != nil {
		return fmt.Errorf("update resolved_model %s: %w", nodeName, err)
	}
	return nil
}

// UpdateNodeSchemaEnforcement records how a node's output_schema held its
// accepted answer: "enforced at decode" or "post-hoc only".
func (r *WorkflowsRepo) UpdateNodeSchemaEnforcement(runID int64, nodeName, enforcement string) error {
	if _, err := r.writeDB.Exec(
		`UPDATE workflow_node_states SET schema_enforcement=? WHERE run_id=? AND node_name=?`,
		enforcement, runID, nodeName,
	); err != nil {
		return fmt.Errorf("update schema_enforcement %s: %w", nodeName, err)
	}
	return nil
}

// AddNodeCutoffCalls adds n to the node's count of model calls whose reply
// the provider stopped at the output cap.
func (r *WorkflowsRepo) AddNodeCutoffCalls(runID int64, nodeName string, n int64) error {
	if _, err := r.writeDB.Exec(
		`UPDATE workflow_node_states SET cutoff_calls = COALESCE(cutoff_calls, 0) + ? WHERE run_id=? AND node_name=?`,
		n, runID, nodeName,
	); err != nil {
		return fmt.Errorf("add cutoff_calls %s: %w", nodeName, err)
	}
	return nil
}

// MarkNodeRejected sets a node to the 'rejected' terminal state. Used by
// the workflow engine when an accept: predicate fails and there is no
// on_reject: handler (or after on_reject: exhausts its retry budget).
func (r *WorkflowsRepo) MarkNodeRejected(runID int64, nodeName, rationale, completedAt string) error {
	if _, err := r.writeDB.Exec(
		`UPDATE workflow_node_states
		 SET status='rejected', rationale=?, completed_at=?
		 WHERE run_id=? AND node_name=?`,
		rationale, completedAt, runID, nodeName,
	); err != nil {
		return fmt.Errorf("reject node %s: %w", nodeName, err)
	}
	return nil
}
