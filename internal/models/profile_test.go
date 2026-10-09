package models

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Every run under a shipped profile prints its quality note in the routing
// log, and a user copies a profile from the shipped config. Each note and
// description says what was measured, and none names a label only the
// project's development explains: a configuration code, an internal script,
// a template version, a self-evaluation or a canary run.
func TestShippedProfileNotesNameNoInternalLabel(t *testing.T) {
	cfg := mustParse(defaultYAML, "test")
	internal := regexp.MustCompile(`(?i)journey|\bC\d\b|template[- ]?v\d|self-evaluation|canary`)
	for name, p := range cfg.Profiles {
		for field, text := range map[string]string{"description": p.Description, "quality": p.Quality} {
			if m := internal.FindString(text); m != "" {
				t.Errorf("%s %s names %q: %s", name, field, m, text)
			}
		}
	}
}

// The shipped profiles are the three Bench-0 local ones, each routes every
// role, and each names in quality: the measurement it rests on, Bench-1.
func TestShippedProfiles(t *testing.T) {
	cfg := mustParse(defaultYAML, "test")
	for _, name := range []string{"local-fast", "local-small", "local-8gb"} {
		p, err := cfg.Profile(name)
		if err != nil {
			t.Fatal(err)
		}
		if p.Provider != "local" {
			t.Errorf("%s: provider %q, want local", name, p.Provider)
		}
		if !strings.Contains(p.Quality, "Bench-1") {
			t.Errorf("%s: quality %q does not name the measurement it rests on", name, p.Quality)
		}
		for _, role := range Roles {
			r, ok := p.Roles[role]
			if !ok || r.Model == "" {
				t.Errorf("%s: role %s has no model", name, role)
				continue
			}
			if r.Provider != "" || r.Because != "" {
				t.Errorf("%s: role %s routes to %q (because %q); every shipped route is the profile's local provider", name, role, r.Provider, r.Because)
			}
			if !slices.Contains(RepairRoles, role) && r.Reasoning != "none" {
				t.Errorf("%s: role %s reasoning %q; Bench-0 measured thinking off", name, role, r.Reasoning)
			}
		}
		if len(p.Roles) != len(Roles) {
			t.Errorf("%s: %d roles, want the %d of Roles", name, len(p.Roles), len(Roles))
		}
	}
}

// A user profile replaces the shipped one of its name whole; the others
// stay. A non-empty hive_tiers replaces the shipped ladder.
func TestMergeConfigs_Profiles(t *testing.T) {
	base := mustParse(defaultYAML, "test")
	override, err := parseConfig([]byte(`
hive_tiers: [a, b]
profiles:
  local-fast:
    provider: local
    roles:
      lens: {model: m1}
  mine:
    provider: anthropic
    roles:
      queen: {model: claude-opus-4-8, because: "bench report 7"}
`))
	if err != nil {
		t.Fatal(err)
	}
	merged := mergeConfigs(base, override)
	if got := merged.Profiles["local-fast"]; len(got.Roles) != 1 || got.Roles["lens"].Model != "m1" {
		t.Errorf("local-fast = %+v, want the override's one role", got)
	}
	if _, ok := merged.Profiles["local-small"]; !ok {
		t.Error("local-small was dropped by an override that does not name it")
	}
	if merged.Profiles["mine"].Roles["queen"].Because != "bench report 7" {
		t.Errorf("mine = %+v", merged.Profiles["mine"])
	}
	if !slices.Equal(merged.HiveTiers, []string{"a", "b"}) {
		t.Errorf("hive_tiers %v, want the override's", merged.HiveTiers)
	}
	if kept := mergeConfigs(base, &Config{}); !slices.Equal(kept.HiveTiers, base.HiveTiers) {
		t.Errorf("an override without hive_tiers gave %v, want the base's %v", kept.HiveTiers, base.HiveTiers)
	}
}

