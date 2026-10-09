package runner

import (
	"sort"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
)

// A tier's slot names a Claude model, which the OpenAI and Gemini backends
// cannot serve as written, so on those providers a `tier: planner` node is
// sent the alias the models config gives the slot, which each backend
// resolves to its own model of that class. The expected model is
// recomputed from the config and each backend's alias table. A repair's
// tier resolves the same way. A slot the config does not list, as a local
// model a user's tiers map to, is sent as written.
func TestNodeModel_ATierOnAProviderOfAnotherFamily(t *testing.T) {
	cfg := models.Load()
	aliasOf := func(id string) string {
		var names []string
		for a, target := range cfg.Aliases {
			if target == id {
				names = append(names, a)
			}
		}
		sort.Strings(names)
		if len(names) == 0 {
			return ""
		}
		return names[0]
	}
	for _, mode := range []BudgetMode{BudgetPremium, BudgetStandard, BudgetCheap} {
		slot := cfg.ResolveTier("planner", string(mode))
		alias := aliasOf(slot)
		if cfg.Models[slot].Family != "anthropic" || alias == "" {
			t.Fatalf("setup: the planner slot at %s is %q, not a Claude model an alias names", mode, slot)
		}
		for kind, want := range map[BackendKind]string{
			BackendOpenAI:    ResolveOpenAIModel(alias),
			BackendGemini:    ResolveGeminiModel(alias),
			BackendGeminiCLI: ResolveGeminiModel(alias),
			BackendAnthropic: string(ResolveModelAlias(slot)),
			BackendClaudeCLI: slot,
			"":               slot,
		} {
			if got := nodeModel("", "planner", mode, kind); got != want {
				t.Errorf("%s, %q: tier planner sends %q, want %q", mode, kind, got, want)
			}
			block := map[string]any{"tier": "planner"}
			if got := resolveModelForProvider(kind, repairModelFor(block, mode, kind)); kind != "" && got != want {
				t.Errorf("%s, %q: a repair on tier planner sends %q, want %q", mode, kind, got, want)
			}
		}
		if got := nodeModel("", "planner", mode, BackendOpenAI); got == slot {
			t.Errorf("%s: the OpenAI backend is sent the Claude id %q", mode, slot)
		}
	}

	const local = "qwen-local:4b"
	userTiers(t, map[string]models.TierSlots{"planner": {Premium: local, Standard: local, Cheap: local}})
	for _, kind := range []BackendKind{BackendOpenAI, BackendGemini} {
		if got := nodeModel("", "planner", BudgetStandard, kind); got != local {
			t.Errorf("%q: a planner mapped to %q sends %q", kind, local, got)
		}
	}
}
