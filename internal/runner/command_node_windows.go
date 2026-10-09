//go:build windows

package runner

import "os/exec"

// killProcessGroupOnCancel keeps exec's default on Windows, which has no
// process groups to signal: cancellation kills the direct child.
func killProcessGroupOnCancel(cmd *exec.Cmd) {}
