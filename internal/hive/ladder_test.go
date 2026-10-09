package hive

import (
	"slices"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
)

// The ladder is the models config's under no profile and the profile's under
// HIVE_PROFILE; a new hive starts at its middle rung, an unknown tier is
// stored as that rung, and the upgrade walks the profile's ladder.
func TestModelTiers_FollowTheProfile(t *testing.T) {
	cfg := models.Load()
	for _, profile := range append([]string{""}, profileNames(cfg)...) {
		t.Run("profile="+profile, func(t *testing.T) {
			t.Setenv("HIVE_PROFILE", profile)
			want := cfg.HiveLadder(profile)
			if got := ModelTiers(); !slices.Equal(got, want) {
				t.Fatalf("ModelTiers() = %v, want %v", got, want)
			}
			mid := want[(len(want)-1)/2]

			s := newTestStore(t)
			if _, _, err := InitProject(s, "p"); err != nil {
				t.Fatal(err)
			}
			if _, _, tier := storedParams(t, s); tier != mid {
				t.Errorf("a new hive's model_tier %q, want the middle rung %q", tier, mid)
			}
			if err := applyGainControl(s.WriteDB, "p", map[string]any{"model_tier": "no-such-tier"}); err != nil {
				t.Fatal(err)
			}
			if _, _, tier := storedParams(t, s); tier != mid {
				t.Errorf("an unknown model_tier was stored as %q, want %q", tier, mid)
			}

			c := newCtx(&State{Hive: HiveState{BatchSize: 5, ModelTier: mid}})
			stepShakingSignal(c, []Signal{{SignalType: "shaking_signal", Payload: map[string]any{"adjust": "upgrade_model_tier"}}})
			i := slices.Index(want, mid)
			switch {
			case i == len(want)-1 && len(c.actions) != 0:
				t.Errorf("at the top rung %q the plan still upgraded: %+v", mid, c.actions)
			case i < len(want)-1 && (len(c.actions) != 1 || c.actions[0].Params["model_tier"] != want[i+1]):
				t.Errorf("from %q the plan emitted %+v, want an upgrade to %q", mid, c.actions, want[i+1])
			}
		})
	}
}

func profileNames(cfg *models.Config) []string {
	var out []string
	for n := range cfg.Profiles {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}
