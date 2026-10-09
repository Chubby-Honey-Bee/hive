package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A cycle with no decision node on it has no exit, so Validate refuses it:
// it would initialise, then run to the iteration cap having completed
// nothing. A loop closed by a decision's branch is the engine's retry loop
// and stays valid (loop_test.go runs one).
func TestValidate_CycleDetection(t *testing.T) {
	cases := []struct {
		name      string
		yaml      string
		wantError bool
	}{
		{
			name: "a decision-free loop is refused",
			yaml: `
name: loop
version: 1
nodes:
  a: {type: agent, agent: researcher, prompt: "a"}
  b: {type: agent, agent: researcher, prompt: "b"}
edges:
  - {from: a, to: b}
  - {from: b, to: a}
`,
			wantError: true,
		},
		{
			name:      "a retry loop closed by a decision stays valid",
			yaml:      decisionWorkflowYAML,
			wantError: false,
		},
		{
			name: "a plain chain stays valid",
			yaml: `
name: chain
version: 1
nodes:
  a: {type: agent, agent: researcher, prompt: "a"}
  b: {type: agent, agent: researcher, prompt: "b"}
edges:
  - {from: a, to: b}
`,
			wantError: false,
		},
		{
			name: "a node edged to itself is refused",
			yaml: `
name: selfloop
version: 1
nodes:
  a: {type: agent, agent: researcher, prompt: "a"}
edges:
  - {from: a, to: a}
`,
			wantError: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := filepath.Join(t.TempDir(), "wf.yaml")
			if err := os.WriteFile(f, []byte(tc.yaml), 0o644); err != nil {
				t.Fatal(err)
			}
			errs, _, err := Validate(f)
			if err != nil {
				t.Fatal(err)
			}
			var cycErr string
			for _, e := range errs {
				if strings.Contains(e, "Cycle") {
					cycErr = e
				}
			}
			if tc.wantError && cycErr == "" {
				t.Errorf("no cycle reported; errors were %v", errs)
			}
			if !tc.wantError && cycErr != "" {
				t.Errorf("reported a cycle on a valid workflow: %s", cycErr)
			}
		})
	}
}

// decisionSelfLoopYAML is a decision whose taken branch is itself.
const decisionSelfLoopYAML = `
name: self
version: 1
nodes:
  start: {type: agent, prompt: s}
  d: {type: decision, condition: "true", true_edge: d, false_edge: finish}
  finish: {type: agent, prompt: f}
edges:
  - {from: start, to: d}
`

// decisionCycleYAML is a cycle made only of decision nodes.
const decisionCycleYAML = `
name: decisions
version: 1
nodes:
  start: {type: agent, prompt: s}
  d1: {type: decision, condition: "true", true_edge: d2, false_edge: finish}
  d2: {type: decision, condition: "true", true_edge: d1, false_edge: finish}
  finish: {type: agent, prompt: f}
edges:
  - {from: start, to: d1}
`

// A cycle made only of decisions runs no node, so nothing a node returns can
// change the branches it takes, and GetNextNodes would take them again and
// again in one call. Validate refuses it, though a decision closes its loop
// and the cycle with decisions cut out is empty.
func TestValidate_RefusesACycleOfDecisionsOnly(t *testing.T) {
	for _, c := range []struct {
		name, yaml, path string
	}{
		{"a decision whose branch is itself", decisionSelfLoopYAML, "d → d"},
		{"two decisions in a loop", decisionCycleYAML, "d1 → d2 → d1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := filepath.Join(t.TempDir(), "wf.yaml")
			if err := os.WriteFile(f, []byte(c.yaml), 0o644); err != nil {
				t.Fatal(err)
			}
			errs, _, err := Validate(f)
			if err != nil {
				t.Fatal(err)
			}
			if len(errs) != 1 || !strings.Contains(errs[0], "only of decision nodes") || !strings.HasSuffix(errs[0], ": "+c.path) {
				t.Fatalf("errors = %v, want the cycle %s refused as one made only of decisions", errs, c.path)
			}
		})
	}
}
