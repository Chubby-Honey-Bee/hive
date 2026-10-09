package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// A child that dies immediately is reported as an error, not as a healthy
// spawn with run_id 0.
func TestSpawnDetachedScript_ReportsAnImmediateDeath(t *testing.T) {
	s := &mcpServer{}
	logPath := filepath.Join(t.TempDir(), "run.log")
	_, _, err := s.spawnDetachedScriptWithEnv("/bin/sh",
		[]string{"-c", "echo 'boom: no such workflow' >&2; exit 3"},
		os.Environ(), "", logPath)
	if err == nil {
		t.Fatal("a child that exits 3 must surface as an error, not a successful spawn")
	}
	if !strings.Contains(err.Error(), "exited immediately") {
		t.Errorf("error should name the early exit, got: %v", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should carry the child's own output, got: %v", err)
	}
}

// A clean exit that never announced a run id is also a failure to report:
// there is nothing for the caller to poll.
func TestSpawnDetachedScript_CleanExitWithoutARunIsAnError(t *testing.T) {
	s := &mcpServer{}
	logPath := filepath.Join(t.TempDir(), "run.log")
	if _, _, err := s.spawnDetachedScriptWithEnv("/bin/sh",
		[]string{"-c", "echo nothing to do"}, os.Environ(), "", logPath); err == nil {
		t.Fatal("a child that exits 0 without starting a run must not report success")
	}
}

// The session budget mode reaches every spawned run, chb_agent_run as well
// as the self-* scripts.
func TestSpawnEnv_CarriesTheSessionBudgetMode(t *testing.T) {
	t.Setenv("HIVE_BUDGET_MODE", "")
	s := &mcpServer{}
	for _, e := range s.spawnEnv() {
		if strings.HasPrefix(e, "HIVE_BUDGET_MODE=") && e != "HIVE_BUDGET_MODE=" {
			t.Fatalf("no override set, yet the env carries %q", e)
		}
	}
	s.budgetMode = "cheap"
	var found string
	for _, e := range s.spawnEnv() {
		if strings.HasPrefix(e, "HIVE_BUDGET_MODE=") {
			found = e
		}
	}
	if found != "HIVE_BUDGET_MODE=cheap" {
		t.Fatalf("spawned run env = %q, want HIVE_BUDGET_MODE=cheap", found)
	}
}

// Each caller names the child's working directory, so the self-* tools'
// children resolve workspace/ under the server's repo root, where the server
// reports their paths.
func TestSpawnDetachedScript_RunsInTheDirectoryTheCallerNames(t *testing.T) {
	s := &mcpServer{}
	dir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "run.log")
	// Prints its cwd, then exits non-zero so the spawn reports it back.
	_, _, err := s.spawnDetachedScriptWithEnv("/bin/sh",
		[]string{"-c", "pwd; exit 3"}, os.Environ(), dir, logPath)
	if err == nil {
		t.Fatal("expected the early exit to be reported")
	}
	want, _ := filepath.EvalSymlinks(dir)
	if !strings.Contains(err.Error(), want) && !strings.Contains(err.Error(), dir) {
		t.Errorf("child did not run in %s; its output was: %v", dir, err)
	}
}

// Moving the child's cwd must not move which database it opens.
func TestAbsoluteDBPathEnv(t *testing.T) {
	got := absoluteDBPathEnv([]string{"A=1", "HIVE_DB_PATH=workspace/x/hive.db", "B=2"})
	var db string
	for _, kv := range got {
		if v, ok := strings.CutPrefix(kv, "HIVE_DB_PATH="); ok {
			db = v
		}
	}
	if !filepath.IsAbs(db) {
		t.Errorf("HIVE_DB_PATH stayed relative: %q", db)
	}
	if !strings.HasSuffix(db, filepath.Join("workspace", "x", "hive.db")) {
		t.Errorf("HIVE_DB_PATH resolved to the wrong file: %q", db)
	}
	abs := absoluteDBPathEnv([]string{"HIVE_DB_PATH=/already/abs.db"})
	if abs[0] != "HIVE_DB_PATH=/already/abs.db" {
		t.Errorf("an absolute path was rewritten: %q", abs[0])
	}
}

