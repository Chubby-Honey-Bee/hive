package runner

import (
	"cmp"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
)

// BudgetMode picks the slot of its tier a `tier:` node is sent, from the
// models config (models.Config.ResolveTier), a four-position cost dial:
//
//   - "premium"  → the premium slot (top quality, costs the most)
//   - "standard" → the standard slot, the middle gear (sonnet plans, haiku works)
//   - "cheap"    → the cheap slot (haiku for every role, per default-models.yaml)
//   - "free"     → the free slot (zero-multiplier on Copilot; GPT-5.x)
//
// Empty string is treated as "standard". Override via the --budget-mode
// flag on agent-run / chb ask / review / implement, or via
// HIVE_BUDGET_MODE in the environment.
type BudgetMode string

const (
	// BudgetStandard sends each tier's standard slot, the middle gear.
	BudgetStandard BudgetMode = "standard"
	// BudgetPremium sends each tier's premium slot, the top-quality default.
	BudgetPremium BudgetMode = "premium"
	// BudgetCheap sends each tier's cheap slot, the aggressive downshift.
	BudgetCheap BudgetMode = "cheap"
	// BudgetFree sends each tier's free slot, the zero-cost path.
	BudgetFree BudgetMode = "free"
)

// tierModel is what tier sends to the provider kind under mode: the slot the
// models config resolves it to (models.Config.ResolveTier), which `chb models
// tiers` shows. The shipped tiers name Claude models, which the OpenAI and
// Gemini backends cannot serve and pass through as written. So for those
// backends a slot naming a model the models config lists in the anthropic
// family becomes the alias that names it there (haiku, sonnet or opus),
// which the backend resolves to its own model of that class, as it does for
// a node's `model: sonnet`. A slot of any other family, or not listed, or a
// Claude model no alias names, is sent as written; a slot mapped to a local
// model is one of those.
func tierModel(kind BackendKind, tier string, mode BudgetMode) string {
	cfg := models.Load()
	slot := cfg.ResolveTier(tier, string(mode))
	if !translatesClaudeSlots(kind) {
		return slot
	}
	if alias, ok := claudeAlias(cfg, slot); ok {
		return alias
	}
	return slot
}

// translatesClaudeSlots reports whether kind's backend cannot serve a
// Claude model and is sent the alias that names one in its place.
func translatesClaudeSlots(kind BackendKind) bool {
	return slices.Contains([]BackendKind{BackendOpenAI, BackendLocal, BackendGemini, BackendGeminiCLI}, kind)
}

// claudeAlias is the alias, the first in sorted order, that names slot in
// cfg, a models config that lists slot in the anthropic family; ok is false
// when cfg does not, or no alias names it.
func claudeAlias(cfg *models.Config, slot string) (string, bool) {
	if m, ok := cfg.Models[slot]; !ok || m.Family != "anthropic" {
		return "", false
	}
	return firstAliasFor(cfg, slot)
}

// firstAliasFor is the first, in sorted order, of the aliases cfg maps to
// model; ok is false when none does.
func firstAliasFor(cfg *models.Config, model string) (string, bool) {
	var aliases []string
	for a, target := range cfg.Aliases {
		if target == model {
			aliases = append(aliases, a)
		}
	}
	if len(aliases) == 0 {
		return "", false
	}
	return slices.Min(aliases), true
}

// ResolveBudgetMode is the budget mode a run takes: flag, a --budget-mode
// value, else HIVE_BUDGET_MODE, read by ParseBudgetMode, so empty is
// standard and a name that is no mode is an error.
func ResolveBudgetMode(flag string) (BudgetMode, error) {
	return ParseBudgetMode(cmp.Or(flag, os.Getenv("HIVE_BUDGET_MODE")))
}

// ParseBudgetMode resolves a budget mode name, case-insensitively; empty is
// standard. A name that is no mode is an error, so a typo such as "premuim"
// never runs a whole swarm on the wrong models without a word.
func ParseBudgetMode(s string) (BudgetMode, error) {
	switch m := BudgetMode(strings.ToLower(strings.TrimSpace(s))); m {
	case "":
		return BudgetStandard, nil
	case BudgetPremium, BudgetStandard, BudgetCheap, BudgetFree:
		return m, nil
	}
	return "", fmt.Errorf("unknown budget mode %q (want premium|standard|cheap|free)", s)
}
