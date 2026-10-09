package runner

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// panickingBackend panics on a call whose prompt holds panicOn and answers
// every other call.
type panickingBackend struct{ panicOn string }

func (b panickingBackend) Run(_ context.Context, req RunRequest) (*RunResult, error) {
	if strings.Contains(req.Prompt, b.panicOn) {
		panic("backend bug on " + b.panicOn)
	}
	return &RunResult{FinalText: "ok", Turns: 1, StopReason: "end_turn"}, nil
}

// A node that panics fails, with the panic's text; the other node of its
// wave completes, and the process lives on, and with it every request
// chb-mcp is serving.
func TestDispatchWave_APanickingNodeFailsAlone(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "wf.yaml")
	yaml := `name: panic-wave
version: 1
nodes:
  a:
    type: agent
    model: sonnet
    prompt: "boom"
  b:
    type: agent
    model: sonnet
    prompt: "fine"
`
	if err := os.WriteFile(wf, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	store := newParallelTestStore(t)
	res, err := Run(context.Background(), store, Config{
		WorkflowYAML: wf, ProjectDir: dir, MaxIterations: 5,
		Backend: panickingBackend{panicOn: "boom"}, Log: io.Discard,
	})
	if err == nil {
		t.Fatal("the run with a failed node reported success")
	}
	if res == nil || res.RunID == 0 {
		t.Fatalf("Run returned no run id (err %v)", err)
	}
	states, err := store.Workflows().GetWorkflowNodeStates(res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][2]string{}
	for _, s := range states {
		got[s.NodeName] = [2]string{s.Status, s.Error.String}
	}
	if s := got["a"]; s[0] != "failed" || !strings.Contains(s[1], "backend bug on boom") {
		t.Errorf("node a is %s with error %q, want failed with the panic's text", s[0], s[1])
	}
	if s := got["b"]; s[0] != "completed" {
		t.Errorf("node b is %s, want completed", s[0])
	}
}

// One item of a parallel_fan that panics fails as a failed call does: its
// section says so, with the panic's text, and the fan completes on the
// other item, half of two.
func TestRunFanOut_APanickingItemFailsAlone(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "wf.yaml")
	yaml := `name: panic-fan
version: 1
inputs:
  - items
nodes:
  fan:
    type: parallel_fan
    model: sonnet
    fan_source: items
    prompt: "check {item}"
    outputs: [joined]
`
	if err := os.WriteFile(wf, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	store := newParallelTestStore(t)
	res, err := Run(context.Background(), store, Config{
		WorkflowYAML: wf, ProjectDir: dir, MaxIterations: 5,
		Inputs:  map[string]any{"items": []any{"boom", "fine"}},
		Backend: panickingBackend{panicOn: "boom"}, Log: io.Discard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	states, err := store.Workflows().GetWorkflowNodeStates(res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].Status != "completed" {
		t.Fatalf("node states %+v, want the fan completed", states)
	}
	out := states[0].OutputsJSON.String
	if !strings.Contains(out, "(no finding: the call failed: panic: backend bug on boom)") || !strings.Contains(out, "ok") {
		t.Errorf("the fan's joined text %q lacks the panicking item's failure or the other item's answer", out)
	}
}

// panicTransport panics on a model call, the request a backend sends while
// it holds an endpoint slot, and refuses anything else.
type panicTransport struct{}

func (panicTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodPost {
		panic("transport bug")
	}
	return nil, errors.New("no server here")
}

// A call that panics while it holds an endpoint slot frees the slot as the
// panic unwinds, so a local server's one slot is not lost to a node whose
// panic was recovered: the next call gets it at once. Each endpoint is on
// this machine, so it has one slot.
func TestBackends_APanicInACallFreesItsEndpointSlot(t *testing.T) {
	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "")
	client := &http.Client{Transport: panicTransport{}}
	reg := &ToolRegistry{Handlers: map[string]ToolHandler{}}
	const anthropicBase = "http://127.0.0.1:11"
	t.Setenv("ANTHROPIC_BASE_URL", anthropicBase)
	cases := []struct {
		name, endpoint string
		call           func(context.Context)
	}{
		{"openai", "http://127.0.0.1:9/v1", func(ctx context.Context) {
			b := &OpenAIBackend{APIKey: "k", BaseURL: "http://127.0.0.1:9/v1", Client: client}
			_, _ = b.Run(ctx, RunRequest{Prompt: "hi", Model: "m", Registry: reg})
		}},
		{"gemini", "http://127.0.0.1:10", func(ctx context.Context) {
			b := &GeminiBackend{APIKey: "k", BaseURL: "http://127.0.0.1:10", Client: client}
			_, _ = b.Run(ctx, RunRequest{Prompt: "hi", Model: "m", Registry: reg})
		}},
		{"anthropic", anthropicBase, func(ctx context.Context) {
			c := &ClaudeRunner{
				Client: anthropic.NewClient(option.WithBaseURL(anthropicBase), option.WithAPIKey("k"),
					option.WithHTTPClient(client), option.WithMaxRetries(0)),
				Model: anthropic.ModelClaudeSonnet4_6, Registry: reg,
				MaxTurns: 1, MaxTokens: 16, PerCallTTL: 5 * time.Second,
			}
			_, _ = c.Run(ctx, "hi")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			panicked := func() (p any) {
				defer func() { p = recover() }()
				c.call(ctx)
				return nil
			}()
			if panicked == nil {
				t.Fatal("the call did not panic: the transport was never reached")
			}
			wait, cancelWait := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancelWait()
			release, err := acquireEndpointSlot(wait, c.name, c.endpoint)
			if err != nil {
				t.Fatalf("after the recovered panic the slot is still held: %v", err)
			}
			release()
		})
	}
}
