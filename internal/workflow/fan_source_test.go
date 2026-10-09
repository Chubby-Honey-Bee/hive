package workflow

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Validate names each decision branch that names no node: BuildGraph would
// drop it, and the node the author meant would become a start node that runs
// first.
func TestValidate_DecisionBranchNamesNoNode(t *testing.T) {
	cases := map[string]struct {
		trueEdge, falseEdge string
		bad                 []string
	}{
		"both real":         {"finish", "a", nil},
		"true misspelled":   {"finsh", "a", []string{"true_edge finsh"}},
		"false misspelled":  {"finish", "aa", []string{"false_edge aa"}},
		"both misspelled":   {"finsh", "aa", []string{"true_edge finsh", "false_edge aa"}},
		"empty true branch": {"", "a", []string{"true_edge "}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wf.yaml")
			src := "name: t\nnodes:\n" +
				"  a:\n    type: agent\n    prompt: go\n" +
				"  d:\n    type: decision\n    condition: \"x == 1\"\n" +
				"    true_edge: \"" + c.trueEdge + "\"\n    false_edge: \"" + c.falseEdge + "\"\n" +
				"  finish:\n    type: agent\n    prompt: done\n" +
				"edges:\n  - from: a\n    to: d\n"
			if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
			errs, _, err := Validate(path)
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			var got []string
			for _, e := range errs {
				if strings.Contains(e, "names no node") {
					got = append(got, e)
				}
			}
			if len(got) != len(c.bad) {
				t.Fatalf("branch errors = %v, want one for each of %v (all errors: %v)", got, c.bad, errs)
			}
			for i, want := range c.bad {
				if !strings.Contains(got[i], `"d"`) || !strings.Contains(got[i], want+" names no node") {
					t.Errorf("error %q does not name decision d and %q", got[i], want)
				}
			}
		})
	}
}

// fanDefn builds a workflow with a plan node, which declares the output
// angles and the state_updates key marked, and a fan node. extraNodes and
// edges are YAML lines.
func fanDefn(t *testing.T, fan, extraNodes, edges string) map[string]any {
	t.Helper()
	src := "name: t\ninputs: [topic, angles_in]\nnodes:\n" +
		"  plan:\n    type: agent\n    prompt: plan\n    outputs: [angles]\n    state_updates:\n      marked: yes\n" +
		"  fan:\n    type: parallel_fan\n" + fan +
		extraNodes +
		"edges:\n" + edges
	defn, err := LoadYAMLString(src)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	return defn
}

