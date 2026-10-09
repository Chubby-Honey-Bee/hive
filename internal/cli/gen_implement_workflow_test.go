package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/review"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// genImplement runs `gen-implement-workflow` on a two-finding findings.json
// with extra flags and returns the parsed workflow's nodes.
func genImplement(t *testing.T, extra ...string) map[string]any {
	t.Helper()
	dir := t.TempDir()
	in := filepath.Join(dir, "findings.json")
	agg := review.Aggregate{AllFindings: []review.Finding{
		{Lens: "l", File: "a.go", Line: 1, Severity: "critical", Issue: "i1", Fix: "f1"},
		{Lens: "l", File: "b.go", Line: 2, Severity: "high", Issue: "i2", Fix: "f2"},
	}}
	b, _ := json.Marshal(agg)
	if err := os.WriteFile(in, b, 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := newGenImplementWorkflowCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(append([]string{in}, extra...))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("gen-implement-workflow: %v", err)
	}
	defn, err := workflow.LoadYAMLString(out.String())
	if err != nil {
		t.Fatalf("parse generated YAML: %v\n%s", err, out.String())
	}
	nodes, _ := defn["nodes"].(map[string]any)
	return nodes
}

func fixNodes(nodes map[string]any) []map[string]any {
	var out []map[string]any
	for _, name := range []string{"fix-1", "fix-2"} {
		if n, ok := nodes[name].(map[string]any); ok {
			out = append(out, n)
		}
	}
	return out
}

// --model and --repair-model, when given, are what the fix nodes and their
// repair run on.
func TestGenImplement_ModelFlagsAreEmitted(t *testing.T) {
	const model, repairModel = "haiku", "claude-opus-4-8"
	fixes := fixNodes(genImplement(t, "--model", model, "--repair-model", repairModel))
	if len(fixes) != 2 {
		t.Fatalf("fix nodes=%d; want 2", len(fixes))
	}
	for i, n := range fixes {
		if n["model"] != model || n["tier"] != nil {
			t.Errorf("fix-%d model=%v tier=%v; want model %q and no tier", i+1, n["model"], n["tier"], model)
		}
		rej, _ := n["on_reject"].(map[string]any)
		if rej["model"] != repairModel || rej["tier"] != nil {
			t.Errorf("fix-%d on_reject model=%v tier=%v; want model %q and no tier", i+1, rej["model"], rej["tier"], repairModel)
		}
	}
}

// Without the flags, the budget mode picks the models through tiers.
func TestGenImplement_DefaultsAreTiers(t *testing.T) {
	fixes := fixNodes(genImplement(t))
	if len(fixes) != 2 {
		t.Fatalf("fix nodes=%d; want 2", len(fixes))
	}
	for i, n := range fixes {
		if n["tier"] != "worker" || n["model"] != nil {
			t.Errorf("fix-%d tier=%v model=%v; want tier worker and no model", i+1, n["tier"], n["model"])
		}
		rej, _ := n["on_reject"].(map[string]any)
		if rej["tier"] != "synthesist" || rej["model"] != nil {
			t.Errorf("fix-%d on_reject tier=%v model=%v; want tier synthesist and no model", i+1, rej["tier"], rej["model"])
		}
	}
}

// `chb implement` forwards its --model and --repair-model to
// gen-implement-workflow as they are (implement_pipeline.go, stage 2).
// Left unset they keep the tier defaults; set, they reach the nodes.
func TestImplement_ModelFlagsReachGenerator(t *testing.T) {
	for _, tc := range []struct {
		name          string
		args          []string
		model, repair string // "" means the tier default
	}{
		{name: "unset"},
		{name: "set", args: []string{"--model", "haiku", "--repair-model", "opus"}, model: "haiku", repair: "opus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			impl := newImplementCmd()
			if err := impl.ParseFlags(tc.args); err != nil {
				t.Fatal(err)
			}
			model, _ := impl.Flags().GetString("model")
			repair, _ := impl.Flags().GetString("repair-model")
			fixes := fixNodes(genImplement(t, "--model", model, "--repair-model", repair))
			if len(fixes) != 2 {
				t.Fatalf("fix nodes=%d; want 2", len(fixes))
			}
			for i, n := range fixes {
				rej, _ := n["on_reject"].(map[string]any)
				if tc.model == "" && (n["tier"] != "worker" || n["model"] != nil) {
					t.Errorf("fix-%d tier=%v model=%v; want tier worker", i+1, n["tier"], n["model"])
				}
				if tc.model != "" && (n["model"] != tc.model || n["tier"] != nil) {
					t.Errorf("fix-%d tier=%v model=%v; want model %q", i+1, n["tier"], n["model"], tc.model)
				}
				if tc.repair == "" && (rej["tier"] != "synthesist" || rej["model"] != nil) {
					t.Errorf("fix-%d on_reject tier=%v model=%v; want tier synthesist", i+1, rej["tier"], rej["model"])
				}
				if tc.repair != "" && (rej["model"] != tc.repair || rej["tier"] != nil) {
					t.Errorf("fix-%d on_reject tier=%v model=%v; want model %q", i+1, rej["tier"], rej["model"], tc.repair)
				}
			}
		})
	}
}
