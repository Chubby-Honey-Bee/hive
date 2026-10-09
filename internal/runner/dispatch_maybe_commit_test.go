package runner

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// newMaybeCommitRC builds a minimal runtimeContext for maybeCommit tests.
func newMaybeCommitRC(t *testing.T, gc *GitCommitter) *runtimeContext {
	t.Helper()
	store := newTempStore(t)
	return &runtimeContext{
		ctx:   context.Background(),
		store: store,
		gc:    gc,
		logf:  makeLogf(&bytes.Buffer{}),
		res:   &Result{},
		resMu: sync.Mutex{},
	}
}

// TestMaybeCommit_Disabled verifies that a disabled GitCommitter is a no-op:
// no commit is appended to res.Commits.
func TestMaybeCommit_Disabled(t *testing.T) {
	gc := &GitCommitter{ProjectDir: "/nonexistent", Branch: "", Enabled: false}
	rc := newMaybeCommitRC(t, gc)
	node := workflow.DispatchNode{Node: "w1-fix", Type: "agent"}

	rc.maybeCommit(node, "some final text")

	rc.resMu.Lock()
	commits := len(rc.res.Commits)
	rc.resMu.Unlock()
	if commits != 0 {
		t.Errorf("expected 0 commits when disabled; got %d", commits)
	}
}

// TestMaybeCommit_HappyPath verifies that a dirty tree produces one commit
// whose SHA lands in res.Commits.
func TestMaybeCommit_HappyPath(t *testing.T) {
	dir := initTempRepo(t)

	// Dirty the tree so CommitIfDirty finds something to commit.
	if err := os.WriteFile(filepath.Join(dir, "patch.txt"), []byte("change\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	gc := &GitCommitter{ProjectDir: dir, Branch: "HEAD", Enabled: true}
	rc := newMaybeCommitRC(t, gc)
	node := workflow.DispatchNode{Node: "w2-coder", Type: "agent"}

	rc.maybeCommit(node, "applied the fix\nsecond line")

	rc.resMu.Lock()
	commits := rc.res.Commits
	rc.resMu.Unlock()

	if len(commits) != 1 {
		t.Fatalf("expected 1 commit SHA; got %d: %v", len(commits), commits)
	}
	if len(commits[0]) < 7 {
		t.Errorf("SHA looks too short: %q", commits[0])
	}
}

// TestMaybeCommit_NothingToCommit verifies that a clean working tree yields
// no entry in res.Commits (the sha=="" early-return branch).
func TestMaybeCommit_NothingToCommit(t *testing.T) {
	dir := initTempRepo(t)

	gc := &GitCommitter{ProjectDir: dir, Branch: "HEAD", Enabled: true}
	rc := newMaybeCommitRC(t, gc)
	node := workflow.DispatchNode{Node: "evaluate", Type: "agent"}

	rc.maybeCommit(node, "nothing changed")

	rc.resMu.Lock()
	commits := len(rc.res.Commits)
	rc.resMu.Unlock()
	if commits != 0 {
		t.Errorf("expected 0 commits on clean tree; got %d", commits)
	}
}

// TestMaybeCommit_CommitError verifies that a git error (non-git directory)
// is absorbed: res.Commits stays empty and no panic occurs.
func TestMaybeCommit_CommitError(t *testing.T) {
	dir := t.TempDir() // not a git repo → CommitIfDirty returns error

	gc := &GitCommitter{ProjectDir: dir, Branch: "main", Enabled: true}
	rc := newMaybeCommitRC(t, gc)

	buf := &bytes.Buffer{}
	rc.logf = makeLogf(buf)

	node := workflow.DispatchNode{Node: "w1-researcher", Type: "agent"}
	rc.maybeCommit(node, "some output")

	rc.resMu.Lock()
	commits := len(rc.res.Commits)
	rc.resMu.Unlock()
	if commits != 0 {
		t.Errorf("expected 0 commits on error path; got %d", commits)
	}
	// The error must have been logged.
	if !strings.Contains(buf.String(), "commit:") {
		t.Errorf("expected 'commit:' in log output; got %q", buf.String())
	}
}

// TestMaybeCommit_SummaryUsesFirstLine verifies that only the first line of
// finalText is used as the commit summary (firstLine behaviour wired through).
func TestMaybeCommit_SummaryUsesFirstLine(t *testing.T) {
	dir := initTempRepo(t)

	if err := os.WriteFile(filepath.Join(dir, "x.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	gc := &GitCommitter{ProjectDir: dir, Branch: "HEAD", Enabled: true}
	rc := newMaybeCommitRC(t, gc)
	node := workflow.DispatchNode{Node: "w3-analyst", Type: "agent"}

	rc.maybeCommit(node, "first line summary\nignored second line\nignored third")

	rc.resMu.Lock()
	commits := rc.res.Commits
	rc.resMu.Unlock()

	if len(commits) == 0 {
		t.Fatal("expected a commit; got none")
	}

	// Verify the commit message contains the first-line summary only.
	gitLog := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	log := gitLog("log", "--format=%s", "-1")
	if strings.Contains(log, "ignored") {
		t.Errorf("commit message must not contain second/third line; got %q", log)
	}
	if !strings.Contains(log, "first line summary") {
		t.Errorf("commit message must contain first-line summary; got %q", log)
	}
}
