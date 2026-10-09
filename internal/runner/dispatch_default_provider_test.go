package runner

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// recordingBackend keeps the last request and returns a fixed usage.
type recordingBackend struct {
	mu  sync.Mutex
	req RunRequest
}

func (b *recordingBackend) Run(_ context.Context, req RunRequest) (*RunResult, error) {
	b.mu.Lock()
	b.req = req
	b.mu.Unlock()
	return &RunResult{FinalText: `{"result":"ok"}`, Turns: 1, InputTokens: 1_000_000, OutputTokens: 1_000_000}, nil
}

// TestDispatch_RunDefaultProviderResolvesModel runs a `model: sonnet` node
// with no `provider:` override under each run default. The model the provider
// serves (the Backends table in docs/specs/runner.md) must be what is
// pinned, what sets the cap, and what is costed. Only the claude CLI gets the
// alias as written, because it resolves aliases itself.
func TestDispatch_RunDefaultProviderResolvesModel(t *testing.T) {
	cfgModels := models.Load()
	claudeSonnet := cfgModels.Aliases["sonnet"]
	cases := []struct {
		provider string
		sent     string // model in the backend request
		served   string // model pinned, capped and costed
	}{
		{"anthropic", claudeSonnet, claudeSonnet},
		{"claude-cli", "sonnet", claudeSonnet},
		{"openai", "gpt-5.1", "gpt-5.1"},
		{"gemini", "gemini-2.5-pro", "gemini-2.5-pro"},
		{"gemini-cli", "gemini-2.5-pro", "gemini-2.5-pro"},
	}
	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			nodeName := "w1-default-" + c.provider
			yamlStr := fmt.Sprintf("name: t\nnodes:\n  %s:\n    type: agent\n    model: sonnet\n    prompt: p\n", nodeName)
			store := newTempStore(t)
			runID := seedAgentRun(t, store, nodeName, yamlStr)
			defn, _ := workflow.LoadYAMLString(yamlStr)
			backend := &recordingBackend{}
			rc := buildAgentNodeRC(t, store, runID, defn, backend, Config{Provider: c.provider})

			rc.executeAgentNode(workflow.DispatchNode{Node: nodeName, Type: "agent", Model: "sonnet"})

			m, ok := cfgModels.Models[c.served]
			if !ok {
				t.Fatalf("precondition: %q has no models config entry", c.served)
			}
			if backend.req.Model != c.sent {
				t.Errorf("model sent = %q, want %q", backend.req.Model, c.sent)
			}
			if backend.req.MaxTokens != m.MaxOutputTokens {
				t.Errorf("cap = %d, want %s's %d", backend.req.MaxTokens, c.served, m.MaxOutputTokens)
			}
			// 1M tokens each way: the cost is the two per-1M prices, in 1/10000 USD.
			if want := int64(m.InputPerMTokUSD*10_000) + int64(m.OutputPerMTokUSD*10_000); rc.res.CostUSDx10000 != want {
				t.Errorf("cost = %d, want %s's %d", rc.res.CostUSDx10000, c.served, want)
			}
			states, err := store.Workflows().GetWorkflowNodeStates(runID)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range states {
				if s.NodeName == nodeName && s.Model != c.served {
					t.Errorf("pinned resolved_model = %q, want %q", s.Model, c.served)
				}
			}
		})
	}
}
