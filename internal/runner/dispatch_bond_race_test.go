package runner

// executeAgentNode writes the Comb forager vantage before it marks the node
// "completed": BuildForagerVantage runs before completeOrRepair (which calls
// workflow.CompleteNode). Otherwise a downstream forager with a `cites` bond
// could tick in between and resolve {comb.forager:a} against an empty
// comb_state, substituting "". This test verifies the ordering holds.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// promptCapturingBackend captures the prompt sent for each node by name.
// It is keyed on the order of calls, but we use a per-node map that the
// test configures via fns — each call pops the next fn.
type promptCapturingBackend struct {
	mu      sync.Mutex
	prompts []string // captured in call order
	fns     []func(RunRequest) (*RunResult, error)
	calls   int
}

func (b *promptCapturingBackend) Run(_ context.Context, req RunRequest) (*RunResult, error) {
	b.mu.Lock()
	idx := b.calls
	if idx >= len(b.fns) {
		idx = len(b.fns) - 1
	}
	b.calls++
	b.prompts = append(b.prompts, req.Prompt)
	b.mu.Unlock()
	return b.fns[idx](req)
}

// TestBondRace verifies that when forager-b's prompt contains
// {comb.forager:a} and forager-b depends on forager-a via a DAG edge,
// forager-b's resolved prompt contains the comb narrative (not the literal
// token and not an empty string): forager-a's comb_state row is written
// before forager-a completes, so it is there when forager-b runs.
func TestBondRace(t *testing.T) {
	// forager-a's verdict JSON — the `recommendation` field becomes the narrative
	// that BuildForagerVantage writes into the Comb. resolveCombTokens then
	// replaces {comb.forager:a} with that narrative in forager-b's prompt.
	const foragerAVerdict = `{"verdict":"support","recommendation":"forager-a says yes","key_points":[],"evidence":[],"uncertainties":[]}`

	// forager-b echoes a fixed output (content irrelevant to the assertion).
	const foragerBVerdict = `{"verdict":"conditional","recommendation":"forager-b defers to a","key_points":[],"evidence":[],"uncertainties":[]}`

	// Build a 2-forager workflow YAML. Node naming follows the `forager-<name>`
	// convention: the workflow engine infers ForagerName="a" and ForagerName="b"
	// from node names "forager-a" and "forager-b". The DAG edge ensures forager-b
	// cannot dispatch until forager-a reaches "completed" status.
	const wfYAML = `
name: bond-race-test
nodes:
  forager-a:
    type: agent
    model: haiku
    prompt: "be forager a"
  forager-b:
    type: agent
    model: haiku
    prompt: "context from a: {comb.forager:a}"
edges:
  - {from: forager-a, to: forager-b}
`

	tmpDir := t.TempDir()
	wfFile := filepath.Join(tmpDir, "bond-race.yaml")
	if err := os.WriteFile(wfFile, []byte(wfYAML), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}

	store := newTempStore(t)

	// First call → forager-a's verdict. Second call → forager-b's verdict.
	// Because the DAG enforces serial dispatch (b after a), calls[0] is
	// always forager-a and calls[1] is always forager-b.
	backend := &promptCapturingBackend{
		fns: []func(RunRequest) (*RunResult, error){
			successResult(foragerAVerdict),
			successResult(foragerBVerdict),
		},
	}

	cfg := Config{
		WorkflowYAML: wfFile,
		ProjectDir:   tmpDir,
		Backend:      backend,
	}

	res, err := Run(context.Background(), store, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.NodesRun != 2 {
		t.Fatalf("NodesRun=%d, want 2", res.NodesRun)
	}

	backend.mu.Lock()
	captured := append([]string(nil), backend.prompts...)
	backend.mu.Unlock()

	if len(captured) != 2 {
		t.Fatalf("expected 2 captured prompts (one per forager), got %d: %v", len(captured), captured)
	}

	foragerBPrompt := captured[1]

	// Assertion 1: forager-b's prompt must be non-empty.
	if strings.TrimSpace(foragerBPrompt) == "" {
		t.Error("forager-b's resolved prompt is empty — Comb substitution produced nothing")
	}

	// Assertion 2: the literal token must NOT remain in the prompt. If it
	// does, resolveCombTokens ran before the Comb row existed (the race).
	if strings.Contains(foragerBPrompt, "{comb.forager:a}") {
		t.Errorf("forager-b's prompt still contains the literal token {comb.forager:a}; "+
			"the comb write raced with CompleteNode.\nprompt: %q", foragerBPrompt)
	}

	// Assertion 3: the comb narrative from forager-a must appear in forager-b's
	// prompt. BuildForagerVantage uses the `recommendation` field as the
	// narrative text (see builder.go:BuildForagerVantage).
	if !strings.Contains(foragerBPrompt, "forager-a says yes") {
		t.Errorf("forager-b's prompt does not contain forager-a's narrative.\n"+
			"expected to find %q\ngot: %q", "forager-a says yes", foragerBPrompt)
	}
}
