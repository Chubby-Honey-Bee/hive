package models

import "testing"

// A user config's context_window is read for its model, by alias and with
// an ollama: prefix, and a model with none reads 0.
func TestContextWindowFor(t *testing.T) {
	base, err := parseConfig(defaultYAML)
	if err != nil {
		t.Fatal(err)
	}
	user, err := parseConfig([]byte(`
models:
  qwen3.5:4b: {family: ollama, context_window: 16384}
aliases:
  small: qwen3.5:4b
`))
	if err != nil {
		t.Fatal(err)
	}
	cfg := mergeConfigs(base, user)
	want := user.Models["qwen3.5:4b"].ContextWindow
	for _, name := range []string{"qwen3.5:4b", "small", "ollama:qwen3.5:4b"} {
		if got := cfg.ContextWindowFor(name); got != want {
			t.Errorf("ContextWindowFor(%q) = %d, want %d", name, got, want)
		}
	}
	for _, name := range []string{"ministral-3:8b", "claude-haiku-4-5", ""} {
		if got := cfg.ContextWindowFor(name); got != 0 {
			t.Errorf("ContextWindowFor(%q) = %d, want 0: none is declared", name, got)
		}
	}
}
