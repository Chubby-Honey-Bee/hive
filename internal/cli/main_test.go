package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// runAsChbEnv, set in a test's environment, makes this test binary behave
// as chb. A workflow command node resolves `chb` to the running binary, and
// under `go test` that is this one, so the subprocesses an offline workflow
// run starts reach the real commands.
const runAsChbEnv = "CHB_TEST_RUN_AS_CHB"

func TestMain(m *testing.M) {
	if os.Getenv(runAsChbEnv) == "1" {
		Main(version)
		os.Exit(0)
	}
	// A call to a test server on this machine holds an endpoint slot's lock
	// file; keep them out of the user's cache directory.
	dir, err := os.MkdirTemp("", "endpointslot-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Setenv("HIVE_ENDPOINT_SLOT_DIR", dir)
	code := runWithShippedModels(m)
	os.RemoveAll(dir)
	os.Exit(code)
}

// runWithShippedModels runs the tests, and the chb processes they start, on
// the models config the binary embeds, no routing profile and no budget
// mode, whatever the user's own models.yaml, HIVE_PROFILE and
// HIVE_BUDGET_MODE say; a test that needs a config of its own points
// HIVE_MODELS_PATH at it.
func runWithShippedModels(m *testing.M) int {
	if os.Getenv(shippedModelsPinned) == "1" {
		return m.Run()
	}
	dir, err := os.MkdirTemp("", "chb-test-models-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	os.Setenv(shippedModelsPinned, "1")
	os.Setenv("HIVE_MODELS_PATH", filepath.Join(dir, "absent.yaml"))
	os.Unsetenv("HIVE_PROFILE")
	os.Unsetenv("HIVE_BUDGET_MODE")
	return m.Run()
}

// shippedModelsPinned marks a process whose models config a test process
// has pinned already, so a child test process keeps the config its test
// gives it.
const shippedModelsPinned = "HIVE_TEST_MODELS_PINNED"
