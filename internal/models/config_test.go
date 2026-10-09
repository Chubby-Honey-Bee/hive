package models

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestEmbeddedDefault_Loads asserts the embedded default-models.yaml
// parses cleanly and validates. Catches accidental YAML syntax breaks
// or missing required fields.
func TestEmbeddedDefault_Loads(t *testing.T) {
	cfg, err := parseConfig(defaultYAML)
	if err != nil {
		t.Fatalf("parse embedded default: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate embedded default: %v", err)
	}
	for _, role := range []string{"planner", "synthesist", "worker", "verifier"} {
		if _, ok := cfg.Tiers[role]; !ok {
			t.Errorf("tiers.%s missing from embedded default", role)
		}
	}
	for _, model := range []string{"claude-opus-4-8", "claude-sonnet-4-6", "claude-haiku-4-5"} {
		if _, ok := cfg.Models[model]; !ok {
			t.Errorf("models.%s missing from embedded default", model)
		}
	}
}

// TestResolveTier_AllModes pins the four-position cost dial, every role at
// every mode, against the embedded default: standard is the middle gear,
// premium top quality, cheap all haiku, free the GPT-5.x path.
func TestResolveTier_AllModes(t *testing.T) {
	cfg := mustParse(defaultYAML, "test")
	cases := []struct {
		role string
		mode string
		want string
	}{
		{"planner", "premium", "claude-opus-4-8"},
		{"synthesist", "premium", "claude-sonnet-4-6"},
		{"worker", "premium", "claude-haiku-4-5"},
		{"verifier", "premium", "gpt-5.3-codex"},

		{"planner", "standard", "claude-sonnet-4-6"},
		{"synthesist", "standard", "claude-haiku-4-5"},
		{"worker", "standard", "claude-haiku-4-5"},
		{"verifier", "standard", "claude-sonnet-4-6"},

		{"planner", "cheap", "claude-haiku-4-5"},
		{"synthesist", "cheap", "claude-haiku-4-5"},
		{"worker", "cheap", "claude-haiku-4-5"},
		{"verifier", "cheap", "claude-haiku-4-5"},

		{"planner", "free", "gpt-5.5"},
		{"synthesist", "free", "gpt-5.2"},
		{"worker", "free", "gpt-5-mini"},
		{"verifier", "free", "gpt-5-mini"},
	}
	for _, c := range cases {
		t.Run(c.role+"/"+c.mode, func(t *testing.T) {
			got := cfg.ResolveTier(c.role, c.mode)
			if got != c.want {
				t.Errorf("ResolveTier(%q, %q) = %q; want %q", c.role, c.mode, got, c.want)
			}
		})
	}
}

// TestResolveTier_UnknownRoleReturnsEmpty: a role the config does not hold
// resolves to no model, and the node's call leaves the model to the backend.
func TestResolveTier_UnknownRoleReturnsEmpty(t *testing.T) {
	if got := mustParse(defaultYAML, "test").ResolveTier("nonexistent", "standard"); got != "" {
		t.Errorf("unknown tier should return empty; got %q", got)
	}
}

// TestResolveTier_EmptySlotsDegradeToPremium: a tier that fills only its
// premium and free slots, as a user's override may, sends its premium model
// at standard and cheap rather than no model.
func TestResolveTier_EmptySlotsDegradeToPremium(t *testing.T) {
	cfg := &Config{Tiers: map[string]TierSlots{"planner": {Premium: "custom-primary", Free: "custom-free"}}}
	for _, c := range []struct{ mode, want string }{
		{"premium", "custom-primary"},
		{"standard", "custom-primary"}, // standard empty → premium
		{"cheap", "custom-primary"},    // cheap and standard empty → premium
		{"free", "custom-free"},
	} {
		if got := cfg.ResolveTier("planner", c.mode); got != c.want {
			t.Errorf("ResolveTier under %s = %q; want %q", c.mode, got, c.want)
		}
	}
}

