package foragers

import (
	"strings"
	"testing"
)

func TestNormalizeArchetype_DefaultsLens(t *testing.T) {
	w := &Forager{Name: "x"}
	if err := w.NormalizeArchetype(); err != nil {
		t.Fatalf("NormalizeArchetype: %v", err)
	}
	if w.Archetype != ArchetypeLens {
		t.Errorf("Archetype = %q; want lens", w.Archetype)
	}
}

func TestNormalizeArchetype_DreamerKept(t *testing.T) {
	w := &Forager{Name: "x", Archetype: "dreamer"}
	if err := w.NormalizeArchetype(); err != nil {
		t.Fatalf("NormalizeArchetype: %v", err)
	}
	if w.Archetype != ArchetypeDreamer {
		t.Errorf("Archetype = %q; want dreamer", w.Archetype)
	}
}

func TestNormalizeArchetype_CaseInsensitive(t *testing.T) {
	w := &Forager{Name: "x", Archetype: "DREAMER"}
	if err := w.NormalizeArchetype(); err != nil {
		t.Fatalf("NormalizeArchetype: %v", err)
	}
	if w.Archetype != ArchetypeDreamer {
		t.Errorf("Archetype = %q; want dreamer", w.Archetype)
	}
}

func TestNormalizeArchetype_UnknownErrors(t *testing.T) {
	w := &Forager{Name: "x", Archetype: "forager-of-oz"}
	err := w.NormalizeArchetype()
	if err == nil {
		t.Fatal("expected error for unknown archetype")
	}
	if !strings.Contains(err.Error(), "unknown archetype") {
		t.Errorf("error = %v; expected mention of unknown archetype", err)
	}
}

func TestNormalizeArchetype_DependsOnMergesToBondCites(t *testing.T) {
	w := &Forager{
		Name:      "x",
		Archetype: "lens",
		DependsOn: []string{"a", "b", "  ", ""},
	}
	if err := w.NormalizeArchetype(); err != nil {
		t.Fatalf("NormalizeArchetype: %v", err)
	}
	// Empty/whitespace entries dropped → 2 bonds.
	if len(w.Bonds) != 2 {
		t.Errorf("len(Bonds) = %d; want 2", len(w.Bonds))
	}
	for _, b := range w.Bonds {
		if b.Kind != BondCites {
			t.Errorf("bond %q kind = %s; want cites", b.To, b.Kind)
		}
	}
	// DependsOn should be cleared (merged into Bonds).
	if w.DependsOn != nil {
		t.Errorf("DependsOn should be nil after normalize; got %v", w.DependsOn)
	}
}

func TestNormalizeArchetype_Idempotent(t *testing.T) {
	w := &Forager{
		Name:      "x",
		Archetype: "lens",
		DependsOn: []string{"a"},
	}
	if err := w.NormalizeArchetype(); err != nil {
		t.Fatal(err)
	}
	if err := w.NormalizeArchetype(); err != nil {
		t.Fatalf("re-normalize: %v", err)
	}
	if len(w.Bonds) != 1 {
		t.Errorf("re-normalize doubled bonds: len = %d; want 1", len(w.Bonds))
	}
}
