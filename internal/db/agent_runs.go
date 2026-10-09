package db

import (
	"database/sql"
	"fmt"
)

// AgentRunsRepo owns the agent_runs table.
type AgentRunsRepo struct {
	writeDB *sql.DB
	readDB  *sql.DB
}

// newAgentRunsRepo binds the two pools.
func newAgentRunsRepo(writeDB, readDB *sql.DB) *AgentRunsRepo {
	return &AgentRunsRepo{writeDB: writeDB, readDB: readDB}
}

// AddAgentRun records the start of an agent run.
func (r *AgentRunsRepo) AddAgentRun(wave int, agentName, agentType, model, promptSummary string,
	targetD1, targetD2, targetD3, targetD4 *int,
) (int64, error) {
	res, err := r.writeDB.Exec(
		`INSERT INTO agent_runs
		 (wave, agent_name, agent_type, model, prompt_summary, target_d1, target_d2, target_d3, target_d4)
		 VALUES (?,?,?,?,?,?,?,?,?)`,
		wave, agentName, agentType, model, promptSummary, targetD1, targetD2, targetD3, targetD4,
	)
	if err != nil {
		return 0, fmt.Errorf("add agent run: %w", err)
	}
	return res.LastInsertId()
}

// CompleteAgentRun marks a run as completed.
func (r *AgentRunsRepo) CompleteAgentRun(runID int64, summary string, toolUses, durationMS, totalTokens *int) error {
	_, err := r.writeDB.Exec(
		`UPDATE agent_runs SET status='completed', summary=?, tool_uses=?, duration_ms=?,
		 total_tokens=?, completed_at=CURRENT_TIMESTAMP WHERE id=?`,
		summary, toolUses, durationMS, totalTokens, runID,
	)
	if err != nil {
		return fmt.Errorf("complete agent run: %w", err)
	}
	return nil
}

// FailAgentRun marks a run as failed.
func (r *AgentRunsRepo) FailAgentRun(runID int64, errorSummary string) error {
	_, err := r.writeDB.Exec(
		"UPDATE agent_runs SET status='failed', summary=?, completed_at=CURRENT_TIMESTAMP WHERE id=?",
		errorSummary, runID,
	)
	if err != nil {
		return fmt.Errorf("fail agent run: %w", err)
	}
	return nil
}
