package runner

// Tests for the shell tool (runner.md § Backends): the shell is the first
// of bash, pwsh, powershell and cmd on PATH, on every OS; on Windows the
// result names it; and `bash` is another name for the one tool. The Windows
// branch runs here on any OS: the resolution takes the OS and the lookup,
// and stub programs on a fake PATH stand for the shells.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// shellOrder is the order the shells are looked for, and shellArgs the
// arguments each is given before the command.
var (
	shellOrder = []string{"bash", "pwsh", "powershell", "cmd"}
	shellArgs  = map[string][]string{
		"bash":       {"-c"},
		"pwsh":       {"-NoProfile", "-NonInteractive", "-Command"},
		"powershell": {"-NoProfile", "-NonInteractive", "-Command"},
		"cmd":        {"/d", "/s", "/c"},
	}
)

// lookIn is a lookup over a fake PATH that holds exactly these programs.
func lookIn(present ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		if slices.Contains(present, name) {
			return filepath.Join("fake", name), nil
		}
		return "", exec.ErrNotFound
	}
}

// lookOnly is this machine's lookup with every program but name hidden.
func lookOnly(name string) func(string) (string, error) {
	return func(n string) (string, error) {
		if n != name {
			return "", exec.ErrNotFound
		}
		return exec.LookPath(n)
	}
}

// The shell is the first of bash, pwsh, powershell and cmd on PATH, on every
// OS, else bash by name; the result is named on Windows alone.
func TestResolveShell_PicksByOSAndPath(t *testing.T) {
	sets := [][]string{
		{"bash", "pwsh", "powershell", "cmd"}, {"pwsh", "powershell", "cmd"}, {"powershell", "cmd"}, {"cmd"},
		{"pwsh"}, {"powershell"}, {"pwsh", "cmd"}, {}, {"bash"}, {"bash", "cmd"}, {"cmd", "bash"}, {"zsh"},
	}
	for _, goos := range []string{"windows", "darwin", "linux", "freebsd"} {
		for _, present := range sets {
			want := "bash"
			for _, name := range shellOrder {
				if slices.Contains(present, name) {
					want = name
					break
				}
			}
			wantPath := want
			if slices.Contains(present, want) {
				wantPath = filepath.Join("fake", want)
			}
			got := resolveShell(goos, lookIn(present...))
			if got.name != want || got.path != wantPath || !slices.Equal(got.args, shellArgs[want]) {
				t.Errorf("%s with %v on PATH: %s at %s %v, want %s at %s %v", goos, present, got.name, got.path, got.args, want, wantPath, shellArgs[want])
			}
			if got.named != (goos == "windows") {
				t.Errorf("%s with %v on PATH: named %v", goos, present, got.named)
			}
		}
	}
}

// On a Windows machine simulated here, stub programs on a fake PATH stand
// for the shells. The tool runs the one the resolution picked, with that
// shell's arguments before the command, its description names the shell,
// and the result's first line names it. Removing the stub the resolution
// picked makes the next resolution fall through to the next shell.
func TestShellTool_RunsTheResolvedShellAndNamesIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stubs are sh scripts; TestShellTool_EveryShellOnThisMachineRunsACommand covers Windows")
	}
	bin := t.TempDir()
	for _, name := range shellOrder {
		script := "#!/bin/sh\nprintf 'stub %s' \"$0\"\nfor a in \"$@\"; do printf ' <%s>' \"$a\"; done\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	const command = "echo hi there"
	for _, want := range shellOrder {
		sh := resolveShell("windows", exec.LookPath)
		if sh.name != want || sh.path != filepath.Join(bin, want) {
			t.Fatalf("resolved %s at %s, want %s at %s", sh.name, sh.path, want, filepath.Join(bin, want))
		}
		r := &ToolRegistry{Handlers: map[string]ToolHandler{}}
		r.registerShell(t.TempDir(), NewFSRecorder(), sh)
		desc := r.Schemas[0].OfTool.Description.Value
		if wantDesc := "through " + want + " (`" + strings.Join(append([]string{want}, shellArgs[want]...), " ") + "`)"; !strings.Contains(desc, wantDesc) {
			t.Errorf("%s: the description %q lacks %q", want, desc, wantDesc)
		}
		out, isErr, err := r.Invoke(context.Background(), "bash", map[string]any{"command": command})
		if err != nil || isErr {
			t.Fatalf("%s: %v, isError %v: %q", want, err, isErr, out)
		}
		first, rest, _ := strings.Cut(out, "\n")
		if first != "[shell: "+want+"]" {
			t.Errorf("%s: the result's first line is %q, want [shell: %s]", want, first, want)
		}
		wantRest := "stub " + filepath.Join(bin, want)
		for _, a := range append(slices.Clone(shellArgs[want]), command) {
			wantRest += " <" + a + ">"
		}
		if rest != wantRest {
			t.Errorf("%s ran as %q, want %q", want, rest, wantRest)
		}
		if err := os.Remove(filepath.Join(bin, want)); err != nil {
			t.Fatal(err)
		}
	}
}

