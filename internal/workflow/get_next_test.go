package workflow

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

func TestGetNextNodes_BadDefnYAML(t *testing.T) {
	store := newWFStore(t)
	id, err := store.Workflows().CreateWorkflowRun(
		"t", 1, "{[!@#$]}", `{}`, []db.NodeSeed{{Name: "a", Type: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = GetNextNodes(store.Workflows(), id)
	if err == nil {
		t.Error("expected parse error from bad YAML")
	}
}

func TestGetNextNodes_DispatchableHappy(t *testing.T) {
	store := newWFStore(t)
	yaml := `name: t
nodes:
  start:
    type: agent
    prompt: "do {input}"
    model: haiku
    outputs: [out_a]
`
	id, err := store.Workflows().CreateWorkflowRun(
		"t", 1, yaml, `{"input":"thing"}`, []db.NodeSeed{{Name: "start", Type: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := GetNextNodes(store.Workflows(), id)
	if err != nil {
		t.Fatalf("GetNextNodes: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d; want 1", len(got))
	}
	if got[0].Node != "start" {
		t.Errorf("Node = %q; want start", got[0].Node)
	}
	if got[0].Model != "haiku" {
		t.Errorf("Model = %q; want haiku", got[0].Model)
	}
	if got[0].ResolvedPrompt != "do thing" {
		t.Errorf("ResolvedPrompt = %q; want 'do thing'", got[0].ResolvedPrompt)
	}
	if len(got[0].Outputs) != 1 || got[0].Outputs[0] != "out_a" {
		t.Errorf("Outputs = %v; want [out_a]", got[0].Outputs)
	}
}

func TestGetNextNodes_ForagerNameInferred(t *testing.T) {
	store := newWFStore(t)
	yaml := `name: t
nodes:
  forager-optimist:
    type: agent
    prompt: "be optimistic"
`
	id, err := store.Workflows().CreateWorkflowRun(
		"t", 1, yaml, `{}`, []db.NodeSeed{{Name: "forager-optimist", Type: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := GetNextNodes(store.Workflows(), id)
	if err != nil {
		t.Fatalf("GetNextNodes: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d; want 1", len(got))
	}
	if got[0].ForagerName != "optimist" {
		t.Errorf("ForagerName = %q; want optimist", got[0].ForagerName)
	}
}

// TestGetNextNodes_ParallelFanFallsBackToPromptTemplate: when prompt is
// empty and the node declares prompt_template, as a parallel_fan does, the
// engine dispatches the template, and ResolveTemplate fills its
// upstream-output placeholders, so the node never dispatches an empty
// ResolvedPrompt.
func TestGetNextNodes_ParallelFanFallsBackToPromptTemplate(t *testing.T) {
	store := newWFStore(t)
	yaml := `name: t
inputs: [topic]
nodes:
  fan:
    type: parallel_fan
    agent: researcher
    prompt_template: "research the angles of {topic}"
    fan_source: angles
`
	id, err := store.Workflows().CreateWorkflowRun(
		"t", 1, yaml, `{"topic":"bees"}`,
		[]db.NodeSeed{{Name: "fan", Type: "parallel_fan"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := GetNextNodes(store.Workflows(), id)
	if err != nil {
		t.Fatalf("GetNextNodes: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d; want 1", len(got))
	}
	if got[0].ResolvedPrompt == "" {
		t.Error("ResolvedPrompt should be non-empty when prompt_template is set")
	}
	if got[0].ResolvedPrompt != "research the angles of bees" {
		t.Errorf("ResolvedPrompt = %q; want template substituted", got[0].ResolvedPrompt)
	}
}

// An upstream node that returns its list as JSON leaves a []any in state.
// That list fans out one item per element: a string element as it is,
// anything else JSON-encoded. A JSON-array string still fans the same way.
// A list or string with no items marks the fan empty. A fan_source that
// no node produced is not empty: the node runs as one ordinary call.
func TestGetNextNodes_ParallelFanFansAListInState(t *testing.T) {
	list := []any{"What about cost?", "What about latency?", map[string]any{"angle": "risk"}, 3.0}
	var want []string
	for _, v := range list {
		if s, ok := v.(string); ok {
			want = append(want, s)
			continue
		}
		b, _ := json.Marshal(v)
		want = append(want, string(b))
	}
	asString, _ := json.Marshal([]string{"a", "b"})

	cases := map[string]struct {
		value any
		want  []string
	}{
		"json list":               {list, want},
		"json-array string":       {string(asString), []string{"a", "b"}},
		"empty list":              {[]any{}, nil},
		"empty json-array string": {"[]", nil},
		"blank string":            {"  \n ", nil},
		"never produced":          {nil, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := newWFStore(t)
			yaml := `name: t
nodes:
  fan:
    type: parallel_fan
    prompt_template: "fill {gap}"
    fan_source: gaps
    fan_placeholder: "{gap}"
`
			st := map[string]any{}
			if tc.value != nil {
				st["gaps"] = tc.value
			}
			state, _ := json.Marshal(st)
			id, err := store.Workflows().CreateWorkflowRun(
				"t", 1, yaml, string(state), []db.NodeSeed{{Name: "fan", Type: "parallel_fan"}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := GetNextNodes(store.Workflows(), id)
			if err != nil {
				t.Fatalf("GetNextNodes: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("len = %d; want 1", len(got))
			}
			if len(tc.want) == 0 {
				if len(got[0].FanItems) != 0 {
					t.Errorf("FanItems = %q; want none", got[0].FanItems)
				}
			} else if !reflect.DeepEqual(got[0].FanItems, tc.want) {
				t.Errorf("FanItems = %q; want %q", got[0].FanItems, tc.want)
			}
			_, isString := tc.value.(string)
			_, isList := tc.value.([]any)
			if wantEmpty := (isString || isList) && len(tc.want) == 0; got[0].FanEmpty != wantEmpty {
				t.Errorf("FanEmpty = %v; want %v", got[0].FanEmpty, wantEmpty)
			}
		})
	}
}

// TestValidate_ParallelFanRequiresPromptOrTemplate asserts that a
// parallel_fan node missing both prompt and prompt_template fails
// validation — preventing the empty-prompt dispatch path entirely.
func TestValidate_ParallelFanRequiresPromptOrTemplate(t *testing.T) {
	tmp := t.TempDir() + "/wf.yaml"
	yamlBody := `name: bad
nodes:
  fan:
    type: parallel_fan
    agent: researcher
`
	if err := os.WriteFile(tmp, []byte(yamlBody), 0o644); err != nil {
		t.Fatal(err)
	}
	errs, _, err := Validate(tmp)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e, "parallel_fan") && strings.Contains(e, "prompt_template") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected validation error about missing prompt/prompt_template; got %v", errs)
	}
}
