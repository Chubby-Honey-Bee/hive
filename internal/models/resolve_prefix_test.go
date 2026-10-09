package models

import (
	"fmt"
	"strings"
	"testing"
)

// A colon that does not follow a provider name is part of the id, so a
// Bedrock id does not become "0", nor an Ollama tag "4b".
func TestResolve_KeepsColonsThatAreNotAProviderPrefix(t *testing.T) {
	cfg := mustParse(defaultYAML, "test")
	for _, id := range []string{
		"qwen3.5:4b",
		"llama3:70b-instruct",
		"anthropic.claude-opus-4-1-20250805-v1:0",
	} {
		if got := cfg.Resolve(id); got != id {
			t.Errorf("Resolve(%q) = %q; want it verbatim", id, got)
		}
	}
}

// Each provider name, in any case, is stripped; the rest is the id.
func TestResolve_StripsEachProviderPrefix(t *testing.T) {
	cfg := mustParse(defaultYAML, "test")
	for _, tc := range []struct{ prefix, id string }{
		{"anthropic", "claude-sonnet-4-6"},
		{"openai", "gpt-5.1"},
		{"gemini", "gemini-2.5-pro"},
		{"google", "gemini-2.5-flash"},
		{"ollama", "qwen3.5:4b"},
		{"OpenAI", "gpt-5.1"},
	} {
		in := tc.prefix + ":" + tc.id
		if got := cfg.Resolve(in); got != tc.id {
			t.Errorf("Resolve(%q) = %q; want %q", in, got, tc.id)
		}
	}
}

// The cap and the price of a model whose id holds a colon come from its own
// entry, not from a miss on the id's tail.
func TestLookups_UseTheWholeIDOfAColonModel(t *testing.T) {
	models := []struct {
		id      string
		maxOut  int64
		in, out float64
	}{
		{id: "qwen3.5:4b", maxOut: 4096, in: 0.5, out: 1.5},
		{id: "anthropic.claude-opus-4-1-20250805-v1:0", maxOut: 32000, in: 15, out: 75},
	}
	var b strings.Builder
	b.WriteString("models:\n")
	for _, m := range models {
		fmt.Fprintf(&b, "  %q:\n    family: test\n    max_output_tokens: %d\n    input_per_mtok_usd: %g\n    output_per_mtok_usd: %g\n",
			m.id, m.maxOut, m.in, m.out)
	}
	cfg, err := parseConfig([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range models {
		if got := cfg.MaxOutputFor(m.id); got != m.maxOut {
			t.Errorf("MaxOutputFor(%q) = %d; want %d", m.id, got, m.maxOut)
		}
		if got, want := cfg.PriceInPer1MTokensX10000(m.id), int64(m.in*10_000); got != want {
			t.Errorf("PriceIn(%q) = %d; want %d", m.id, got, want)
		}
		if got, want := cfg.PriceOutPer1MTokensX10000(m.id), int64(m.out*10_000); got != want {
			t.Errorf("PriceOut(%q) = %d; want %d", m.id, got, want)
		}
	}
}
