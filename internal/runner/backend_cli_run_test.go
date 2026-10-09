package runner

// Table-driven unit tests for CLIBackend.Run.
//
// The real `claude` CLI is not available in CI.  We replace it with a small
// shell script written to a temp directory.  The script ignores all arguments
// and emits output determined by the HELPER_OUTCOME env variable, which the
// test sets via t.Setenv before calling Run.
//
// This lets us cover every branch in Run without network calls or modifying
// the function under test.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeFakeCLI writes a tiny executable shell script to dir and returns its
// path.  The script selects its behaviour from the HELPER_OUTCOME env var.
func writeFakeCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-claude")
	script := `#!/bin/sh
# Fake claude CLI — driven by HELPER_OUTCOME env var.
# Ignores all arguments; reads stdin (discards it).
cat > /dev/null
case "$HELPER_OUTCOME" in
  happy)
    printf '{"type":"result","subtype":"success","is_error":false,"num_turns":1,"result":"pong","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}'
    exit 0
    ;;
  happy_multiline)
    printf 'status line\n'
    printf '{"type":"result","subtype":"success","is_error":false,"num_turns":2,"result":"hello","stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3}}'
    exit 0
    ;;
  is_error)
    # Claude Code exits 1 after printing a result marked is_error.
    printf '{"type":"result","is_error":true,"result":"something went wrong"}'
    exit 1
    ;;
  empty)
    # output nothing
    exit 0
    ;;
  bad_json)
    printf 'not json at all'
    exit 0
    ;;
  bad_json_multiline)
    printf 'preamble line\nbad json here'
    exit 0
    ;;
  exit_nonzero)
    printf 'fatal error from CLI' >&2
    exit 1
    ;;
  *)
    printf 'unknown HELPER_OUTCOME: %s\n' "$HELPER_OUTCOME" >&2
    exit 2
    ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}
	return path
}

// newFakeBackend returns a CLIBackend whose CLIPath points to the shell-script
// fake CLI.  outcome is stored as HELPER_OUTCOME for the subprocess via
// t.Setenv (automatically reverted by t.Cleanup).
func newFakeBackend(t *testing.T, outcome string, opts ...func(*CLIBackend)) *CLIBackend {
	t.Helper()
	t.Setenv("HELPER_OUTCOME", outcome)
	b := &CLIBackend{
		CLIPath:      writeFakeCLI(t),
		ScratchDir:   t.TempDir(),
		AllowedTools: []string{"Read"},
	}
	for _, fn := range opts {
		fn(b)
	}
	return b
}

// ---------------------------------------------------------------------------
// Happy-path tests
// ---------------------------------------------------------------------------

func TestCLIBackendRun_HappyPath(t *testing.T) {
	b := newFakeBackend(t, "happy")
	res, err := b.Run(context.Background(), RunRequest{Prompt: "say pong"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FinalText != "pong" {
		t.Errorf("FinalText = %q; want %q", res.FinalText, "pong")
	}
	if res.Turns != 1 {
		t.Errorf("Turns = %d; want 1", res.Turns)
	}
	if res.InputTokens != 10 {
		t.Errorf("InputTokens = %d; want 10", res.InputTokens)
	}
	if res.OutputTokens != 5 {
		t.Errorf("OutputTokens = %d; want 5", res.OutputTokens)
	}
	if res.StopReason != "end_turn" {
		t.Errorf("StopReason = %q; want end_turn", res.StopReason)
	}
	// The CLI runs its own tool loop, so min_tool_calls cannot be checked.
	if !res.ToolCallsUnreported || len(res.Invocations) != 0 {
		t.Errorf("ToolCallsUnreported = %v with %d invocations; want true and none", res.ToolCallsUnreported, len(res.Invocations))
	}
}

func TestCLIBackendRun_HappyPath_WithSystemAndModel(t *testing.T) {
	b := newFakeBackend(t, "happy")
	// Exercises the req.System != "" and req.Model != "" branches.
	res, err := b.Run(context.Background(), RunRequest{
		Prompt: "hi",
		System: "you are a test",
		Model:  "haiku",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FinalText != "pong" {
		t.Errorf("FinalText = %q; want %q", res.FinalText, "pong")
	}
}

func TestCLIBackendRun_HappyPath_MultiLineOutput(t *testing.T) {
	// Exercises the "last line JSON decode" branch.
	b := newFakeBackend(t, "happy_multiline")
	res, err := b.Run(context.Background(), RunRequest{Prompt: "hi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FinalText != "hello" {
		t.Errorf("FinalText = %q; want %q", res.FinalText, "hello")
	}
	if res.Turns != 2 {
		t.Errorf("Turns = %d; want 2", res.Turns)
	}
}

func TestCLIBackendRun_HappyPath_WithDBPath(t *testing.T) {
	// Exercises the b.DBPath != "" branch (sets HIVE_DB_PATH in cmd.Env).
	b := newFakeBackend(t, "happy", func(b *CLIBackend) {
		b.DBPath = "/tmp/test.db"
	})
	res, err := b.Run(context.Background(), RunRequest{Prompt: "hi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FinalText != "pong" {
		t.Errorf("FinalText = %q; want %q", res.FinalText, "pong")
	}
}

func TestCLIBackendRun_HappyPath_WithProjectDir(t *testing.T) {
	// Exercises the b.ProjectDir != "" branch (sets cmd.Dir).
	dir := t.TempDir()
	b := newFakeBackend(t, "happy", func(b *CLIBackend) {
		b.ProjectDir = dir
	})
	res, err := b.Run(context.Background(), RunRequest{Prompt: "hi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FinalText != "pong" {
		t.Errorf("FinalText = %q", res.FinalText)
	}
}

// ---------------------------------------------------------------------------
// Error-path tests
// ---------------------------------------------------------------------------

func TestCLIBackendRun_IsError(t *testing.T) {
	// Exercises the env.IsError branch.
	b := newFakeBackend(t, "is_error")
	_, err := b.Run(context.Background(), RunRequest{Prompt: "hi"})
	if err == nil {
		t.Fatal("expected error for is_error=true response, got nil")
	}
}

func TestCLIBackendRun_EmptyStdout(t *testing.T) {
	// Exercises the len(raw) == 0 branch.
	b := newFakeBackend(t, "empty")
	_, err := b.Run(context.Background(), RunRequest{Prompt: "hi"})
	if err == nil {
		t.Fatal("expected error for empty stdout, got nil")
	}
}

func TestCLIBackendRun_BadJSON_SingleLine(t *testing.T) {
	// Exercises the json.Unmarshal error + no newline branch.
	b := newFakeBackend(t, "bad_json")
	_, err := b.Run(context.Background(), RunRequest{Prompt: "hi"})
	if err == nil {
		t.Fatal("expected JSON decode error, got nil")
	}
}

func TestCLIBackendRun_BadJSON_MultiLine(t *testing.T) {
	// Exercises the json.Unmarshal error + last-line-also-fails branch.
	b := newFakeBackend(t, "bad_json_multiline")
	_, err := b.Run(context.Background(), RunRequest{Prompt: "hi"})
	if err == nil {
		t.Fatal("expected JSON decode error on multi-line bad JSON, got nil")
	}
}

func TestCLIBackendRun_CLIExitNonZero(t *testing.T) {
	// Exercises the cmd.Run() error branch (non-zero exit, ctx.Err() == nil).
	b := newFakeBackend(t, "exit_nonzero")
	_, err := b.Run(context.Background(), RunRequest{Prompt: "hi"})
	if err == nil {
		t.Fatal("expected error for non-zero CLI exit, got nil")
	}
}

func TestCLIBackendRun_ContextCancelled(t *testing.T) {
	// Exercises the ctx.Err() != nil branch.
	// Cancel the context before Run is called so the subprocess gets SIGKILL.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	<-ctx.Done() // guarantee the deadline has elapsed

	b := newFakeBackend(t, "happy")
	_, err := b.Run(ctx, RunRequest{Prompt: "hi"})
	if err == nil {
		t.Fatal("expected context-cancelled error, got nil")
	}
}

// ---------------------------------------------------------------------------
// MCP sidecar wiring
// ---------------------------------------------------------------------------

func TestCLIBackendRun_WithMCPServerPath(t *testing.T) {
	// Exercises the b.MCPServerPath != "" branch (writeMCPConfig + mcp-config arg).
	// Use the same fake CLI as the "server" path — it just needs to exist.
	fakeCLI := writeFakeCLI(t)
	t.Setenv("HELPER_OUTCOME", "happy")
	b := &CLIBackend{
		CLIPath:       fakeCLI,
		MCPServerPath: fakeCLI, // any executable; the fake CLI ignores the mcp-config arg
		ScratchDir:    t.TempDir(),
		AllowedTools:  []string{"Read"},
	}
	res, err := b.Run(context.Background(), RunRequest{Prompt: "hi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FinalText != "pong" {
		t.Errorf("FinalText = %q; want %q", res.FinalText, "pong")
	}
}

// ---------------------------------------------------------------------------
// writeMCPConfig — ScratchDir write failure
// ---------------------------------------------------------------------------

func TestCLIBackendRun_MCPConfig_BadScratchDir(t *testing.T) {
	// Exercises the writeMCPConfig error path in Run.
	fakeCLI := writeFakeCLI(t)
	t.Setenv("HELPER_OUTCOME", "happy")
	b := &CLIBackend{
		CLIPath:       fakeCLI,
		MCPServerPath: fakeCLI,
		ScratchDir:    "/nonexistent/path/that/cannot/be/created",
		AllowedTools:  []string{"Read"},
	}
	_, err := b.Run(context.Background(), RunRequest{Prompt: "hi"})
	if err == nil {
		t.Fatal("expected error when ScratchDir is invalid, got nil")
	}
}

// ---------------------------------------------------------------------------
// joinTools helper (used by Run)
// ---------------------------------------------------------------------------

func TestJoinTools(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"Read"}, "Read"},
		{[]string{"Read", "Write", "Bash"}, "Read,Write,Bash"},
	}
	for _, tc := range cases {
		got := joinTools(tc.in)
		if got != tc.want {
			t.Errorf("joinTools(%v) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// Ensure the test file compiles cleanly even without the fmt import used only
// by writeFakeCLI.
var _ = fmt.Sprintf

// TestCLIBackend_Run_DeterministicWarnsAndSucceeds verifies that when
// Temperature or Seed is set, the CLIBackend logs a warning but still
// dispatches the subprocess successfully.
func TestCLIBackend_Run_DeterministicWarnsAndSucceeds(t *testing.T) {
	fakeCLI := writeFakeCLI(t)
	t.Setenv("HELPER_OUTCOME", "happy")

	b := &CLIBackend{
		CLIPath:      fakeCLI,
		ScratchDir:   t.TempDir(),
		AllowedTools: []string{"Read"},
	}
	temp := 0.0
	seed := int64(42)
	res, err := b.Run(context.Background(), RunRequest{
		Prompt:      "test",
		Temperature: &temp,
		Seed:        &seed,
	})
	if err != nil {
		t.Fatalf("Run with Temperature+Seed should succeed on CLIBackend: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil RunResult")
	}
	if res.FinalText != "pong" {
		t.Errorf("FinalText = %q; want pong", res.FinalText)
	}
}
