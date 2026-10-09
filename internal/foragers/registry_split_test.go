package foragers

import "testing"

func TestSplitByArchetype_OnlyLens(t *testing.T) {
	swarm := []Forager{
		{Name: "skeptic", Archetype: "lens"},
		{Name: "optimist", Archetype: "lens"},
	}
	lens, dreamers := SplitByArchetype(swarm)
	if len(lens) != 2 {
		t.Errorf("len(lens) = %d; want 2", len(lens))
	}
	if len(dreamers) != 0 {
		t.Errorf("len(dreamers) = %d; want 0", len(dreamers))
	}
	// Sorted alphabetically.
	if lens[0].Name != "optimist" {
		t.Errorf("lens[0] = %q; want optimist", lens[0].Name)
	}
}

func TestSplitByArchetype_OnlyDreamers(t *testing.T) {
	swarm := []Forager{
		{Name: "z-dreamer", Archetype: "dreamer"},
		{Name: "a-dreamer", Archetype: "dreamer"},
	}
	lens, dreamers := SplitByArchetype(swarm)
	if len(lens) != 0 {
		t.Errorf("len(lens) = %d; want 0", len(lens))
	}
	if len(dreamers) != 2 {
		t.Errorf("len(dreamers) = %d; want 2", len(dreamers))
	}
	if dreamers[0].Name != "a-dreamer" {
		t.Errorf("dreamers[0] = %q; want a-dreamer", dreamers[0].Name)
	}
}

func TestSplitByArchetype_Mixed(t *testing.T) {
	swarm := []Forager{
		{Name: "optimist", Archetype: "lens"},
		{Name: "dreamer", Archetype: "dreamer"},
		{Name: "skeptic", Archetype: "lens"},
	}
	lens, dreamers := SplitByArchetype(swarm)
	if len(lens) != 2 || len(dreamers) != 1 {
		t.Errorf("split = %d lens / %d dreamers; want 2/1", len(lens), len(dreamers))
	}
	if lens[0].Name != "optimist" || lens[1].Name != "skeptic" {
		t.Errorf("lens order = %v; want sorted [optimist, skeptic]", []string{lens[0].Name, lens[1].Name})
	}
}

func TestSplitByArchetype_EmptySwarm(t *testing.T) {
	lens, dreamers := SplitByArchetype(nil)
	if lens != nil || dreamers != nil {
		t.Errorf("expected nil/nil for empty input; got %v/%v", lens, dreamers)
	}
}
