package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chb models tiers names the file its tiers are overridden from, the
// user's models config.
func TestModelsTiers_NamesTheOverride(t *testing.T) {
	modelsPath := filepath.Join(t.TempDir(), "models.yaml")
	t.Setenv("HIVE_MODELS_PATH", modelsPath)
	var out bytes.Buffer
	if err := showModelTiers(&out, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), modelsPath) {
		t.Errorf("chb models tiers does not name %s:\n%s", modelsPath, out.String())
	}
}

// userTiers points HIVE_MODELS_PATH, for this test and the chb
// processes it starts, at a models config whose tiers: section maps role
// to model at every budget mode.
func userTiers(t *testing.T, role, model string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.yaml")
	body := fmt.Sprintf("tiers:\n  %s:\n    premium: %q\n    standard: %q\n    cheap: %q\n    free: %q\n", role, model, model, model, model)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HIVE_MODELS_PATH", path)
}
