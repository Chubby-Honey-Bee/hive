// Package models holds the unified pricing + tier + alias config that
// drives the runner's cost meter and the workflow engine's tier
// resolution. The whole table ships embedded (default-models.yaml) so
// the binary is self-contained; users override any subset by writing
// to ~/.config/hive/models.yaml (or $XDG_CONFIG_HOME/hive/
// models.yaml, or $HIVE_MODELS_PATH).
//
// Pricing, tier slots and aliases share one file because they describe
// the same set of models: adding a model is one edit, a user overrides
// any of them with one YAML file, and the runner's pricing.go and
// tiers.go read the same loaded Config.
package models

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Model captures everything the cost meter + Copilot multiplier need
// for one model id. Fields are USD per million tokens (what every
// provider's pricing page publishes).
type Model struct {
	Family            string  `yaml:"family"`
	InputPerMTokUSD   float64 `yaml:"input_per_mtok_usd"`
	OutputPerMTokUSD  float64 `yaml:"output_per_mtok_usd"`
	CopilotMultiplier float64 `yaml:"copilot_multiplier"`
	// CachedInputPerMTokUSD is the price of an input token the provider
	// read from its prompt cache. Nil means the config gives none, and a
	// cached token is priced at InputPerMTokUSD.
	CachedInputPerMTokUSD *float64 `yaml:"cached_input_per_mtok_usd"`
	// CacheWritePerMTokUSD is the price of an input token the provider
	// wrote to its prompt cache, at its default (5-minute) lifetime. Nil
	// means the config gives none, and such a token is priced at
	// InputPerMTokUSD.
	CacheWritePerMTokUSD *float64 `yaml:"cache_write_per_mtok_usd"`
	// CacheWrite1hPerMTokUSD is the price of an input token written to the
	// prompt cache with a 1-hour lifetime. Nil means the config gives none,
	// and such a token is priced as a 5-minute write.
	CacheWrite1hPerMTokUSD *float64 `yaml:"cache_write_1h_per_mtok_usd"`
	// MaxOutputTokens is the per-turn output cap the dispatcher applies when
	// issuing backend.Run. Zero means the 8192 default — see MaxOutputFor.
	// Each model's value should match the provider's published max output
	// for that model id.
	MaxOutputTokens int64 `yaml:"max_output_tokens"`
	// ContextWindow is the model's context window in tokens, prompt and
	// reply together, as the server that runs it is set up. Zero means not
	// declared. The OpenAI-compatible backend refuses a call whose prompt
	// would not fit it (runner.md § Context window guard).
	ContextWindow int64 `yaml:"context_window"`
}

// defaultMaxOutputTokens is the per-turn output cap applied when a
// model has no max_output_tokens entry. Matches the historical
// hardcoded value in internal/runner/claude.go so existing fixtures
// and behavior stay byte-identical for unconfigured models.
const defaultMaxOutputTokens int64 = 8192

// TierSlots is the per-budget-mode model selection for one role. An empty
// slot degrades as ResolveTier says. Fallback is the model a persistently
// rate-limited premium model steps down to before Free; an empty one is
// skipped.
type TierSlots struct {
	Premium  string `yaml:"premium"`
	Standard string `yaml:"standard"`
	Cheap    string `yaml:"cheap"`
	Fallback string `yaml:"fallback"`
	Free     string `yaml:"free"`
}

// Config is the parsed YAML config.
type Config struct {
	LastUpdated string               `yaml:"last_updated"`
	Models      map[string]Model     `yaml:"models"`
	Tiers       map[string]TierSlots `yaml:"tiers"`
	Aliases     map[string]string    `yaml:"aliases"`
	// HiveTiers is the hive's model-tier ladder, cheapest first: the tiers
	// a shaking signal moves hive_state.model_tier through (HiveLadder).
	HiveTiers []string `yaml:"hive_tiers"`
	// Profiles are the named routing profiles (Profile).
	Profiles map[string]Profile `yaml:"profiles"`
}

// Roles are the roles a routing profile maps and a workflow node names with
// `role:`. RepairRoles are the ones an `on_reject:` block names instead.
var (
	Roles       = []string{"scope", "lens", "evaluate", "followup", "queen", "hive-research", "hive-synthesis", "review-lens", "review-synthesis", "implement-plan", "implement-fix", "implement-repair"}
	RepairRoles = []string{"implement-repair"}
)

