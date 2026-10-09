package runner

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// toolCountBackend answers each call with a well-formed report and the tool
// calls its script gives that call: calls[i] for the i-th call, or the last
// entry once the script runs out.
type toolCountBackend struct {
	mu    sync.Mutex
	calls []toolCountCall
	n     int
}

type toolCountCall struct {
	invocations int
	unreported  bool
}

func (b *toolCountBackend) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	b.mu.Lock()
	c := b.calls[min(b.n, len(b.calls)-1)]
	b.n++
	b.mu.Unlock()
	res := &RunResult{FinalText: `{"said":"done"}`, Turns: 1, StopReason: "end_turn", ToolCallsUnreported: c.unreported}
	for i := 0; i < c.invocations; i++ {
		res.Invocations = append(res.Invocations, ToolInvocation{Tool: "read_file", Input: `{"path":"notes.md"}`, Output: "notes"})
	}
	return res, nil
}

// A node with min_tool_calls whose call made fewer tool calls fails, however
// well-formed its answer. The failure goes through max_retries, so the
// prompt runs again, and nothing the short call answered reaches state. A
// backend that reports none of its tool calls, as the CLIs run their own
// tool loop, cannot be checked, and the node completes.
func TestMinToolCalls(t *testing.T) {
	const wf = `
name: t
version: 1
nodes:
  work:
    type: agent
    prompt: do the work
    outputs: [said]
    min_tool_calls: 1
    max_retries: 1
edges: []
`
	cases := []struct {
		name     string
		calls    []toolCountCall
		status   string
		dispatch int
	}{
		{"no tool call, twice", []toolCountCall{{0, false}}, "failed", 2},
		{"no tool call, then one", []toolCountCall{{0, false}, {1, false}}, "completed", 2},
		{"one tool call", []toolCountCall{{1, false}}, "completed", 1},
		{"tool calls unreported", []toolCountCall{{0, true}}, "completed", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "wf.yaml")
			if err := os.WriteFile(path, []byte(wf), 0o644); err != nil {
				t.Fatal(err)
			}
			store := newTempStore(t)
			backend := &toolCountBackend{calls: c.calls}
			res, err := Run(context.Background(), store, Config{
				WorkflowYAML: path, ProjectDir: dir, MaxIterations: 10, Backend: backend, Log: io.Discard,
			})
			if res == nil {
				t.Fatalf("Run returned no result: %v", err)
			}
			if (err == nil) != (c.status == "completed") {
				t.Fatalf("Run error = %v, want one exactly when the node fails", err)
			}
			var status string
			if err := store.ReadDB.QueryRow(`SELECT status FROM workflow_node_states WHERE run_id=? AND node_name='work'`, res.RunID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != c.status || backend.n != c.dispatch {
				t.Fatalf("work is %s after %d call(s), want %s after %d", status, backend.n, c.status, c.dispatch)
			}
			run, err := store.Workflows().GetWorkflowRun(res.RunID)
			if err != nil {
				t.Fatal(err)
			}
			var st map[string]any
			if err := json.Unmarshal([]byte(run.StateJSON), &st); err != nil {
				t.Fatal(err)
			}
			if _, has := st["said"]; has != (c.status == "completed") {
				t.Fatalf("state %v: the answer reached state = %v, want %v", st, has, c.status == "completed")
			}
		})
	}
}
