package runner

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// gitMu serializes all git operations across parallel node dispatches.
// Multiple goroutines may hold their own *GitCommitter, but they all touch
// the same working tree and the same index, and concurrent git invocations
// on one index corrupt each other.
//
// What it does NOT do is attribute edits to a node. `git add -A` stages the
// whole tree, so whichever node commits first sweeps in every other node's
// in-flight edits — the mutex makes the commits sequential, not separable.
// Per-node attribution would need a worktree per node.
var gitMu sync.Mutex

// GitCommitter auto-commits agent-made edits to a dedicated branch so the full
// run produces a reviewable diff. Uses the `git` CLI rather than go-git because
// shelling out is simpler, works with existing hooks/config, and stays out of
// go-git's complex merge semantics for our append-only branch use case.
type GitCommitter struct {
	ProjectDir string
	Branch     string
	Enabled    bool
}

// NewGitCommitter returns a committer. If branch is empty, commits are skipped.
func NewGitCommitter(projectDir, branch string) *GitCommitter {
	return &GitCommitter{
		ProjectDir: projectDir,
		Branch:     branch,
		Enabled:    branch != "",
	}
}

func (g *GitCommitter) run(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = g.ProjectDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// inWorkTree reports whether ProjectDir lies inside a git working tree. It
// asks git rather than looking for ProjectDir/.git, which a project in a
// subdirectory of a repository does not have.
func (g *GitCommitter) inWorkTree() bool {
	out, err := g.run("rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

// EnsureCleanTree refuses to proceed when the working tree has uncommitted
// changes. CommitIfDirty stages with `git add -A`, so on a dirty tree the
// first auto-commit would sweep the user's own unfinished work into the
// branch — and, with --auto-pr, into a pull request. Guarding here, before
// the branch is created, is what keeps the -A to changes the run produced.
//
// Within the run, -A still does not separate one node from another: on a
// parallel wave a commit carries whatever every concurrent node had written
// by then. The guarantee is "nothing of yours", not "one node per commit".
func (g *GitCommitter) EnsureCleanTree() error {
	if !g.Enabled {
		return nil
	}
	out, err := g.run("status", "--porcelain")
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}
	if out = strings.TrimSpace(out); out == "" {
		return nil
	}
	return dirtyTreeError(strings.Split(out, "\n"))
}

// dirtyTreeError refuses a working tree with uncommitted changes, the
// status lines given, naming the first five.
func dirtyTreeError(lines []string) error {
	shown := lines[:min(len(lines), 5)]
	more := ""
	if len(lines) > len(shown) {
		more = fmt.Sprintf(" (+%d more)", len(lines)-len(shown))
	}
	return fmt.Errorf("working tree has uncommitted changes%s:\n  %s\n"+
		"auto-commit would sweep them into the branch — commit or stash them, or pass --allow-dirty",
		more, strings.Join(shown, "\n  "))
}

// EnsureBranch is idempotent: creates the auto-corrections branch off HEAD
// if it doesn't exist yet, and switches to it.
func (g *GitCommitter) EnsureBranch() error {
	if !g.Enabled {
		return nil
	}
	if !g.inWorkTree() {
		return fmt.Errorf("not a git repo: %s", g.ProjectDir)
	}
	return g.checkoutBranch()
}

// checkoutBranch switches to the branch, creating it from the current HEAD
// when it does not exist yet.
func (g *GitCommitter) checkoutBranch() error {
	if g.branchExists() {
		_, err := g.run("checkout", g.Branch)
		return err
	}
	if _, err := g.run("checkout", "-b", g.Branch); err != nil {
		return fmt.Errorf("create branch %s: %w", g.Branch, err)
	}
	return nil
}

// branchExists reports whether the branch exists.
func (g *GitCommitter) branchExists() bool {
	out, err := g.run("rev-parse", "--verify", g.Branch)
	return err == nil && strings.TrimSpace(out) != ""
}

// CommitIfDirty stages everything under the project and makes a single commit
// with the given message if there's anything to commit. Returns the commit SHA
// (empty if nothing to commit). The commit uses the repo's configured user and
// adds no trailer.
func (g *GitCommitter) CommitIfDirty(message string) (string, error) {
	if !g.Enabled {
		return "", nil
	}
	gitMu.Lock()
	defer gitMu.Unlock()
	if _, err := g.run("add", "-A"); err != nil {
		return "", fmt.Errorf("git add: %w", err)
	}
	if !g.hasStagedChanges() {
		return "", nil
	}
	return g.commit(message)
}

// hasStagedChanges reports whether the index holds changes to commit.
func (g *GitCommitter) hasStagedChanges() bool {
	out, _ := g.run("diff", "--cached", "--name-only")
	return strings.TrimSpace(out) != ""
}

// commit commits the index with message and returns the commit's SHA.
// Callers hold gitMu.
func (g *GitCommitter) commit(message string) (string, error) {
	if _, err := g.run("commit", "-m", message); err != nil {
		return "", fmt.Errorf("git commit: %w", err)
	}
	sha, err := g.run("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(sha), nil
}

// Push pushes the branch to origin. Best-effort — returns err on failure so the
// caller can decide whether to stop.
func (g *GitCommitter) Push() error {
	if !g.Enabled {
		return nil
	}
	_, err := g.run("push", "-u", "origin", g.Branch)
	return err
}

// OpenPR shells out to the `gh` CLI to open a rollup PR. Returns the PR URL.
// If `gh` is unavailable or auth fails, returns an error (caller logs and
// continues).
func OpenPR(projectDir, branch, title, body string) (string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", errors.New("gh CLI not installed")
	}
	cmd := exec.Command("gh", "pr", "create",
		"--head", branch,
		"--title", title,
		"--body", body)
	cmd.Dir = projectDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gh pr create: %w — %s", err, string(out))
	}
	// gh prints the PR URL on stdout.
	return strings.TrimSpace(string(out)), nil
}

// CommitMessageFor builds a short commit subject for a completed workflow node.
func CommitMessageFor(wave int, nodeName, summary string) string {
	summary = strings.TrimSpace(summary)
	if len(summary) > 80 {
		summary = summary[:runeStart(summary, 77)] + "..."
	}
	if summary == "" {
		summary = "agent edit"
	}
	if wave > 0 {
		return fmt.Sprintf("validate(wave-%d): %s — %s", wave, nodeName, summary)
	}
	return fmt.Sprintf("validate: %s — %s", nodeName, summary)
}

// Timestamp is a tiny helper for commit bodies and audit trails.
func Timestamp() string { return time.Now().UTC().Format(time.RFC3339) }
