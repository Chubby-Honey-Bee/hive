package runner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initTempRepo creates a temp directory with a minimal git repo configured
// for use in tests (no GPG signing, throwaway identity).
func initTempRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("config", "commit.gpgsign", "false")

	// Commit an initial file so HEAD exists.
	initial := filepath.Join(dir, "init.txt")
	if err := os.WriteFile(initial, []byte("init\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "init.txt")
	run("commit", "-m", "initial")

	return dir
}

// TestCommitIfDirty_Disabled verifies that a disabled committer is a no-op.
func TestCommitIfDirty_Disabled(t *testing.T) {
	g := &GitCommitter{ProjectDir: "/nonexistent", Branch: "", Enabled: false}
	sha, err := g.CommitIfDirty("should not run")
	if err != nil {
		t.Fatalf("expected nil error; got %v", err)
	}
	if sha != "" {
		t.Fatalf("expected empty SHA; got %q", sha)
	}
}

// TestCommitIfDirty_NothingToCommit verifies that a clean working tree returns
// ("", nil) without creating a commit.
func TestCommitIfDirty_NothingToCommit(t *testing.T) {
	dir := initTempRepo(t)

	// Count commits before.
	countBefore := countCommits(t, dir)

	g := &GitCommitter{ProjectDir: dir, Branch: "HEAD", Enabled: true}
	sha, err := g.CommitIfDirty("empty commit?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != "" {
		t.Fatalf("expected empty SHA on clean tree; got %q", sha)
	}

	countAfter := countCommits(t, dir)
	if countAfter != countBefore {
		t.Fatalf("commit count changed (%d → %d) on clean tree", countBefore, countAfter)
	}
}

// TestCommitIfDirty_HappyPath verifies that a dirty working tree produces a
// commit and returns a non-empty SHA.
func TestCommitIfDirty_HappyPath(t *testing.T) {
	dir := initTempRepo(t)

	// Dirty the tree.
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &GitCommitter{ProjectDir: dir, Branch: "HEAD", Enabled: true}
	sha, err := g.CommitIfDirty("add new.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha == "" {
		t.Fatal("expected non-empty SHA after committing dirty tree")
	}
	// SHA should look like a hex string.
	if len(sha) < 7 {
		t.Fatalf("SHA too short: %q", sha)
	}

	// Verify the commit actually landed in git log.
	cmd := exec.Command("git", "log", "--oneline", "-1")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "add new.txt") {
		t.Errorf("expected commit message in log; got %q", string(out))
	}
}

// TestCommitIfDirty_InvalidDir verifies that a non-git directory returns an
// error from "git add".
func TestCommitIfDirty_InvalidDir(t *testing.T) {
	dir := t.TempDir() // not a git repo

	g := &GitCommitter{ProjectDir: dir, Branch: "main", Enabled: true}
	sha, err := g.CommitIfDirty("should fail")
	if err == nil {
		t.Fatalf("expected an error for non-git dir; got sha=%q err=nil", sha)
	}
	if sha != "" {
		t.Fatalf("expected empty SHA on error; got %q", sha)
	}
}

// TestCommitIfDirty_MultipleDirtyFiles verifies that multiple staged files are
// bundled into one commit.
func TestCommitIfDirty_MultipleDirtyFiles(t *testing.T) {
	dir := initTempRepo(t)

	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	g := &GitCommitter{ProjectDir: dir, Branch: "HEAD", Enabled: true}
	sha, err := g.CommitIfDirty("batch commit")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha == "" {
		t.Fatal("expected non-empty SHA")
	}

	// Verify all three files appear in the diff of that commit.
	cmd := exec.Command("git", "diff-tree", "--no-commit-id", "-r", "--name-only", sha)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git diff-tree: %v\n%s", err, out)
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if !strings.Contains(string(out), name) {
			t.Errorf("expected %q in commit diff; got %q", name, string(out))
		}
	}
}

// TestPush_Disabled verifies that a disabled committer is a no-op and returns nil.
func TestPush_Disabled(t *testing.T) {
	g := &GitCommitter{ProjectDir: "/nonexistent", Branch: "", Enabled: false}
	if err := g.Push(); err != nil {
		t.Fatalf("expected nil error for disabled committer; got %v", err)
	}
}

