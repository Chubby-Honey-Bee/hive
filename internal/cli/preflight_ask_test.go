package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// `chb ask --no-dispatch` prints a `chb preflight` command for the workflow
// it wrote. The workflow checks in that preflight must pass for every shape
// ask generates, the default coverage pass included. The machine checks
// (provider auth, PATH, target dir) are left out: they judge the host, not
// the YAML.
func TestAsk_GeneratedWorkflowPassesPreflightWorkflowChecks(t *testing.T) {
	t.Setenv("HIVE_FORAGERS_DIR", filepath.Join("..", "..", "foragers"))
	shapes := [][]string{
		{},
		{"--foragers", "all"},
		{"--foragers", "minimal", "--human"},
		{"--scope"},
		{"--no-eval"},
		{"--persona-profile", "lean"},
		{"--persona-profile", "lean", "--foragers", "all", "--scope"},
		{"--persona-profile", "lean", "--persona-sections", "1,2,3,4,5,7"},
	}
	for _, flags := range shapes {
		t.Run(strings.Join(append([]string{"ask"}, flags...), " "), func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "ask.yaml")
			ask := newSwarmAskCmd()
			ask.SetArgs(append([]string{"Should we adopt X?", "--no-dispatch", "--out", out}, flags...))
			var stderr bytes.Buffer
			ask.SetOut(&stderr)
			ask.SetErr(&stderr)
			if err := ask.Execute(); err != nil {
				t.Fatalf("ask: %v\n%s", err, stderr.String())
			}

			r := newPreflightReport()
			r.checkYAML(out)
			defn := r.loadDefinition(out)
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