// TestShippedTiers_AllRolesPopulated catches a role dropped from the
// embedded default's tiers, or a mode's slot left empty in it: a dispatch of
// that role would get no model, or another mode's.
func TestShippedTiers_AllRolesPopulated(t *testing.T) {
	cfg := mustParse(defaultYAML, "test")
	for _, role := range []string{"planner", "synthesist", "worker", "verifier"} {
		t.Run(role, func(t *testing.T) {
			tier, ok := cfg.Tiers[role]
			if !ok {
				t.Fatalf("tiers.%s missing from the embedded default", role)
			}
			if tier.Premium == "" {
				t.Errorf("%s: premium is empty", role)
			}
			if tier.Standard == "" {
				t.Errorf("%s: standard is empty (would drop standard mode to premium)", role)
			}
			if tier.Cheap == "" {
				t.Errorf("%s: cheap is empty (would drop cheap mode to standard)", role)
			}
			if tier.Free == "" {
				t.Errorf("%s: free is empty (would drop free mode to premium)", role)
			}
		})
	}
}

// TestCheckReasoningLevel: each of the four levels passes, and anything
// else, case and the empty string included, is refused naming the level and
// the four.
func TestCheckReasoningLevel(t *testing.T) {
	for _, level := range ReasoningLevels {
		if err := CheckReasoningLevel(level); err != nil {
			t.Errorf("%q refused: %v", level, err)
		}
	}
	for _, level := range []string{"", "HIGH", "extreme", "<nil>"} {
		want := `reasoning "` + level + `" is not one of none|low|medium|high`
		if err := CheckReasoningLevel(level); err == nil || err.Error() != want {
			t.Errorf("%q: err = %v, want %q", level, err, want)
		}
	}
}

