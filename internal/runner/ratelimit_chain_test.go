package runner

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// always429 records the model and time of every call and rate-limits each.
type always429 struct {
	mu     sync.Mutex
	models []string
	at     []time.Time
}

func (b *always429) Run(_ context.Context, req RunRequest) (*RunResult, error) {
	b.mu.Lock()
	b.models = append(b.models, req.Model)
	b.at = append(b.at, time.Now())
	b.mu.Unlock()
	return nil, errors.New("HTTP 429: rate limit exceeded")
}

// TestTierFallbackChain_StaysInProviderFamily: for every tier and every
// provider kind, the chain starts at the premium model and every later step
// is one of the tier's own fallbacks whose models config family is the
// provider's. Every such fallback is kept.
func TestTierFallbackChain_StaysInProviderFamily(t *testing.T) {
	cfg := models.Load()
	family := map[BackendKind]string{
		BackendAnthropic: "anthropic", BackendClaudeCLI: "anthropic",
		BackendOpenAI: "openai",
		BackendGemini: "google", BackendGeminiCLI: "google",
	}
	for name, tier := range cfg.Tiers {
		for kind, fam := range family {
			chain := tierFallbackChain(tier.Premium, providerFamily(string(kind)))
			if len(chain) == 0 || chain[0] != tier.Premium {
				t.Fatalf("%s/%s: chain %v does not start at %s", name, kind, chain, tier.Premium)
			}
			for _, step := range []string{tier.Fallback, tier.Free} {
				if step == "" || step == tier.Premium {
					continue
				}
				inFamily := cfg.Models[cfg.Resolve(step)].Family == fam
				if got := slices.Contains(chain[1:], step); got != inFamily {
					t.Errorf("%s/%s: step %s (family %q) in chain = %v, want %v",
						name, kind, step, cfg.Models[cfg.Resolve(step)].Family, got, inFamily)
				}
			}
		}
	}
}

// TestRateLimited_ChainStepsEverySecondRetry: a tier-premium model that is
// always rate-limited gets 5 retries in all, stepping down one model on
// every second retry, and every retry waits a backoff first.
func TestRateLimited_ChainStepsEverySecondRetry(t *testing.T) {
	fastBackoff(t)
	t.Setenv("HIVE_RPM_DEFAULT", "6000")
	ResetRateLimitState()
	chain := []string{"claude-opus-4-6", "claude-sonnet-4-5", "claude-haiku-4-5-20251001"}
	userTiers(t, map[string]models.TierSlots{"rl-test": {Premium: chain[0], Fallback: chain[1], Free: chain[2]}})

	inner := &always429{}
	wrapped := NewRateLimitedBackend(inner, "anthropic")
	wrapped.maxWall = time.Minute
	if _, err := wrapped.Run(context.Background(), RunRequest{Model: chain[0]}); err == nil {
		t.Fatal("expected retry exhaustion")
	}

	if want := wrapped.maxRetry + 1; len(inner.models) != want {
		t.Fatalf("calls = %d %v, want %d (1 + %d retries)", len(inner.models), inner.models, want, wrapped.maxRetry)
	}
	for k, got := range inner.models {
		if want := chain[min(k/2, len(chain)-1)]; got != want {
			t.Errorf("call %d model = %s, want %s (sequence %v)", k, got, want, inner.models)
		}
	}
	for k := 1; k < len(inner.at); k++ {
		if gap := inner.at[k].Sub(inner.at[k-1]); gap < backoffBase {
			t.Errorf("retry %d went out after %v, want a backoff of at least %v", k, gap, backoffBase)
		}
	}
}

// TestRateLimited_NoCrossProviderStep: the synthesist tier's fallbacks are a
// Gemini and an OpenAI model, which an Anthropic backend cannot serve.
func TestRateLimited_NoCrossProviderStep(t *testing.T) {
	fastBackoff(t)
	t.Setenv("HIVE_RPM_DEFAULT", "6000")
	ResetRateLimitState()
	cfg := models.Load()
	premium := cfg.Tiers["synthesist"].Premium

	inner := &always429{}
	wrapped := NewRateLimitedBackend(inner, "anthropic")
	wrapped.maxWall = time.Minute
	_, _ = wrapped.Run(context.Background(), RunRequest{Model: premium})
	for _, m := range inner.models {
		if f := cfg.Models[cfg.Resolve(m)].Family; f != "anthropic" {
			t.Errorf("anthropic backend was sent %s (family %q); sequence %v", m, f, inner.models)
		}
	}
}

