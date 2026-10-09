package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_HappyPath verifies that run returns combined output and nil error
// for a valid git command executed in a real repo directory.
func TestRun_HappyPath(t *testing.T) {
	dir := initTempRepo(t)
	g := &GitCommitter{ProjectDir: dir, Branch: "main", Enabled: true}
	out, err := g.run("rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("run rev-parse HEAD: unexpected error: %v (output: %q)", err, out)
	}
	sha := strings.TrimSpace(out)
	if len(sha) < 7 {
		t.Fatalf("expected a full SHA from rev-parse HEAD; got %q", sha)
	}
}

// TestRun_CommandFailure verifies that a non-zero git exit status is returned
// as a non-nil error (the caller can inspect the combined output for details).
func TestRun_CommandFailure(t *testing.T) {
	dir := initTempRepo(t)
	g := &GitCommitter{ProjectDir: dir, Branch: "main", Enabled: true}
	// "git rev-parse --verify <nonexistent-ref>" exits non-zero in a fresh repo.
	out, err := g.run("rev-parse", "--verify", "refs/heads/nonexistent-branch-xyz")
	if err == nil {
		t.Fatalf("expected non-nil error for bad ref; got nil (output: %q)", out)
	}
}

// TestRun_InvalidDir verifies that run returns a non-nil error when
// ProjectDir does not exist (exec.Cmd.Run sets the chdir error).
func TestRun_InvalidDir(t *testing.T) {
	g := &GitCommitter{ProjectDir: "/nonexistent/path/that/cannot/exist/xyz", Branch: "main", Enabled: true}
	out, err := g.run("status")
	if err == nil {
		t.Fatalf("expected error for non-existent ProjectDir; got nil (output: %q)", out)
	}
}

// TestRun_OutputCaptured verifies that combined stdout+stderr is captured and
// returned as the first return value.
func TestRun_OutputCaptured(t *testing.T) {
	dir := initTempRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "probe.txt"), []byte("probe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &GitCommitter{ProjectDir: dir, Branch: "main", Enabled: true}
	out, err := g.run("status", "--short")
	if err != nil {
		t.Fatalf("run git status: unexpected error: %v", err)
	}
	if !strings.Contains(out, "probe.txt") {
		t.Errorf("expected 'probe.txt' in status output; got %q", out)
	}
}

// TestRun_TableDriven covers multiple valid git sub-commands to ensure the
// happy path works for each, confirming the Dir injection is correct.
func TestRun_TableDriven(t *testing.T) {
	dir := initTempRepo(t)
	g := &GitCommitter{ProjectDir: dir, Branch: "main", Enabled: true}

	cases := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"rev-parse HEAD", []string{"rev-parse", "HEAD"}, false},
		{"log oneline", []string{"log", "--oneline", "-1"}, false},
		{"status", []string{"status"}, false},
		{"bad subcommand", []string{"not-a-real-git-command"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := g.run(tc.args...)
			if tc.wantErr && err == nil {
				t.Errorf("expected error; got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
