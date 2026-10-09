package hive

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain runs the tests on the models config the binary embeds, no
// routing profile and no budget mode, whatever the user's own models.yaml,
// HIVE_PROFILE and HIVE_BUDGET_MODE say; a test that needs a config
// of its own points HIVE_MODELS_PATH at it. A child test process keeps
// the config its test gives it.
func TestMain(m *testing.M) {
	if os.Getenv("HIVE_TEST_MODELS_PINNED") == "1" {
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "hive-test-models-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("HIVE_TEST_MODELS_PINNED", "1")
	os.Setenv("HIVE_MODELS_PATH", filepath.Join(dir, "absent.yaml"))
	os.Unsetenv("HIVE_PROFILE")
	os.Unsetenv("HIVE_BUDGET_MODE")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
