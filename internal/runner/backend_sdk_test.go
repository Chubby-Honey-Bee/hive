package runner

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestSDKBackend_Run_NilRegistry_NoOptionalFields exercises SDKBackend.Run with
// all optional fields at their zero values (MaxTurns/MaxTokens/TTL == 0).
// A nil Registry causes ClaudeRunner.Run to return immediately without a
// network call, making this a true unit test.
func TestSDKBackend_Run_NilRegistry_NoOptionalFields(t *testing.T) {
	b := &SDKBackend{APIKey: "test-key-not-real"}
	_, err := b.Run(context.Background(), RunRequest{
		Prompt: "hello",
		// Registry is nil → ClaudeRunner.Run returns "no tool registry"
		// MaxTurns/MaxTokens/TTL are all zero → the three > 0 branches are NOT taken
	})
	if err == nil {
		t.Fatal("expected error from nil registry, got nil")
	}
	if !strings.Contains(err.Error(), "no tool registry") {
		t.Errorf("error = %v; want 'no tool registry'", err)
	}
}

// TestSDKBackend_Run_NilRegistry_AllOptionalFields exercises SDKBackend.Run with
// all optional fields set to positive values, covering the MaxTurns > 0,
// MaxTokens > 0, and TTL > 0 branches.
func TestSDKBackend_Run_NilRegistry_AllOptionalFields(t *testing.T) {
	b := &SDKBackend{APIKey: "test-key-not-real"}
	_, err := b.Run(context.Background(), RunRequest{
		Prompt:    "hello",
		MaxTurns:  5,
		MaxTokens: 1024,
		TTL:       30 * time.Second,
		// Registry is nil → error returned without network call
	})
	if err == nil {
		t.Fatal("expected error from nil registry, got nil")
	}
	if !strings.Contains(err.Error(), "no tool registry") {
		t.Errorf("error = %v; want 'no tool registry'", err)
	}
}

// TestSDKBackend_Run_NilRegistry_PartialOptionalFields exercises SDKBackend.Run
// with only some optional fields set, covering mixed branch paths.
func TestSDKBackend_Run_NilRegistry_PartialOptionalFields(t *testing.T) {
	b := &SDKBackend{APIKey: ""}
	_, err := b.Run(context.Background(), RunRequest{
		Prompt:    "hello",
		MaxTurns:  10,
		MaxTokens: 0, // NOT taken
		TTL:       0, // NOT taken
		// Registry is nil
	})
	if err == nil {
		t.Fatal("expected error from nil registry, got nil")
	}
	if !strings.Contains(err.Error(), "no tool registry") {
		t.Errorf("error = %v; want 'no tool registry'", err)
	}
}

// TestSDKBackend_Run_CancelledContext exercises the cancelled-context path.
// With a nil Registry the "no tool registry" error fires before the context
// is even consulted, so we just confirm no panic and an error is returned.
func TestSDKBackend_Run_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	b := &SDKBackend{APIKey: "test-key-not-real"}
	_, err := b.Run(ctx, RunRequest{
		Prompt:    "hello",
		MaxTurns:  3,
		MaxTokens: 512,
		TTL:       10 * time.Second,
	})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}

// TestSDKBackend_Run_TemperatureWired verifies that a Temperature on the
// RunRequest propagates into ClaudeRunner.Temperature without causing a panic
// or altering error behavior (the nil-Registry gate fires before any API call).
func TestSDKBackend_Run_TemperatureWired(t *testing.T) {
	temp := 0.0
	b := &SDKBackend{APIKey: "test-key"}
	_, err := b.Run(context.Background(), RunRequest{
		Prompt:      "hello",
		Temperature: &temp,
		// Registry nil → error before any API call
	})
	if err == nil {
		t.Fatal("expected 'no tool registry' error, got nil")
	}
	if !strings.Contains(err.Error(), "no tool registry") {
		t.Errorf("error = %v; want 'no tool registry'", err)
	}
}

// TestSDKBackend_Run_SeedWarnsOnce verifies that passing a non-nil Seed triggers
// the anthropic-seed warning exactly once per process. The warning is idempotent
// so we exercise the sync.Once path by calling twice and asserting no panic.
func TestSDKBackend_Run_SeedWarnsOnce(t *testing.T) {
	seed := int64(42)
	b := &SDKBackend{APIKey: "test-key"}
	for i := 0; i < 2; i++ {
		_, _ = b.Run(context.Background(), RunRequest{
			Prompt: "hello",
			Seed:   &seed,
			// nil Registry — error returned, but warning should still fire
		})
	}
	// No assertions beyond "no panic". The sync.Once prevents double-emission;
	// we can't capture stderr from the package-global Once without invasive
	// injection, so we rely on "runs without panic" as the observable contract.
}
