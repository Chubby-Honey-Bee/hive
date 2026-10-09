package models

import "testing"

// TestMaxOutputFor_EmbeddedDefaults asserts the per-model output caps
// in default-models.yaml load correctly via MaxOutputFor() for a few
// representative models.
func TestMaxOutputFor_EmbeddedDefaults(t *testing.T) {
	cfg, err := parseConfig(defaultYAML)
	if err != nil {
		t.Fatalf("parse embedded default: %v", err)
	}
	cases := []struct {
		model    string
		expected int64
	}{
		{"claude-opus-4-8", 32768},
		{"claude-sonnet-4-6", 65536},
		{"claude-haiku-4-5", 8192},
		{"claude-haiku-4-5-20251001", 8192},
		{"gemini-2.5-pro", 65536},
		{"gemini-2.5-flash-lite", 16384},
		{"gpt-5.1-pro", 32768},
		{"gpt-5-mini", 16384},
	}
	for _, tc := range cases {
		got := cfg.MaxOutputFor(tc.model)
		if got != tc.expected {
			t.Errorf("MaxOutputFor(%q) = %d; want %d", tc.model, got, tc.expected)
		}
	}
}

// TestMaxOutputFor_AliasResolution asserts aliases resolve to the
// underlying canonical model's cap, not to the fallback default.
func TestMaxOutputFor_AliasResolution(t *testing.T) {
	cfg, err := parseConfig(defaultYAML)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cases := []struct {
		alias    string
		canonHex string
		// expected = MaxOutputFor of the canonical id
	}{
		{"opus", "claude-opus-4-8"},
		{"sonnet", "claude-sonnet-4-6"},
		{"haiku", "claude-haiku-4-5"},
	}
	for _, tc := range cases {
		got := cfg.MaxOutputFor(tc.alias)
		want := cfg.MaxOutputFor(tc.canonHex)
		if got == 0 || want == 0 {
			t.Fatalf("either lookup returned 0: alias=%q got=%d, canon=%q want=%d",
				tc.alias, got, tc.canonHex, want)
		}
		if got != want {
			t.Errorf("alias %q = %d, canonical %q = %d; expected equal",
				tc.alias, got, tc.canonHex, want)
		}
	}
}

// TestMaxOutputFor_UnknownFallsBack asserts unknown models fall back
// to the historical 8192 default (preserving prior behaviour).
func TestMaxOutputFor_UnknownFallsBack(t *testing.T) {
	cfg, err := parseConfig(defaultYAML)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, name := range []string{"", "unknown-model", "completely-bogus-id", "provider:nothing"} {
		got := cfg.MaxOutputFor(name)
		if got != defaultMaxOutputTokens {
			t.Errorf("MaxOutputFor(%q) = %d; want fallback %d",
				name, got, defaultMaxOutputTokens)
		}
	}
}

// TestMaxOutputFor_NilConfig asserts a nil receiver returns the
// fallback (mirroring the safety nets in PriceInPer1MTokensX10000 etc).
func TestMaxOutputFor_NilConfig(t *testing.T) {
	var cfg *Config
	if got := cfg.MaxOutputFor("claude-opus-4-8"); got != defaultMaxOutputTokens {
		t.Errorf("nil cfg = %d; want %d", got, defaultMaxOutputTokens)
	}
}

// Every model in default-models.yaml declares max_output_tokens, so none
// falls back to 8192 silently.
func TestMaxOutputFor_AllShippedModelsHaveCap(t *testing.T) {
	cfg, err := parseConfig(defaultYAML)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for id, m := range cfg.Models {
		if m.MaxOutputTokens <= 0 {
			t.Errorf("model %q has no max_output_tokens; add one to default-models.yaml", id)
		}
	}
}
