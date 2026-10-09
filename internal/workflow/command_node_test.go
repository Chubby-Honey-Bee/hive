package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A command node that validates can never auto-complete: an unknown key, a
// missing argv, or outputs it has no way to read are refused.
func TestValidate_CommandNode(t *testing.T) {
	cases := []struct {
		name, node string
		want       []string // substrings of the errors; none means valid
	}{
		{"valid", `{type: command, argv: [chb, hive, init], outputs_from: stdout_json, outputs: [phase], state_updates: {x: "{phase}"}, max_retries: 1, accept: ["outputs.phase != ''"], stdin: "{p}"}`, nil},
		{"no outputs", `{type: command, argv: [go, test, ./...]}`, nil},
		{"missing argv", `{type: command}`, []string{`Command node "a": missing 'argv'`}},
		{"empty argv", `{type: command, argv: []}`, []string{`missing 'argv'`}},
		{"argv not a list", `{type: command, argv: "chb hive init"}`, []string{`missing 'argv'`}},
		{"empty argv element", `{type: command, argv: [chb, ""]}`, []string{`argv[1] is not a non-empty string`}},
		{"unknown keys", `{type: command, argv: [chb], prompt: "run chb", model: sonnet}`, []string{`unknown key "model"`, `unknown key "prompt"`}},
		{"on_reject", `{type: command, argv: [chb], on_reject: {max_repair_iterations: 1}}`, []string{`unknown key "on_reject"`}},
		{"other outputs_from", `{type: command, argv: [chb], outputs_from: stdout, outputs: [x]}`, []string{`outputs_from stdout is not stdout_json`}},
		{"outputs without outputs_from", `{type: command, argv: [chb], outputs: [x]}`, []string{`'outputs' needs outputs_from: stdout_json`}},
		{"stdin not a string", `{type: command, argv: [chb], stdin: [a]}`, []string{`'stdin' must be a string`}},
		{"number in argv", `{type: command, argv: [chb, guard, --wave, 1]}`, []string{`argv[3] is not a non-empty string`}},
		{"ok_exit", `{type: command, argv: [chb], ok_exit: [0, 1]}`, nil},
		{"ok_exit empty", `{type: command, argv: [chb], ok_exit: []}`, []string{`ok_exit must be a non-empty list of exit codes`}},
		{"ok_exit out of range", `{type: command, argv: [chb], ok_exit: [0, 256]}`, []string{`ok_exit must be a non-empty list of exit codes`}},
		{"ok_exit not a list", `{type: command, argv: [chb], ok_exit: 1}`, []string{`ok_exit must be a non-empty list of exit codes`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wf.yaml")
			if err := os.WriteFile(path, []byte("name: t\nnodes:\n  a: "+c.node+"\nedges: []\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			errs, _, err := Validate(path)
			if err != nil {
				t.Fatal(err)
			}
			if (len(errs) == 0) != (len(c.want) == 0) {
				t.Fatalf("errors = %q, want them to hold %q", errs, c.want)
			}
			all := strings.Join(errs, "\n")
			for _, w := range c.want {
				if !strings.Contains(all, w) {
					t.Fatalf("errors = %q, want one containing %q", errs, w)
				}
			}
			// Every way of starting a run refuses what Validate refuses: a
			// run started with `chb agent-run` never calls Validate.
			_, initErr := InitWorkflow(newWFStore(t).Workflows(), path, nil)
			if (initErr != nil) != (len(c.want) > 0) {
				t.Fatalf("InitWorkflow = %v, want an error iff Validate reports one", initErr)
			}
			for _, w := range c.want {
				if !strings.Contains(initErr.Error(), w) {
					t.Fatalf("InitWorkflow = %v, want it to contain %q", initErr, w)
				}
			}
		})
	}
}

// A command node's argv is templated in one pass. A placeholder no state
// value fills is named in CommandError, so the runner fails the node rather
// than run the program with a literal `{name}`; braces inside a value are
// never read as placeholders; and a whole number renders as JSON writes it,
// never in exponent form.
func TestGetNextNodes_CommandNodeTemplating(t *testing.T) {
	inputs := map[string]any{
		"run":  1234567.0,
		"text": "a {run} and a {missing} inside a value",
	}
	repo := newWFStore(t).Workflows()
	runID := initYAML(t, repo, `
name: cmd
version: 1
nodes:
  relay:
    type: command
    argv: [chb, x, --run, "{run}", --text, "{text}", --gap, "{gap_id}", '{"literal": true}']
    stdin: "{finding_id}"
edges: []
`, inputs)
	nodes, err := GetNextNodes(repo, runID)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("next = %+v, %v", nodes, err)
	}
	d := nodes[0]
	run, _ := json.Marshal(inputs["run"])
	want := []string{"chb", "x", "--run", string(run), "--text", inputs["text"].(string), "--gap", "{gap_id}", `{"literal": true}`}
	if !reflect.DeepEqual(d.Argv, want) {
		t.Fatalf("argv = %q, want %q", d.Argv, want)
	}
	if strings.ContainsAny(d.Argv[3], "e+.") {
		t.Fatalf("{run} rendered %q, want a whole number", d.Argv[3])
	}
	for _, name := range []string{"{gap_id}", "{finding_id}"} {
		if !strings.Contains(d.CommandError, name) {
			t.Fatalf("CommandError = %q, want it to name %s", d.CommandError, name)
		}
	}
	if strings.Contains(d.CommandError, "{missing}") || strings.Contains(d.CommandError, "{run}") {
		t.Fatalf("CommandError = %q names a placeholder inside a value", d.CommandError)
	}
}

// A ready command node carries its argv and stdin templated from state, and
// no model.
func TestGetNextNodes_CommandNode(t *testing.T) {
	inputs := map[string]any{"project": "two words", "plan": []any{map[string]any{"gap_id": 3.0}}}
	repo := newWFStore(t).Workflows()
	runID := initYAML(t, repo, `
name: cmd
version: 1
nodes:
  scan:
    type: command
    argv: [chb, hive, next, --project, "{project}", --plan, "{plan}"]
    stdin: "project={project}"
    outputs_from: stdout_json
    outputs: [phase]
edges: []
`, inputs)
	nodes, err := GetNextNodes(repo, runID)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("next = %+v, %v", nodes, err)
	}
	plan, _ := json.Marshal(inputs["plan"])
	want := []string{"chb", "hive", "next", "--project", inputs["project"].(string), "--plan", string(plan)}
	d := nodes[0]
	if !reflect.DeepEqual(d.Argv, want) {
		t.Fatalf("argv = %q, want %q", d.Argv, want)
	}
	if d.Stdin != "project="+inputs["project"].(string) || d.OutputsFrom != "stdout_json" || d.Model != "" || d.Type != "command" {
		t.Fatalf("node = %+v, want stdin templated, outputs_from stdout_json, no model", d)
	}
}

