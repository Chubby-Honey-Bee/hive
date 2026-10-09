package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// lakeForTest is the lake binary on PATH or under ~/.elan; without one the
// Lean half of a test is skipped.
func lakeForTest(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("lake"); err == nil {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no lake on PATH and no home directory")
	}
	p := filepath.Join(home, ".elan", "bin", "lake")
	if _, err := os.Stat(p); err != nil {
		t.Skip("no lake toolchain")
	}
	return p
}

// leanCheck writes src to a file and typechecks it against the built MSS
// library; it returns the file's path, lean's exit code and its output.
func leanCheck(t *testing.T, lake, lean4Dir, name, src string) (string, int, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(lake, "env", "lean", path)
	cmd.Dir = lean4Dir
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		return path, exitErr.ExitCode(), string(out)
	case err != nil:
		t.Fatal(err)
	}
	return path, 0, string(out)
}

// The extraction states acyclicity beside the three one-hop invariants, as a
// `by decide` obligation through MSS.dec_acyclic_finite, and the Lean side
// decides it: the file for an acyclic database typechecks, and the file for a
// cyclic one fails on that theorem's line and nowhere else.
func TestLean4Extract_StatesAcyclicity(t *testing.T) {
	// A path holding `/-` and `-/`, which open and close a nested Lean block
	// comment: the extraction must keep it out of its header comment.
	const dbPath = "/tmp/-nested-/comment-/e.db"
	emit := func(findings []lean4Finding, wave *int) string {
		t.Helper()
		out, err := captureStdout(t, func() error { emitLean4Output(findings, dbPath, wave); return nil })
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	chain := []lean4Finding{
		{ID: 1, Label: "definition"},
		{ID: 2, Label: "guarantee", Deps: []int64{1}},
		{ID: 3, Label: "assumption", Deps: []int64{2}},
	}
	wave := 4
	for name, w := range map[string]*int{"all": nil, "wave4": &wave} {
		out := emit(chain, w)
		if want := fmt.Sprintf("theorem %s_acyclic : Acyclic %s_db := by decide", name, name); !strings.Contains(out, want) {
			t.Errorf("%s extraction lacks %q:\n%s", name, want, out)
		}
	}

	lake := lakeForTest(t)
	lean4Dir, err := filepath.Abs("../../lean4")
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command(lake, "build", "MSS")
	build.Dir = lean4Dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("lake build MSS: %v\n%s", err, out)
	}

	if _, code, out := leanCheck(t, lake, lean4Dir, "All.lean", emit(chain, nil)); code != 0 {
		t.Fatalf("the extraction of an acyclic database did not typecheck (exit %d):\n%s", code, out)
	}

	cyclic := []lean4Finding{
		{ID: 1, Label: "assumption", Deps: []int64{2}},
		{ID: 2, Label: "assumption", Deps: []int64{1}},
	}
	src := emit(cyclic, nil)
	theoremLine := 0
	for i, l := range strings.Split(src, "\n") {
		if strings.HasPrefix(l, "theorem all_acyclic ") {
			theoremLine = i + 1
		}
	}
	if theoremLine == 0 {
		t.Fatalf("no all_acyclic theorem in:\n%s", src)
	}
	path, code, out := leanCheck(t, lake, lean4Dir, "Cyclic.lean", src)
	if code == 0 {
		t.Fatalf("the extraction of a cyclic database typechecked:\n%s", out)
	}
	var errLines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, ": error:") {
			errLines = append(errLines, l)
		}
	}
	if len(errLines) == 0 {
		t.Fatalf("lean exited %d with no error line:\n%s", code, out)
	}
	for _, l := range errLines {
		if want := fmt.Sprintf("%s:%d:", path, theoremLine); !strings.HasPrefix(l, want) {
			t.Errorf("error off the acyclicity theorem's line %d: %s", theoremLine, l)
		}
	}
}
