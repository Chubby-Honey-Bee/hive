//go:build windows

package runner

import (
	"os/exec"
	"syscall"
)

// cmdCommandLine hands cmd its command line whole: `<cmd> /d /s /c
// "<command>"`. Go quotes each argument as CommandLineToArgvW reads it,
// which cmd does not: it would strip the quotes Go puts around the command
// and leave the backslashes Go puts before the command's own. With /s, cmd
// strips the first and the last quote and runs what is between them as
// written.
func cmdCommandLine(cmd *exec.Cmd, command string) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: syscall.EscapeArg(cmd.Path) + ` /d /s /c "` + command + `"`}
}