// TestRESTStatusErrors_503AndRetryAfter drives the real OpenAI and Gemini
// backends and the Anthropic SDK against a stub. A 503 whose body names no
// rate limit must still count as one, and a 429's Retry-After header must set
// the next wait.
func TestRESTStatusErrors_503AndRetryAfter(t *testing.T) {
	const retryAfter = 7 * time.Second
	stub := func(status int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if status == http.StatusTooManyRequests {
				w.Header().Set("Retry-After", "7")
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"try later"}}`))
		}))
	}
	registry := &ToolRegistry{Handlers: map[string]ToolHandler{}}
	backends := map[string]func(url string) (LLMBackend, error){
		"openai": func(url string) (LLMBackend, error) {
			t.Setenv("OPENAI_API_KEY", "k")
			t.Setenv("OPENAI_BASE_URL", url)
			return NewOpenAIBackend(Config{})
		},
		"gemini": func(url string) (LLMBackend, error) {
			t.Setenv("GEMINI_API_KEY", "k")
			t.Setenv("GEMINI_BASE_URL", url)
			return NewGeminiBackend(Config{})
		},
	}
	call := func(name, url string) error {
		if name == "anthropic" {
			cr := &ClaudeRunner{
				Client:     anthropic.NewClient(option.WithBaseURL(url), option.WithAPIKey("k"), option.WithMaxRetries(0)),
				Model:      "claude-sonnet-4-6",
				Registry:   registry,
				MaxTurns:   1,
				MaxTokens:  16,
				PerCallTTL: 5 * time.Second,
			}
			_, err := cr.Run(context.Background(), "hi")
			return err
		}
		b, err := backends[name](url)
		if err != nil {
			t.Fatal(err)
		}
		_, err = b.Run(context.Background(), RunRequest{Prompt: "hi", Registry: registry})
		return err
	}
	for _, name := range []string{"openai", "gemini", "anthropic"} {
		t.Run(name, func(t *testing.T) {
			srv503 := stub(http.StatusServiceUnavailable)
			defer srv503.Close()
			if err := call(name, srv503.URL); !isRateLimitError(err) {
				t.Errorf("503 not treated as a rate limit: %v", err)
			}

			srv429 := stub(http.StatusTooManyRequests)
			defer srv429.Close()
			err := call(name, srv429.URL)
			if got := extractRetryAfter(err); got != retryAfter {
				t.Errorf("Retry-After read as %v, want %v (err %v)", got, retryAfter, err)
			}
			if got := computeBackoff(0, err, time.Minute); got != retryAfter {
				t.Errorf("backoff = %v, want the header's %v", got, retryAfter)
			}
		})
	}
}

// TestParseRetryAfter_HTTPDate: the header may be a date instead of seconds.
func TestParseRetryAfter_HTTPDate(t *testing.T) {
	const ahead = 30 * time.Second
	got := parseRetryAfter(time.Now().Add(ahead).UTC().Format(http.TimeFormat))
	// HTTP-date has whole-second precision, so allow the truncated second.
	if got <= ahead-2*time.Second || got > ahead {
		t.Errorf("parseRetryAfter(now+%v) = %v", ahead, got)
	}
	if got := parseRetryAfter(time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat)); got != 0 {
		t.Errorf("a past date gave %v, want 0", got)
	}
}

// Tiers that share a premium model give the fallback chain of the first of
// them in name order, on every call, so the models a rate-limited call
// steps down to are the same from run to run, whatever order the map gives.
func TestTierFallbackChain_SharedPremiumTakesTheFirstTierByName(t *testing.T) {
	userTiers(t, map[string]models.TierSlots{
		"zeta":  {Premium: "claude-opus-5-5", Fallback: "claude-sonnet-5-5", Free: "claude-haiku-4-5"},
		"alpha": {Premium: "claude-opus-5-5", Fallback: "claude-opus-5", Free: "claude-sonnet-5"},
	})
	want := []string{"claude-opus-5-5", "claude-opus-5", "claude-sonnet-5"}
	for i := range 100 {
		if got := tierFallbackChain("claude-opus-5-5", "anthropic"); !slices.Equal(got, want) {
			t.Fatalf("call %d: chain %v; want alpha's, %v, the first tier by name", i, got, want)
		}
	}
}
