package models

import (
	"cmp"
	_ "embed"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed default-models.yaml
var defaultYAML []byte

// UserConfigPath returns the path to the user's override YAML, or "" if
// no path is resolvable. Precedence: $HIVE_MODELS_PATH >
// $XDG_CONFIG_HOME/hive/models.yaml > ~/.config/hive/models.yaml.
func UserConfigPath() string {
	if v := os.Getenv("HIVE_MODELS_PATH"); v != "" {
		return v
	}
	rel := filepath.Join("hive", "models.yaml")
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, rel)
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", rel)
	}
	return ""
}

var (
	loadMu sync.Mutex
	loaded *Config
	// loadedFrom is the override path loaded was read from.
	loadedFrom string
)

// Load returns the active config: the embedded default, merged with the
// override at UserConfigPath(). It is read once for the override path the
// environment names, so repeated calls during a run don't re-read the disk,
// and read again when that path changes. An override that does not parse,
// or a merge that is invalid, is left out, with a warning on stderr.
func Load() *Config {
	from := UserConfigPath()
	loadMu.Lock()
	defer loadMu.Unlock()
	if loaded == nil || from != loadedFrom {
		loaded, loadedFrom = loadFresh(), from
	}
	return loaded
}

// loadFresh reads the config: the embedded default, merged with the
// override at UserConfigPath when there is one. A missing override file is
// the common case, not an error.
func loadFresh() *Config {
	cfg := mustParse(defaultYAML, "<embedded default>")
	path := UserConfigPath()
	if path == "" {
		return cfg
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	return withOverride(cfg, path, data)
}

// withOverride is def merged with data, the override file read from path.
// An override that does not parse, or a merge that is invalid, leaves def,
// with a warning on stderr.
func withOverride(def *Config, path string, data []byte) *Config {
	override, err := parseConfig(data)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"[models] warning: %s parse failed (%v); using built-in models config\n",
			path, err)
		return def
	}
	merged := mergeConfigs(def, override)
	if err := merged.Validate(); err != nil {
		fmt.Fprintf(os.Stderr,
			"[models] warning: merged config invalid (%v); using built-in models config\n",
			err)
		return def
	}
	return merged
}

func mustParse(data []byte, label string) *Config {
	cfg, err := parseConfig(data)
	if err != nil {
		panic(fmt.Sprintf("models: %s: %v", label, err))
	}
	if err := cfg.Validate(); err != nil {
		panic(fmt.Sprintf("models: %s invalid: %v", label, err))
	}
	return cfg
}

func parseConfig(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// mergeConfigs returns a new Config where override entries take
// precedence over base entries, but missing override fields fall
// back to base. Map merges are key-level (override.Models["x"]
// replaces base.Models["x"] entirely; keys only in base survive). A profile
// is replaced whole by one of the same name, and a non-empty hive_tiers
// replaces the base's.
func mergeConfigs(base, override *Config) *Config {
	merged := &Config{
		LastUpdated: cmp.Or(override.LastUpdated, base.LastUpdated),
		Models:      mergeMaps(base.Models, override.Models),
		Tiers:       mergeMaps(base.Tiers, override.Tiers),
		Aliases:     mergeMaps(base.Aliases, override.Aliases),
		HiveTiers:   base.HiveTiers,
		Profiles:    mergeMaps(base.Profiles, override.Profiles),
	}
	if len(override.HiveTiers) > 0 {
		merged.HiveTiers = override.HiveTiers
	}
	return merged
}

// mergeMaps is a new map holding base's entries, each key override sets
// holding override's.
func mergeMaps[K comparable, V any](base, override map[K]V) map[K]V {
	merged := make(map[K]V, len(base))
	maps.Copy(merged, base)
	maps.Copy(merged, override)
	return merged
}
