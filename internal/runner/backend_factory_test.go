package runner

import (
	"os"
	"strings"
	"testing"
)

func TestNewRawBackend_KnownKinds(t *testing.T) {
	t.Setenv("HIVE_DISABLE_RATE_LIMIT", "1")
	cases := []struct {
		kind    BackendKind
		needKey bool
		canFail bool // true if it can fail without a CLI/key
	}{
		{BackendAnthropic, true, false},
		{BackendClaudeCLI, false, true},
		{BackendOpenAI, true, true},
		{BackendGemini, true, true},
		{BackendGeminiCLI, false, true},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			cfg := Config{}
			if tc.needKey {
				cfg.APIKey = "test-key-not-real"
			}
			_, err := newRawBackend(tc.kind, cfg)
			if err != nil && !tc.canFail {
				t.Errorf("newRawBackend(%q, ...) returned %v; expected nil", tc.kind, err)
			}
		})
	}
}

func TestNewRawBackend_UnknownKind_Errors(t *testing.T) {
	_, err := newRawBackend(BackendKind("totally-fake"), Config{})
	if err == nil {
		t.Fatal("expected error for unknown backend")
	}
	if !strings.Contains(err.Error(), "unknown backend") {
		t.Errorf("error = %v; expected 'unknown backend'", err)
	}
}

func TestNewBackend_DisableRateLimit_ReturnsRaw(t *testing.T) {
	t.Setenv("HIVE_DISABLE_RATE_LIMIT", "1")
	b, err := NewBackend(BackendAnthropic, Config{APIKey: "k"})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	// When disabled, the returned backend is the bare SDKBackend, not a
	// *RateLimitedBackend wrapper. Check by type assertion.
	if _, ok := b.(*RateLimitedBackend); ok {
		t.Error("expected raw SDKBackend (rate-limit disabled), got *RateLimitedBackend")
	}
	if _, ok := b.(*SDKBackend); !ok {
		t.Errorf("expected *SDKBackend, got %T", b)
	}
}

func TestNewBackend_DefaultWrapsWithRateLimit(t *testing.T) {
	// Make sure we don't accidentally inherit the env from another test.
	_ = os.Unsetenv("HIVE_DISABLE_RATE_LIMIT")

	b, err := NewBackend(BackendAnthropic, Config{APIKey: "k"})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	if _, ok := b.(*RateLimitedBackend); !ok {
		t.Errorf("expected *RateLimitedBackend wrapper, got %T", b)
	}
}

func TestNewBackend_UnknownKind_Errors(t *testing.T) {
	_, err := NewBackend(BackendKind("nonsense"), Config{})
	if err == nil {
		t.Fatal("expected error for unknown backend")
	}
}
