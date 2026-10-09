package runner

import (
	"os"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
)

// Pricing tables live in internal/models/default-models.yaml (with an
// optional user override at ~/.config/hive/models.yaml). The helpers
// below are thin wrappers that read the unified config, so pricing, tier
// slots and aliases stay in lockstep and adding a model is one edit in
// models.yaml.

// PricingLastUpdated is the date the embedded defaults were verified.
// `chb preflight` warns when this is more than 60 days behind today.
// Reads from the active config so user overrides can extend the
// freshness window.
func PricingLastUpdated() string {
	return models.Load().LastUpdated
}

// resolveAliasForPricing maps a model name, such as a bare alias in a
// workflow YAML, to the canonical model id the models config gives it, for
// cost lookup, even when the dispatcher hasn't canonicalised it (preflight,
// dry-run, speculative cost preview).
func resolveAliasForPricing(alias string) string {
	if alias == "" {
		return ""
	}
	return models.Load().Resolve(alias)
}

// isCopilotBilling reports whether the user has opted into Copilot
// premium-request accounting. Direct API users (Anthropic, Gemini,
// OpenAI) and CLI-backend users (claude-cli, gemini-cli) bill in
// dollars; only operators routing through Copilot pay in credits, so
// the credit meter must stay dark for everyone else.
//
// Opt-in via HIVE_BILLING=copilot in the environment.
func isCopilotBilling() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("HIVE_BILLING")), "copilot")
}

// copilotMultiplier returns the credit multiplier for a model under
// Copilot billing. Returns 0.0 when the user has not opted into
// Copilot billing (HIVE_BILLING != "copilot") so the credit meter
// stays dark for direct-API and CLI-backend users.
//
// Unknown models under Copilot billing resolve to 1.0× (Copilot's
// default for unrecognized requests). Routes through the unified
// models config so updates only need to land in one place.
func copilotMultiplier(model string) float64 {
	if !isCopilotBilling() {
		return 0.0
	}
	return models.Load().CopilotMultiplier(model)
}

// ComputeCostUSDx10000 returns the cost of an LLM call in 1/10000 USD
// (10000 = $1.00). Returns 0 on lookup miss — callers should treat zero
// as "unknown" rather than "free".
//
// Cost is priced from whatever usage a backend reports, and claude-cli does
// report usage, so its calls are priced like any other. Zero means the
// backend reported no usage (a gemini CLI without --output-format json) or
// the model has no pricing entry — not "this was a CLI backend".
//
// Routes through the unified models config so adding a model is a
// single-line YAML edit. Aliases ("sonnet", "opus", "haiku") and
// "provider:model" syntax both resolve via the config's Resolve()
// helper.
//
// Units: PriceInPer1MTokensX10000 returns 1/10000-USD per 1M tokens
// (so opus's $5 → 50_000). The final /1_000_000 yields cost in
// 1/10000-USD per actual token count.
func ComputeCostUSDx10000(model string, inTokens, outTokens int64) int64 {
	cost, _, _ := usageCostUSDx10000([]ModelUsage{{Model: model, InputTokens: inTokens, OutputTokens: outTokens}})
	return cost
}

// usageCostUSDx10000 prices a call's tokens model by model, in 1/10000 USD:
// uncached input at the input price, cached input at the cached price (the
// input price when the models config gives none), input written to the
// prompt cache at the cache-write price for its lifetime (5 minutes, or 1
// hour), and output, thought tokens included, at the output price. priced
// is false when a model has no models-config entry, and unpriced names the
// first such model; the call's cost is then unknown, and cost counts only
// the priced models.
func usageCostUSDx10000(usage []ModelUsage) (cost int64, priced bool, unpriced string) {
	cfg := models.Load()
	var sum int64
	priced = true
	for _, u := range usage {
		if priced && !isMetered(u.Model) {
			priced, unpriced = false, u.Model
		}
		cached := min(u.CachedInputTokens, u.InputTokens)
		written := min(u.CacheWriteTokens, u.InputTokens-cached)
		written1h := min(u.CacheWrite1hTokens, written)
		sum += (u.InputTokens-cached-written)*cfg.PriceInPer1MTokensX10000(u.Model) +
			cached*cfg.PriceCachedInPer1MTokensX10000(u.Model) +
			(written-written1h)*cfg.PriceCacheWritePer1MTokensX10000(u.Model) +
			written1h*cfg.PriceCacheWrite1hPer1MTokensX10000(u.Model) +
			u.OutputTokens*cfg.PriceOutPer1MTokensX10000(u.Model)
	}
	return sum / 1_000_000, priced, unpriced
}

// isMetered reports whether a model has an entry in the models config, so its
// calls can be priced. A call to a model with no entry costs "0" only
// because nothing knows its price: it is unmetered, not free, unless it ran
// on this machine (costKnown). An entry priced at 0 is metered at $0.
func isMetered(model string) bool {
	cfg := models.Load()
	_, ok := cfg.Models[cfg.Resolve(model)]
	return ok
}

// costKnown reports whether a call's cost is known: every model it used has
// a price, or the one that has none, unpriced, ran on this machine on
// provider, at baseURL (onThisMachine), where a call costs nothing.
func costKnown(priced bool, unpriced, provider, baseURL string) bool {
	return priced || onThisMachine(canonicalKind(provider), baseURL, unpriced)
}

// onThisMachine reports whether a call to model on provider kind, sent to
// baseURL, is served on this machine: provider local at a loopback endpoint,
// and not an Ollama cloud model, which Ollama forwards to ollama.com
// (offMachine). That such a call costs nothing is a definition: the meter
// counts what a provider bills per token, and a server on this machine bills
// none; its power and wear are not counted.
func onThisMachine(kind BackendKind, baseURL, model string) bool {
	return kind == BackendLocal && offMachine(kind, baseURL, model) == ""
}

// formatCost renders a run's cost for its log lines (models.CostLabel):
// dollars when every call was metered, "unmetered" when none was, and both
// when the run mixed them.
func formatCost(costX10000 int64, metered, unmetered int) string {
	return models.CostLabel("$"+formatUSDx10000(costX10000), int64(metered), int64(unmetered))
}

// providerLabel is a small shim for the dispatcher to label cost rows
// with the active provider. Maps the canonical BackendKind to the same
// string the user typed in --provider / HIVE_PROVIDER, so a node's row
// and the artifact name the provider as the operator named it.
func providerLabel(cfg Config) string {
	return providerLabels[ResolveBackendKind(cfg)]
}

// providerLabels are the labels providerLabel gives each kind; any other
// kind has none.
var providerLabels = map[BackendKind]string{
	BackendAnthropic: "anthropic",
	BackendOpenAI:    "openai",
	BackendGemini:    "gemini",
	BackendClaudeCLI: "claude-cli",
	BackendGeminiCLI: "gemini-cli",
	BackendLocal:     "local",
}
