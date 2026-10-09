//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package endpointslot

import (
	"fmt"
	"os"
	"runtime"
)

// errNoFileLocks says why a build without flock(2) keeps each server's
// slots in the process.
var errNoFileLocks = fmt.Errorf("this build (%s) has no flock", runtime.GOOS)

func flock(*os.File) (bool, error) { return false, errNoFileLocks }
