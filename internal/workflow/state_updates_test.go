package workflow

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestApplyStateUpdatesEngine_NoNodes(t *testing.T) {
	state := map[string]any{}
	applyStateUpdatesEngine(map[string]any{}, "x", state)
	if len(state) != 0 {
		t.Errorf("state mutated: %v", state)
	}
}

func TestApplyStateUpdatesEngine_NodeMissing(t *testing.T) {
	defn := map[string]any{"nodes": map[string]any{}}
	state := map[string]any{}
	applyStateUpdatesEngine(defn, "ghost", state)
	if len(state) != 0 {
		t.Error("state mutated for ghost")
	}
}

func TestApplyStateUpdatesEngine_MalformedNode(t *testing.T) {
	defn := map[string]any{"nodes": map[string]any{"x": "string-not-map"}}
	state := map[string]any{}
	applyStateUpdatesEngine(defn, "x", state)
	if len(state) != 0 {
		t.Error("state mutated for non-map node")
	}
}

func TestApplyStateUpdatesEngine_NoStateUpdatesField(t *testing.T) {
	defn := map[string]any{
		"nodes": map[string]any{
			"x": map[string]any{"type": "agent"},
		},
	}
	state := map[string]any{}
	applyStateUpdatesEngine(defn, "x", state)
	if len(state) != 0 {
		t.Error("state mutated when no state_updates present")
	}
}

func TestApplyStateUpdatesEngine_LiteralAndTemplate(t *testing.T) {
	defn := map[string]any{
		"nodes": map[string]any{
			"x": map[string]any{
				"state_updates": map[string]any{
					"literal":  42,
					"template": "hello {name}",
					"plain":    "no braces",
				},
			},
		},
	}
	state := map[string]any{"name": "world"}
	applyStateUpdatesEngine(defn, "x", state)
	if state["literal"] != 42 {
		t.Errorf("literal = %v; want 42", state["literal"])
	}
	if state["template"] != "hello world" {
		t.Errorf("template = %v; want 'hello world'", state["template"])
	}
	if state["plain"] != "no braces" {
		t.Errorf("plain = %v; want 'no braces'", state["plain"])
	}
}

// A decision applies its state_updates when it completes, so `{routed}`
// reaches the prompt resolved.
func TestDecision_AppliesItsStateUpdates(t *testing.T) {
	repo := newWFStore(t).Workflows()
	runID := initYAML(t, repo, `
name: decision-state-updates
version: 1
nodes:
  a: {type: agent, prompt: a, outputs: [x]}
  d:
    type: decision
    condition: "x == 1"
    true_edge: b
    false_edge: c
    state_updates: {routed: "yes", via: "x={x}"}
  b: {type: agent, prompt: "routed={routed} via={via}"}
  c: {type: agent, prompt: c}
edges:
  - {from: a, to: d}
`, nil)
	expectNext(t, repo, runID, "a")
	x := 1
	complete(t, repo, runID, "a", map[string]any{"x": x})

	next, err := GetNextNodes(repo, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].Node != "b" {
		t.Fatalf("next = %+v, want b", next)
	}
	via := fmt.Sprintf("x=%d", x)
	if want := "routed=yes via=" + via; next[0].ResolvedPrompt != want {
		t.Errorf("prompt = %q, want %q", next[0].ResolvedPrompt, want)
	}
	run, err := repo.GetWorkflowRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal([]byte(run.StateJSON), &state); err != nil {
		t.Fatal(err)
	}
	if state["routed"] != "yes" || state["via"] != via {
		t.Errorf("state = %v, want routed=yes and via=%s persisted", state, via)
	}
}