// The check was strings.Contains, so chb 1.2.10 passed for server 1.2.1, and
// any chb reporting dev was exempt even against a release.
func TestVersionSkewed(t *testing.T) {
	for _, tc := range []struct {
		server, chb string
	}{
		{"1.2.1", "1.2.10"},
		{"1.2.1", "1.2.1"},
		{"1.2.1", "dev"},
		{"dev", "1.2.1"},
		{"dev", "dev"},
		{"1.3.0", "1.2.1"},
	} {
		out := "chb version " + tc.chb + "\n"
		want := tc.chb != tc.server
		if got := versionSkewed(tc.server, out); got != want {
			t.Errorf("server %s, %q: skewed = %v, want %v", tc.server, out, got, want)
		}
	}
	if !versionSkewed("1.2.1", "") {
		t.Error("a chb that reports no version is not a match")
	}
}

// TestChbBin_PrefersAdjacentBinary pins the resolution order: a chb beside
// the server's real executable wins — including when the server is reached
// through a symlink in another directory — and the bare name, left to $PATH,
// is the fallback.
func TestChbBin_PrefersAdjacentBinary(t *testing.T) {
	install := t.TempDir()
	server := filepath.Join(install, "chb-mcp")
	sibling := filepath.Join(install, chbExeName())
	for _, p := range []string{server, sibling} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// t.TempDir can sit under a symlinked directory (/var → /private/var),
	// so compare against the resolved form.
	wantSibling, err := filepath.EvalSymlinks(sibling)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolveChbBinFrom(server); got != wantSibling {
		t.Errorf("adjacent chb: got %q, want %q", got, wantSibling)
	}

	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "chb-mcp")
	if err := os.Symlink(server, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got := resolveChbBinFrom(link); got != wantSibling {
		t.Errorf("via symlink: got %q, want the chb beside the real server %q", got, wantSibling)
	}

	lone := filepath.Join(t.TempDir(), "chb-mcp")
	if err := os.WriteFile(lone, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveChbBinFrom(lone); got != chbExeName() {
		t.Errorf("no sibling: got %q, want the bare name %q", got, chbExeName())
	}
}

// TestRepoRoot_FindsWorkflowsAndAgentsDir creates a temp dir with the
// expected `workflows/` + `agents/` markers and asserts repoRoot walks
// up from a nested cwd to find it.
func TestRepoRoot_FindsWorkflowsAndAgentsDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	prevWD, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(prevWD) })
	if err := os.Chdir(nested); err != nil {
		t.Fatal(err)
	}

	s := &mcpServer{}
	got := s.repoRoot()
	// Resolve symlinks for comparison — macOS /var → /private/var, etc.
	gotResolved, _ := filepath.EvalSymlinks(got)
	rootResolved, _ := filepath.EvalSymlinks(root)
	if gotResolved != rootResolved {
		t.Errorf("repoRoot = %q (resolved %q); want %q", got, gotResolved, rootResolved)
	}
}

// TestScanRunID_MatchesAgentRunBanner asserts the parser pulls a run
// id out of a real-shaped agent-run log line.
func TestScanRunID_MatchesAgentRunBanner(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"[agent-run] workflow run ID: 42\nmore log\n", 42},
		{"prefix\n[agent-run] workflow run ID: 7\n", 7},
		{"no banner here", 0},
		{"workflow run ID: not-a-number", 0},
		{"", 0},
	}
	for _, tc := range cases {
		if got := scanRunID(tc.in); got != tc.want {
			t.Errorf("scanRunID(%q) = %d; want %d", tc.in, got, tc.want)
		}
	}
}

// A log's tail is cut on a rune boundary, so the error that carries it is
// valid UTF-8.
func TestLogTail_CutsOnARuneBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	if err := os.WriteFile(path, []byte("—"+strings.Repeat("x", 399)), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := logTail(path, 400); !utf8.ValidString(got) || len(got) > 400 {
		t.Errorf("logTail = %q, want valid UTF-8 of at most 400 bytes", got)
	}
}
