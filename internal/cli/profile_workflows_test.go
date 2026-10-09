package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	hive "github.com/Chubby-Honey-Bee/hive"
	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
	"github.com/Chubby-Honey-Bee/hive/internal/models"
	"github.com/Chubby-Honey-Bee/hive/internal/review"
	"github.com/Chubby-Honey-Bee/hive/internal/runner"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
	"gopkg.in/yaml.v3"
)

// Every workflow a --profile command runs routes under each shipped
// profile: each workflow the binary carries, whose nodes may name no role,
// and the workflows chb generates for chb ask's swarm, under either persona
// profile, and for chb implement. Every model call goes to a model the
// profile names on provider local, and the routing refuses nothing. The
// routing lists each repair whose on_reject block names a model or a tier of
// its own as "<node> on_reject"; every other repair runs on its node's
// model, so the repairs are held to the same rule.
func TestShippedProfilesRouteTheirWorkflows(t *testing.T) {
	t.Setenv("HIVE_LOCAL_BASE_URL", "")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	texts := generatedWorkflows(t)
	carried, err := fs.Glob(hive.Workflows, "*.yaml")
	if err != nil || len(carried) == 0 {
		t.Fatalf("carried workflows %v (%v)", carried, err)
	}
	for _, f := range carried {
		raw, err := fs.ReadFile(hive.Workflows, f)
		if err != nil {
			t.Fatal(err)
		}
		texts[f] = string(raw)
	}
	named := shippedProfileModels(t)
	if len(named) == 0 {
		t.Fatal("the shipped models config has no profiles")
	}
	for name, names := range named {
		p, err := runner.ResolveProfile(name)
		if err != nil {
			t.Fatal(err)
		}
		for wf, text := range texts {
			t.Run(name+"/"+wf, func(t *testing.T) {
				routed, err := runner.ApplyProfile(text, p)
				if err != nil {
					t.Fatal(err)
				}
				defn, err := workflow.LoadYAMLString(routed)
				if err != nil {
					t.Fatal(err)
				}
				lines := runner.Routing(runner.Config{Provider: string(p.Provider)}, defn)
				if len(lines) == 0 {
					t.Fatal("no model call routed")
				}
				for _, l := range lines {
					if l.Provider != runner.BackendLocal || !names[l.Model] {
						t.Errorf("%s: want a model %s names, on provider local", l, name)
					}
				}
				if err := runner.PreflightLocality(p, lines); err != nil {
					t.Error(err)
				}
				for nodeName, raw := range defn["nodes"].(map[string]any) {
					node := raw.(map[string]any)
					if node["model"] != nil && node["tier"] != nil {
						t.Errorf("node %s keeps tier %v beside its routed model", nodeName, node["tier"])
					}
				}
			})
		}
	}
}

// generatedWorkflows are the workflows chb generates for a --profile
// command: chb ask's swarm, under each persona profile, and chb implement's.
func generatedWorkflows(t *testing.T) map[string]string {
	t.Helper()
	all, err := foragers.Load(filepath.Join("..", "..", "foragers"))
	if err != nil {
		t.Fatal(err)
	}
	balanced, err := foragers.Filter(all, []string{"balanced"})
	if err != nil {
		t.Fatal(err)
	}
	synth, _ := foragers.ByName(all, "queen")
	swarm, err := foragers.GenerateWorkflow(balanced, foragers.WorkflowOptions{Scope: true, Evaluate: true, Synthesizer: synth})
	if err != nil {
		t.Fatal(err)
	}
	lean, err := foragers.GenerateWorkflow(balanced, foragers.WorkflowOptions{Scope: true, Evaluate: true, Synthesizer: synth, PersonaProfile: foragers.ProfileLean})
	if err != nil {
		t.Fatal(err)
	}
	agg := &review.Aggregate{AllFindings: []review.Finding{{Lens: "l", Severity: "critical", File: "a.go", Line: 1, Issue: "i", Fix: "f"}}}
	implement, _, err := review.GenerateImplementWorkflow(agg, review.ImplementOptions{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"swarm": swarm, "swarm-lean": lean, "implement": implement}
}

// shippedProfileModels are the models each profile of the shipped models
// config names, in any of its routes, by profile.
func shippedProfileModels(t *testing.T) map[string]map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "models", "default-models.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Profiles map[string]any `yaml:"profiles"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(models.Load().Profiles) != len(cfg.Profiles) {
		t.Fatalf("the models config in use has %d profiles, the shipped one %d", len(models.Load().Profiles), len(cfg.Profiles))
	}
	out := map[string]map[string]bool{}
	for name, p := range cfg.Profiles {
		out[name] = map[string]bool{}
		collectModels(p, out[name])
	}
	return out
}

// collectModels adds to into the value of every model: key under v.
func collectModels(v any, into map[string]bool) {
	switch v := v.(type) {
	case map[string]any:
		for k, sub := range v {
			if s, ok := sub.(string); ok && k == "model" {
				into[s] = true
			}
			collectModels(sub, into)
		}
	case []any:
		for _, sub := range v {
			collectModels(sub, into)
		}
	}
}
