package embed

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// A call to a test server on this machine holds an endpoint slot's lock
	// file; keep them out of the user's cache directory.
	dir, err := os.MkdirTemp("", "endpointslot-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Setenv("HIVE_ENDPOINT_SLOT_DIR", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