// Route is what a profile sends one role: the model, the provider that
// serves it, the reasoning level, the tools and the per-call TTL. A field
// left empty keeps what the node declares; the model is required. Because
// records the evidence for a route that leaves this machine.
type Route struct {
	Model     string    `yaml:"model"`
	Provider  string    `yaml:"provider"`
	Reasoning string    `yaml:"reasoning"`
	Tools     *[]string `yaml:"tools"`
	TTL       string    `yaml:"ttl"`
	Because   string    `yaml:"because"`
	// Unknown are the keys the route sets that are none of RouteKeys, in
	// the order written. Decoding keeps them so a misspelled key is
	// refused (runner.ResolveProfile), not dropped.
	Unknown []string `yaml:"-"`
}

// ReasoningLevels are the reasoning levels a route, a workflow node's
// `reasoning:` and the flags that set one may name.
var ReasoningLevels = []string{"none", "low", "medium", "high"}

// CheckReasoningLevel refuses level unless it is one of ReasoningLevels.
func CheckReasoningLevel(level string) error {
	if slices.Contains(ReasoningLevels, level) {
		return nil
	}
	return fmt.Errorf("reasoning %q is not one of %s", level, strings.Join(ReasoningLevels, "|"))
}

// RouteKeys and ProfileKeys are the keys a route and a profile may set.
var (
	RouteKeys   = []string{"model", "provider", "reasoning", "tools", "ttl", "because"}
	ProfileKeys = []string{"description", "quality", "provider", "hive_tiers", "default", "roles"}
)

// UnmarshalYAML decodes a route and records its unknown keys.
func (r *Route) UnmarshalYAML(n *yaml.Node) error {
	type plain Route
	if err := n.Decode((*plain)(r)); err != nil {
		return err
	}
	r.Unknown = unknownKeys(n, RouteKeys)
	return nil
}

// Profile is a named routing, one route per role. Provider is the provider
// of a route that names none, and the run default under the profile.
// Quality says how far the profile has been measured. HiveTiers, when set,
// is the hive's ladder under the profile (HiveLadder). Default, when set, is
// the route of a model node that no role route serves and that names no
// model and no provider of its own.
type Profile struct {
	Description string           `yaml:"description"`
	Quality     string           `yaml:"quality"`
	Provider    string           `yaml:"provider"`
	HiveTiers   []string         `yaml:"hive_tiers"`
	Default     *Route           `yaml:"default"`
	Roles       map[string]Route `yaml:"roles"`
	// Unknown are the keys the profile sets that are none of ProfileKeys,
	// as Route.Unknown.
	Unknown []string `yaml:"-"`
}

// UnmarshalYAML decodes a profile and records its unknown keys.
func (p *Profile) UnmarshalYAML(n *yaml.Node) error {
	type plain Profile
	if err := n.Decode((*plain)(p)); err != nil {
		return err
	}
	p.Unknown = unknownKeys(n, ProfileKeys)
	return nil
}

// unknownKeys are the keys of mapping n that are not in known, in order. A
// merge key (<<) is not one: what it merges is decoded where it is written.
func unknownKeys(n *yaml.Node, known []string) []string {
	var out []string
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if k := n.Content[i]; isUnknownKey(k, known) {
			out = append(out, k.Value)
		}
	}
	return out
}

// isUnknownKey reports whether mapping key k is a key, not a merge key, and
// none of known.
func isUnknownKey(k *yaml.Node, known []string) bool {
	return k.ShortTag() != "!!merge" && !slices.Contains(known, k.Value)
}

