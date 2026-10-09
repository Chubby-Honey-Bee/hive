//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package endpointslot

import (
	"errors"
	"os"
	"syscall"
)

// errNoFileLocks is nil: this build locks files with flock(2).
var errNoFileLocks error

// flock takes an exclusive lock on f without waiting. It reports false and
// no error when another open of the file holds it: flock's locks belong to
// an open file, so two opens in one process exclude each other too.
func flock(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}
