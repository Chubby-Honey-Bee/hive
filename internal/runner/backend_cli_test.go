package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeClaudeScript writes a stand-in `claude` that answers with the CLI's
// JSON envelope, carrying its own arguments as the result text.
func fakeClaudeScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	body := "#!/bin/sh\nprintf '{\"result\":\"%s\",\"is_error\":false,\"num_turns\":1}\\n' \"$*\"\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// The CLI takes --max-turns, and the backend passes the request's turn cap
// to it.
func TestCLIBackend_PassesMaxTurns(t *testing.T) {
	b := &CLIBackend{CLIPath: fakeClaudeScript(t)}
	res, err := b.Run(context.Background(), RunRequest{Prompt: "hi", Model: "sonnet", MaxTurns: 7})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.FinalText, "--max-turns 7") {
		t.Fatalf("claude was called with %q, want --max-turns 7", res.FinalText)
	}
}

// Without a cap the flag is absent, so the CLI keeps its own default.
func TestCLIBackend_OmitsMaxTurnsWhenUnset(t *testing.T) {
	b := &CLIBackend{CLIPath: fakeClaudeScript(t)}
	res, err := b.Run(context.Background(), RunRequest{Prompt: "hi", Model: "sonnet"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.FinalText, "--max-turns") {
		t.Fatalf("claude was called with %q, want no --max-turns", res.FinalText)
	}
}

// `agent-run --db X` must reach the agents: otherwise the agent's own `chb
// db-write` resolves the default path under its working directory, and the
// run's findings land in a different database than its workflow rows.
func TestNewCLIBackend_ForwardsTheRunsDatabase(t *testing.T) {
	t.Setenv("HIVE_DB_PATH", "/env/hive.db")
	t.Setenv("PATH", filepath.Dir(fakeClaudeScript(t))+string(os.PathListSeparator)+os.Getenv("PATH"))
	b, err := NewCLIBackend(Config{DBPath: "/run/chosen.db"})
	if err != nil {
		t.Fatal(err)
	}
	if b.DBPath != "/run/chosen.db" {
		t.Fatalf("DBPath = %q, want the run's database", b.DBPath)
	}
}

// With no database on the config, the environment still decides.
func TestNewCLIBackend_FallsBackToTheEnvironment(t *testing.T) {
	t.Setenv("HIVE_DB_PATH", "/env/hive.db")
	t.Setenv("PATH", filepath.Dir(fakeClaudeScript(t))+string(os.PathListSeparator)+os.Getenv("PATH"))
	b, err := NewCLIBackend(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if b.DBPath != "/env/hive.db" {
		t.Fatalf("DBPath = %q, want the environment's database", b.DBPath)
	}
}

func TestCanonicalCLIModel(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "sonnet"},
		{"sonnet", "sonnet"},
		{"SONNET", "sonnet"},
		{"  sonnet  ", "sonnet"},
		{"claude-sonnet", "sonnet"},
		{"sonnet-4-6", "sonnet"},
		{"opus", "opus"},
		{"OPUS", "opus"},
		{"claude-opus", "opus"},
		{"opus-4-6", "opus"},
		{"haiku", "haiku"},
		{"haiku-4-5", "haiku"},
		{"claude-haiku", "haiku"},
		{"custom-model-id", "custom-model-id"}, // pass-through
	}
	for _, tc := range cases {
		if got := canonicalCLIModel(tc.in); got != tc.want {
			t.Errorf("canonicalCLIModel(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestWriteMCPConfig_HappyPath(t *testing.T) {
	dir := t.TempDir()
	b := &CLIBackend{
		MCPServerPath: "/usr/local/bin/chb-mcp",
		DBPath:        "/data/hive.db",
		ProjectDir:    "/projects/my-project",
		ScratchDir:    dir,
	}

	path, cleanup, err := b.writeMCPConfig()
	if err != nil {
		t.Fatalf("writeMCPConfig() unexpected error: %v", err)
	}
	defer cleanup()

	// File must exist and be non-empty.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	// Parse and verify JSON structure.
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("JSON unmarshal: %v", err)
	}

	mcpServers, ok := cfg["mcpServers"].(map[string]any)
	if !ok {
		t.Fatal("mcpServers key missing or wrong type")
	}
	swarm, ok := mcpServers["chb"].(map[string]any)
	if !ok {
		t.Fatal("mcpServers.chb key missing or wrong type")
	}
	if got := swarm["command"]; got != "/usr/local/bin/chb-mcp" {
		t.Errorf("command = %v; want /usr/local/bin/chb-mcp", got)
	}
	env, ok := swarm["env"].(map[string]any)
	if !ok {
		t.Fatal("env key missing or wrong type")
	}
	if got := env["HIVE_DB_PATH"]; got != "/data/hive.db" {
		t.Errorf("HIVE_DB_PATH = %v; want /data/hive.db", got)
	}
	if len(env) != 1 {
		t.Errorf("env = %v; want HIVE_DB_PATH alone, the one variable chb-mcp reads from it", env)
	}

	// After cleanup, file must be removed.
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected file to be removed after cleanup, got err=%v", err)
	}
}

func TestWriteMCPConfig_FileCreatedInScratchDir(t *testing.T) {
	dir := t.TempDir()
	b := &CLIBackend{ScratchDir: dir}

	path, cleanup, err := b.writeMCPConfig()
	if err != nil {
		t.Fatalf("writeMCPConfig() error: %v", err)
	}
	defer cleanup()

	if filepath.Dir(path) != dir {
		t.Errorf("file created in %q; want %q", filepath.Dir(path), dir)
	}
}

func TestWriteMCPConfig_InvalidScratchDir(t *testing.T) {
	b := &CLIBackend{ScratchDir: "/this/dir/does/not/exist/at/all"}

	path, cleanup, err := b.writeMCPConfig()
	cleanup() // always safe; no-op when error returned
	if err == nil {
		t.Errorf("expected error for non-existent ScratchDir, got path=%q", path)
	}
}

func TestWriteMCPConfig_EmptyFields(t *testing.T) {
	dir := t.TempDir()
	b := &CLIBackend{ScratchDir: dir} // MCPServerPath, DBPath, ProjectDir all empty

	path, cleanup, err := b.writeMCPConfig()
	if err != nil {
		t.Fatalf("writeMCPConfig() with empty fields: %v", err)
	}
	defer cleanup()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("JSON unmarshal: %v", err)
	}
	// Validates that the JSON is well-formed even with empty values.
	if _, ok := cfg["mcpServers"]; !ok {
		t.Error("mcpServers key missing")
	}
}

