package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
	"gopkg.in/yaml.v3"
)

// TestParseBudgetMode_Cases asserts every supported mode parses,
// case-insensitively, empty is standard, and a name that is no mode is
// refused rather than quietly run as standard.
func TestParseBudgetMode_Cases(t *testing.T) {
	cases := []struct {
		in   string
		want BudgetMode
	}{
		{"premium", BudgetPremium},
		{"PREMIUM", BudgetPremium},
		{"  Premium  ", BudgetPremium},
		{"standard", BudgetStandard},
		{"", BudgetStandard},
		{"cheap", BudgetCheap},
		{"CHEAP", BudgetCheap},
		{"free", BudgetFree},
		{"frEE", BudgetFree},
	}
	for _, c := range cases {
		got, err := ParseBudgetMode(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseBudgetMode(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"unknown-mode", "opus", "premuim"} {
		if _, err := ParseBudgetMode(bad); err == nil {
			t.Errorf("ParseBudgetMode(%q) accepted a name that is no mode", bad)
		}
	}
}

// ResolveBudgetMode takes the --budget-mode value, else
// HIVE_BUDGET_MODE, else standard, and refuses a name that is no mode
// wherever it came from.
func TestResolveBudgetMode_FlagThenEnvironment(t *testing.T) {
	cases := []struct {
		flag, env string
		want      BudgetMode
	}{
		{"cheap", "premium", BudgetCheap},
		{"", "premium", BudgetPremium},
		{"", "", BudgetStandard},
		{"FREE", "", BudgetFree},
	}
	for _, c := range cases {
		t.Setenv("HIVE_BUDGET_MODE", c.env)
		if got, err := ResolveBudgetMode(c.flag); err != nil || got != c.want {
			t.Errorf("ResolveBudgetMode(%q) with HIVE_BUDGET_MODE=%q = %q, %v; want %q", c.flag, c.env, got, err, c.want)
		}
	}
	for _, c := range []struct{ flag, env string }{{"premuim", ""}, {"", "premuim"}, {"premuim", "cheap"}} {
		t.Setenv("HIVE_BUDGET_MODE", c.env)
		if _, err := ResolveBudgetMode(c.flag); err == nil {
			t.Errorf("ResolveBudgetMode(%q) with HIVE_BUDGET_MODE=%q accepted a name that is no mode", c.flag, c.env)
		}
	}
}

// A run's tier node is sent the model `chb models tiers` shows for its role
// and the run's budget mode, for every role and mode, with a user's models
// config that replaces one role and adds another. `chb models tiers` prints
// models.Load().ResolveTier for each role the config holds.
func TestRun_TierNodeSendsWhatModelsTiersShows(t *testing.T) {
	slots := func(prefix string) models.TierSlots {
		return models.TierSlots{Premium: prefix + "-premium", Standard: prefix + "-standard", Cheap: prefix + "-cheap", Free: prefix + "-free"}
	}
	userTiers(t, map[string]models.TierSlots{"planner": slots("override"), "probe": slots("probe")})
	cfg := models.Load()
	for _, role := range []string{"planner", "synthesist", "worker", "verifier", "probe"} {
		for _, mode := range []BudgetMode{BudgetPremium, BudgetStandard, BudgetCheap, BudgetFree} {
			if got, want := runTierNode(t, role, mode), cfg.ResolveTier(role, string(mode)); got != want {
				t.Errorf("tier %s at %s: the run's node was sent %q; chb models tiers shows %q", role, mode, got, want)
			}
		}
	}
}

// userTiers points HIVE_MODELS_PATH at a models config whose tiers:
// section holds tiers, each replacing the shipped tier of its name whole or
// added beside them.
func userTiers(t *testing.T, tiers map[string]models.TierSlots) {
	t.Helper()
	b, err := yaml.Marshal(map[string]any{"tiers": tiers})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HIVE_MODELS_PATH", path)
}

// runTierNode runs a workflow of one agent node naming tier role, under
// budget mode, on a backend that records the model it is sent, and returns
// that model.
func runTierNode(t *testing.T, role string, mode BudgetMode) string {
	t.Helper()
	dir := t.TempDir()
	wf := filepath.Join(dir, "tier.yaml")
	if err := os.WriteFile(wf, []byte("name: tier\nnodes:\n  ask:\n    type: agent\n    tier: "+role+"\n    prompt: p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	backend := &recordingBackend{}
	store := newTempStore(t)
	if _, err := Run(context.Background(), store, Config{WorkflowYAML: wf, ProjectDir: dir, DBPath: store.Path, MaxIterations: 5, Backend: backend, BudgetMode: mode, Log: io.Discard}); err != nil {
		t.Fatal(err)
	}
	return backend.req.Model
}
