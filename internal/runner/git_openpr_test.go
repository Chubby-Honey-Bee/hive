package runner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeFakeGh creates a temporary directory with a fake `gh` script, prepends
// it to PATH, and returns a cleanup func. The script exits with exitCode and
// prints output to stdout.
func makeFakeGh(t *testing.T, output string, exitCode int) (restorePath func()) {
	t.Helper()
	bin := t.TempDir()

	script := "#!/bin/sh\n"
	if output != "" {
		script += fmt.Sprintf("echo '%s'\n", output)
	}
	script += fmt.Sprintf("exit %d\n", exitCode)

	ghPath := filepath.Join(bin, "gh")
	if err := os.WriteFile(ghPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}

	orig := os.Getenv("PATH")
	if err := os.Setenv("PATH", bin+string(os.PathListSeparator)+orig); err != nil {
		t.Fatalf("setenv PATH: %v", err)
	}
	return func() { _ = os.Setenv("PATH", orig) }
}

// TestOpenPR_GhNotInstalled verifies that OpenPR returns an error when the gh
// CLI is absent from PATH.
func TestOpenPR_GhNotInstalled(t *testing.T) {
	// Point PATH at an empty dir so gh cannot be found.
	emptyDir := t.TempDir()
	orig := os.Getenv("PATH")
	if err := os.Setenv("PATH", emptyDir); err != nil {
		t.Fatalf("setenv PATH: %v", err)
	}
	defer func() { _ = os.Setenv("PATH", orig) }()

	_, err := OpenPR(t.TempDir(), "my-branch", "My Title", "My body")
	if err == nil {
		t.Fatal("expected error when gh is not installed; got nil")
	}
	if !strings.Contains(err.Error(), "gh CLI not installed") {
		t.Errorf("expected 'gh CLI not installed' in error; got %q", err)
	}
}

// TestOpenPR_HappyPath verifies that OpenPR returns the PR URL printed by gh.
func TestOpenPR_HappyPath(t *testing.T) {
	wantURL := "https://github.com/owner/repo/pull/42"
	restore := makeFakeGh(t, wantURL, 0)
	defer restore()

	// Verify the fake gh is actually findable before calling OpenPR.
	if _, err := exec.LookPath("gh"); err != nil {
		t.Skipf("fake gh not on PATH after setup: %v", err)
	}

	dir := t.TempDir()
	got, err := OpenPR(dir, "feature-branch", "Add feature", "Details here")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != wantURL {
		t.Errorf("expected PR URL %q; got %q", wantURL, got)
	}
}

// TestOpenPR_GhFails verifies that OpenPR surfaces the error when gh exits
// with a non-zero status (e.g., auth failure, duplicate PR, etc.).
func TestOpenPR_GhFails(t *testing.T) {
	restore := makeFakeGh(t, "authentication required", 1)
	defer restore()

	if _, err := exec.LookPath("gh"); err != nil {
		t.Skipf("fake gh not on PATH after setup: %v", err)
	}

	dir := t.TempDir()
	_, err := OpenPR(dir, "feature-branch", "Add feature", "Details here")
	if err == nil {
		t.Fatal("expected error when gh exits non-zero; got nil")
	}
	if !strings.Contains(err.Error(), "gh pr create") {
		t.Errorf("expected 'gh pr create' in error; got %q", err)
	}
}

// TestOpenPR_EmptyInputs verifies that OpenPR still works (or at least
// attempts the gh call) when title and body are empty strings — gh itself
// enforces those constraints, not OpenPR.
func TestOpenPR_EmptyInputs(t *testing.T) {
	wantURL := "https://github.com/owner/repo/pull/99"
	restore := makeFakeGh(t, wantURL, 0)
	defer restore()

	if _, err := exec.LookPath("gh"); err != nil {
		t.Skipf("fake gh not on PATH after setup: %v", err)
	}

	dir := t.TempDir()
	got, err := OpenPR(dir, "", "", "")
	if err != nil {
		t.Fatalf("unexpected error with empty inputs: %v", err)
	}
	if got != wantURL {
		t.Errorf("expected PR URL %q; got %q", wantURL, got)
	}
}