// With nothing configured, the claude CLI is a fallback nobody asked for,
// so its absence names what to set: the three keys, the CLIs and the
// provider variable. An explicit claude-cli, or a key that names another
// provider, keeps the message about the binary and its path variable.
func TestNewCLIBackend_NothingConfiguredNamesTheFix(t *testing.T) {
	for _, k := range []string{"ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "OPENAI_API_KEY", "HIVE_PROVIDER"} {
		t.Setenv(k, "")
	}
	t.Setenv("CLAUDE_CODE_CLI_PATH", filepath.Join(t.TempDir(), "no-such-claude"))
	prev := lookPath
	lookPath = func(string) (string, error) { return "", errors.New("not on PATH") }
	t.Cleanup(func() { lookPath = prev })

	_, err := NewCLIBackend(Config{})
	if err == nil {
		t.Fatal("expected an error: no claude CLI")
	}
	for _, want := range []string{"no provider configured", "ANTHROPIC_API_KEY, GEMINI_API_KEY or OPENAI_API_KEY", "--provider or HIVE_PROVIDER", "install the claude or gemini CLI", "no-such-claude"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%s", want, err)
		}
	}
	if n := strings.Count(err.Error(), "\n"); n > 0 {
		t.Errorf("the error spans %d extra lines; one screen is one paragraph:\n%s", n, err)
	}

	for _, cfg := range []Config{{Provider: "claude-cli"}, {APIKey: "k"}} {
		_, err = NewCLIBackend(cfg)
		if err == nil {
			t.Fatal("expected an error: no claude CLI")
		}
		if strings.Contains(err.Error(), "no provider configured") || !strings.Contains(err.Error(), "set CLAUDE_CODE_CLI_PATH") {
			t.Errorf("with %+v the error should be about the binary, not the configuration:\n%s", cfg, err)
		}
	}
	t.Setenv("HIVE_PROVIDER", "claude-cli")
	if _, err = NewCLIBackend(Config{}); err == nil || strings.Contains(err.Error(), "no provider configured") {
		t.Errorf("with HIVE_PROVIDER=claude-cli the error should be about the binary:\n%v", err)
	}
}

// Forces the os.CreateTemp failure branch by passing a non-writable
// directory.
func TestWriteMCPConfig_CreateTempFails(t *testing.T) {
	b := &CLIBackend{
		MCPServerPath: "/x",
		ScratchDir:    "/proc/nonexistent/cannot-write-here",
	}
	_, cleanup, err := b.writeMCPConfig()
	defer cleanup()
	if err == nil {
		t.Error("expected error when CreateTemp fails")
	}
}

// Verifies cleanup is a no-op closure when write fails — must not panic.
func TestWriteMCPConfig_CleanupIsSafeAfterError(t *testing.T) {
	b := &CLIBackend{
		MCPServerPath: "/x",
		ScratchDir:    "/proc/nonexistent",
	}
	_, cleanup, err := b.writeMCPConfig()
	if err == nil {
		t.Skip("CreateTemp succeeded unexpectedly — skip")
	}
	cleanup() // must not panic
}

// Hits the b.MCPServerPath / b.DBPath / b.ProjectDir conditional
// (none of these are special-cased; they're just embedded literally).
func TestWriteMCPConfig_LongScratchAndPaths(t *testing.T) {
	dir := t.TempDir()
	b := &CLIBackend{
		MCPServerPath: strings.Repeat("/very/long/path", 20),
		DBPath:        strings.Repeat("/db", 50),
		ProjectDir:    strings.Repeat("/proj", 30),
		ScratchDir:    dir,
	}
	path, cleanup, err := b.writeMCPConfig()
	if err != nil {
		t.Fatalf("writeMCPConfig: %v", err)
	}
	defer cleanup()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), strings.Repeat("/very/long/path", 20)) {
		t.Error("MCPServerPath not embedded")
	}
}