func TestFanSourceProblems(t *testing.T) {
	const planToFan = "  - from: plan\n    to: fan\n"
	const noSource = `is not an input, and no node upstream`
	const noItem = `its prompt has no`
	cases := []struct {
		name, fan, extra, edges string
		want                    []string // substrings, one per expected problem
	}{
		{
			name:  "declared in upstream outputs",
			fan:   "    fan_source: angles\n    prompt_template: \"do {item}\"\n",
			edges: planToFan,
		},
		{
			name:  "declared in upstream state_updates",
			fan:   "    fan_source: marked\n    prompt_template: \"do {item}\"\n",
			edges: planToFan,
		},
		{
			name:  "a workflow input",
			fan:   "    fan_source: angles_in\n    prompt_template: \"do {item}\"\n",
			edges: planToFan,
		},
		{
			name:  "declared by no node",
			fan:   "    fan_source: wave1_tasks\n    prompt_template: \"do {item}\"\n",
			edges: planToFan,
			want:  []string{`"fan": fan_source "wave1_tasks" ` + noSource},
		},
		{
			name:  "declared only downstream",
			fan:   "    fan_source: later\n    prompt_template: \"do {item}\"\n",
			extra: "  after:\n    type: agent\n    prompt: after\n    outputs: [later]\n",
			edges: planToFan + "  - from: fan\n    to: after\n",
			want:  []string{`"fan": fan_source "later" ` + noSource},
		},
		{
			// start → plan → fan → after → loop, and loop's false_edge
			// returns to plan. after reaches fan only over that back edge, so
			// on the first pass it has not run.
			name: "declared only across a loop's back edge",
			fan:  "    fan_source: later\n    prompt_template: \"do {item}\"\n",
			extra: "  start:\n    type: agent\n    prompt: start\n" +
				"  after:\n    type: agent\n    prompt: after\n    outputs: [later]\n" +
				"  loop:\n    type: decision\n    condition: \"x == 1\"\n    true_edge: end\n    false_edge: plan\n" +
				"  end:\n    type: agent\n    prompt: end\n",
			edges: "  - from: start\n    to: plan\n" + planToFan +
				"  - from: fan\n    to: after\n  - from: after\n    to: loop\n",
			want: []string{`"fan": fan_source "later" ` + noSource},
		},
		{
			name: "declared two hops up, through a decision branch",
			fan:  "    fan_source: angles\n    prompt_template: \"do {item}\"\n",
			extra: "  gate:\n    type: decision\n    condition: \"x == 1\"\n    true_edge: fan\n    false_edge: end\n" +
				"  end:\n    type: agent\n    prompt: end\n",
			edges: "  - from: plan\n    to: gate\n",
		},
		{
			name:  "prompt without the placeholder",
			fan:   "    fan_source: angles\n    prompt: \"do all of {angles}\"\n",
			edges: planToFan,
			want:  []string{`"fan": ` + noItem + ` {item}`},
		},
		{
			name:  "prompt wins over prompt_template",
			fan:   "    fan_source: angles\n    prompt: \"do all\"\n    prompt_template: \"do {item}\"\n",
			edges: planToFan,
			want:  []string{`"fan": ` + noItem + ` {item}`},
		},
		{
			name:  "custom placeholder",
			fan:   "    fan_source: angles\n    fan_placeholder: \"{gap}\"\n    prompt_template: \"fill {gap}\"\n",
			edges: planToFan,
		},
		{
			name:  "custom placeholder missing",
			fan:   "    fan_source: angles\n    fan_placeholder: \"{gap}\"\n    prompt_template: \"fill {item}\"\n",
			edges: planToFan,
			want:  []string{`"fan": ` + noItem + ` {gap}`},
		},
		{
			name:  "both problems",
			fan:   "    fan_source: nothing\n    prompt: \"do all\"\n",
			edges: planToFan,
			want:  []string{`fan_source "nothing" ` + noSource, noItem + ` {item}`},
		},
		{
			name:  "no fan_source is not checked",
			fan:   "    prompt: \"do all\"\n",
			edges: planToFan,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := FanSourceProblems(fanDefn(t, c.fan, c.extra, c.edges))
			if err != nil {
				t.Fatalf("FanSourceProblems: %v", err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("problems = %v, want %d matching %v", got, len(c.want), c.want)
			}
			for i, w := range c.want {
				if !strings.Contains(got[i], w) {
					t.Errorf("problem %d = %q, want it to contain %q", i, got[i], w)
				}
			}
		})
	}
}

// decompose.yaml's first wave fans one call per task its plan node returns.
func TestDecomposeWave1FansThePlansTasks(t *testing.T) {
	repo := newWFStore(t).Workflows()
	runID, err := InitWorkflow(repo, filepath.Join("..", "..", "workflows", "decompose.yaml"), map[string]any{"goal": "g"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GetNextNodes(repo, runID); err != nil {
		t.Fatal(err)
	}
	tasks := []any{"1 research: price the parts", "2 analysis: size the market"}
	if err := CompleteNode(repo, runID, "plan", map[string]any{
		"subtask_plan": "p", "task_count": len(tasks), "wave_count": 1, "wave1_tasks": tasks,
	}); err != nil {
		t.Fatal(err)
	}
	next, err := GetNextNodes(repo, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].Node != "wave1-execute" {
		t.Fatalf("next = %+v, want wave1-execute", next)
	}
	if len(next[0].FanItems) != len(tasks) {
		t.Fatalf("fan items = %v, want one per task in %v", next[0].FanItems, tasks)
	}
	for i, task := range tasks {
		if next[0].FanItems[i] != task {
			t.Errorf("fan item %d = %q, want %q", i, next[0].FanItems[i], task)
		}
	}
	if !strings.Contains(next[0].ResolvedPrompt, next[0].FanItemPlaceholder) {
		t.Errorf("prompt has no %s to substitute: %q", next[0].FanItemPlaceholder, next[0].ResolvedPrompt)
	}
}

// Every fan in every shipped workflow fans out: its fan_source is declared
// upstream and its prompt holds its placeholder.
func TestFanSourceProblems_ShippedWorkflows(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "workflows", "*.yaml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no shipped workflows found: %v", err)
	}
	for _, p := range paths {
		defn, err := LoadYAML(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		got, err := FanSourceProblems(defn)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(p), err)
			continue
		}
		if !reflect.DeepEqual(got, []string(nil)) {
			t.Errorf("%s:\n  %s", filepath.Base(p), strings.Join(got, "\n  "))
		}
	}
}