// TestResolve_Aliases asserts the alias map resolves the common
// shorthand names case-insensitively.
func TestResolve_Aliases(t *testing.T) {
	cfg := mustParse(defaultYAML, "test")
	cases := []struct{ in, want string }{
		{"haiku", "claude-haiku-4-5"},
		{"HAIKU", "claude-haiku-4-5"},
		{"  Sonnet  ", "claude-sonnet-4-6"},
		{"opus", "claude-opus-4-8"},
		{"pro", "gemini-2.5-pro"},
		{"flash", "gemini-2.5-flash"},
		// pass-through for non-aliases
		{"claude-opus-4-8", "claude-opus-4-8"},
		{"gpt-5.1", "gpt-5.1"},
		{"", ""},
		// provider:model strip
		{"anthropic:claude-sonnet-4-6", "claude-sonnet-4-6"},
	}
	for _, c := range cases {
		got := cfg.Resolve(c.in)
		if got != c.want {
			t.Errorf("Resolve(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

// TestPricing_EveryShippedPriceRoundTrips: each price the embedded config
// lists, read apart from the loader as plain YAML, comes back from its
// Price*Per1MTokensX10000 function in 1/10000 USD. A price an entry leaves
// out falls back: a cache read or a 5-minute cache write to the input
// price, a 1-hour cache write to the 5-minute write price.
func TestPricing_EveryShippedPriceRoundTrips(t *testing.T) {
	var raw struct {
		Models map[string]map[string]any `yaml:"models"`
	}
	if err := yaml.Unmarshal(defaultYAML, &raw); err != nil {
		t.Fatal(err)
	}
	cfg := mustParse(defaultYAML, "test")
	x10000 := func(v any) int64 {
		switch n := v.(type) {
		case int:
			return int64(n) * 10_000
		case float64:
			return int64(math.Round(n * 10_000))
		}
		t.Fatalf("price %v (%T) is not a number", v, v)
		return 0
	}
	var cacheWrites int
	for id, fields := range raw.Models {
		price := func(key string, fallback int64) int64 {
			if v, ok := fields[key]; ok {
				return x10000(v)
			}
			return fallback
		}
		in := price("input_per_mtok_usd", 0)
		write := price("cache_write_per_mtok_usd", in)
		if _, ok := fields["cache_write_per_mtok_usd"]; ok {
			cacheWrites++
		}
		for _, c := range []struct {
			name      string
			got, want int64
		}{
			{"input", cfg.PriceInPer1MTokensX10000(id), in},
			{"output", cfg.PriceOutPer1MTokensX10000(id), price("output_per_mtok_usd", 0)},
			{"cache read", cfg.PriceCachedInPer1MTokensX10000(id), price("cached_input_per_mtok_usd", in)},
			{"5-minute cache write", cfg.PriceCacheWritePer1MTokensX10000(id), write},
			{"1-hour cache write", cfg.PriceCacheWrite1hPer1MTokensX10000(id), price("cache_write_1h_per_mtok_usd", write)},
		} {
			if c.got != c.want {
				t.Errorf("%s %s price = %d, want %d", id, c.name, c.got, c.want)
			}
		}
	}
	if cacheWrites == 0 {
		t.Error("precondition: no shipped model lists a cache-write price")
	}
}

// TestPricing_CacheWriteFallbacks: an entry that lists no cache-write price
// prices a 5-minute write at its input price, and one that lists no 1-hour
// price prices a 1-hour write as a 5-minute one.
func TestPricing_CacheWriteFallbacks(t *testing.T) {
	cfg, err := parseConfig([]byte(`models:
  bare:
    input_per_mtok_usd: 2.00
    output_per_mtok_usd: 8.00
  five-minute-only:
    input_per_mtok_usd: 2.00
    cache_write_per_mtok_usd: 2.50
    output_per_mtok_usd: 8.00
`))
	if err != nil {
		t.Fatal(err)
	}
	in := cfg.PriceInPer1MTokensX10000("bare")
	if got := cfg.PriceCacheWritePer1MTokensX10000("bare"); got != in {
		t.Errorf("bare 5-minute write = %d, want the input price %d", got, in)
	}
	if got := cfg.PriceCacheWrite1hPer1MTokensX10000("bare"); got != in {
		t.Errorf("bare 1-hour write = %d, want the input price %d", got, in)
	}
	write := cfg.PriceCacheWritePer1MTokensX10000("five-minute-only")
	if write == in {
		t.Fatalf("precondition: five-minute-only's write price equals its input price %d", in)
	}
	if got := cfg.PriceCacheWrite1hPer1MTokensX10000("five-minute-only"); got != write {
		t.Errorf("five-minute-only 1-hour write = %d, want its 5-minute write price %d", got, write)
	}
	if got := cfg.PriceCacheWrite1hPer1MTokensX10000("unlisted"); got != 0 {
		t.Errorf("unlisted 1-hour write = %d, want 0", got)
	}
}

// TestUserOverride_MergesOnTopOfDefaults writes a partial user config
// and asserts that overridden entries replace the default while
// non-overridden entries survive.
func TestUserOverride_MergesOnTopOfDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	override := filepath.Join(tmpDir, "models.yaml")
	body := `models:
  custom-model:
    family: test
    input_per_mtok_usd: 1.23
    output_per_mtok_usd: 4.56
    copilot_multiplier: 2.0
tiers:
  planner:
    premium: custom-model
    standard: custom-model
aliases:
  testalias: custom-model
`
	if err := os.WriteFile(override, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HIVE_MODELS_PATH", override)
	cfg := loadFresh()

	// Custom model inherited
	if _, ok := cfg.Models["custom-model"]; !ok {
		t.Error("custom-model should be present after override")
	}
	// Default models still survive
	if _, ok := cfg.Models["claude-opus-4-8"]; !ok {
		t.Error("default opus should survive partial override")
	}
	// Overridden tier picks up the new model
	if got := cfg.ResolveTier("planner", "premium"); got != "custom-model" {
		t.Errorf("planner premium = %q; want custom-model", got)
	}
	// Untouched tiers (synthesist) still resolve to defaults
	if got := cfg.ResolveTier("synthesist", "premium"); got != "claude-sonnet-4-6" {
		t.Errorf("synthesist premium = %q; want default sonnet", got)
	}
	// Custom alias works
	if got := cfg.Resolve("testalias"); got != "custom-model" {
		t.Errorf("custom alias = %q; want custom-model", got)
	}
	// Default alias still works
	if got := cfg.Resolve("opus"); got != "claude-opus-4-8" {
		t.Errorf("default opus alias broken after override: %q", got)
	}
}

// TestUserOverride_MalformedFallsBackToDefault asserts that a broken
// override file logs a warning and falls back to the embedded default
// instead of panicking.
func TestUserOverride_MalformedFallsBackToDefault(t *testing.T) {
	tmpDir := t.TempDir()
	override := filepath.Join(tmpDir, "models.yaml")
	if err := os.WriteFile(override, []byte("[ this is not yaml @#$"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HIVE_MODELS_PATH", override)
	cfg := loadFresh()
	// Should still resolve default opus
	if got := cfg.ResolveTier("planner", "premium"); got != "claude-opus-4-8" {
		t.Errorf("malformed override should fall back to default; got %q", got)
	}
}
