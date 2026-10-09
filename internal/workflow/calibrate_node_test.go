package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A calibrate node that validates can never auto-complete: an unknown key,
// a rebuild that is not a boolean, a scope that is not a string or an
// output it does not write is refused, by Validate and by every way of
// starting a run.
func TestValidate_CalibrateNode(t *testing.T) {
	cases := []struct {
		name, node string
		want       []string
	}{
		{"bare", `{type: calibrate}`, nil},
		{"every key", `{type: calibrate, rebuild: true, scope: "d1=2", outputs: [calibration_drift_count, lowest_calibrated_lens], accept: ["outputs.calibration_drift_count == 0"], state_updates: {drift: "{calibration_drift_count}"}}`, nil},
		{"global scope", `{type: calibrate, scope: ""}`, nil},
		{"prompt and model", `{type: calibrate, prompt: "score", model: sonnet}`, []string{`Calibrate node "a": unknown key "model"`, `unknown key "prompt"`}},
		{"argv", `{type: calibrate, argv: [chb, calibrate]}`, []string{`unknown key "argv"`}},
		{"on_reject", `{type: calibrate, on_reject: {max_repair_iterations: 1}}`, []string{`unknown key "on_reject"`}},
		{"tools", `{type: calibrate, tools: []}`, []string{`unknown key "tools"`}},
		{"rebuild as a string", `{type: calibrate, rebuild: "yes"}`, []string{`'rebuild' must be true or false`}},
		{"scope as a number", `{type: calibrate, scope: 2}`, []string{`'scope' must be a string`}},
		{"foreign output", `{type: calibrate, outputs: [calibration_drift_count, verdict]}`, []string{`output verdict is not one it writes`}},
		{"outputs not a list", `{type: calibrate, outputs: calibration_drift_count}`, []string{`'outputs' must be a list`}},
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
			_, initErr := InitWorkflow(newWFStore(t).Workflows(), path, nil)
			if (initErr != nil) != (len(c.want) > 0) {
				t.Fatalf("InitWorkflow = %v, want an error iff Validate reports one", initErr)
			}
		})
	}
}

// GetNextNodes hands a calibrate node out with its rebuild and scope, no
// model and no prompt; a scope given as "" is set and global, an absent
// one is unset.
func TestGetNextNodes_CalibrateNodeFields(t *testing.T) {
	cases := []struct {
		name, node string
		rebuild    bool
		scope      string
		scopeSet   bool
	}{
		{"bare", `{type: calibrate}`, false, "", false},
		{"rebuild and scope", `{type: calibrate, rebuild: true, scope: "d1=2"}`, true, "d1=2", true},
		{"global scope", `{type: calibrate, scope: ""}`, false, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wf.yaml")
			if err := os.WriteFile(path, []byte("name: t\nnodes:\n  cal: "+c.node+"\nedges: []\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			repo := newWFStore(t).Workflows()
			runID, err := InitWorkflow(repo, path, nil)
			if err != nil {
				t.Fatal(err)
			}
			nodes, err := GetNextNodes(repo, runID)
			if err != nil {
				t.Fatal(err)
			}
			if len(nodes) != 1 {
				t.Fatalf("nodes = %+v, want the calibrate node", nodes)
			}
			n := nodes[0]
			if n.Type != "calibrate" || n.Rebuild != c.rebuild || n.Scope != c.scope || n.ScopeSet != c.scopeSet || n.Model != "" || n.ResolvedPrompt != "" {
				t.Fatalf("node = %+v, want rebuild %v scope %q set %v, no model, no prompt", n, c.rebuild, c.scope, c.scopeSet)
			}
		})
	}
}
