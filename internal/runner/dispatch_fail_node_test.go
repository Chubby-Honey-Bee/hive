package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// erroringBackend always returns the configured error from Run, to pin the
// backend-error → FailNode behavior of executeAgentNode.
type erroringBackend struct {
	err error
}

func (e *erroringBackend) Run(_ context.Context, _ RunRequest) (*RunResult, error) {
	return nil, e.err
}

// TestExecuteAgentNode_BackendErrorMarksFailed asserts that when the LLM
// backend returns an error, the runner:
//  1. Marks the node "failed" in workflow_node_states, not "running".
//  2. Persists the error string on the node row (truncated/wrapped form).
//  3. Records nodes_run = 0 (the node didn't successfully complete).
func TestExecuteAgentNode_BackendErrorMarksFailed(t *testing.T) {
	tmpDir := t.TempDir()
	wfFile := filepath.Join(tmpDir, "single.yaml")
	yaml := `name: single-fail
version: 1
inputs:
  - topic
nodes:
  only:
    type: agent
    agent: analyst
    model: sonnet
    prompt: "do {topic}"
    outputs: [out]
`
	if err := os.WriteFile(wfFile, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	store := newParallelTestStore(t)
	backend := &erroringBackend{err: errors.New("simulated CLI failure")}

	cfg := Config{
		WorkflowYAML:  wfFile,
		ProjectName:   "failnode-test",
		ProjectDir:    tmpDir,
		Inputs:        map[string]any{"topic": "bees"},
		MaxIterations: 5,
		Branch:        "",
		Backend:       backend,
		Log:           os.Stderr,
	}

	res, err := Run(context.Background(), store, cfg)
	// The failure is recorded on the node row, and Run reports the failed
	// run, so agent-run exits non-zero.
	if err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("Run error = %v, want the run reported failed", err)
	}
	if res.NodesRun != 0 {
		t.Errorf("NodesRun = %d; want 0 (no node completed successfully)", res.NodesRun)
	}

	// Inspect the node state directly. Schema for workflow_node_states uses
	// these columns: node_name, status, error, attempt.
	var status, errStr string
	row := store.WriteDB.QueryRow(
		`SELECT status, COALESCE(error, '') FROM workflow_node_states
		 WHERE run_id = ? AND node_name = ?`,
		res.RunID, "only",
	)
	if scanErr := row.Scan(&status, &errStr); scanErr != nil {
		t.Fatalf("scan node state: %v", scanErr)
	}

	// Status must be "failed" — NOT "running" (orphan state).
	if status != "failed" {
		t.Errorf("node status = %q; want %q", status, "failed")
	}
	if !strings.Contains(errStr, "simulated CLI failure") {
		t.Errorf("error column = %q; want it to contain backend error message", errStr)
	}
}
