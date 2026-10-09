package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A fan whose fan_source no upstream node declares runs once on its
// unsubstituted prompt. Preflight refuses it and passes the same workflow
// once the source is declared upstream.
func TestPreflight_FanSourceMustBeDeclaredUpstream(t *testing.T) {
	const tmpl = `name: t
inputs: [goal]
nodes:
  plan:
    type: agent
    prompt: "plan {goal}"
    outputs: [DECLARED]
    accept:
      - "outputs.DECLARED != ''"
  run:
    type: parallel_fan
    prompt: "do {item}"
    fan_source: wave1_tasks
    outputs: [results]
    accept:
      - "outputs.results != ''"
edges:
  - from: plan
    to: run
`
	for _, c := range []struct {
		declared string
		fails    bool
	}{
		{"subtask_plan", true},
		{"wave1_tasks", false},
	} {
		t.Run("plan declares "+c.declared, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wf.yaml")
			if err := os.WriteFile(path, []byte(strings.ReplaceAll(tmpl, "DECLARED", c.declared)), 0o644); err != nil {
				t.Fatal(err)
			}
			r := newPreflightReport()
			r.checkFanSources(r.loadDefinition(path))
			var report bytes.Buffer
			r.print(&report)
			if r.hasFailures() != c.fails {
				t.Fatalf("fan check failed=%v, want %v:\n%s", r.hasFailures(), c.fails, report.String())
			}
			if c.fails && !strings.Contains(report.String(), `fan_source "wave1_tasks"`) {
				t.Errorf("the failure does not name the fan_source:\n%s", report.String())
			}
		})
	}
}

// Every shipped workflow passes preflight's workflow checks, the fan check
// included. The machine checks are left out: they judge the host.
func TestPreflight_ShippedWorkflowsPassWorkflowChecks(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "workflows", "*.yaml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no shipped workflows found: %v", err)
	}
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			r := newPreflightReport()
			r.checkYAML(p)
			defn := r.loadDefinition(p)
			r.checkAcceptPresence(defn)
			r.checkAcceptAsksForItsOutputs(defn)
			r.checkFanSources(defn)
			if r.hasFailures() {
				var report bytes.Buffer
				r.print(&report)
				t.Errorf("preflight workflow checks failed:\n%s", report.String())
			}
		})
	}
}

// The fan check walks the graph, so it can say nothing about a workflow with
// no nodes or one whose graph does not build. It warns that it skipped
// rather than passing a check it did not run.
func TestPreflight_FanCheckSkipsWhatItCannotCheck(t *testing.T) {
	const ghostEdge = `name: t
nodes:
  plan:
    type: agent
    prompt: plan
    outputs: [angles]
  run:
    type: parallel_fan
    prompt: "do all"
    fan_source: nothing
    outputs: [results]
edges:
  - from: plan
    to: run
  - from: run
    to: ghost
`
	for name, src := range map[string]string{
		"edge to a missing node": ghostEdge,
		"no nodes":               "name: t\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wf.yaml")
			if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
			r := newPreflightReport()
			r.checkFanSources(r.loadDefinition(path))
			if len(r.results) != 1 || r.results[0].name != "parallel_fan wiring" || r.results[0].level != checkWarn {
				var report bytes.Buffer
				r.print(&report)
				t.Errorf("want one parallel_fan wiring warning, got:\n%s", report.String())
			}
		})
	}
}
