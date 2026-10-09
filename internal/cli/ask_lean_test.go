package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// askPrinted runs `chb ask q --no-dispatch` with args and returns the argv
// of the agent-run command it prints, as sh would pass it, and the error.
func askPrinted(t *testing.T, args ...string) ([]string, error) {
	t.Helper()
	foragersAbs, err := filepath.Abs("../../foragers")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HIVE_FORAGERS_DIR", foragersAbs)
	root := &cobra.Command{Use: "chb", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(newSwarmAskCmd())
	var stderr bytes.Buffer
	root.SetErr(&stderr)
	root.SetOut(&bytes.Buffer{})
	root.SetArgs(append([]string{"ask", "Should we adopt X?", "--no-dispatch", "--no-eval", "--out", filepath.Join(t.TempDir(), "s.yaml")}, args...))
	if err := root.Execute(); err != nil {
		return nil, err
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
	lines := strings.Split(stderr.String(), "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "To run it manually:" && i+2 < len(lines) {
			return shellArgv(t, []string{"sh"}, strings.TrimSpace(lines[i+2])), nil
		}
	}
	t.Fatalf("no printed command in:\n%s", stderr.String())
	return nil, nil
}

// contextOf is the context the printed agent-run command passes in --inputs.
func contextOf(t *testing.T, argv []string) string {
	t.Helper()
	for i, a := range argv {
		if a == "--inputs" && i+1 < len(argv) {
			var in map[string]any
			if err := json.Unmarshal([]byte(argv[i+1]), &in); err != nil {
				t.Fatal(err)
			}
			s, _ := in["context"].(string)
			return s
		}
	}
	t.Fatalf("no --inputs in %q", argv)
	return ""
}

// --context-file gives every forager the file's text as its context, after
// --context's when both are given; a file that is not UTF-8 text is refused.
func TestAsk_ContextFile(t *testing.T) {
	pack := "## Roster\n\n- skeptic: wasp E, mss unk\n- scholar: cde decompose — {not a token}\n"
	path := filepath.Join(t.TempDir(), "pack.md")
	if err := os.WriteFile(path, []byte(pack), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		args []string
		want string
	}{
		"file alone":    {[]string{"--context-file", path}, pack},
		"with context":  {[]string{"--context", "Read the roster.", "--context-file", path}, "Read the roster.\n\n" + pack},
		"context alone": {[]string{"--context", "Read the roster."}, "Read the roster."},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			argv, err := askPrinted(t, c.args...)
			if err != nil {
				t.Fatal(err)
			}
			if got := contextOf(t, argv); got != c.want {
				t.Errorf("context %q, want %q", got, c.want)
			}
		})
	}

	bad := filepath.Join(t.TempDir(), "bad.bin")
	if err := os.WriteFile(bad, []byte{0xff, 0xfe, 'x'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := askPrinted(t, "--context-file", bad); err == nil || !strings.Contains(err.Error(), bad) {
		t.Errorf("err = %v, want the non-UTF-8 file named", err)
	}
	if _, err := askPrinted(t, "--context-file", filepath.Join(t.TempDir(), "missing")); err == nil || !strings.Contains(err.Error(), "--context-file") {
		t.Errorf("err = %v, want the missing file refused", err)
	}
}

// A profile or section list ask cannot use is refused before anything is
// written, naming the value and what the flag takes.
func TestAsk_PersonaFlagsRefused(t *testing.T) {
	for _, c := range []struct {
		args []string
		want []string
	}{
		{[]string{"--persona-profile", "tiny"}, []string{`persona profile "tiny"`, "want full or lean"}},
		{[]string{"--persona-sections", "1,x"}, []string{"--persona-sections", `"1,x"`, "want section numbers"}},
		{[]string{"--persona-sections", "0"}, []string{"--persona-sections", `"0"`, "want section numbers"}},
	} {
		_, err := askPrinted(t, c.args...)
		if err == nil {
			t.Errorf("ask %v passed", c.args)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("ask %v: error %q lacks %q", c.args, err, w)
			}
		}
	}
}
