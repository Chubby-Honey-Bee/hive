package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/runner"
)

// TestAgentRun_KeyGuardUsesResolvedKind: the missing-ANTHROPIC_API_KEY
// refusal follows the backend the run resolves to. --provider outranks
// HIVE_PROVIDER=anthropic, and the provider's name matches in any case.
// The claude CLI path points at nothing, so a run that gets past the guard
// stops at "claude CLI not found" before any model call.
func TestAgentRun_KeyGuardUsesResolvedKind(t *testing.T) {
	dir := t.TempDir()
	s, err := db.NewStore(filepath.Join(dir, "guard.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	prev := store
	store = s
	t.Cleanup(func() { store = prev; s.Close() })

	wf := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(wf, []byte("name: guard\nnodes:\n  w1-a:\n    type: agent\n    model: haiku\n    prompt: hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("CLAUDE_CODE_CLI_PATH", filepath.Join(dir, "no-such-claude"))
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")

	cases := []struct {
		name, envProvider, flagProvider string
		refused                         bool
	}{
		{"--provider beats HIVE_PROVIDER", "anthropic", "claude-cli", false},
		{"HIVE_PROVIDER names anthropic", "anthropic", "", true},
		{"HIVE_PROVIDER in upper case", "ANTHROPIC", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HIVE_PROVIDER", c.envProvider)
			args := []string{wf, "--dir", dir, "--branch", ""}
			if c.flagProvider != "" {
				args = append(args, "--provider", c.flagProvider)
			}
			cmd := newAgentRunCmd()
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			cmd.SetArgs(args)
			err := cmd.Execute()
			if err == nil {
				t.Fatal("expected an error: no key and no claude CLI")
			}
			refused := strings.Contains(err.Error(), "ANTHROPIC_API_KEY")
			if refused != c.refused {
				t.Errorf("refused for a missing key = %v, want %v (err: %v)", refused, c.refused, err)
			}
			if !c.refused && !strings.Contains(err.Error(), "claude CLI not found") {
				t.Errorf("err = %v, want the run to resolve to claude-cli", err)
			}
		})
	}
}

// TestRequireAnthropicKey_AcceptsAuthToken: anthropic-sdk-go authenticates
// with ANTHROPIC_AUTH_TOKEN as well as ANTHROPIC_API_KEY, so an anthropic run
// with only the token set gets past the guard.
func TestRequireAnthropicKey_AcceptsAuthToken(t *testing.T) {
	cfg := runner.Config{Provider: "anthropic"}
	for _, token := range []string{"", "test-token"} {
		t.Setenv("ANTHROPIC_AUTH_TOKEN", token)
		wantRefused := cfg.APIKey == "" && token == ""
		if err := requireAnthropicKey(cfg); (err != nil) != wantRefused {
			t.Errorf("ANTHROPIC_AUTH_TOKEN=%q: err = %v, want refused = %v", token, err, wantRefused)
		}
	}
}