// TestPush_NoRemote verifies that Push returns an error when no remote is
// configured (the common failure path — no network required).
func TestPush_NoRemote(t *testing.T) {
	dir := initTempRepo(t)

	g := &GitCommitter{ProjectDir: dir, Branch: "main", Enabled: true}
	err := g.Push()
	if err == nil {
		t.Fatal("expected an error when no remote is configured; got nil")
	}
}

// TestPush_HappyPath verifies the successful push path using a bare local repo
// as the "origin" remote so no network is required.
func TestPush_HappyPath(t *testing.T) {
	// Create a bare repo to act as origin.
	bare := t.TempDir()
	runInDir := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runInDir(bare, "init", "--bare")

	// Create a working repo and point it at the bare remote.
	src := initTempRepo(t)
	runInDir(src, "remote", "add", "origin", bare)

	// Determine the default branch name (may be "master" or "main").
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = src
	branchBytes, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v\n%s", err, branchBytes)
	}
	branch := strings.TrimSpace(string(branchBytes))

	g := &GitCommitter{ProjectDir: src, Branch: branch, Enabled: true}
	if err := g.Push(); err != nil {
		t.Fatalf("unexpected error on Push to local bare remote: %v", err)
	}
}

// TestEnsureBranch_Disabled verifies that a disabled committer is a no-op.
func TestEnsureBranch_Disabled(t *testing.T) {
	g := &GitCommitter{ProjectDir: "/nonexistent", Branch: "", Enabled: false}
	if err := g.EnsureBranch(); err != nil {
		t.Fatalf("expected nil error for disabled committer; got %v", err)
	}
}

// TestEnsureBranch_NotGitRepo verifies that a non-git directory returns an error.
func TestEnsureBranch_NotGitRepo(t *testing.T) {
	dir := t.TempDir() // not a git repo
	g := &GitCommitter{ProjectDir: dir, Branch: "auto-corrections", Enabled: true}
	err := g.EnsureBranch()
	if err == nil {
		t.Fatal("expected an error for non-git dir; got nil")
	}
	if !strings.Contains(err.Error(), "not a git repo") {
		t.Errorf("expected 'not a git repo' in error; got %q", err)
	}
}

// TestEnsureBranch_CreatesNewBranch verifies that a new branch is created when
// it doesn't exist yet, and that the working tree switches to it.
func TestEnsureBranch_CreatesNewBranch(t *testing.T) {
	dir := initTempRepo(t)
	branchName := "auto-corrections"
	g := &GitCommitter{ProjectDir: dir, Branch: branchName, Enabled: true}
	if err := g.EnsureBranch(); err != nil {
		t.Fatalf("unexpected error creating new branch: %v", err)
	}
	// Verify we're now on the new branch.
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != branchName {
		t.Errorf("expected branch %q; got %q", branchName, got)
	}
}

// TestEnsureBranch_ExistingBranch verifies that EnsureBranch checks out an
// already-existing branch without creating a duplicate.
func TestEnsureBranch_ExistingBranch(t *testing.T) {
	dir := initTempRepo(t)
	branchName := "auto-corrections"
	// Pre-create the branch (but stay on the default branch).
	preCreate := exec.Command("git", "branch", branchName)
	preCreate.Dir = dir
	if out, err := preCreate.CombinedOutput(); err != nil {
		t.Fatalf("git branch: %v\n%s", err, out)
	}

	g := &GitCommitter{ProjectDir: dir, Branch: branchName, Enabled: true}
	if err := g.EnsureBranch(); err != nil {
		t.Fatalf("unexpected error checking out existing branch: %v", err)
	}
	// Verify we're now on the branch.
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != branchName {
		t.Errorf("expected branch %q; got %q", branchName, got)
	}
}

// TestEnsureBranch_Idempotent verifies that calling EnsureBranch twice is safe.
func TestEnsureBranch_Idempotent(t *testing.T) {
	dir := initTempRepo(t)
	branchName := "auto-corrections"
	g := &GitCommitter{ProjectDir: dir, Branch: branchName, Enabled: true}
	if err := g.EnsureBranch(); err != nil {
		t.Fatalf("first EnsureBranch call failed: %v", err)
	}
	if err := g.EnsureBranch(); err != nil {
		t.Fatalf("second EnsureBranch call failed (idempotency broken): %v", err)
	}
}

// countCommits returns the number of commits reachable from HEAD.
func countCommits(t *testing.T, dir string) int {
	t.Helper()
	cmd := exec.Command("git", "rev-list", "--count", "HEAD")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-list: %v\n%s", err, out)
	}
	n := 0
	_, _ = fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n)
	return n
}
