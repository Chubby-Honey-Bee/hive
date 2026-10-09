package models

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStderr runs fn with os.Stderr redirected and returns what it wrote.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = prev
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// A fallback warning is one line naming the subsystem that failed, [models],
// not a binary: chb and chb-mcp both load this package, so a binary name
// would be wrong in one of them.
func TestFallbackWarningsNameTheModelsSubsystem(t *testing.T) {
	for name, body := range map[string]string{
		"unparseable":    "[ this is not yaml @#$",
		"merged invalid": "tiers:\n  scout:\n    standard: some-model\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "models.yaml")
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HIVE_MODELS_PATH", path)
			out := captureStderr(t, func() { loadFresh() })
			if !strings.HasPrefix(out, "[models] warning: ") || strings.Count(out, "\n") != 1 {
				t.Errorf("stderr = %q, want one line starting with %q", out, "[models] warning: ")
			}
		})
	}
}
