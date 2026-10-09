package runner

import (
	"strings"
	"testing"
)

func TestEnforceProviderAllowlist(t *testing.T) {
	t.Run("empty env allows anything", func(t *testing.T) {
		t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
		if err := EnforceProviderAllowlist(BackendAnthropic); err != nil {
			t.Fatalf("empty allowlist should permit any provider, got %v", err)
		}
	})

	t.Run("listed provider passes, unlisted fails", func(t *testing.T) {
		t.Setenv("HIVE_PROVIDER_ALLOWLIST", "claude-cli")
		if err := EnforceProviderAllowlist(BackendClaudeCLI); err != nil {
			t.Fatalf("claude-cli should be allowed, got %v", err)
		}
		if err := EnforceProviderAllowlist(BackendAnthropic); err == nil {
			t.Fatal("anthropic should be refused when only claude-cli is allowed")
		}
	})

	t.Run("canonicalizes aliases on both sides", func(t *testing.T) {
		t.Setenv("HIVE_PROVIDER_ALLOWLIST", "copilot, claude-code")
		if err := EnforceProviderAllowlist(BackendOpenAI); err != nil {
			t.Fatalf("copilot alias should permit openai, got %v", err)
		}
		if err := EnforceProviderAllowlist(BackendClaudeCLI); err != nil {
			t.Fatalf("claude-code alias should permit claude-cli, got %v", err)
		}
		if err := EnforceProviderAllowlist(BackendGemini); err == nil {
			t.Fatal("gemini should be refused when allowlist is copilot,claude-code")
		}
	})

	t.Run("resolveLLMBackend refuses a non-allowlisted provider", func(t *testing.T) {
		t.Setenv("HIVE_PROVIDER_ALLOWLIST", "claude-cli")
		noop := func(string, ...any) {}
		if _, err := resolveLLMBackend(Config{Provider: "gemini", DryRun: true}, noop); err == nil {
			t.Fatal("resolveLLMBackend should refuse gemini when only claude-cli is allowed")
		}
	})
}

// A node-level `provider:` override builds its own backend, out of the reach
// of EnforceProviderAllowlist, which sees the run default. The allowlist
// promises a run is refused before any model call, so the preflight checks
// the override too.
func TestPreflightWorkflowProviders(t *testing.T) {
	defn := map[string]any{
		"nodes": map[string]any{
			"plan":     map[string]any{"type": "agent"},
			"research": map[string]any{"type": "agent", "provider": "openai"},
		},
	}

	t.Run("refuses a node pinned outside the allowlist", func(t *testing.T) {
		t.Setenv("HIVE_PROVIDER_ALLOWLIST", "claude-cli,anthropic")
		err := PreflightWorkflowProviders(defn)
		if err == nil {
			t.Fatal("expected the run to be refused before any model call")
		}
		if !strings.Contains(err.Error(), "research") {
			t.Errorf("error should name the offending node, got: %v", err)
		}
	})

	t.Run("allows a node pinned inside the allowlist", func(t *testing.T) {
		t.Setenv("HIVE_PROVIDER_ALLOWLIST", "claude-cli,openai")
		if err := PreflightWorkflowProviders(defn); err != nil {
			t.Fatalf("allowlisted override should pass: %v", err)
		}
	})

	t.Run("no allowlist set means no restriction", func(t *testing.T) {
		t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
		if err := PreflightWorkflowProviders(defn); err != nil {
			t.Fatalf("opt-in policy should not fire when unset: %v", err)
		}
	})

	t.Run("a workflow with no nodes key is not a failure", func(t *testing.T) {
		t.Setenv("HIVE_PROVIDER_ALLOWLIST", "anthropic")
		if err := PreflightWorkflowProviders(map[string]any{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
