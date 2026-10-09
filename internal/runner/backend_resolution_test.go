package runner

// Tests for provider precedence, per-provider pricing, and model-alias
// resolution.

import (
	"os"
	"strings"
	"testing"
)

// withEnv sets `name=value` for the duration of the test, restoring the
// previous value (including unset) on cleanup. Necessary because the
// resolution rules read process env at call time.
func withEnv(t *testing.T, name, value string) {
	t.Helper()
	old, had := os.LookupEnv(name)
	if value == "" {
		_ = os.Unsetenv(name)
	} else {
		_ = os.Setenv(name, value)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(name, old)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}

// withLookPath stubs the `lookPath` package var so we can simulate
// "claude on PATH" / "gemini on PATH" without modifying the test
// environment. Restored on cleanup.
func withLookPath(t *testing.T, fn func(string) (string, error)) {
	t.Helper()
	prev := lookPath
	lookPath = fn
	t.Cleanup(func() { lookPath = prev })
}

func TestResolveBackendKind_Precedence(t *testing.T) {
	// Clear every env var the resolver consults so tests are isolated.
	for _, k := range []string{
		"HIVE_PROVIDER",
		"ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY",
		"OPENAI_API_KEY",
	} {
		withEnv(t, k, "")
	}
	withLookPath(t, func(string) (string, error) { return "", os.ErrNotExist })

	t.Run("cfg.Provider wins over everything", func(t *testing.T) {
		withEnv(t, "HIVE_PROVIDER", "openai")
		withEnv(t, "ANTHROPIC_API_KEY", "sk-ant-x")
		got := ResolveBackendKind(Config{Provider: "gemini"})
		if got != BackendGemini {
			t.Fatalf("want gemini, got %s", got)
		}
	})

	t.Run("HIVE_PROVIDER beats SDK envs", func(t *testing.T) {
		withEnv(t, "HIVE_PROVIDER", "openai")
		withEnv(t, "ANTHROPIC_API_KEY", "sk-ant-x")
		got := ResolveBackendKind(Config{})
		if got != BackendOpenAI {
			t.Fatalf("want openai, got %s", got)
		}
		// Drop HIVE_PROVIDER → the Anthropic key wins.
		withEnv(t, "HIVE_PROVIDER", "")
		got = ResolveBackendKind(Config{})
		if got != BackendAnthropic {
			t.Fatalf("no HIVE_PROVIDER: want anthropic, got %s", got)
		}
	})

	t.Run("only GEMINI_API_KEY → gemini", func(t *testing.T) {
		withEnv(t, "GEMINI_API_KEY", "key")
		got := ResolveBackendKind(Config{})
		if got != BackendGemini {
			t.Fatalf("want gemini, got %s", got)
		}
	})

	t.Run("only OPENAI_API_KEY → openai", func(t *testing.T) {
		withEnv(t, "OPENAI_API_KEY", "key")
		got := ResolveBackendKind(Config{})
		if got != BackendOpenAI {
			t.Fatalf("want openai, got %s", got)
		}
	})

	t.Run("anthropic key beats gemini key beats openai key", func(t *testing.T) {
		withEnv(t, "ANTHROPIC_API_KEY", "k")
		withEnv(t, "GEMINI_API_KEY", "k")
		withEnv(t, "OPENAI_API_KEY", "k")
		got := ResolveBackendKind(Config{})
		if got != BackendAnthropic {
			t.Fatalf("want anthropic, got %s", got)
		}
	})

	t.Run("no env, claude on PATH → claude-cli", func(t *testing.T) {
		for _, k := range []string{"ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "OPENAI_API_KEY"} {
			withEnv(t, k, "")
		}
		withLookPath(t, func(name string) (string, error) {
			if name == "claude" {
				return "/usr/bin/claude", nil
			}
			return "", os.ErrNotExist
		})
		got := ResolveBackendKind(Config{})
		if got != BackendClaudeCLI {
			t.Fatalf("want claude-cli, got %s", got)
		}
	})

	t.Run("no env, gemini on PATH (no claude) → gemini-cli", func(t *testing.T) {
		withLookPath(t, func(name string) (string, error) {
			if name == "gemini" {
				return "/usr/bin/gemini", nil
			}
			return "", os.ErrNotExist
		})
		got := ResolveBackendKind(Config{})
		if got != BackendGeminiCLI {
			t.Fatalf("want gemini-cli, got %s", got)
		}
	})

	t.Run("nothing available → claude-cli (final fallback)", func(t *testing.T) {
		withLookPath(t, func(string) (string, error) { return "", os.ErrNotExist })
		got := ResolveBackendKind(Config{})
		if got != BackendClaudeCLI {
			t.Fatalf("want claude-cli fallback, got %s", got)
		}
	})
}

func TestCanonicalKind_Aliases(t *testing.T) {
	cases := map[string]BackendKind{
		"":             "",
		"anthropic":    BackendAnthropic,
		"openai":       BackendOpenAI,
		"copilot":      BackendOpenAI,
		"azure-openai": BackendOpenAI,
		"gemini":       BackendGemini,
		"google":       BackendGemini,
		"claude-cli":   BackendClaudeCLI,
		"claude-code":  BackendClaudeCLI,
		"gemini-cli":   BackendGeminiCLI,
		"unknown":      "",
	}
	for in, want := range cases {
		if got := canonicalKind(in); got != want {
			t.Errorf("canonicalKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestComputeCostUSDx10000_PerProvider(t *testing.T) {
	cases := []struct {
		model string
		in    int64
		out   int64
		want  int64
	}{
		// Anthropic: claude-haiku-4-5-20251001 = $1/$5 per 1M
		// 100k in + 100k out = $0.10 + $0.50 = $0.60 = 6000 x10000
		{"claude-haiku-4-5-20251001", 100_000, 100_000, 6_000},
		// claude-sonnet-4-6 = $3/$15 per 1M; 1M in + 1M out = $18 = 180_000
		{"claude-sonnet-4-6", 1_000_000, 1_000_000, 180_000},
		// gemini-2.5-flash = $0.30/$2.50 per 1M; 1M in + 1M out = $2.80 = 28_000
		{"gemini-2.5-flash", 1_000_000, 1_000_000, 28_000},
		// gpt-5.1-mini = $0.60/$2.40 per 1M; 1M in + 1M out = $3.00 = 30_000
		{"gpt-5.1-mini", 1_000_000, 1_000_000, 30_000},
		// Unknown model → 0
		{"unknown-model-xyz", 1_000_000, 1_000_000, 0},
	}
	for _, c := range cases {
		got := ComputeCostUSDx10000(c.model, c.in, c.out)
		if got != c.want {
			t.Errorf("ComputeCostUSDx10000(%q, %d, %d) = %d, want %d", c.model, c.in, c.out, got, c.want)
		}
	}
}

func TestResolveModelForProvider(t *testing.T) {
	cases := []struct {
		provider string
		alias    string
		want     string
	}{
		{"anthropic", "haiku", "claude-haiku-4-5"},
		{"openai", "haiku", "gpt-5.1-mini"},
		{"gemini", "haiku", "gemini-2.5-flash"},
		{"gemini-cli", "sonnet", "gemini-2.5-pro"},
		{"openai", "opus", "gpt-5.1-pro"},
	}
	for _, c := range cases {
		got := resolveModelForProvider(canonicalKind(c.provider), c.alias)
		if got != c.want {
			t.Errorf("resolveModelForProvider(%q,%q) = %q, want %q", c.provider, c.alias, got, c.want)
		}
	}
}

func TestProviderLabel(t *testing.T) {
	cases := []struct {
		provider string
		want     string
	}{
		{"anthropic", "anthropic"},
		{"openai", "openai"},
		{"gemini", "gemini"},
		{"gemini-cli", "gemini-cli"},
		{"claude-cli", "claude-cli"},
	}
	for _, c := range cases {
		got := providerLabel(Config{Provider: c.provider})
		if got != c.want {
			t.Errorf("providerLabel(%q) = %q, want %q", c.provider, got, c.want)
		}
	}
}

// TestProviderAliases_NoCollision sanity-checks that the haiku alias on
// each provider maps to a model that has a non-zero pricing entry. If
// somebody updates one alias table without the other this test fails
// with a clear message rather than silently turning the cost meter to $0.
//
// TestResolveLLMBackend covers all branches of resolveLLMBackend.
// The injected-backend branch is already covered by
// TestResolveLLMBackend_InjectedTakesPrecedence in run_phases_test.go;
// these sub-tests cover the remaining non-injected branches.
func TestResolveLLMBackend(t *testing.T) {
	noop := func(string, ...any) {}

	t.Run("injected backend is returned directly", func(t *testing.T) {
		stub := &stubBackend{}
		got, err := resolveLLMBackend(Config{Backend: stub}, noop)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != stub {
			t.Fatalf("want injected stub, got %T", got)
		}
	})

	t.Run("valid provider resolves to a non-nil backend", func(t *testing.T) {
		// BackendAnthropic (SDKBackend) constructs with no API key at build time
		// — no network call happens during backend construction.
		withEnv(t, "HIVE_DISABLE_RATE_LIMIT", "1")
		got, err := resolveLLMBackend(Config{Provider: "anthropic"}, noop)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil {
			t.Fatal("expected non-nil backend")
		}
	})

	t.Run("missing API key DryRun=false returns error", func(t *testing.T) {
		// Force gemini provider, clear its key — NewGeminiBackend returns an error
		// which resolveLLMBackend must propagate when DryRun is false.
		withEnv(t, "GEMINI_API_KEY", "")
		withEnv(t, "GOOGLE_API_KEY", "")
		_, err := resolveLLMBackend(Config{Provider: "gemini"}, noop)
		if err == nil {
			t.Fatal("expected error when GEMINI_API_KEY is unset and DryRun=false, got nil")
		}
	})

	t.Run("missing API key DryRun=true suppresses error", func(t *testing.T) {
		// Same setup: backend construction fails, but DryRun bypasses the error gate.
		withEnv(t, "GEMINI_API_KEY", "")
		withEnv(t, "GOOGLE_API_KEY", "")
		got, err := resolveLLMBackend(Config{Provider: "gemini", DryRun: true}, noop)
		if err != nil {
			t.Fatalf("DryRun must suppress backend error, got: %v", err)
		}
		// nil backend is acceptable in dry-run; the runner checks DryRun
		// before any LLM call.
		_ = got
	})
}

func TestProviderAliases_NoCollision(t *testing.T) {
	for _, alias := range []string{"haiku", "sonnet", "opus"} {
		for _, provider := range []string{"anthropic", "openai", "gemini"} {
			model := resolveModelForProvider(canonicalKind(provider), alias)
			if cost := ComputeCostUSDx10000(model, 1_000_000, 1_000_000); cost == 0 {
				t.Errorf("%s alias %q resolves to %q which has no pricing entry", provider, alias, model)
			}
		}
	}
	if !strings.Contains(PricingLastUpdated(), "-") {
		t.Errorf("PricingLastUpdated() %q is not in YYYY-MM-DD form", PricingLastUpdated())
	}
}
