package runner

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExecuteAgentNode_RateLimitErrorLogsDistinctly asserts that when a
// backend returns an error that pattern-matches a rate-limit signal
// (HTTP 429, "rate limit", "quota exceeded", etc.), the dispatcher logs
// `RATE_LIMITED` rather than `FAILED`. Operators reading the run log
// then know "retry the run later" vs "the fix is broken".
//
// Pin point: internal/runner/dispatch.go, the runErr != nil branch
// inside executeAgentNode. Coupled to internal/runner/ratelimit.go's
// isRateLimitError pattern table.
func TestExecuteAgentNode_RateLimitErrorLogsDistinctly(t *testing.T) {
	tmpDir := t.TempDir()
	wfFile := filepath.Join(tmpDir, "single.yaml")
	yaml := `name: single-rl
version: 1
inputs: [topic]
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
	// Pattern matches isRateLimitError's "429" + "rate limit" entries.
	backend := &erroringBackend{err: errors.New("HTTP 429: rate limit exceeded for org")}

	var logBuf bytes.Buffer
	cfg := Config{
		WorkflowYAML:  wfFile,
		ProjectName:   "rl-test",
		ProjectDir:    tmpDir,
		Inputs:        map[string]any{"topic": "bees"},
		MaxIterations: 5,
		Backend:       backend,
		Log:           &logBuf,
	}

	res, err := Run(context.Background(), store, cfg)
	if err == nil {
		t.Fatal("Run returned nil for a failed run")
	}
	if res.NodesRun != 0 {
		t.Errorf("NodesRun = %d; want 0", res.NodesRun)
	}

	logged := logBuf.String()
	if !strings.Contains(logged, "RATE_LIMITED") {
		t.Errorf("log missing RATE_LIMITED tag\nlog:\n%s", logged)
	}
	if strings.Contains(logged, "node only FAILED:") {
		t.Errorf("log incorrectly tagged FAILED for rate-limit error\nlog:\n%s", logged)
	}
}

// TestExecuteAgentNode_GenuineFailureLogsAsFAILED is the negative half:
// an error that does NOT match the rate-limit table must still log as
// FAILED so we don't silently mask real bugs.
func TestExecuteAgentNode_GenuineFailureLogsAsFAILED(t *testing.T) {
	tmpDir := t.TempDir()
	wfFile := filepath.Join(tmpDir, "single.yaml")
	yaml := `name: single-fail
version: 1
inputs: [topic]
nodes:
  only:
    type: agent
    agent: analyst
    model: sonnet
    prompt: "do"
    outputs: [out]
`
	if err := os.WriteFile(wfFile, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	store := newParallelTestStore(t)
	// "compile error" is unambiguously not a rate-limit signal.
	backend := &erroringBackend{err: errors.New("compile error: undefined symbol Foo")}

	var logBuf bytes.Buffer
	cfg := Config{
		WorkflowYAML:  wfFile,
		ProjectName:   "fail-test",
		ProjectDir:    tmpDir,
		Inputs:        map[string]any{"topic": "x"},
		MaxIterations: 5,
		Backend:       backend,
		Log:           &logBuf,
	}

	if _, err := Run(context.Background(), store, cfg); err == nil {
		t.Fatal("Run returned nil for a failed run")
	}
	logged := logBuf.String()
	if !strings.Contains(logged, "node only FAILED:") {
		t.Errorf("genuine error not tagged FAILED\nlog:\n%s", logged)
	}
	if strings.Contains(logged, "RATE_LIMITED") {
		t.Errorf("genuine error incorrectly tagged RATE_LIMITED\nlog:\n%s", logged)
	}
}
