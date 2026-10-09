package models

import (
	"os"
	"path/filepath"
	"testing"
)

// overrideFile writes body as the user's models config, in a directory of
// its own, and points HIVE_MODELS_PATH at it.
func overrideFile(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HIVE_MODELS_PATH", path)
}

// The override's tiers: section replaces each tier it names whole, adds a
// tier the config lacks, and leaves the others as the config has them.
func TestLoad_TheOverrideReplacesTheTiersItNames(t *testing.T) {
	overrideFile(t, "tiers:\n  planner:\n    premium: p\n    standard: s\n    cheap: c\n    fallback: f\n    free: x\n  probe:\n    premium: q\n")
	shipped := mustParse(defaultYAML, "test").Tiers
	cfg := Load()
	for role, want := range map[string]TierSlots{
		"planner":    {Premium: "p", Standard: "s", Cheap: "c", Fallback: "f", Free: "x"},
		"probe":      {Premium: "q"},
		"synthesist": shipped["synthesist"],
	} {
		if got := cfg.Tiers[role]; got != want {
			t.Errorf("tier %s = %+v, want %+v", role, got, want)
		}
	}
}

// Load reads the config once for the override path the environment names,
// and again when it changes, so a process that points HIVE_MODELS_PATH
// elsewhere gets the tiers that file names.
func TestLoad_ReadsAgainWhenTheOverridePathChanges(t *testing.T) {
	overrideFile(t, "tiers:\n  planner:\n    premium: first\n")
	first := Load()
	if Load() != first {
		t.Error("a second Load with the same path read the config again")
	}
	overrideFile(t, "tiers:\n  planner:\n    premium: second\n")
	if got := Load().Tiers["planner"].Premium; got != "second" {
		t.Errorf("planner premium after the path changed = %q, want second", got)
	}
}
