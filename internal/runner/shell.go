package runner

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// shellRunner is the program the shell tool runs a command through, with
// the arguments that go before the command.
type shellRunner struct {
	name  string // what the tool's description and a result name
	path  string // what is run: the path the lookup found, or the name
	args  []string
	named bool // the result's first line names the shell: on Windows
}

// shells are the shells the tool runs through, in the order it looks for
// them on every OS: bash first, since every shipped prompt's example
// commands are bash-flavoured and a Windows machine with Git for Windows or
// WSL has a bash that runs them unchanged; then PowerShell 7, Windows
// PowerShell, cmd.
var shells = []shellRunner{
	{name: "bash", args: []string{"-c"}},
	{name: "pwsh", args: []string{"-NoProfile", "-NonInteractive", "-Command"}},
	{name: "powershell", args: []string{"-NoProfile", "-NonInteractive", "-Command"}},
	{name: "cmd", args: []string{"/d", "/s", "/c"}},
}

// resolveShell picks the first of shells that look, exec.LookPath or a
// test's, finds. When none is found it is bash by name, so the call fails
// where it runs. On Windows the result names the shell.
func resolveShell(goos string, look func(string) (string, error)) shellRunner {
	windows := goos == "windows"
	for _, sh := range shells {
		if path, err := look(sh.name); err == nil {
			sh.path, sh.named = path, windows
			return sh
		}
	}
	sh := shells[0]
	sh.path, sh.named = sh.name, windows
	return sh
}

// hostShell is the shell for this machine.
func hostShell() shellRunner { return resolveShell(runtime.GOOS, exec.LookPath) }

// command is cmdStr run through the shell. cmd reads its command line by
// rules of its own, so it is given the line whole (cmdCommandLine). As a
// command node's program, the shell runs in a process group of its own,
// killed whole when ctx ends, and a child that keeps the output open, as
// `cmd &` leaves one, holds Wait for at most 5 s after the shell ends.
func (sh shellRunner) command(ctx context.Context, cmdStr string) *exec.Cmd {
	args := append(append([]string{}, sh.args...), cmdStr)
	cmd := exec.CommandContext(ctx, sh.path, args...)
	if sh.name == "cmd" {
		cmdCommandLine(cmd, cmdStr)
	}
	killProcessGroupOnCancel(cmd)
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// result is the shell tool's result for a command that printed out and ended
// with err: out, after "EXIT <err>" when it failed. On Windows the result
// names the shell, so the model and the audit know which ran; elsewhere it
// is the output alone.
func (sh shellRunner) result(out string, err error) string {
	if err != nil {
		out = fmt.Sprintf("EXIT %v\n%s", err, out)
	}
	if sh.named {
		out = "[shell: " + sh.name + "]\n" + out
	}
	return out
}

// String is the shell and its arguments, as the tool's description names them.
func (sh shellRunner) String() string {
	return strings.Join(append([]string{sh.name}, sh.args...), " ")
}
