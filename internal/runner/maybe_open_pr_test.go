package runner

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// setupRepoWithRemote creates a src repo (via initTempRepo) and points it at a
// bare "origin" so that GitCommitter.Push() succeeds without any network.
// Returns (src dir, default branch name).
func setupRepoWithRemote(t *testing.T) (srcDir, branch string) {
	t.Helper()
	bare := t.TempDir()
	initBare := exec.Command("git", "init", "--bare")
	initBare.Dir = bare
	if out, err := initBare.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}

	src := initTempRepo(t) // creates repo with an initial commit
	runGit := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit(src, "remote", "add", "origin", bare)

	c := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	c.Dir = src
	bOut, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v\n%s", err, bOut)
	}
	return src, strings.TrimSpace(string(bOut))
}

// TestMaybeOpenPR_NoopBranches verifies all three early-return guards.
func TestMaybeOpenPR_NoopBranches(t *testing.T) {
	nop := func(string, ...any) {}

	cases := []struct {
		name string
		cfg  Config
		gc   *GitCommitter
		res  *Result
	}{
		{
			name: "autoPR_false",
			cfg:  Config{AutoPR: false},
			gc:   &GitCommitter{Enabled: true, Branch: "b"},
			res:  &Result{Commits: []string{"abc"}},
		},
		{
			name: "git_disabled",
			cfg:  Config{AutoPR: true},
			gc:   &GitCommitter{Enabled: false, Branch: "b"},
			res:  &Result{Commits: []string{"abc"}},
		},
		{
			name: "nil_commits",
			cfg:  Config{AutoPR: true},
			gc:   &GitCommitter{Enabled: true, Branch: "b"},
			res:  &Result{Commits: nil},
		},
		{
			name: "empty_commits_slice",
			cfg:  Config{AutoPR: true},
			gc:   &GitCommitter{Enabled: true, Branch: "b"},
			res:  &Result{Commits: []string{}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			maybeOpenPR(tc.cfg, tc.gc, tc.res, nop)
			if tc.res.PRURL != "" {
				t.Fatalf("expected empty PRURL; got %q", tc.res.PRURL)
			}
		})
	}
}

// TestMaybeOpenPR_PushFails verifies that a push error is logged and PRURL
// remains empty (non-git dir triggers an immediate push failure).
func TestMaybeOpenPR_PushFails(t *testing.T) {
	dir := t.TempDir() // not a git repo — push will fail
	res := &Result{Commits: []string{"abc"}}
	gc := &GitCommitter{ProjectDir: dir, Branch: "main", Enabled: true}
	cfg := Config{AutoPR: true, ProjectDir: dir, Branch: "main"}

	var logs []string
	maybeOpenPR(cfg, gc, res, func(f string, a ...any) {
		logs = append(logs, fmt.Sprintf(f, a...))
	})

	if res.PRURL != "" {
		t.Fatalf("expected empty PRURL after push failure; got %q", res.PRURL)
	}
	found := false
	for _, l := range logs {
		if strings.Contains(l, "push") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected push-related log entry; got %v", logs)
	}
}

// TestMaybeOpenPR_OpenPRFails verifies that when push succeeds but gh fails,
// the error is logged and PRURL remains empty.
func TestMaybeOpenPR_OpenPRFails(t *testing.T) {
	src, branch := setupRepoWithRemote(t)

	restore := makeFakeGh(t, "auth error", 1)
	defer restore()

	res := &Result{Commits: []string{"sha1"}, RunID: 1}
	gc := &GitCommitter{ProjectDir: src, Branch: branch, Enabled: true}
	cfg := Config{AutoPR: true, ProjectDir: src, Branch: branch, ProjectName: "proj"}

	var logs []string
	maybeOpenPR(cfg, gc, res, func(f string, a ...any) {
		logs = append(logs, fmt.Sprintf(f, a...))
	})

	if res.PRURL != "" {
		t.Fatalf("expected empty PRURL after OpenPR failure; got %q", res.PRURL)
	}
	found := false
	for _, l := range logs {
		if strings.Contains(strings.ToLower(l), "pr") || strings.Contains(l, "open") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected PR-related log entry; got %v", logs)
	}
}

// TestMaybeOpenPR_HappyPath_ExplicitTitleBody verifies the happy path when the
// caller supplies PRTitle and PRBody explicitly.
func TestMaybeOpenPR_HappyPath_ExplicitTitleBody(t *testing.T) {
	src, branch := setupRepoWithRemote(t)

	wantURL := "https://github.com/owner/repo/pull/7"
	restore := makeFakeGh(t, wantURL, 0)
	defer restore()

	res := &Result{Commits: []string{"sha1"}, RunID: 1}
	gc := &GitCommitter{ProjectDir: src, Branch: branch, Enabled: true}
	cfg := Config{
		AutoPR:      true,
		ProjectDir:  src,
		Branch:      branch,
		ProjectName: "myproject",
		PRTitle:     "My Title",
		PRBody:      "My Body",
	}

	maybeOpenPR(cfg, gc, res, func(string, ...any) {})

	if res.PRURL != wantURL {
		t.Fatalf("expected PRURL=%q; got %q", wantURL, res.PRURL)
	}
}

// TestMaybeOpenPR_HappyPath_DefaultTitleBody verifies that when PRTitle and
// PRBody are empty, defaults are generated and the PR URL is recorded.
func TestMaybeOpenPR_HappyPath_DefaultTitleBody(t *testing.T) {
	src, branch := setupRepoWithRemote(t)

	wantURL := "https://github.com/owner/repo/pull/8"
	restore := makeFakeGh(t, wantURL, 0)
	defer restore()

	res := &Result{Commits: []string{"sha1", "sha2"}, RunID: 42}
	gc := &GitCommitter{ProjectDir: src, Branch: branch, Enabled: true}
	cfg := Config{
		AutoPR:      true,
		ProjectDir:  src,
		Branch:      branch,
		ProjectName: "myproject",
		// PRTitle / PRBody intentionally empty → defaults generated inside maybeOpenPR
	}

	var logs []string
	maybeOpenPR(cfg, gc, res, func(f string, a ...any) {
		logs = append(logs, fmt.Sprintf(f, a...))
	})

	if res.PRURL != wantURL {
		t.Fatalf("expected PRURL=%q; got %q", wantURL, res.PRURL)
	}
	found := false
	for _, l := range logs {
		if strings.Contains(l, "PR opened") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'PR opened' log entry; got %v", logs)
	}
}
