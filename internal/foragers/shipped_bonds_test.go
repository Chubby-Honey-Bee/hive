package foragers

import "testing"

// A dreamer's forager vantage always carries verdict abstain
// (internal/runner/dispatch.go), and the ∇ sensor never counts abstain
// (internal/comb/quorum.go), so a resonates pair with a dreamer on either
// side can never fire. The generated prompt would still tell the lens that
// the pair can converge, so the shipped roster declares none.
func TestShippedRoster_NoResonatesWithDreamer(t *testing.T) {
	all, err := Load("../../foragers")
	if err != nil {
		t.Fatal(err)
	}
	dreamers := map[string]bool{}
	for _, w := range all {
		if w.Archetype == ArchetypeDreamer {
			dreamers[w.Name] = true
		}
	}
	if len(dreamers) == 0 {
		t.Fatal("the shipped roster has no dreamer, so this check would pass without checking anything")
	}
	for _, w := range all {
		for _, b := range w.Bonds {
			if b.Kind == BondResonates && (dreamers[w.Name] || dreamers[b.To]) {
				t.Errorf("%s declares resonates with %s: a dreamer always abstains, so the pair can never fire", w.Name, b.To)
			}
		}
	}
}
