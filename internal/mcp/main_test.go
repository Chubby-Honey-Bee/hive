package mcp

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// `chb-mcp --version` answers and exits, as `chb --version` does, instead
// of waiting on stdin for a host that is not there.
func TestChbMCP_VersionFlagAnswersAndExits(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "chb-mcp")
	build := exec.Command("go", "build", "-o", bin, "./cmd/chb-mcp")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/chb-mcp: %v\n%s", err, out)
	}
	for _, flag := range []string{"--version", "-v"} {
		cmd := exec.Command(bin, flag)
		// A terminal, not a host: stdin never closes. The command must
		// not wait on it.
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		defer w.Close()
		cmd.Stdin = r
		cmd.Env = append(os.Environ(), "HIVE_DB_PATH="+filepath.Join(t.TempDir(), "e.db"))
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("chb-mcp %s: %v\n%s", flag, err, stderr.String())
		}
		if got := stdout.String(); got != "chb-mcp version dev\n" {
			t.Errorf("chb-mcp %s printed %q, want %q", flag, got, "chb-mcp version dev\n")
		}
	}
}