// A graph BuildGraph refuses cannot be walked for upstream nodes, so the
// check reports BuildGraph's error rather than an empty, passing list.
func TestFanSourceProblems_GraphDoesNotBuild(t *testing.T) {
	defn := fanDefn(t, "    fan_source: nothing\n    prompt: \"do all\"\n", "",
		"  - from: plan\n    to: fan\n  - from: fan\n    to: ghost\n")
	_, _, wantErr := BuildGraph(defn)
	if wantErr == nil {
		t.Fatal("test graph builds; it needs an edge to a node that does not exist")
	}
	got, err := FanSourceProblems(defn)
	if err == nil || err.Error() != wantErr.Error() {
		t.Errorf("FanSourceProblems = %v, %v; want BuildGraph's error %q", got, err, wantErr)
	}
}

// A fan whose fan_source key is missing from state runs once on its
// unsubstituted prompt. A node that returns a fan's fan_source among several
// outputs asks for them in a JSON reply, and a prose reply leaves the key
// out; a node with one output stores a prose reply under that key. So in
// every shipped workflow each node that returns a fan_source among several
// outputs refuses, in accept:, a reply that drops the key or sends null, and
// its on_reject: repairs it.
func TestFanSourceDeclarersRequireTheKey(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "workflows", "*.yaml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no shipped workflows found: %v", err)
	}
	checked := 0
	for _, p := range paths {
		file := filepath.Base(p)
		defn, err := LoadYAML(p)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		nodes, _ := defn["nodes"].(map[string]any)
		sources := map[string]bool{}
		for _, raw := range nodes {
			n, _ := raw.(map[string]any)
			if s, _ := n["fan_source"].(string); n["type"] == "parallel_fan" && s != "" {
				sources[s] = true
			}
		}
		for name, raw := range nodes {
			n, _ := raw.(map[string]any)
			outs, _ := n["outputs"].([]any)
			if len(outs) < 2 {
				continue
			}
			// A reply that holds every declared output: fan sources as a
			// one-item list, the rest as a non-empty string.
			full := map[string]any{}
			var fed []string
			for _, o := range outs {
				key, _ := o.(string)
				if sources[key] {
					full[key] = []any{"x"}
					fed = append(fed, key)
				} else {
					full[key] = "x"
				}
			}
			if len(fed) == 0 {
				continue
			}
			if rej := evaluateAccept(defn, name, full, full); rej != nil {
				t.Errorf("%s %s: a full reply is rejected: %v", file, name, rej)
			}
			if NodeOnRejectBlock(defn, name) == nil {
				t.Errorf("%s %s: no on_reject: block, so a rejected reply is not repaired", file, name)
			}
			for _, key := range fed {
				checked++
				for _, v := range []struct {
					label string
					drop  bool
				}{{"without " + key, true}, {key + " null", false}} {
					reply := map[string]any{}
					for k, val := range full {
						reply[k] = val
					}
					if v.drop {
						delete(reply, key)
					} else {
						reply[key] = nil
					}
					if evaluateAccept(defn, name, reply, reply) == nil {
						t.Errorf("%s %s: a reply %s is accepted", file, name, v.label)
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Error("no shipped node returns a fan_source among several outputs")
	}
}
