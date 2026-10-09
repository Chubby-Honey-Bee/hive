//go:build !windows

package runner

import "os/exec"

// cmdCommandLine is for Windows, where cmd reads its own command line;
// elsewhere the arguments stand, so a test can run a stub in cmd's place.
func cmdCommandLine(*exec.Cmd, string) {}
