package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A list or a map in state reaches a prompt as JSON, which keeps the
// boundaries between items, where Go's %v would write [a b c]. A scalar is
// written as itself.
func TestResolveTemplate_ListsAndMapsAsJSON(t *testing.T) {
	values := map[string]any{
		"list":   []any{"What about cost?", "a <b> & c", 3.0},
		"map":    map[string]any{"k": []any{"x", "y"}, "n": 2.0},
		"empty":  []any{},
		"text":   "plain",
		"number": 4.0,
	}
	for key, v := range values {
		got := ResolveTemplate("<{"+key+"}>", map[string]any{key: v})
		got = strings.TrimSuffix(strings.TrimPrefix(got, "<"), ">")
		switch v.(type) {
		case []any, map[string]any:
			var back any
			if err := json.Unmarshal([]byte(got), &back); err != nil {
				t.Errorf("%s rendered as %q, which is not JSON: %v", key, got, err)
				continue
			}
			if !reflect.DeepEqual(back, v) {
				t.Errorf("%s rendered as %q, which decodes to %v, want %v", key, got, back, v)
			}
			if strings.Contains(got, `\u003c`) {
				t.Errorf("%s rendered with HTML escapes: %q", key, got)
			}
		default:
			if want := fmt.Sprintf("%v", v); got != want {
				t.Errorf("%s rendered as %q, want %q", key, got, want)
			}
		}
	}
}

func writeWorkflow(t *testing.T, src string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wf.yaml")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// tools: names tools from ToolNames on an agent or parallel_fan node,
// min_tool_calls: is a positive integer on an agent node that has a tool, and
// fan_limit: needs fan_overflow: on a parallel_fan. Validate reports a bad
// field, and a run does not start on one.
func TestCheckNodeFields(t *testing.T) {
	cases := map[string]struct {
		node string
		bad  string // a phrase the error names; empty when the node is valid
	}{
		"no tools":            {"    type: agent\n    prompt: p\n", ""},
		"empty tools":         {"    type: agent\n    prompt: p\n    tools: []\n", ""},
		"every known tool":    {"    type: agent\n    prompt: p\n    tools: [" + strings.Join(ToolNames, ", ") + "]\n", ""},
		"unknown tool":        {"    type: agent\n    prompt: p\n    tools: [read_file, zsh]\n", "zsh"},
		"bash, shell's alias": {"    type: agent\n    prompt: p\n    tools: [bash]\n", ""},
		"tools not a list":    {"    type: agent\n    prompt: p\n    tools: read_file\n", "must be a list"},
		"tools on decision":   {"    type: decision\n    condition: \"x == 1\"\n    true_edge: a\n    false_edge: a\n    tools: []\n", "agent and parallel_fan"},
		"fan limit":           {"    type: parallel_fan\n    prompt: \"{item}\"\n    fan_source: xs\n    fan_limit: 2\n    fan_overflow: rest\n", ""},
		"limit, no overflow":  {"    type: parallel_fan\n    prompt: \"{item}\"\n    fan_source: xs\n    fan_limit: 2\n", "fan_overflow"},
		"overflow, no limit":  {"    type: parallel_fan\n    prompt: \"{item}\"\n    fan_source: xs\n    fan_overflow: rest\n", "without fan_limit"},
		"zero limit":          {"    type: parallel_fan\n    prompt: \"{item}\"\n    fan_source: xs\n    fan_limit: 0\n    fan_overflow: rest\n", "positive integer"},
		"limit on agent":      {"    type: agent\n    prompt: p\n    fan_limit: 2\n    fan_overflow: rest\n", "parallel_fan nodes"},
		"min tool calls":      {"    type: agent\n    prompt: p\n    min_tool_calls: 1\n", ""},
		"min with a tool":     {"    type: agent\n    prompt: p\n    tools: [read_file]\n    min_tool_calls: 2\n", ""},
		"min zero":            {"    type: agent\n    prompt: p\n    min_tool_calls: 0\n", "positive integer"},
		"min not a number":    {"    type: agent\n    prompt: p\n    min_tool_calls: one\n", "positive integer"},
		"min with no tools":   {"    type: agent\n    prompt: p\n    tools: []\n    min_tool_calls: 1\n", "offers none"},
		"min on a fan":        {"    type: parallel_fan\n    prompt: \"{item}\"\n    fan_source: xs\n    min_tool_calls: 1\n", "agent nodes only"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			src := "name: t\ninputs: [xs]\nnodes:\n  a:\n    type: agent\n    prompt: go\n  n:\n" + c.node +
				"edges:\n  - {from: a, to: n}\n"
			defn, err := LoadYAMLString(src)
			if err != nil {
				t.Fatal(err)
			}
			checkErr := CheckNodeFields(defn)
			path := writeWorkflow(t, src)
			errs, _, err := Validate(path)
			if err != nil {
				t.Fatal(err)
			}
			_, initErr := InitWorkflow(newTestStore(t).Workflows(), path, map[string]any{"xs": []any{}})
			if c.bad == "" {
				if checkErr != nil || initErr != nil || len(errs) != 0 {
					t.Fatalf("valid node refused: check %v, init %v, validate %v", checkErr, initErr, errs)
				}
				return
			}
			if checkErr == nil || !strings.Contains(checkErr.Error(), c.bad) {
				t.Errorf("CheckNodeFields = %v, want an error naming %q", checkErr, c.bad)
			}
			if !strings.Contains(strings.Join(errs, "\n"), c.bad) {
				t.Errorf("Validate errors %v, want one naming %q", errs, c.bad)
			}
			if initErr == nil {
				t.Error("InitWorkflow started a run on the bad node")
			}
		})
	}
}

