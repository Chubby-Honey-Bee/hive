package cli

import "testing"

// checkProviderAuth judges the kind the run resolves to: with no provider
// resolvable, or an explicit one without its key, preflight fails; an
// explicit provider with its key passes, and a node's provider: override is
// checked as well.
func TestCheckProviderAuth_ResolvesCanonicalKinds(t *testing.T) {
	for _, k := range []string{"ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "OPENAI_API_KEY", "HIVE_PROVIDER"} {
		t.Setenv(k, "")
	}
	t.Setenv("PATH", t.TempDir()) // no claude / gemini CLI resolvable

	t.Run("nothing resolvable fails", func(t *testing.T) {
		r := newPreflightReport()
		r.checkProviderAuth("", map[string]any{})
		if !r.hasFailures() {
			t.Fatalf("expected a failure, got %+v", r.results)
		}
	})
	t.Run("explicit provider without its key fails", func(t *testing.T) {
		r := newPreflightReport()
		r.checkProviderAuth("anthropic", map[string]any{})
		if !r.hasFailures() {
			t.Fatalf("expected a failure, got %+v", r.results)
		}
	})
	t.Run("explicit provider with its key passes", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "k")
		r := newPreflightReport()
		r.checkProviderAuth("anthropic", map[string]any{})
		if r.hasFailures() || len(r.results) != 1 || r.results[0].level != checkPass {
			t.Fatalf("expected one pass, got %+v", r.results)
		}
	})
	t.Run("per-node override still checked", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "k")
		r := newPreflightReport()
		r.checkProviderAuth("anthropic", map[string]any{"nodes": map[string]any{"n": map[string]any{"provider": "openai"}}})
		if r.failures() != 1 {
			t.Fatalf("expected the openai node to fail, got %+v", r.results)
		}
	})
}

// A dreamer-archetype node has no LLM output for accept: to judge, so accept
// coverage exempts it; an ordinary agent node without accept: fails.
func TestCheckAcceptPresence_DreamerNodesAreExempt(t *testing.T) {
	r := newPreflightReport()
	r.checkAcceptPresence(map[string]any{"nodes": map[string]any{
		"dreamer-dreamer": map[string]any{"type": "agent", "archetype": "dreamer"},
	}})
	if r.hasFailures() {
		t.Errorf("a dreamer node failed accept coverage: %+v", r.results)
	}
	r = newPreflightReport()
	r.checkAcceptPresence(map[string]any{"nodes": map[string]any{
		"forager-skeptic": map[string]any{"type": "agent"},
	}})
	if !r.hasFailures() {
		t.Error("an agent node without accept: passed")
	}
}
