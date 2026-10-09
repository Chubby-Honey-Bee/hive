package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

const passingTest = `package pkg

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatal("nope")
	}
}
`

const failingTest = `package sub

import "testing"

func TestBroken(t *testing.T) { t.Fatal("untouched subpackage fails") }
`

func claimAndVerify(t *testing.T, projectDir string) map[string]any {
	t.Helper()
	rc := &runtimeContext{cfg: Config{ProjectDir: projectDir}, logf: makeLogf(os.Stderr)}
	rr := &RunResult{FinalText: `{"tests_pass": true}`}
	rc.verifyTestsClaim(context.Background(), workflow.DispatchNode{VerifyTests: true}, rr)
	return workflow.ExtractJSONOutput(rr.FinalText)
}

// A project that is a module in a subdirectory of its repository: the diff's
// paths are relative to the project, so a true claim stays true.
func TestVerifyTestsClaim_ProjectInRepoSubdir(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go binary not on PATH")
	}
	repo := initTinyGitRepo(t)
	proj := filepath.Join(repo, "svc")
	mustWriteFile(t, filepath.Join(proj, "go.mod"), "module verifier.test/svc\n\ngo 1.23\n")
	mustWriteFile(t, filepath.Join(proj, "pkg", "x.go"), "package pkg\n\nfunc Add(a, b int) int { return a + b }\n")
	mustWriteFile(t, filepath.Join(proj, "pkg", "x_test.go"), passingTest)
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "initial")
	mustWriteFile(t, filepath.Join(proj, "pkg", "x.go"), "package pkg\n\n// edited\nfunc Add(a, b int) int { return a + b }\n")

	if got, want := derivedAffectedPackages(proj), []string{"./pkg"}; !reflect.DeepEqual(got, want) {
		t.Errorf("packages=%v; want %v", got, want)
	}
	if out := claimAndVerify(t, proj); out["tests_pass"] != true {
		t.Errorf("tests_pass=%v (override %v); the package passes", out["tests_pass"], out["verifier_override"])
	}
}

// A package directory the agent deleted has nothing to test; it is skipped
// instead of failing `go test` with a pattern that matches nothing.
func TestDerivedAffectedPackages_SkipsDeletedDir(t *testing.T) {
	repo := initTinyGitRepo(t)
	mustWriteFile(t, filepath.Join(repo, "keep", "a.go"), "package keep\n")
	mustWriteFile(t, filepath.Join(repo, "gone", "b.go"), "package gone\n")
	mustWriteFile(t, filepath.Join(repo, "main.go"), "package main\n")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "initial")
	if err := os.RemoveAll(filepath.Join(repo, "gone")); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(repo, "keep", "a.go"), "package keep\n// edit\n")
	mustWriteFile(t, filepath.Join(repo, "main.go"), "package main\n// edit\n")

	if got, want := derivedAffectedPackages(repo), []string{".", "./keep"}; !reflect.DeepEqual(got, want) {
		t.Errorf("packages=%v; want %v", got, want)
	}
}

// Only the packages holding changed files are tested: a failing test in a
// subpackage the agent never touched does not overturn a true claim.
func TestVerifyTestsClaim_UntouchedSubpackageNotTested(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go binary not on PATH")
	}
	repo := initGoModuleRepo(t)
	mustWriteFile(t, filepath.Join(repo, "pkg", "x.go"), "package pkg\n\nfunc Add(a, b int) int { return a + b }\n")
	mustWriteFile(t, filepath.Join(repo, "pkg", "x_test.go"), passingTest)
	mustWriteFile(t, filepath.Join(repo, "pkg", "sub", "s.go"), "package sub\n")
	mustWriteFile(t, filepath.Join(repo, "pkg", "sub", "s_test.go"), failingTest)
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "initial")
	mustWriteFile(t, filepath.Join(repo, "pkg", "x.go"), "package pkg\n\n// edited\nfunc Add(a, b int) int { return a + b }\n")

	if out := claimAndVerify(t, repo); out["tests_pass"] != true {
		t.Errorf("tests_pass=%v (override %v); the changed package passes", out["tests_pass"], out["verifier_override"])
	}
}