// Profile returns the profile named name, or an error naming the profiles
// the config holds.
func (c *Config) Profile(name string) (Profile, error) {
	var names []string
	if c != nil {
		if p, ok := c.Profiles[name]; ok {
			return p, nil
		}
		for n := range c.Profiles {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	return Profile{}, fmt.Errorf("no routing profile %q in the models config (it has %s); a profile of your own goes under profiles: in %s",
		name, strings.Join(names, ", "), UserConfigPath())
}

// defaultHiveTiers is the ladder when the config names none.
var defaultHiveTiers = []string{"haiku", "sonnet", "opus"}

// HiveLadder is the hive's model-tier ladder under the named profile: the
// profile's hive_tiers, else its hive-research model alone, else its default
// route's model alone, so the hive never moves onto a model the profile does
// not route. With no profile, or a name the config does not hold, it is the
// config's hive_tiers, else haiku, sonnet, opus.
func (c *Config) HiveLadder(profile string) []string {
	if c == nil {
		return defaultHiveTiers
	}
	if ladder := c.profileLadder(profile); len(ladder) > 0 {
		return ladder
	}
	if len(c.HiveTiers) > 0 {
		return c.HiveTiers
	}
	return defaultHiveTiers
}

// profileLadder is the ladder of the profile named profile; none when the
// config holds no such profile.
func (c *Config) profileLadder(profile string) []string {
	p, ok := c.Profiles[profile]
	if !ok || profile == "" {
		return nil
	}
	return p.ladder()
}

// ladder is the profile's hive_tiers, else the model its hive-research node
// runs on (researchModel) alone; none when it sets neither.
func (p Profile) ladder() []string {
	if len(p.HiveTiers) > 0 {
		return p.HiveTiers
	}
	if m := p.researchModel(); m != "" {
		return []string{m}
	}
	return nil
}

// researchModel is the model of the profile's hive-research route, else of
// its default route, which serves a hive-research node no role route serves;
// "" when it sets neither.
func (p Profile) researchModel() string {
	if r := p.Roles["hive-research"]; r.Model != "" {
		return r.Model
	}
	if p.Default != nil {
		return p.Default.Model
	}
	return ""
}

// cloudFamilies are the families of the models the cloud providers serve.
var cloudFamilies = []string{"anthropic", "openai", "google"}

// OffMachineModel reports whether model is served off this machine,
// whatever route names it: an Ollama cloud model (OllamaCloudModel), or a
// model the config lists, by id or alias, in a cloud provider's family
// (anthropic, openai, google). That every model listed in those families is
// served by that provider's cloud is a bet on how the config is written.
func (c *Config) OffMachineModel(model string) bool {
	if OllamaCloudModel(model) {
		return true
	}
	if c == nil {
		return false
	}
	m, ok := c.Models[c.Resolve(strings.TrimSpace(model))]
	return ok && slices.Contains(cloudFamilies, strings.ToLower(strings.TrimSpace(m.Family)))
}

// OllamaCloudModel reports whether model names an Ollama cloud model, which
// a local Ollama forwards to ollama.com: a tag of `cloud` or one ending in
// `-cloud`, such as gpt-oss:120b-cloud. That every cloud model is tagged so
// is a bet on Ollama's naming.
func OllamaCloudModel(model string) bool {
	i := strings.LastIndex(model, ":")
	if i < 0 {
		return false
	}
	tag := strings.ToLower(strings.TrimSpace(model[i+1:]))
	return tag == "cloud" || strings.HasSuffix(tag, "-cloud")
}

// Validate checks the config for missing required fields. Returns the
// first error found.
func (c *Config) Validate() error {
	if len(c.Models) == 0 {
		return fmt.Errorf("models: empty (config has no model entries)")
	}
	if len(c.Tiers) == 0 {
		return fmt.Errorf("tiers: empty (config has no tier roles)")
	}
	return c.validateTiers()
}

// validateTiers refuses a tier role with no premium slot.
func (c *Config) validateTiers() error {
	for role, t := range c.Tiers {
		if t.Premium == "" {
			return fmt.Errorf("tiers.%s.premium: empty (every tier must have at least a premium slot)", role)
		}
	}
	return nil
}

// PriceInPer1MTokensX10000 returns the input price in 1/10000-USD per
// 1M tokens (so the cost meter can stay integer-clean). Returns 0 on
// lookup miss.
func (c *Config) PriceInPer1MTokensX10000(model string) int64 {
	if c == nil {
		return 0
	}
	if m, ok := c.Models[c.Resolve(model)]; ok {
		return int64(m.InputPerMTokUSD * 10_000)
	}
	return 0
}

// PriceOutPer1MTokensX10000 returns the output price in 1/10000-USD per
// 1M tokens. Returns 0 on lookup miss.
func (c *Config) PriceOutPer1MTokensX10000(model string) int64 {
	if c == nil {
		return 0
	}
	if m, ok := c.Models[c.Resolve(model)]; ok {
		return int64(m.OutputPerMTokUSD * 10_000)
	}
	return 0
}

// PriceCachedInPer1MTokensX10000 returns the price of a cached input token
// in 1/10000-USD per 1M tokens: the model's cached_input_per_mtok_usd, else
// its input price. Returns 0 on lookup miss.
func (c *Config) PriceCachedInPer1MTokensX10000(model string) int64 {
	if c == nil {
		return 0
	}
	m, ok := c.Models[c.Resolve(model)]
	if !ok {
		return 0
	}
	if m.CachedInputPerMTokUSD != nil {
		return int64(*m.CachedInputPerMTokUSD * 10_000)
	}
	return int64(m.InputPerMTokUSD * 10_000)
}

// PriceCacheWritePer1MTokensX10000 returns the price of an input token
// written to the prompt cache at its default (5-minute) lifetime, in
// 1/10000-USD per 1M tokens: the model's cache_write_per_mtok_usd, else its
// input price. Returns 0 on lookup miss.
func (c *Config) PriceCacheWritePer1MTokensX10000(model string) int64 {
	if c == nil {
		return 0
	}
	m, ok := c.Models[c.Resolve(model)]
	if !ok {
		return 0
	}
	if m.CacheWritePerMTokUSD != nil {
		return int64(*m.CacheWritePerMTokUSD * 10_000)
	}
	return int64(m.InputPerMTokUSD * 10_000)
}

// PriceCacheWrite1hPer1MTokensX10000 returns the price of an input token
// written to the prompt cache with a 1-hour lifetime, in 1/10000-USD per 1M
// tokens: the model's cache_write_1h_per_mtok_usd, else its 5-minute write
// price. Returns 0 on lookup miss.
func (c *Config) PriceCacheWrite1hPer1MTokensX10000(model string) int64 {
	if c == nil {
		return 0
	}
	m, ok := c.Models[c.Resolve(model)]
	if !ok {
		return 0
	}
	if m.CacheWrite1hPerMTokUSD != nil {
		return int64(*m.CacheWrite1hPerMTokUSD * 10_000)
	}
	return c.PriceCacheWritePer1MTokensX10000(model)
}

// CopilotMultiplier returns the Copilot premium-request multiplier.
// Unknown models return 1.0× (Copilot's default).
func (c *Config) CopilotMultiplier(model string) float64 {
	if c == nil {
		return 1.0
	}
	if m, ok := c.Models[c.Resolve(model)]; ok {
		return m.CopilotMultiplier
	}
	return 1.0
}

// MaxOutputFor returns the per-turn output cap (in tokens) for the given
// model id. Aliases are canonicalised via Resolve. Unknown models, models
// with no max_output_tokens entry, and the empty string all return the 8192
// default.
func (c *Config) MaxOutputFor(model string) int64 {
	if c == nil {
		return defaultMaxOutputTokens
	}
	if m, ok := c.Models[c.Resolve(model)]; ok && m.MaxOutputTokens > 0 {
		return m.MaxOutputTokens
	}
	return defaultMaxOutputTokens
}

// MaxOutputFor is the package-level convenience that uses the cached
// loaded Config.
func MaxOutputFor(model string) int64 {
	return Load().MaxOutputFor(model)
}

// ContextWindowFor returns the context_window the config declares for the
// model, aliases canonicalised via Resolve; 0 when it declares none.
func (c *Config) ContextWindowFor(model string) int64 {
	if c == nil {
		return 0
	}
	if m, ok := c.Models[c.Resolve(model)]; ok && m.ContextWindow > 0 {
		return m.ContextWindow
	}
	return 0
}

// ContextWindowFor is the package-level convenience that uses the cached
// loaded Config.
func ContextWindowFor(model string) int64 {
	return Load().ContextWindowFor(model)
}

// providerPrefixes are the provider names a "provider:model" string may
// lead with.
var providerPrefixes = []string{"anthropic", "openai", "gemini", "google", "ollama"}

// Resolve canonicalises an alias or "provider:model" string to the
// model id used as the Models map key. Empty / unknown inputs pass
// through unchanged.
func (c *Config) Resolve(input string) string {
	if c == nil || input == "" {
		return input
	}
	input = stripProvider(input)
	// Alias lookup (case-insensitive, trimmed).
	key := strings.ToLower(strings.TrimSpace(input))
	if canon, ok := c.Aliases[key]; ok {
		return canon
	}
	// Fall back to the raw input (might already be canonical).
	return input
}

// stripProvider strips a "provider:" prefix only when it names a provider. A
// colon is part of many ids — a Bedrock id ends "-v1:0", an Ollama tag is
// "qwen3.5:4b" — and cutting at the first one would leave "0" and "4b".
func stripProvider(input string) string {
	prefix, rest, ok := strings.Cut(input, ":")
	if ok && slices.Contains(providerPrefixes, strings.ToLower(strings.TrimSpace(prefix))) {
		return rest
	}
	return input
}

// ResolveTier picks the model id for a tier role under a budget mode.
// Empty slots degrade: standard→premium, cheap→standard→premium,
// free→premium. Unknown role returns "". A run's dispatch and `chb models
// tiers` both resolve a tier through it, on Load's config.
func (c *Config) ResolveTier(role, mode string) string {
	if c == nil {
		return ""
	}
	t, ok := c.Tiers[role]
	if !ok {
		return ""
	}
	return cmp.Or(t.degradation(mode)...)
}

// degradation is the slots a budget mode reads, in order: its own, then
// those it degrades to, premium last. An unknown mode degrades as standard
// does.
func (t TierSlots) degradation(mode string) []string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "premium":
		return []string{t.Premium}
	case "cheap":
		return []string{t.Cheap, t.Standard, t.Premium}
	case "free":
		return []string{t.Free, t.Premium}
	}
	return []string{t.Standard, t.Premium}
}
