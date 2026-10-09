package db

import (
	"database/sql"
	"testing"
)

func TestAddAgentRun_HappyPath(t *testing.T) {
	s := newTestStore(t)
	d1, d2 := 0, 1
	id, err := s.AgentRuns().AddAgentRun(1, "researcher-market", "researcher", "sonnet", "research market", &d1, &d2, nil, nil)
	if err != nil {
		t.Fatalf("AddAgentRun happy path: %v", err)
	}
	if id == 0 {
		t.Error("expected non-zero ID")
	}
}

func TestAddAgentRun_NilCoordinates(t *testing.T) {
	s := newTestStore(t)
	id, err := s.AgentRuns().AddAgentRun(2, "coder-fix", "coder", "opus", "fix the bug", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("AddAgentRun with all nil coords: %v", err)
	}
	if id == 0 {
		t.Error("expected non-zero ID")
	}
}

func TestAddAgentRun_ReturnsIncrementingIDs(t *testing.T) {
	s := newTestStore(t)
	id1, err := s.AgentRuns().AddAgentRun(1, "agent-a", "researcher", "haiku", "summary a", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	id2, err := s.AgentRuns().AddAgentRun(1, "agent-b", "analyst", "sonnet", "summary b", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if id2 <= id1 {
		t.Errorf("expected id2 > id1; got id1=%d id2=%d", id1, id2)
	}
}

// ───── CompleteAgentRun ─────

func TestCompleteAgentRun_HappyPath(t *testing.T) {
	s := newTestStore(t)
	id, err := s.AgentRuns().AddAgentRun(1, "researcher-x", "researcher", "sonnet", "research x", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("AddAgentRun: %v", err)
	}

	toolUses := 3
	durationMS := 1200
	totalTokens := 500
	if err := s.AgentRuns().CompleteAgentRun(id, "all done", &toolUses, &durationMS, &totalTokens); err != nil {
		t.Fatalf("CompleteAgentRun happy path: %v", err)
	}

	var status, summary string
	var gotToolUses, gotDurationMS, gotTotalTokens sql.NullInt64
	var completedAt sql.NullString
	row := s.ReadDB.QueryRow(
		"SELECT status, summary, tool_uses, duration_ms, total_tokens, completed_at FROM agent_runs WHERE id=?", id,
	)
	if err := row.Scan(&status, &summary, &gotToolUses, &gotDurationMS, &gotTotalTokens, &completedAt); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if status != "completed" {
		t.Errorf("expected status=completed, got %q", status)
	}
	if summary != "all done" {
		t.Errorf("expected summary='all done', got %q", summary)
	}
	if !gotToolUses.Valid || gotToolUses.Int64 != 3 {
		t.Errorf("expected tool_uses=3, got %v", gotToolUses)
	}
	if !gotDurationMS.Valid || gotDurationMS.Int64 != 1200 {
		t.Errorf("expected duration_ms=1200, got %v", gotDurationMS)
	}
	if !gotTotalTokens.Valid || gotTotalTokens.Int64 != 500 {
		t.Errorf("expected total_tokens=500, got %v", gotTotalTokens)
	}
	if !completedAt.Valid || completedAt.String == "" {
		t.Error("expected completed_at to be set")
	}
}

func TestCompleteAgentRun_NilOptionalFields(t *testing.T) {
	s := newTestStore(t)
	id, err := s.AgentRuns().AddAgentRun(1, "researcher-nil", "researcher", "haiku", "nil test", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("AddAgentRun: %v", err)
	}

	if err := s.AgentRuns().CompleteAgentRun(id, "summary only", nil, nil, nil); err != nil {
		t.Fatalf("CompleteAgentRun with nil fields: %v", err)
	}

	var status string
	var gotToolUses, gotDurationMS, gotTotalTokens sql.NullInt64
	row := s.ReadDB.QueryRow(
		"SELECT status, tool_uses, duration_ms, total_tokens FROM agent_runs WHERE id=?", id,
	)
	if err := row.Scan(&status, &gotToolUses, &gotDurationMS, &gotTotalTokens); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if status != "completed" {
		t.Errorf("expected status=completed, got %q", status)
	}
	if gotToolUses.Valid {
		t.Errorf("expected tool_uses=NULL, got %v", gotToolUses.Int64)
	}
	if gotDurationMS.Valid {
		t.Errorf("expected duration_ms=NULL, got %v", gotDurationMS.Int64)
	}
	if gotTotalTokens.Valid {
		t.Errorf("expected total_tokens=NULL, got %v", gotTotalTokens.Int64)
	}
}

func TestCompleteAgentRun_NonexistentID(t *testing.T) {
	// UPDATE on a non-existent row is not an error in SQLite; the function
	// should return nil (no rows affected is not an error condition).
	s := newTestStore(t)
	if err := s.AgentRuns().CompleteAgentRun(99999, "ghost", nil, nil, nil); err != nil {
		t.Errorf("expected nil for missing runID, got: %v", err)
	}
}

func TestCompleteAgentRun_TableDriven(t *testing.T) {
	toolUses := 5
	durationMS := 2000
	totalTokens := 1000

	cases := []struct {
		name        string
		summary     string
		toolUses    *int
		durationMS  *int
		totalTokens *int
		wantErr     bool
	}{
		{
			name:    "all fields set",
			summary: "complete with everything", toolUses: &toolUses,
			durationMS: &durationMS, totalTokens: &totalTokens, wantErr: false,
		},
		{
			name:    "only summary",
			summary: "summary only", toolUses: nil,
			durationMS: nil, totalTokens: nil, wantErr: false,
		},
		{
			name:    "empty summary",
			summary: "", toolUses: nil,
			durationMS: nil, totalTokens: nil, wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			id, err := s.AgentRuns().AddAgentRun(1, "agent-"+tc.name, "researcher", "sonnet", "x", nil, nil, nil, nil)
			if err != nil {
				t.Fatalf("AddAgentRun: %v", err)
			}
			err = s.AgentRuns().CompleteAgentRun(id, tc.summary, tc.toolUses, tc.durationMS, tc.totalTokens)
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestCompleteAgentRun_DBError(t *testing.T) {
	s := newTestStore(t)
	id, err := s.AgentRuns().AddAgentRun(1, "agent-complete-dberror", "researcher", "haiku", "will fail db", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("AddAgentRun: %v", err)
	}
	// Close the write DB so the next Exec returns an error.
	if err := s.WriteDB.Close(); err != nil {
		t.Fatalf("Close writeDB: %v", err)
	}
	err = s.AgentRuns().CompleteAgentRun(id, "injected db error", nil, nil, nil)
	if err == nil {
		t.Error("expected error when writeDB is closed, got nil")
	}
}

// ───── FailAgentRun ─────

func TestFailAgentRun_HappyPath(t *testing.T) {
	s := newTestStore(t)
	id, err := s.AgentRuns().AddAgentRun(1, "researcher-fail", "researcher", "sonnet", "will fail", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("AddAgentRun: %v", err)
	}

	if err := s.AgentRuns().FailAgentRun(id, "something went wrong"); err != nil {
		t.Fatalf("FailAgentRun happy path: %v", err)
	}

	var status, summary string
	var completedAt sql.NullString
	row := s.ReadDB.QueryRow(
		"SELECT status, summary, completed_at FROM agent_runs WHERE id=?", id,
	)
	if err := row.Scan(&status, &summary, &completedAt); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if status != "failed" {
		t.Errorf("expected status=failed, got %q", status)
	}
	if summary != "something went wrong" {
		t.Errorf("expected summary='something went wrong', got %q", summary)
	}
	if !completedAt.Valid || completedAt.String == "" {
		t.Error("expected completed_at to be set")
	}
}

func TestFailAgentRun_NonexistentID(t *testing.T) {
	// UPDATE on a non-existent row is not an error in SQLite; the function
	// should return nil (no rows affected is not an error condition).
	s := newTestStore(t)
	if err := s.AgentRuns().FailAgentRun(99999, "ghost error"); err != nil {
		t.Errorf("expected nil for missing runID, got: %v", err)
	}
}

func TestFailAgentRun_TableDriven(t *testing.T) {
	cases := []struct {
		name         string
		errorSummary string
	}{
		{name: "short error", errorSummary: "timeout"},
		{name: "empty summary", errorSummary: ""},
		{name: "long error", errorSummary: "connection refused after 3 retries: dial tcp 127.0.0.1:5432: connect: connection refused"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			id, err := s.AgentRuns().AddAgentRun(1, "agent-"+tc.name, "researcher", "haiku", "x", nil, nil, nil, nil)
			if err != nil {
				t.Fatalf("AddAgentRun: %v", err)
			}
			if err := s.AgentRuns().FailAgentRun(id, tc.errorSummary); err != nil {
				t.Errorf("FailAgentRun(%q): unexpected error: %v", tc.errorSummary, err)
			}

			var status, summary string
			row := s.ReadDB.QueryRow("SELECT status, summary FROM agent_runs WHERE id=?", id)
			if err := row.Scan(&status, &summary); err != nil {
				t.Fatalf("scan: %v", err)
			}
			if status != "failed" {
				t.Errorf("expected status=failed, got %q", status)
			}
			if summary != tc.errorSummary {
				t.Errorf("expected summary=%q, got %q", tc.errorSummary, summary)
			}
		})
	}
}

func TestFailAgentRun_DBError(t *testing.T) {
	s := newTestStore(t)
	id, err := s.AgentRuns().AddAgentRun(1, "agent-dberror", "researcher", "haiku", "will fail db", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("AddAgentRun: %v", err)
	}
	// Close the write DB so the next Exec returns an error.
	if err := s.WriteDB.Close(); err != nil {
		t.Fatalf("Close writeDB: %v", err)
	}
	err = s.AgentRuns().FailAgentRun(id, "injected db error")
	if err == nil {
		t.Error("expected error when writeDB is closed, got nil")
	}
}

func TestAddAgentRun_TableDriven(t *testing.T) {
	d1 := 0
	cases := []struct {
		name          string
		wave          int
		agentName     string
		agentType     string
		model         string
		promptSummary string
		targetD1      *int
		wantErr       bool
	}{
		{
			name: "researcher type",
			wave: 1, agentName: "r1", agentType: "researcher", model: "haiku",
			promptSummary: "research", targetD1: nil, wantErr: false,
		},
		{
			name: "analyst type",
			wave: 2, agentName: "a1", agentType: "analyst", model: "sonnet",
			promptSummary: "analyse", targetD1: &d1, wantErr: false,
		},
		{
			name: "evaluator type",
			wave: 1, agentName: "e1", agentType: "evaluator", model: "opus",
			promptSummary: "evaluate", targetD1: nil, wantErr: false,
		},
		{
			name: "coder type",
			wave: 3, agentName: "c1", agentType: "coder", model: "sonnet",
			promptSummary: "code", targetD1: nil, wantErr: false,
		},
		{
			name: "invalid agent_type violates CHECK constraint",
			wave: 1, agentName: "bad", agentType: "invalid_type", model: "haiku",
			promptSummary: "bad", targetD1: nil, wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			id, err := s.AgentRuns().AddAgentRun(tc.wave, tc.agentName, tc.agentType, tc.model, tc.promptSummary, tc.targetD1, nil, nil, nil)
			if tc.wantErr && err == nil {
				t.Errorf("expected error, got nil (id=%d)", id)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if !tc.wantErr && id == 0 {
				t.Error("expected non-zero ID on success")
			}
		})
	}
}
