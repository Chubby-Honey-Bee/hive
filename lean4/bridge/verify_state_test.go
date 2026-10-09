package bridge

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// scriptTree copies verify-state.sh into <root>/lean4/bridge, so the
// script's repo-root lookup ($LEAN4_DIR/../chb) resolves to <root>/chb, and
// returns a bin dir that holds only the tools the script runs before Lean.
func scriptTree(t *testing.T) (root, script, bin string) {
	t.Helper()
	root = t.TempDir()
	dir := filepath.Join(root, "lean4", "bridge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile("verify-state.sh")
	if err != nil {
		t.Fatal(err)
	}
	script = filepath.Join(dir, "verify-state.sh")
	if err := os.WriteFile(script, src, 0o755); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"dirname", "mkdir"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not available: %v", tool, err)
		}
		if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	return root, script, bin
}

func runScript(t *testing.T, script, bin string, args ...string) (int, string) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	cmd := exec.Command(bash, append([]string{script}, args...)...)
	cmd.Env = []string{"PATH=" + bin}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), stderr.String()
	case err != nil:
		t.Fatal(err)
	}
	return 0, stderr.String()
}

// With no chb the script has no extractor. The header documents exit 2 for
// a setup error, and nothing may be extracted by any other means.
func TestVerifyStateWithoutChbIsSetupError(t *testing.T) {
	root, script, bin := scriptTree(t)
	code, stderr := runScript(t, script, bin, filepath.Join(root, "e.db"), "1")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 (setup error); stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "chb lean4-extract") {
		t.Errorf("stderr does not name the extractor chb lean4-extract:\n%s", stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "lean4", "instance")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("instance dir exists after a setup error (stat err = %v)", err)
	}
}

// A chb on PATH or at the repo root is the extractor, called with the
// database and wave the script was given.
func TestVerifyStateExtractsWithChb(t *testing.T) {
	const fakeChb = "#!/bin/sh\necho \"$@\"\n"
	for _, where := range []string{"path", "repo-root"} {
		t.Run(where, func(t *testing.T) {
			root, script, bin := scriptTree(t)
			dir := bin
			if where == "repo-root" {
				dir = root
			}
			if err := os.WriteFile(filepath.Join(dir, "chb"), []byte(fakeChb), 0o755); err != nil {
				t.Fatal(err)
			}
			db := filepath.Join(root, "e.db")
			wave := "3"
			_, stderr := runScript(t, script, bin, db, wave)
			got, err := os.ReadFile(filepath.Join(root, "lean4", "instance", "Wave"+wave+".lean"))
			if err != nil {
				t.Fatalf("no instance file: %v; stderr:\n%s", err, stderr)
			}
			want := strings.Join([]string{"lean4-extract", "--db", db, "--wave", wave}, " ") + "\n"
			if string(got) != want {
				t.Errorf("instance file = %q, want the extractor's output %q", got, want)
			}
		})
	}
}
