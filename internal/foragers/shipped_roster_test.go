package foragers

import "testing"

// TestMinimal_FrameworkComplete above runs against a synthetic roster in
// which Editor owns WASP-I and is deliberation-eligible. The shipped roster
// is different: Editor is render-layer only, so the minimal preset has no
// WASP-I owner. This pins what is shipped, which swarm.md and
// foragers/README.md document.
func TestMinimal_ShippedRosterCoverage(t *testing.T) {
	all, err := Load("../../foragers")
	if err != nil {
		t.Fatal(err)
	}
	minimal := Minimal(all)
	wasp, cde, mss := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, w := range minimal {
		if w.Coverage.Wasp != "" {
			wasp[w.Coverage.Wasp] = true
		}
		if w.Coverage.Cde != "" {
			cde[w.Coverage.Cde] = true
		}
		if w.Coverage.Mss != "" {
			mss[w.Coverage.Mss] = true
		}
	}
	if len(minimal) != 7 {
		t.Errorf("minimal preset has %d foragers, docs say 7", len(minimal))
	}
	for _, axis := range []string{"k", "E", "T", "F"} {
		if !wasp[axis] {
			t.Errorf("WASP axis %q lost its owner in the shipped minimal preset", axis)
		}
	}
	if wasp["I"] {
		t.Errorf("WASP-I now has a deliberation-eligible owner in minimal — update swarm.md and foragers/README.md, which document the gap")
	}
	for _, phase := range []string{"detect", "decompose", "execute"} {
		if !cde[phase] {
			t.Errorf("CDE phase %q lost its owner", phase)
		}
	}
	for _, label := range []string{"def", "gua", "asm", "unk"} {
		if !mss[label] {
			t.Errorf("MSS label %q lost its owner", label)
		}
	}
}
