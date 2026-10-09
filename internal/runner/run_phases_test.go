package runner

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestApplyConfigDefaults_FillsMissing(t *testing.T) {
	in := Config{}
	got := applyConfigDefaults(in)
	if got.Log != os.Stderr {
		t.Errorf("Log default = %v, want os.Stderr", got.Log)
	}
	if got.MaxIterations != 500 {
		t.Errorf("MaxIterations default = %d, want 500", got.MaxIterations)
	}
	// Empty stays empty: loadAgentPersona reads it as agents/, then the
	// copy the binary carries, where a filled "agents" would be an explicit
	// directory with no fallback.
	if got.AgentsDir != "" {
		t.Errorf("AgentsDir default = %q, want empty", got.AgentsDir)
	}
	if got.ProjectDir == "" {
		t.Errorf("ProjectDir should default to cwd, got empty")
	}
}

func TestApplyConfigDefaults_PreservesExplicit(t *testing.T) {
	custom := &bytes.Buffer{}
	in := Config{
		Log:           custom,
		MaxIterations: 7,
		ProjectDir:    "/tmp/explicit",
		AgentsDir:     "personas",
	}
	got := applyConfigDefaults(in)
	if got.Log != custom {
		t.Errorf("Log was overwritten")
	}
	if got.MaxIterations != 7 {
		t.Errorf("MaxIterations was overwritten: %d", got.MaxIterations)
	}
	if got.ProjectDir != "/tmp/explicit" {
		t.Errorf("ProjectDir overwritten: %q", got.ProjectDir)
	}
	if got.AgentsDir != "personas" {
		t.Errorf("AgentsDir overwritten: %q", got.AgentsDir)
	}
}

func TestMakeLogf_PrefixesOutput(t *testing.T) {
	buf := &bytes.Buffer{}
	logf := makeLogf(buf)
	logf("hello %s", "world")
	got := buf.String()
	if !strings.HasPrefix(got, "[agent-run] ") {
		t.Errorf("missing prefix: %q", got)
	}
	if !strings.Contains(got, "hello world") {
		t.Errorf("missing formatted body: %q", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("missing trailing newline: %q", got)
	}
}

func TestPrepareGitCommitter_NoBranchDisablesCommitter(t *testing.T) {
	cfg := Config{ProjectDir: t.TempDir(), Branch: ""}
	gc, err := prepareGitCommitter(cfg, makeLogf(&bytes.Buffer{}))
	if err != nil {
		t.Fatalf("prepareGitCommitter: %v", err)
	}
	if gc.Enabled {
		t.Errorf("empty Branch should disable committer")
	}
}

// TestPrepareGitCommitter_EnsureBranchError covers the branch where EnsureBranch
// returns an error (non-git directory). The committer must be disabled and the
// error logged.
func TestPrepareGitCommitter_EnsureBranchError(t *testing.T) {
	var buf bytes.Buffer
	// t.TempDir() is not a git repo, so EnsureBranch will fail.
	cfg := Config{ProjectDir: t.TempDir(), Branch: "auto-fix"}
	gc, err := prepareGitCommitter(cfg, makeLogf(&buf))
	if err != nil {
		t.Fatalf("prepareGitCommitter: %v", err)
	}
	if gc.Enabled {
		t.Errorf("committer should be disabled after EnsureBranch failure")
	}
	if !strings.Contains(buf.String(), "continuing without auto-commit") {
		t.Errorf("expected 'continuing without auto-commit' in log, got: %q", buf.String())
	}
}

// TestPrepareGitCommitter_HappyPath covers the successful branch: a real git
// repo with a valid branch name. The committer must stay enabled and the branch
// name must appear in the log.
func TestPrepareGitCommitter_HappyPath(t *testing.T) {
	dir := initTempRepo(t)
	var buf bytes.Buffer
	cfg := Config{ProjectDir: dir, Branch: "auto-fix-happy"}
	gc, err := prepareGitCommitter(cfg, makeLogf(&buf))
	if err != nil {
		t.Fatalf("prepareGitCommitter: %v", err)
	}
	enterBranch(gc, makeLogf(&buf))
	if !gc.Enabled {
		t.Errorf("committer should remain enabled after successful EnsureBranch")
	}
	if !strings.Contains(buf.String(), "auto-fix-happy") {
		t.Errorf("expected branch name in log, got: %q", buf.String())
	}
}

func TestResolveLLMBackend_InjectedTakesPrecedence(t *testing.T) {
	// Reuse stubBackend from parallel_test.go (same package).
	stub := &stubBackend{}
	cfg := Config{Backend: stub}
	got, err := resolveLLMBackend(cfg, makeLogf(&bytes.Buffer{}))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != stub {
		t.Errorf("expected injected backend, got %T", got)
	}
}

// --dry-run documents "no file mutations", so the committer is skipped
// entirely: no auto-commit branch is created or checked out.
func TestCommitterFor_DryRunLeavesGitUntouched(t *testing.T) {
	dir := initCleanRepo(t)
	gc, err := committerFor(Config{ProjectDir: dir, Branch: "dry/should-not-exist", DryRun: true}, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	if gc != nil {
		t.Fatal("dry-run must not construct a committer")
	}
	out, _ := exec.Command("git", "-C", dir, "branch", "--list", "dry/should-not-exist").Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("dry-run created the branch: %q", out)
	}
}