// Every shell on this machine runs a command with a quoted word: bash and
// the PowerShells print it bare, cmd prints the quotes, and on Windows the
// result names the shell. On windows-latest that is bash (Git for Windows),
// pwsh, powershell and cmd, so cmd's whole command line runs there.
func TestShellTool_EveryShellOnThisMachineRunsACommand(t *testing.T) {
	var ran []string
	for _, name := range shellOrder {
		if _, err := exec.LookPath(name); err != nil {
			continue
		}
		sh := resolveShell(runtime.GOOS, lookOnly(name))
		if sh.name != name {
			t.Fatalf("with %s alone on PATH the shell is %s", name, sh.name)
		}
		r := &ToolRegistry{Handlers: map[string]ToolHandler{}}
		r.registerShell(t.TempDir(), NewFSRecorder(), sh)
		out, isErr, err := r.Invoke(context.Background(), "shell", map[string]any{"command": `echo "hi"`})
		if err != nil || isErr {
			t.Errorf("%s: %v, isError %v: %q", name, err, isErr, out)
			continue
		}
		want := "hi\n"
		if name == "cmd" {
			want = "\"hi\"\n"
		}
		if runtime.GOOS == "windows" {
			want = "[shell: " + name + "]\n" + want
		}
		if got := strings.ReplaceAll(out, "\r", ""); got != want {
			t.Errorf("%s: the result is %q, want %q", name, got, want)
		}
		ran = append(ran, name)
	}
	if len(ran) == 0 {
		t.Fatal("no shell on this machine")
	}
	t.Logf("ran through %v", ran)
}

// On this machine the registry offers `shell` and runs a command through the
// first shell of the order on PATH; on Windows the result names it, elsewhere
// it is the output alone. `bash` and `shell` are one tool: one schema, one
// handler, one rest hint, and an audit row that names the tool shell under
// either name.
func TestShellTool_RunsOnThisMachineUnderBothNames(t *testing.T) {
	sh := hostShell()
	wantShell := "bash"
	for _, name := range shellOrder {
		if _, err := exec.LookPath(name); err == nil {
			wantShell = name
			break
		}
	}
	if sh.name != wantShell {
		t.Errorf("the shell resolved is %s; the first of %v on PATH is %s", sh.name, shellOrder, wantShell)
	}
	r := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
	if names := r.Names(); !slices.Contains(names, "shell") || slices.Contains(names, "bash") {
		t.Fatalf("the registry offers %v; want shell and not bash", names)
	}
	want := "hi\n"
	if runtime.GOOS == "windows" {
		want = "[shell: " + sh.name + "]\n" + want
	}
	var rows []ToolInvocation
	for _, name := range []string{"shell", "bash"} {
		out, isErr, err := r.Invoke(context.Background(), name, map[string]any{"command": "echo hi"})
		if err != nil || isErr {
			t.Fatalf("%s: %v, isError %v: %q", name, err, isErr, out)
		}
		if got := strings.ReplaceAll(out, "\r", ""); got != want {
			t.Errorf("%s: the result is %q, want %q", name, got, want)
		}
		rows = r.Invocations()
		row := rows[len(rows)-1]
		if row.Tool != "shell" {
			t.Errorf("%s: the audit row names %q, want shell", name, row.Tool)
		}
		if r.restOf(name, nil) == nil || r.restOf(name, nil)("") != r.restOf("shell", nil)("") {
			t.Errorf("%s: the rest hint is not shell's", name)
		}
	}
	if len(rows) != 2 || rows[0].Input != rows[1].Input || rows[0].Output != rows[1].Output || rows[0].IsError != rows[1].IsError {
		t.Errorf("the two calls' audit rows differ: %+v", rows)
	}

	for _, keep := range []string{"shell", "bash"} {
		r := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
		r.Restrict([]string{keep})
		if names := r.Names(); !slices.Equal(names, []string{"shell"}) {
			t.Errorf("tools: [%s] keeps %v, want [shell]", keep, names)
		}
		if out, isErr, err := r.Invoke(context.Background(), "bash", map[string]any{"command": "echo hi"}); err != nil || isErr {
			t.Errorf("tools: [%s]: a bash call got %v, isError %v: %q", keep, err, isErr, out)
		}
	}
	if !slices.Contains(workflow.ToolNames, "shell") || workflow.CanonicalTool("bash") != "shell" || workflow.CanonicalTool("shell") != "shell" {
		t.Errorf("workflow.ToolNames %v, CanonicalTool(bash) %q", workflow.ToolNames, workflow.CanonicalTool("bash"))
	}
}
