//go:build !windows

package runner

import (
	"os/exec"
	"syscall"
)

// killProcessGroupOnCancel starts cmd in a process group of its own and
// makes cancellation kill that whole group. Killing only the direct child
// would leave its children running: the test binaries of `go test ./...`,
// the chb subprocesses of `chb validate`.
func killProcessGroupOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
