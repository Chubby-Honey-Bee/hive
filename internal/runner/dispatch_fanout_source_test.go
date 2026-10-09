package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

const fanSourceWorkflow = `name: fan-source
inputs: [goal]
nodes:
  plan:
    type: agent
    model: haiku
    prompt: "PLAN {goal}"
    outputs: [plan]
  wave:
    type: parallel_fan
    model: haiku
    prompt: "WAVE for {goal}: {item}"
    fan_source: tasks
    outputs: [wave_results]
    accept:
      - "outputs.wave_results != ''"
    state_updates:
      wave: 1
  synth:
    type: agent
    model: haiku
    prompt: "SYNTH {wave_results}"
    outputs: [final]
edges:
  - {from: plan, to: wave}
  - {from: wave, to: synth}
`

// fanSourceBackend answers the plan node with a fixed reply and records
// every wave and synth prompt.
type fanSourceBackend struct {
	planReply string

	mu    sync.Mutex
	wave  []string
	synth []string
}

func (b *fanSourceBackend) Run(_ context.Context, req RunRequest) (*RunResult, error) {
	p := req.Prompt
	var text string
	switch {
	case strings.HasPrefix(p, "PLAN"):
		text = b.planReply
	case strings.HasPrefix(p, "WAVE"):
		b.mu.Lock()
		b.wave = append(b.wave, p)
		b.mu.Unlock()
		out, _ := json.Marshal(map[string]string{"wave_results": waveAnswer(p)})
		text = string(out)
	case strings.HasPrefix(p, "SYNTH"):
		b.mu.Lock()
		b.synth = append(b.synth, p)
		b.mu.Unlock()
		text = `{"final":"done"}`
	}
	return &RunResult{FinalText: text, InputTokens: 1, OutputTokens: 1, StopReason: "end_turn"}, nil
}

func waveAnswer(prompt string) string { return "answered " + prompt }

// A parallel_fan reads its items from the state value fan_source names. A
// list or a string fans one call per item, and none when it holds no items.
// A fan_source no node produced runs the node once as an ordinary call on
// its prompt. Either way the node completes and its state_updates apply.
func TestRun_ParallelFanFollowsItsSource(t *testing.T) {
	const goal = "ship it"
	prompt := "WAVE for " + goal + ": {item}"
	cases := map[string]any{
		"never produced":          nil,
		"list of two":             []any{"write the tests", "fix the bug"},
		"empty list":              []any{},
		"empty json-array string": "[]",
	}
	for name, tasks := range cases {
		t.Run(name, func(t *testing.T) {
			reply := map[string]any{"plan": "p"}
			if tasks != nil {
				reply["tasks"] = tasks
			}
			planReply, _ := json.Marshal(reply)

			var wantWave []string
			switch v := tasks.(type) {
			case nil:
				wantWave = []string{prompt}
			case []any:
				for _, item := range v {
					wantWave = append(wantWave, strings.ReplaceAll(prompt, "{item}", item.(string)))
				}
			case string:
				var items []string
				if err := json.Unmarshal([]byte(v), &items); err != nil {
					t.Fatal(err)
				}
				for _, item := range items {
					wantWave = append(wantWave, strings.ReplaceAll(prompt, "{item}", item))
				}
			}

			dir := t.TempDir()
			wf := filepath.Join(dir, "fan.yaml")
			if err := os.WriteFile(wf, []byte(fanSourceWorkflow), 0o644); err != nil {
				t.Fatal(err)
			}
			store := newTempStore(t)
			backend := &fanSourceBackend{planReply: string(planReply)}
			res, err := Run(context.Background(), store, Config{
				WorkflowYAML: wf,
				ProjectDir:   dir,
				Inputs:       map[string]any{"goal": goal},
				Backend:      backend,
				Log:          discardWriter{},
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			got := append([]string(nil), backend.wave...)
			sort.Strings(got)
			sort.Strings(wantWave)
			if strings.Join(got, "\n") != strings.Join(wantWave, "\n") {
				t.Errorf("wave prompts = %q, want %q", got, wantWave)
			}

			if len(backend.synth) != 1 {
				t.Fatalf("synth calls = %d, want 1", len(backend.synth))
			}
			for _, p := range wantWave {
				if !strings.Contains(backend.synth[0], waveAnswer(p)) {
					t.Errorf("synth prompt lacks the wave answer %q", waveAnswer(p))
				}
			}

			states, err := store.Workflows().GetWorkflowNodeStates(res.RunID)
			if err != nil {
				t.Fatal(err)
			}
			for _, ns := range states {
				if ns.Status != "completed" {
					t.Errorf("node %s status = %q, want completed", ns.NodeName, ns.Status)
				}
			}
			run, err := store.Workflows().GetWorkflowRun(res.RunID)
			if err != nil {
				t.Fatal(err)
			}
			var state map[string]any
			if err := json.Unmarshal([]byte(run.StateJSON), &state); err != nil {
				t.Fatal(err)
			}
			if state["wave"] != float64(1) {
				t.Errorf("state wave = %v, want 1 from the fan's state_updates", state["wave"])
			}
		})
	}
}
