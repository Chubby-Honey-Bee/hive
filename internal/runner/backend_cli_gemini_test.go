package runner

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
)

// fakeGeminiScript writes a tiny shell script to a temp dir and returns its
// path. args is any text to echo; exitCode controls process exit status.
func fakeGeminiScript(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/gemini"
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write fake gemini script: %v", err)
	}
	return path
}

func TestGeminiCLIBackend_Run_HappyPath(t *testing.T) {
	cli := fakeGeminiScript(t, `echo "hello from gemini"`)
	b := &GeminiCLIBackend{CLIPath: cli}

	res, err := b.Run(context.Background(), RunRequest{Prompt: "say hi"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res == nil {
		t.Fatal("nil RunResult")
	}
	if !strings.Contains(res.FinalText, "hello from gemini") {
		t.Errorf("FinalText = %q; expected to contain 'hello from gemini'", res.FinalText)
	}
	if res.StopReason != "end_turn" {
		t.Errorf("StopReason = %q; want end_turn", res.StopReason)
	}
	if res.Turns != 1 {
		t.Errorf("Turns = %d; want 1", res.Turns)
	}
	if res.InputTokens != 0 || res.OutputTokens != 0 {
		t.Errorf("tokens = %d/%d; want 0/0", res.InputTokens, res.OutputTokens)
	}
}

func TestGeminiCLIBackend_Run_WithModel(t *testing.T) {
	// The script echoes its args so we can verify -m was injected.
	cli := fakeGeminiScript(t, `echo "args: $@"`)
	b := &GeminiCLIBackend{CLIPath: cli}

	res, err := b.Run(context.Background(), RunRequest{
		Prompt: "hello",
		Model:  "haiku",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// haiku resolves to gemini-2.5-flash
	if !strings.Contains(res.FinalText, "gemini-2.5-flash") {
		t.Errorf("FinalText = %q; expected -m gemini-2.5-flash to appear", res.FinalText)
	}
}

func TestGeminiCLIBackend_Run_WithSystemPrompt(t *testing.T) {
	// Script reads stdin and echoes it back.
	cli := fakeGeminiScript(t, `cat`)
	b := &GeminiCLIBackend{CLIPath: cli}

	res, err := b.Run(context.Background(), RunRequest{
		Prompt: "the prompt",
		System: "system instructions",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// When System is set, stdin is "System\n\nPrompt"
	if !strings.Contains(res.FinalText, "system instructions") {
		t.Errorf("FinalText = %q; expected system instructions in stdin", res.FinalText)
	}
	if !strings.Contains(res.FinalText, "the prompt") {
		t.Errorf("FinalText = %q; expected prompt in stdin", res.FinalText)
	}
}

func TestGeminiCLIBackend_Run_WithProjectDir(t *testing.T) {
	// Script prints its working directory.
	cli := fakeGeminiScript(t, `pwd`)
	projDir := t.TempDir()
	b := &GeminiCLIBackend{CLIPath: cli, ProjectDir: projDir}

	res, err := b.Run(context.Background(), RunRequest{Prompt: "test"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.FinalText, projDir) {
		t.Errorf("FinalText = %q; expected working dir %q", res.FinalText, projDir)
	}
}

func TestGeminiCLIBackend_Run_CLIError(t *testing.T) {
	// Script exits non-zero with a stderr message.
	cli := fakeGeminiScript(t, `echo "auth failed" >&2; exit 1`)
	b := &GeminiCLIBackend{CLIPath: cli}

	_, err := b.Run(context.Background(), RunRequest{Prompt: "fail"})
	if err == nil {
		t.Fatal("expected error when CLI exits non-zero")
	}
	if !strings.Contains(err.Error(), "gemini CLI") {
		t.Errorf("error = %v; expected 'gemini CLI' prefix", err)
	}
	if !strings.Contains(err.Error(), "auth failed") {
		t.Errorf("error = %v; expected stderr 'auth failed' in message", err)
	}
}

func TestGeminiCLIBackend_Run_NonExistentCLI(t *testing.T) {
	b := &GeminiCLIBackend{CLIPath: "/nonexistent/gemini-binary-xyz"}

	_, err := b.Run(context.Background(), RunRequest{Prompt: "test"})
	if err == nil {
		t.Fatal("expected error when CLI binary does not exist")
	}
}

func TestGeminiCLIBackend_Run_EmptyOutput(t *testing.T) {
	// Script produces no output: an empty answer is not an answer, so the
	// call fails naming it rather than completing the node with nothing.
	cli := fakeGeminiScript(t, `exit 0`)
	b := &GeminiCLIBackend{CLIPath: cli}

	res, err := b.Run(context.Background(), RunRequest{Prompt: "quiet"})
	if err == nil || !strings.Contains(err.Error(), "empty reply") {
		t.Fatalf("Run error = %v, want an empty-reply error", err)
	}
	if res == nil || res.FinalText != "" {
		t.Errorf("result = %+v; want the empty result returned with the error", res)
	}
}

// TestGeminiCLIBackend_Run_DeterministicWarnsAndSucceeds verifies that when
// Temperature or Seed is set, the CLI backend logs a warning (deduplicated)
// but still succeeds — it never errors on these fields.
func TestGeminiCLIBackend_Run_DeterministicWarnsAndSucceeds(t *testing.T) {
	cli := fakeGeminiScript(t, `echo "result"`)
	// Reset the once so this test is independent regardless of parallel runs.
	// We can't reset a sync.Once directly; instead we verify the run succeeds.
	b := &GeminiCLIBackend{CLIPath: cli}

	temp := 0.0
	seed := int64(99)
	res, err := b.Run(context.Background(), RunRequest{
		Prompt:      "test",
		Temperature: &temp,
		Seed:        &seed,
	})
	if err != nil {
		t.Fatalf("Run with Temperature+Seed should succeed on CLI backend: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil RunResult")
	}
}

// The prompt goes once, on stdin behind the system prompt, as runner.md
// names for this backend: one message, one channel, so a persona-carrying
// node does not send it twice and a long prompt never meets the platform's
// argument-length limit.
func TestGeminiCLIBackend_SendsThePromptOnceOnStdin(t *testing.T) {
	// The script echoes stdin only, so the output is exactly what reached
	// stdin — argv would not appear in it.
	b := &GeminiCLIBackend{CLIPath: fakeGeminiScript(t, `cat`)}

	res, err := b.Run(context.Background(), RunRequest{Prompt: "THE-PROMPT"})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(res.FinalText, "THE-PROMPT"); n != 1 {
		t.Errorf("prompt reached stdin %d times, want exactly 1 (got %q)", n, res.FinalText)
	}

	res, err = b.Run(context.Background(), RunRequest{System: "THE-PERSONA", Prompt: "THE-PROMPT"})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(res.FinalText, "THE-PROMPT"); n != 1 {
		t.Errorf("with a persona the prompt reached stdin %d times, want exactly 1 (got %q)", n, res.FinalText)
	}
	if !strings.Contains(res.FinalText, "THE-PERSONA") {
		t.Errorf("the persona did not reach stdin: %q", res.FinalText)
	}
}

// TestGeminiCLIBackend_WarnsThatTheCapHasNoEffect: the gemini CLI is not
// passed an output cap, so a request carrying one warns, as the claude CLI
// backend does.
func TestGeminiCLIBackend_WarnsThatTheCapHasNoEffect(t *testing.T) {
	_cliMaxTokensWarnOnce = sync.Once{}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	_, runErr := (&GeminiCLIBackend{CLIPath: fakeGeminiScript(t, `echo ok`)}).Run(
		context.Background(), RunRequest{Prompt: "p", MaxTokens: 4096})
	os.Stderr = orig
	_ = w.Close()
	stderr, _ := io.ReadAll(r)
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !strings.Contains(string(stderr), "max_output_tokens have no effect on CLI backends") {
		t.Errorf("stderr = %q, want the max-output-tokens warning", stderr)
	}
}
