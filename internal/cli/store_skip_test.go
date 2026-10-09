package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// db-repair skips the pre-run that opens the store and runs Init(): the
// recovery verb opens its own connection, and `chb db-repair --dry-run` must
// not create the schema in the database it was asked only to inspect.
func TestStoreSkipPredicates(t *testing.T) {
	if !storeUntouched("db-repair") {
		t.Error("storeUntouched(db-repair) = false; a repair tool must not have the schema migrated under it")
	}
	for _, name := range []string{"db-init", "ask", "db-read"} {
		if storeUntouched(name) {
			t.Errorf("%q should open and migrate the store normally", name)
		}
	}
}

// A command that never reads or writes the default database opens none: run
// in an empty directory, it leaves no workspace/ behind. A command that keeps
// a workspace of its own has it pointed elsewhere, so only the default
// database could put one here; each is given arguments that get it past its
// flag checks, so the pre-run hook that opens the database runs. chb init's
// project database is the one it is asked for, not the default one. A
// command that reads the default database still opens it, among them hive
// init and hive report, which share their last word with chb init and
// design report.
func TestStoreFreeCommandsLeaveNoDatabase(t *testing.T) {
	other := t.TempDir()
	in := func(name string) string { return filepath.Join(other, name) }
	models := in("absent-models.yaml")
	exists := func(dir string, parts ...string) bool {
		_, err := os.Stat(filepath.Join(append([]string{dir}, parts...)...))
		return err == nil
	}
	run := func(t *testing.T, cmd string, args ...string) (dir, out string) {
		t.Helper()
		dir = t.TempDir()
		out, _ = runChb(t, dir, models, nil, append(strings.Fields(cmd), args...)...)
		return dir, out
	}
	for _, c := range []struct {
		cmd  string
		args []string
	}{
		{"help", nil},
		{"completion bash", nil},
		{"__complete list", []string{""}},
		{"list", []string{"--color", "never"}},
		{"palette", nil},
		{"preflight", []string{"workflows/proof.yaml", "--skip-provider-check"}},
		{"validate-personas", nil},
		{"verify-artifact", []string{in("none.json")}},
		{"validate", []string{"--workspace", in("validate"), "--chb-bin", in("no-chb"), "--stop-on-first"}},
		{"workflow list", nil},
		{"workflow validate", []string{"workflows/proof.yaml"}},
		{"models list", nil},
		{"generate", []string{"Should we adopt X?", "--out", in("swarm.yaml")}},
		{"gen-implement-workflow", []string{in("none.json")}},
		{"render-review", []string{in("none.json")}},
		{"design report", []string{in("none.jsonl")}},
		{"bench decide", []string{in("none.jsonl")}},
		{"calibration-merge", []string{in("none.json")}},
		{"hive write-synthesis", []string{"--project", "demo", "--out", in("synthesis.md")}},
		{"mcp-smoke", []string{"--mcp-bin", in("no-chb-mcp")}},
		{"replay-behavior", []string{in("none.jsonl")}},
		{"gen-behavior", []string{"--workspace", in("behavior"), "--out", in("behavior.jsonl")}},
		{"replicate", []string{"Should we adopt X?", "--n", "0"}},
		{"review", []string{"--target", other, "--workspace", in("review"), "--workflow", in("none.yaml")}},
		{"implement", []string{"--findings", in("none.json"), "--workspace", in("implement")}},
		{"proof", []string{"--workspace", in("proof"), "--workflow", in("none.yaml")}},
		{"agent-harness", []string{"--workspace", in("harness"), "--suite", in("none.yaml")}},
	} {
		t.Run(c.cmd, func(t *testing.T) {
			if dir, out := run(t, c.cmd, c.args...); exists(dir, "workspace") {
				t.Errorf("chb %s left workspace/ behind:\n%s", c.cmd, out)
			}
		})
	}
	t.Run("init", func(t *testing.T) {
		if dir, out := run(t, "init", "demo"); exists(dir, "workspace", "hive.db") || !exists(dir, "workspace", "demo", "hive.db") {
			t.Errorf("chb init demo: want workspace/demo/hive.db and no workspace/hive.db:\n%s", out)
		}
	})
	for _, cmd := range []string{"db-read summary", "hive init --project demo", "hive report --project demo"} {
		t.Run(cmd, func(t *testing.T) {
			if dir, out := run(t, cmd); !exists(dir, "workspace", "hive.db") {
				t.Errorf("chb %s opened no default database:\n%s", cmd, out)
			}
		})
	}
}