// A list or an object renders as JSON in a template, with no HTML escaping
// to garble a prompt; a scalar renders as before.
func TestResolveTemplate_ListsAndObjectsRenderAsJSON(t *testing.T) {
	state := map[string]any{
		"gaps":  []any{"a <b> & c", "d"},
		"obj":   map[string]any{"k": 1.0, "list": []any{"x"}},
		"empty": []any{},
		"n":     3.0,
		"s":     "text",
	}
	for _, key := range []string{"gaps", "obj", "empty"} {
		got := ResolveTemplate("{"+key+"}", state)
		var back any
		if err := json.Unmarshal([]byte(got), &back); err != nil {
			t.Fatalf("{%s} rendered %q, which is not JSON: %v", key, got, err)
		}
		if !reflect.DeepEqual(back, state[key]) {
			t.Fatalf("{%s} rendered %q, which reads back as %#v, want %#v", key, got, back, state[key])
		}
	}
	if got := ResolveTemplate("{gaps}", state); !strings.Contains(got, state["gaps"].([]any)[0].(string)) {
		t.Fatalf("{gaps} rendered %q; the HTML characters were escaped", got)
	}
	if got := ResolveTemplate("{n} {s}", state); got != "3 text" {
		t.Fatalf("scalars rendered %q, want %q", got, "3 text")
	}
}