// Profile names the profiles the config holds when asked for one it lacks.
func TestProfile_Unknown(t *testing.T) {
	cfg := mustParse(defaultYAML, "test")
	_, err := cfg.Profile("nope")
	if err == nil {
		t.Fatal("no error for an unknown profile")
	}
	for name := range cfg.Profiles {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}
}

// The hive ladder: a profile's hive_tiers, else its hive-research model
// alone, else the config's, else haiku, sonnet, opus.
func TestHiveLadder(t *testing.T) {
	cfg := &Config{
		HiveTiers: []string{"x", "y"},
		Profiles: map[string]Profile{
			"tiers":    {HiveTiers: []string{"p1", "p2", "p3"}, Roles: map[string]Route{"hive-research": {Model: "r"}}},
			"research": {Roles: map[string]Route{"hive-research": {Model: "r"}}},
			"neither":  {Roles: map[string]Route{"lens": {Model: "l"}}},
		},
	}
	for _, c := range []struct {
		profile string
		want    []string
	}{
		{"tiers", cfg.Profiles["tiers"].HiveTiers},
		{"research", []string{cfg.Profiles["research"].Roles["hive-research"].Model}},
		{"neither", cfg.HiveTiers},
		{"", cfg.HiveTiers},
		{"absent", cfg.HiveTiers},
	} {
		if got := cfg.HiveLadder(c.profile); !slices.Equal(got, c.want) {
			t.Errorf("HiveLadder(%q) = %v, want %v", c.profile, got, c.want)
		}
	}
	// A profile that routes no hive-research runs the hive's research node
	// on its default route, so its ladder is that route's model alone.
	withDefault, err := parseConfig([]byte("hive_tiers: [x, y]\nprofiles:\n  d:\n    default: {model: dm}\n    roles:\n      lens: {model: l}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := withDefault.HiveLadder("d"); !slices.Equal(got, []string{"dm"}) {
		t.Errorf("HiveLadder(d) = %v, want the default route's model", got)
	}
	if got := (&Config{}).HiveLadder(""); !slices.Equal(got, defaultHiveTiers) {
		t.Errorf("empty config ladder %v, want %v", got, defaultHiveTiers)
	}
	shipped := mustParse(defaultYAML, "test")
	if !slices.Equal(shipped.HiveLadder(""), []string{"haiku", "sonnet", "opus"}) {
		t.Errorf("shipped ladder %v", shipped.HiveLadder(""))
	}
}

// A profile and a route keep the keys they set that are none of their own,
// in the order written, so a misspelling can be refused; a merge key is not
// one. The shipped profiles set none.
func TestProfile_UnknownKeys(t *testing.T) {
	cfg, err := parseConfig([]byte(`base: &base {model: m, reasoning: none}
profiles:
  p:
    provider: local
    hive_tier: [a]
    roles:
      lens: {model: m, reasonig: none, tool: []}
      queen: {<<: *base, ttl: 5m}
`))
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Profiles["p"]
	if !slices.Equal(p.Unknown, []string{"hive_tier"}) {
		t.Errorf("profile unknown keys %v, want [hive_tier]", p.Unknown)
	}
	if got := p.Roles["lens"].Unknown; !slices.Equal(got, []string{"reasonig", "tool"}) {
		t.Errorf("lens unknown keys %v, want [reasonig tool]", got)
	}
	if q := p.Roles["queen"]; len(q.Unknown) != 0 || q.Model != "m" || q.Reasoning != "none" || q.TTL != "5m" {
		t.Errorf("queen %+v, want the merged route with no unknown keys", q)
	}
	shipped := mustParse(defaultYAML, "test")
	for name, sp := range shipped.Profiles {
		if len(sp.Unknown) > 0 {
			t.Errorf("shipped profile %s sets unknown keys %v", name, sp.Unknown)
		}
		for role, r := range sp.Roles {
			if len(r.Unknown) > 0 {
				t.Errorf("shipped profile %s role %s sets unknown keys %v", name, role, r.Unknown)
			}
		}
	}
}
