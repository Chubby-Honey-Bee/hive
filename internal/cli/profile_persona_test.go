package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/runner"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
	"github.com/spf13/cobra"
)

// chb ask takes a routing profile and a persona profile together: the
// workflow renders the lean personas, Queen reads the swarm ledger, every
// role carries its role: for the profile to route, and the printed
// commands carry the routing profile.
func TestAsk_RoutingProfileWithLeanPersonas(t *testing.T) {
	foragersAbs, err := filepath.Abs("../../foragers")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HIVE_FORAGERS_DIR", foragersAbs)
	t.Setenv("HIVE_PROFILE", "")
	const profile = "local-8gb"
	out := filepath.Join(t.TempDir(), "s.yaml")
	root := &cobra.Command{Use: "chb", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(newSwarmAskCmd())
	var stderr bytes.Buffer
	root.SetErr(&stderr)
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"ask", "q", "--no-dispatch", "--out", out, "--profile", profile, "--persona-profile", "lean", "--persona-sections", "1,2,4,5,7"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(stderr.String(), shellJoin([]string{"--profile", profile})); n != 2 {
		t.Errorf("%d printed commands carry the profile, want the preflight and the agent-run:\n%s", n, stderr.String())
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	defn, err := workflow.LoadYAMLString(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	nodes := defn["nodes"].(map[string]any)
	queen, _ := nodes["queen"].(map[string]any)
	if prompt, _ := queen["prompt"].(string); !strings.Contains(prompt, "{swarm.ledger}") {
		t.Errorf("Queen's prompt does not read the swarm ledger, so the persona profile was not lean")
	}
	p, err := runner.ResolveProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	routed, err := runner.ApplyProfile(string(raw), p)
	if err != nil {
		t.Fatal(err)
	}
	routedDefn, err := workflow.LoadYAMLString(routed)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range runner.Routing(runner.Config{Provider: string(p.Provider)}, routedDefn) {
		if r, ok := p.Routes[l.Role]; !ok || l.Model != r.Model || l.Provider != r.Provider {
			t.Errorf("%s: want the %s route's model on its provider", l, l.Role)
		}
	}
}
