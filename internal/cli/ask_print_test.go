package cli

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// pasteShells are the shells a printed command is checked against. zsh
// matters beyond sh: it expands a word that starts with '=' to a command
// path, which sh leaves alone. -f keeps a user's zsh startup files out.
var pasteShells = [][]string{{"sh"}, {"bash"}, {"zsh", "-f"}}

// shellArgv runs `chb <args>` through the shell with chb defined as a
// function that prints its arguments, and returns the argv the shell would
// hand chb.
func shellArgv(t *testing.T, shell []string, line string) []string {
	t.Helper()
	script := `chb() { for a in "$@"; do printf '%s\0' "$a"; done; }; ` + line
	out, err := exec.Command(shell[0], append(shell[1:], "-c", script)...).Output()
	if err != nil {
		t.Fatalf("%s -c %q: %v", shell[0], script, err)
	}
	argv := strings.Split(string(out), "\x00")
	return argv[:len(argv)-1]
}

// The commands --no-dispatch prints are the ones ask dispatches: the same
// argv, --branch "" included, plus the caller's --db, and every shell passes
// each argument through verbatim. The out path starts with '=', and the db
// path holds a quote and a space.
func TestAskNoDispatch_PrintsTheDispatchedCommand(t *testing.T) {
	foragersAbs, err := filepath.Abs("../../foragers")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HIVE_FORAGERS_DIR", foragersAbs)
	t.Setenv("HIVE_MAX_OUTPUT_TOKENS", "")
	oldDB := dbPath
	t.Cleanup(func() { dbPath = oldDB })

	t.Chdir(t.TempDir())
	outPath := "=swarm.yaml"
	db := filepath.Join(t.TempDir(), "it's a dir", "my db.sqlite")
	question := "is $HOME worth `whoami`? it's \"odd\" \\ really"

	root := &cobra.Command{Use: "chb", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().StringVar(&dbPath, "db", "", "")
	root.AddCommand(newSwarmAskCmd())
	var stderr bytes.Buffer
	root.SetErr(&stderr)
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"--db", db, "ask", question, "--no-dispatch", "--no-eval", "--out", outPath})
	if err := root.Execute(); err != nil {
		t.Fatalf("ask --no-dispatch: %v\n%s", err, stderr.String())
	}

	inputs, _ := json.Marshal(map[string]any{"question": question, "context": ""})
	wantPreflight := []string{"--db", db, "preflight", outPath}
	wantRun := []string{"--db", db, "agent-run", outPath, "--inputs", string(inputs), "--branch", ""}

	// The two lines after the header are the preflight and agent-run commands.
	lines := strings.Split(stderr.String(), "\n")
	var printed []string
	for i, l := range lines {
		if strings.TrimSpace(l) == "To run it manually:" && i+2 < len(lines) {
			printed = []string{strings.TrimSpace(lines[i+1]), strings.TrimSpace(lines[i+2])}
		}
	}
	if len(printed) != 2 {
		t.Fatalf("printed commands not found in:\n%s", stderr.String())
	}

	ran := 0
	for _, shell := range pasteShells {
		if _, err := exec.LookPath(shell[0]); err != nil {
			continue
		}
		ran++
		t.Run(shell[0], func(t *testing.T) {
			for i, want := range [][]string{wantPreflight, wantRun} {
				if got := shellArgv(t, shell, printed[i]); !equalStrings(got, want) {
					t.Errorf("%s\n  shell argv %q\n  want       %q", printed[i], got, want)
				}
			}
		})
	}
	if ran == 0 {
		t.Skip("no shell on PATH")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
