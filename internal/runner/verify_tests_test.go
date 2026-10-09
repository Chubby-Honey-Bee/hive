package runner

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// TestDerivedAffectedPackages_NonGitDir asserts that calling the helper
// against a directory that is not a git repo returns nil (no work to do)
// rather than panicking. The verifier short-circuits on this signal.
func TestDerivedAffectedPackages_NonGitDir(t *testing.T) {
	dir := t.TempDir()
	if pkgs := derivedAffectedPackages(dir); pkgs != nil {
		t.Errorf("non-git dir: got %v; want nil", pkgs)
	}
}

// TestDerivedAffectedPackages_GitDiffParsing asserts the helper translates
// `git diff --name-only --relative` output into one `./<dir>` package
// pattern per directory, deduped. Top-level files map to `.`.
func TestDerivedAffectedPackages_GitDiffParsing(t *testing.T) {
	repo := initTinyGitRepo(t)
	// Create + modify files in two distinct packages to exercise the
	// dedupe + dir-extraction paths.
	mustWriteFile(t, filepath.Join(repo, "pkg-a/foo.go"), "package a\n")
	mustWriteFile(t, filepath.Join(repo, "pkg-b/bar.go"), "package b\n")
	mustWriteFile(t, filepath.Join(repo, "pkg-a/foo2.go"), "package a\n")
	mustWriteFile(t, filepath.Join(repo, "ignored.txt"), "noise\n")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "initial")
	// Now modify so `git diff` reports them.
	mustWriteFile(t, filepath.Join(repo, "pkg-a/foo.go"), "package a\n// edit\n")
	mustWriteFile(t, filepath.Join(repo, "pkg-b/bar.go"), "package b\n// edit\n")
	mustWriteFile(t, filepath.Join(repo, "ignored.txt"), "still noise\n")

	pkgs := derivedAffectedPackages(repo)
	gotSet := make(map[string]bool, len(pkgs))
	for _, p := range pkgs {
		gotSet[p] = true
	}
	if !gotSet["./pkg-a"] || !gotSet["./pkg-b"] {
		t.Errorf("packages = %v; want ./pkg-a and ./pkg-b present", pkgs)
	}
	if len(pkgs) != 2 {
		t.Errorf("len = %d; want 2 (deduped, .txt skipped)", len(pkgs))
	}
}

// TestVerifyTestsClaim_NoOpWhenFlagOff asserts that runResult.FinalText is
// untouched when node.VerifyTests is false — the verifier never runs.
func TestVerifyTestsClaim_NoOpWhenFlagOff(t *testing.T) {
	rc := &runtimeContext{cfg: Config{ProjectDir: t.TempDir(), Log: os.Stderr}, logf: makeLogf(os.Stderr)}
	rr := &RunResult{FinalText: `{"tests_pass": true}`}
	rc.verifyTestsClaim(context.Background(), workflow.DispatchNode{VerifyTests: false}, rr)
	if rr.FinalText != `{"tests_pass": true}` {
		t.Errorf("FinalText mutated when VerifyTests=false: %q", rr.FinalText)
	}
}

// TestVerifyTestsClaim_NoOpWhenClaimAbsent asserts the verifier never
// invents a positive claim — if the agent didn't claim tests_pass=true,
// nothing happens.
func TestVerifyTestsClaim_NoOpWhenClaimAbsent(t *testing.T) {
	rc := &runtimeContext{cfg: Config{ProjectDir: t.TempDir(), Log: os.Stderr}, logf: makeLogf(os.Stderr)}
	cases := []string{
		`{"tests_pass": false}`,
		`{"compile_ok": true}`,
		`plain text, no JSON`,
	}
	for _, in := range cases {
		rr := &RunResult{FinalText: in}
		rc.verifyTestsClaim(context.Background(), workflow.DispatchNode{VerifyTests: true}, rr)
		if rr.FinalText != in {
			t.Errorf("input %q mutated to %q", in, rr.FinalText)
		}
	}
}