// A node's tools: reaches the dispatch as its allowlist: absent is nil,
// so the node keeps every tool, and [] is an empty list, so it has none.
func TestGetNextNodes_CarriesTools(t *testing.T) {
	cases := map[string]struct {
		line string
		want *[]string
	}{
		"absent": {"", nil},
		"none":   {"    tools: []\n", &[]string{}},
		"read":   {"    tools: [read_file, glob, grep]\n", &[]string{"read_file", "glob", "grep"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeWorkflow(t, "name: t\nnodes:\n  a:\n    type: agent\n    prompt: go\n"+c.line)
			store := newTestStore(t)
			runID, err := InitWorkflow(store.Workflows(), path, nil)
			if err != nil {
				t.Fatal(err)
			}
			next, err := GetNextNodes(store.Workflows(), runID)
			if err != nil || len(next) != 1 {
				t.Fatalf("next = %v, %v", next, err)
			}
			if !reflect.DeepEqual(next[0].Tools, c.want) {
				t.Errorf("Tools = %v, want %v", deref(next[0].Tools), deref(c.want))
			}
		})
	}
}

// A node's min_tool_calls: reaches the dispatch; absent is 0, no minimum.
func TestGetNextNodes_CarriesMinToolCalls(t *testing.T) {
	for line, want := range map[string]int{"": 0, "    min_tool_calls: 2\n": 2} {
		path := writeWorkflow(t, "name: t\nnodes:\n  a:\n    type: agent\n    prompt: go\n"+line)
		store := newTestStore(t)
		runID, err := InitWorkflow(store.Workflows(), path, nil)
		if err != nil {
			t.Fatal(err)
		}
		next, err := GetNextNodes(store.Workflows(), runID)
		if err != nil || len(next) != 1 || next[0].MinToolCalls != want {
			t.Fatalf("%q: next = %+v, %v; want MinToolCalls %d", line, next, err, want)
		}
	}
}

func deref(p *[]string) any {
	if p == nil {
		return nil
	}
	return *p
}

// A parallel_fan with fan_limit dispatches its first fan_limit items and,
// when it completes, lists the rest under fan_overflow, so no item is
// dropped silently. An empty fan lists none.
func TestFanLimit_DispatchesTheFirstItemsAndListsTheRest(t *testing.T) {
	const limit = 2
	cases := map[string][]any{
		"over the limit":  {"g1", "g2", "g3", "g4", "g5"},
		"at the limit":    {"g1", "g2"},
		"under the limit": {"g1"},
		"empty":           {},
	}
	for name, items := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeWorkflow(t, fmt.Sprintf(`name: t
inputs: [xs]
nodes:
  fan:
    type: parallel_fan
    prompt: "look at {item}"
    fan_source: xs
    fan_limit: %d
    fan_overflow: rest
    outputs: [found]
`, limit))
			store := newTestStore(t)
			repo := store.Workflows()
			runID, err := InitWorkflow(repo, path, map[string]any{"xs": items})
			if err != nil {
				t.Fatal(err)
			}
			next, err := GetNextNodes(repo, runID)
			if err != nil || len(next) != 1 {
				t.Fatalf("next = %v, %v", next, err)
			}
			n := min(len(items), limit)
			var want []string
			for _, it := range items[:n] {
				want = append(want, it.(string))
			}
			if !reflect.DeepEqual(next[0].FanItems, want) {
				t.Errorf("FanItems = %v, want the first %d of %v", next[0].FanItems, limit, items)
			}
			if next[0].FanEmpty != (len(items) == 0) {
				t.Errorf("FanEmpty = %v with %d items", next[0].FanEmpty, len(items))
			}

			if len(items) == 0 {
				err = CompleteEmptyFan(repo, runID, "fan")
			} else {
				err = CompleteNode(repo, runID, "fan", map[string]any{"found": "x"})
			}
			if err != nil {
				t.Fatal(err)
			}
			run, err := repo.GetWorkflowRun(runID)
			if err != nil {
				t.Fatal(err)
			}
			var state map[string]any
			if err := json.Unmarshal([]byte(run.StateJSON), &state); err != nil {
				t.Fatal(err)
			}
			wantRest := append([]any{}, items[n:]...)
			if !reflect.DeepEqual(state["rest"], wantRest) {
				t.Errorf("state rest = %v, want the items past the limit %v", state["rest"], wantRest)
			}
		})
	}
}
