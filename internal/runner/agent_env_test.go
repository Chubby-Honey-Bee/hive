package runner

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envValue(env []string, key string) (string, int) {
	val, n := "", 0
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			val, n = strings.TrimPrefix(kv, key+"="), n+1
		}
	}
	return val, n
}

// The running binary's directory leads PATH, and the run's database replaces
// any inherited HIVE_DB_PATH rather than sitting beside it.
func TestAgentEnv_PutsTheBinaryFirstAndTheRunsDatabase(t *testing.T) {
	t.Setenv("HIVE_DB_PATH", "/inherited/hive.db")
	env := agentEnv("/run/chosen.db")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path, n := envValue(env, "PATH")
	if n != 1 || filepath.SplitList(path)[0] != filepath.Dir(exe) {
		t.Fatalf("PATH = %q (%d entries), want %q first", path, n, filepath.Dir(exe))
	}
	if db, n := envValue(env, "HIVE_DB_PATH"); db != "/run/chosen.db" || n != 1 {
		t.Fatalf("HIVE_DB_PATH = %q (%d entries), want the run's database once", db, n)
	}
}

// With no database on the run, the inherited one stands.
func TestAgentEnv_KeepsTheInheritedDatabase(t *testing.T) {
	t.Setenv("HIVE_DB_PATH", "/inherited/hive.db")
	if db, _ := envValue(agentEnv(""), "HIVE_DB_PATH"); db != "/inherited/hive.db" {
		t.Fatalf("HIVE_DB_PATH = %q, want the inherited value", db)
	}
}

// The gemini CLI backend hands its agents the run's database, so `--db`
// reaches them.
func TestGeminiCLIBackend_ForwardsTheRunsDatabase(t *testing.T) {
	cli := fakeGeminiScript(t, `echo "db=$HIVE_DB_PATH"`)
	b := &GeminiCLIBackend{CLIPath: cli, DBPath: "/run/chosen.db"}
	res, err := b.Run(context.Background(), RunRequest{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.FinalText, "db=/run/chosen.db") {
		t.Fatalf("agent saw %q, want the run's database", res.FinalText)
	}
	// The CLI runs its own tool loop, so min_tool_calls cannot be checked.
	if !res.ToolCallsUnreported {
		t.Fatal("the gemini CLI result claims its tool calls are reported")
	}
}

// The in-process bash tool runs with the registry's environment.
func TestBashTool_UsesTheAgentEnvironment(t *testing.T) {
	r := &ToolRegistry{Handlers: map[string]ToolHandler{}}
	r.registerShell(t.TempDir(), NewFSRecorder(), hostShell())
	r.Env = agentEnv("/run/chosen.db")
	out, err := r.Handlers["shell"](context.Background(), map[string]any{"command": `echo "db=$HIVE_DB_PATH"`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "db=/run/chosen.db") {
		t.Fatalf("bash saw %q, want the run's database", out)
	}
}

// The bash tool's output is stored in tool_invocations and sent back to the
// provider, so `env` must not print a provider key into it. Other variables
// still reach the command.
func TestBashTool_DropsProviderCredentials(t *testing.T) {
	keys := []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY"}
	for _, k := range keys {
		t.Setenv(k, "sentinel-"+k)
	}
	t.Setenv("HIVE_TEST_KEPT", "kept-value")

	for _, tc := range []struct {
		name string
		env  []string
	}{
		{"agent environment", agentEnv("/run/chosen.db")},
		{"inherited environment", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &ToolRegistry{Handlers: map[string]ToolHandler{}}
			r.registerShell(t.TempDir(), NewFSRecorder(), hostShell())
			r.Env = tc.env
			out, err := r.Handlers["shell"](context.Background(), map[string]any{"command": "env"})
			if err != nil {
				t.Fatal(err)
			}
			for _, k := range keys {
				if strings.Contains(out, k+"=") || strings.Contains(out, "sentinel-"+k) {
					t.Errorf("bash env printed %s", k)
				}
			}
			if !strings.Contains(out, "HIVE_TEST_KEPT=kept-value") {
				t.Errorf("bash env lost an ordinary variable:\n%s", out)
			}
		})
	}
}

// A process a model drives, the claude and gemini CLIs and the in-process
// shell tool, carries HIVE_AGENT_DB naming its run's database, so a
// command that only the workflow's own steps may run against it can refuse
// it there. A command node's program is one of those steps and does not
// carry it.
func TestModelEnv_MarksOnlyWhatAModelDrives(t *testing.T) {
	t.Setenv("HIVE_AGENT_DB", "")
	os.Unsetenv("HIVE_AGENT_DB")
	ctx := context.Background()
	var out, errOut bytes.Buffer
	for name, env := range map[string][]string{
		"the claude CLI": (&CLIBackend{CLIPath: "claude", DBPath: "/run/x.db"}).command(ctx, nil, "", &out, &errOut).Env,
		"the gemini CLI": (&GeminiCLIBackend{CLIPath: "gemini", DBPath: "/run/x.db"}).command(ctx, nil, "", &out, &errOut).Env,
	} {
		if val, n := envValue(env, "HIVE_AGENT_DB"); val != "/run/x.db" || n != 1 {
			t.Errorf("%s runs with HIVE_AGENT_DB=%q (%d entries), want the run's database, once", name, val, n)
		}
	}
	r := &ToolRegistry{Handlers: map[string]ToolHandler{}}
	r.registerShell(t.TempDir(), NewFSRecorder(), hostShell())
	r.Env = modelEnv("/run/x.db")
	got, err := r.Handlers["shell"](ctx, map[string]any{"command": `echo "run=$HIVE_AGENT_DB"`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "run=/run/x.db") {
		t.Errorf("the shell tool saw %q, want run=/run/x.db", got)
	}
	if _, n := envValue(agentEnv("/run/x.db"), "HIVE_AGENT_DB"); n != 0 {
		t.Error("a command node's environment carries HIVE_AGENT_DB")
	}
}
