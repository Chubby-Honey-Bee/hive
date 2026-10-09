package runner

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// newRecordRC builds a minimal runtimeContext for recordInvocations tests.
func newRecordRC(t *testing.T) *runtimeContext {
	t.Helper()
	store := newTempStore(t)
	buf := &bytes.Buffer{}
	lf := makeLogf(buf)
	return &runtimeContext{
		ctx:   context.Background(),
		store: store,
		gc:    &GitCommitter{Enabled: false},
		logf:  lf,
		res:   &Result{},
		resMu: sync.Mutex{},
		runID: 1,
	}
}

// countRows returns the number of rows in tool_invocations for a given run_id.
func countRows(t *testing.T, rc *runtimeContext, runID int64) int {
	t.Helper()
	var n int
	row := rc.store.WriteDB.QueryRow(`SELECT COUNT(*) FROM tool_invocations WHERE run_id = ?`, runID)
	if err := row.Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return n
}

// TestRecordInvocations_NilRunResult verifies that a nil runResult is a no-op.
func TestRecordInvocations_NilRunResult(t *testing.T) {
	rc := newRecordRC(t)
	node := workflow.DispatchNode{Node: "w1-agent", Type: "agent"}

	rc.recordInvocations(node, nil)

	if n := countRows(t, rc, rc.runID); n != 0 {
		t.Errorf("expected 0 rows for nil runResult; got %d", n)
	}
	rc.resMu.Lock()
	in, out := rc.res.InputTokens, rc.res.OutputTokens
	rc.resMu.Unlock()
	if in != 0 || out != 0 {
		t.Errorf("expected 0 tokens for nil runResult; got in=%d out=%d", in, out)
	}
}

// TestRecordInvocations_EmptyInvocations verifies that a RunResult with no
// invocations still accumulates token counts.
func TestRecordInvocations_EmptyInvocations(t *testing.T) {
	rc := newRecordRC(t)
	node := workflow.DispatchNode{Node: "w1-agent", Type: "agent"}
	rr := &RunResult{
		InputTokens:  10,
		OutputTokens: 5,
	}

	rc.recordInvocations(node, rr)

	if n := countRows(t, rc, rc.runID); n != 0 {
		t.Errorf("expected 0 rows for empty invocations; got %d", n)
	}
	rc.resMu.Lock()
	in, out := rc.res.InputTokens, rc.res.OutputTokens
	rc.resMu.Unlock()
	if in != 10 || out != 5 {
		t.Errorf("tokens = %d/%d; want 10/5", in, out)
	}
}

// TestRecordInvocations_HappyPath verifies that invocations are persisted and
// tokens are accumulated.
func TestRecordInvocations_HappyPath(t *testing.T) {
	tests := []struct {
		name        string
		invocations []ToolInvocation
		wantRows    int
		wantIn      int64
		wantOut     int64
	}{
		{
			name: "single non-error invocation",
			invocations: []ToolInvocation{
				{Tool: "read", Input: `{"path":"a.txt"}`, Output: "hello", IsError: false, DurationS: 0.1},
			},
			wantRows: 1,
			wantIn:   20,
			wantOut:  8,
		},
		{
			name: "single error invocation",
			invocations: []ToolInvocation{
				{Tool: "write", Input: `{"path":"b.txt"}`, Output: "permission denied", IsError: true, DurationS: 0.05},
			},
			wantRows: 1,
			wantIn:   15,
			wantOut:  3,
		},
		{
			name: "multiple invocations accumulate tokens",
			invocations: []ToolInvocation{
				{Tool: "read", Input: `{"path":"x"}`, Output: "x", IsError: false, DurationS: 0.01},
				{Tool: "write", Input: `{"path":"y"}`, Output: "y", IsError: false, DurationS: 0.02},
			},
			wantRows: 2,
			wantIn:   100,
			wantOut:  50,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rc := newRecordRC(t)
			node := workflow.DispatchNode{Node: "w2-coder", Type: "agent"}
			rr := &RunResult{
				Invocations:  tc.invocations,
				InputTokens:  tc.wantIn,
				OutputTokens: tc.wantOut,
			}

			rc.recordInvocations(node, rr)

			if n := countRows(t, rc, rc.runID); n != tc.wantRows {
				t.Errorf("rows = %d; want %d", n, tc.wantRows)
			}
			rc.resMu.Lock()
			in, out := rc.res.InputTokens, rc.res.OutputTokens
			rc.resMu.Unlock()
			if in != tc.wantIn || out != tc.wantOut {
				t.Errorf("tokens = %d/%d; want %d/%d", in, out, tc.wantIn, tc.wantOut)
			}
		})
	}
}

// TestRecordInvocations_IsErrorFlag verifies the is_error column is set
// correctly for both error and non-error invocations.
func TestRecordInvocations_IsErrorFlag(t *testing.T) {
	rc := newRecordRC(t)
	node := workflow.DispatchNode{Node: "w1-researcher", Type: "agent"}
	rr := &RunResult{
		Invocations: []ToolInvocation{
			{Tool: "read", Input: `{}`, Output: "ok", IsError: false},
			{Tool: "write", Input: `{}`, Output: "fail", IsError: true},
		},
	}

	rc.recordInvocations(node, rr)

	rows, err := rc.store.WriteDB.Query(
		`SELECT tool, is_error FROM tool_invocations WHERE run_id = ? ORDER BY rowid`,
		rc.runID,
	)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	type row struct {
		tool    string
		isError int
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.tool, &r.isError); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 rows; got %d", len(got))
	}
	if got[0].isError != 0 {
		t.Errorf("read: is_error = %d; want 0", got[0].isError)
	}
	if got[1].isError != 1 {
		t.Errorf("write: is_error = %d; want 1", got[1].isError)
	}
}

// TestRecordInvocations_OutputTruncation verifies that very long outputs are
// truncated before insert (the truncate(output, 20_000) call in the function).
func TestRecordInvocations_OutputTruncation(t *testing.T) {
	rc := newRecordRC(t)
	node := workflow.DispatchNode{Node: "w1-analyst", Type: "agent"}
	longOutput := strings.Repeat("x", 25_000)
	rr := &RunResult{
		Invocations: []ToolInvocation{
			{Tool: "read", Input: `{}`, Output: longOutput, IsError: false},
		},
	}

	rc.recordInvocations(node, rr)

	var output string
	row := rc.store.WriteDB.QueryRow(
		`SELECT output FROM tool_invocations WHERE run_id = ?`, rc.runID,
	)
	if err := row.Scan(&output); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(output) >= 25_000 {
		t.Errorf("output not truncated: len=%d", len(output))
	}
	if len(output) == 0 {
		t.Errorf("output should not be empty after truncation")
	}
}

// TestRecordInvocations_TokensAccumulateAcrossCalls verifies that multiple
// calls to recordInvocations on the same rc correctly sum tokens.
func TestRecordInvocations_TokensAccumulateAcrossCalls(t *testing.T) {
	rc := newRecordRC(t)
	node := workflow.DispatchNode{Node: "w1-agent", Type: "agent"}

	rc.recordInvocations(node, &RunResult{InputTokens: 10, OutputTokens: 5})
	rc.recordInvocations(node, &RunResult{InputTokens: 20, OutputTokens: 8})

	rc.resMu.Lock()
	in, out := rc.res.InputTokens, rc.res.OutputTokens
	rc.resMu.Unlock()

	if in != 30 {
		t.Errorf("InputTokens = %d; want 30", in)
	}
	if out != 13 {
		t.Errorf("OutputTokens = %d; want 13", out)
	}
}
