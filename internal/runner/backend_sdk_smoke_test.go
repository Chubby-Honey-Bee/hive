package runner

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestSDKBackendSmoke exercises SDKBackend.Run with a trivial prompt.
// Skipped when ANTHROPIC_API_KEY is not set (most CI configurations).
// Run locally to verify SDK and CLI backends are both healthy before
// merging CLI-backend changes.
func TestSDKBackendSmoke(t *testing.T) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY not set — skipping SDK smoke test")
	}
	if os.Getenv("HIVE_SKIP_SDK_SMOKE") != "" {
		t.Skip("HIVE_SKIP_SDK_SMOKE set")
	}

	b := &SDKBackend{APIKey: key}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := b.Run(ctx, RunRequest{
		Prompt: "Respond with exactly the word 'pong' and nothing else.",
		Model:  "sonnet",
		TTL:    30 * time.Second,
	})
	if err != nil {
		t.Fatalf("SDKBackend.Run: %v", err)
	}
	if res == nil {
		t.Fatal("nil RunResult")
	}
	if res.FinalText == "" {
		t.Fatal("empty FinalText")
	}
	t.Logf("ok: stop=%s turns=%d in=%d out=%d result=%q",
		res.StopReason, res.Turns, res.InputTokens, res.OutputTokens, res.FinalText)
}

// TestBackendParity_WhenBothAvailable is a soft parity check: if both the
// SDK (via ANTHROPIC_API_KEY) and the CLI (via `claude` on PATH) are
// available, both must return non-empty FinalText for the same trivial
// prompt. We do NOT assert byte-equal outputs — LLM responses are
// non-deterministic. The check is "both backends are wired up and both
// produce a plausible result for the same input."
func TestBackendParity_WhenBothAvailable(t *testing.T) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY not set — parity test requires both backends")
	}
	cli, err := NewCLIBackend(Config{})
	if err != nil {
		t.Skipf("CLI backend unavailable: %v", err)
	}
	sdk := &SDKBackend{APIKey: key}

	prompt := "Respond with exactly the word 'pong' and nothing else."
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	sdkRes, err := sdk.Run(ctx, RunRequest{Prompt: prompt, Model: "sonnet", TTL: 30 * time.Second})
	if err != nil {
		t.Fatalf("SDK: %v", err)
	}
	if sdkRes.FinalText == "" {
		t.Fatal("SDK FinalText empty")
	}

	cliRes, err := cli.Run(ctx, RunRequest{Prompt: prompt, Model: "sonnet", TTL: 30 * time.Second})
	if err != nil {
		t.Fatalf("CLI: %v", err)
	}
	if cliRes.FinalText == "" {
		t.Fatal("CLI FinalText empty")
	}

	t.Logf("SDK: %q   CLI: %q", sdkRes.FinalText, cliRes.FinalText)
}
