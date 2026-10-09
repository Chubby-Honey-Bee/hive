package runner

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestCLIBackendSmoke exercises CLIBackend.Run against the real claude CLI
// with a trivial prompt. It makes a paid model call, so it is opt-in: set
// HIVE_RUN_CLI_SMOKE=1. It also cannot run nested inside a Claude Code
// session (the CLI refuses), so keeping it opt-in keeps a plain `go test
// ./...` green when Claude Code drives the suite.
func TestCLIBackendSmoke(t *testing.T) {
	if os.Getenv("HIVE_RUN_CLI_SMOKE") == "" {
		t.Skip("opt-in: set HIVE_RUN_CLI_SMOKE=1 to run the live claude CLI smoke (paid model call)")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Fatal("HIVE_RUN_CLI_SMOKE is set but the claude CLI is not in PATH")
	}

	b, err := NewCLIBackend(Config{})
	if err != nil {
		t.Fatalf("NewCLIBackend: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := b.Run(ctx, RunRequest{
		Prompt: "Respond with exactly the word 'pong' and nothing else.",
	})
	if err != nil {
		t.Fatalf("CLIBackend.Run: %v", err)
	}
	if res == nil {
		t.Fatal("nil RunResult")
	}
	if res.FinalText == "" {
		t.Fatal("empty FinalText")
	}
	if res.StopReason == "" {
		t.Logf("warning: empty StopReason (got %+v)", res)
	}
	t.Logf("ok: stop=%s turns=%d in=%d out=%d result=%q",
		res.StopReason, res.Turns, res.InputTokens, res.OutputTokens, res.FinalText)
}
