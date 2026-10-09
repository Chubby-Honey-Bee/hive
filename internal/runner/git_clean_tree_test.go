package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initCleanRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "a.txt")
	git("commit", "-q", "-m", "init")
	return dir
}

// `git add -A` in CommitIfDirty would sweep the user's uncommitted work into
// the auto-commit branch, so the clean-tree guard runs before the branch
// exists.
func TestEnsureCleanTree(t *testing.T) {
	dir := initCleanRepo(t)
	gc := NewGitCommitter(dir, "auto")
	if err := gc.EnsureCleanTree(); err != nil {
		t.Fatalf("clean tree reported dirty: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := gc.EnsureCleanTree()
	if err == nil {
		t.Fatal("dirty tree accepted")
	}
	if !strings.Contains(err.Error(), "scratch.txt") || !strings.Contains(err.Error(), "--allow-dirty") {
		t.Errorf("error should name the file and the escape hatch: %v", err)
	}
	if gc := (&GitCommitter{Enabled: false}); gc.EnsureCleanTree() != nil {
		t.Error("a disabled committer must not inspect the tree")
	}
}

func TestPrepareGitCommitter_RefusesDirtyTreeUnlessAllowed(t *testing.T) {
	dir := initCleanRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "wip.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	logf := func(string, ...any) {}
	if _, err := prepareGitCommitter(Config{ProjectDir: dir, Branch: "auto"}, logf); err == nil {
		t.Fatal("dirty tree should refuse to prepare auto-commit")
	}
	gc, err := prepareGitCommitter(Config{ProjectDir: dir, Branch: "auto", AllowDirty: true}, logf)
	if err != nil || gc == nil || !gc.Enabled {
		t.Fatalf("--allow-dirty should proceed: gc=%v err=%v", gc, err)
	}
	if gc, err := prepareGitCommitter(Config{ProjectDir: dir, Branch: ""}, logf); err != nil || gc.Enabled {
		t.Fatalf("no branch means no auto-commit and no guard: gc=%v err=%v", gc, err)
	}
}