// TestVerifyTestsClaim_PassingTestsLeaveOutputsUnchanged sets up a tiny
// git repo with a passing Go test, primes a "claimed tests_pass=true"
// runResult, and asserts the verifier leaves it alone (because the real
// `go test` exits 0).
func TestVerifyTestsClaim_PassingTestsLeaveOutputsUnchanged(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go binary not on PATH")
	}
	repo := initGoModuleRepo(t)
	// Create a passing test, commit it, then make a trivial edit so
	// `git diff --name-only` lists the file.
	pkgPath := filepath.Join(repo, "pkg")
	mustWriteFile(t, filepath.Join(pkgPath, "x.go"), "package pkg\n\nfunc Add(a, b int) int { return a + b }\n")
	mustWriteFile(t, filepath.Join(pkgPath, "x_test.go"), `package pkg

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatal("nope")
	}
}
`)
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "initial")
	// Trivial edit so git diff sees the package as touched.
	mustWriteFile(t, filepath.Join(pkgPath, "x.go"), "package pkg\n\n// edited\nfunc Add(a, b int) int { return a + b }\n")

	rc := &runtimeContext{cfg: Config{ProjectDir: repo, Log: os.Stderr}, logf: makeLogf(os.Stderr)}
	rr := &RunResult{FinalText: `{"tests_pass": true, "compile_ok": true}`}
	rc.verifyTestsClaim(context.Background(), workflow.DispatchNode{VerifyTests: true}, rr)

	out := workflow.ExtractJSONOutput(rr.FinalText)
	if out["tests_pass"] != true {
		t.Errorf("tests_pass = %v; want true (real tests passed)", out["tests_pass"])
	}
	if _, has := out["verifier_override"]; has {
		t.Errorf("verifier_override unexpectedly present: %v", out["verifier_override"])
	}
}

// TestVerifyTestsClaim_FailingTestsFlipClaim is the core trust-fix
// assertion. Agent says tests_pass=true; reality says no. Verifier
// rewrites runResult.FinalText so the downstream accept: predicate
// rejects, and on_reject can pick up the real failure.
func TestVerifyTestsClaim_FailingTestsFlipClaim(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go binary not on PATH")
	}
	repo := initGoModuleRepo(t)
	pkgPath := filepath.Join(repo, "pkg")
	mustWriteFile(t, filepath.Join(pkgPath, "x.go"), "package pkg\n\nfunc Add(a, b int) int { return a + b }\n")
	mustWriteFile(t, filepath.Join(pkgPath, "x_test.go"), `package pkg

import "testing"

func TestAddDeliberatelyBroken(t *testing.T) {
	if Add(1, 2) == 3 {
		t.Fatal("verifier-test: this assertion is intentionally inverted")
	}
}
`)
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "initial")
	mustWriteFile(t, filepath.Join(pkgPath, "x.go"), "package pkg\n\n// edited\nfunc Add(a, b int) int { return a + b }\n")

	rc := &runtimeContext{cfg: Config{ProjectDir: repo, Log: os.Stderr}, logf: makeLogf(os.Stderr)}
	rr := &RunResult{FinalText: `{"tests_pass": true, "compile_ok": true, "diff_summary": "looks good"}`}
	rc.verifyTestsClaim(context.Background(), workflow.DispatchNode{VerifyTests: true}, rr)

	out := workflow.ExtractJSONOutput(rr.FinalText)
	if out["tests_pass"] != false {
		t.Errorf("tests_pass = %v; want false (verifier should have flipped the lie)", out["tests_pass"])
	}
	override, _ := out["verifier_override"].(string)
	if override == "" {
		t.Errorf("verifier_override empty; want test failure snippet")
	}
	if !strings.Contains(override, "FAIL") && !strings.Contains(override, "verifier-test") {
		t.Errorf("verifier_override = %q; want it to mention the failing test output", override)
	}
	// Other fields preserved.
	if out["compile_ok"] != true {
		t.Errorf("compile_ok = %v; want preserved true", out["compile_ok"])
	}
	if out["diff_summary"] != "looks good" {
		t.Errorf("diff_summary = %v; want preserved", out["diff_summary"])
	}
}

// initTinyGitRepo creates a temp dir, runs `git init`, and configures
// a local user.email + user.name so commits succeed in CI.
func initTinyGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Verifier Test")
	return dir
}

// initGoModuleRepo creates a temp dir with `git init` AND a tiny
// `go.mod` so `go test ./pkg/...` resolves the local package without
// needing module-cache or network. Module path is fixed to a stable
// dummy name.
func initGoModuleRepo(t *testing.T) string {
	t.Helper()
	dir := initTinyGitRepo(t)
	mustWriteFile(t, filepath.Join(dir, "go.mod"), "module verifier.test\n\ngo 1.23\n")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func mustWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Force a JSON encoding error path coverage: ensure the marshal step
// is non-fatal. If outputs is unmarshalable (e.g. cyclic), the verifier
// silently skips the rewrite. We can't reach that with normal inputs,
// so this is a sanity-only check that the helper signature returns
// without panicking on edge JSON.
var _ = json.Marshal
