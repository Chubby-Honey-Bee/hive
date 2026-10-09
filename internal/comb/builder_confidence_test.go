package comb

import (
	"fmt"
	"math/big"
	"testing"
)

// exactConfidence evaluates the spec's formula in exact rationals and
// rounds down: clamp(100 × (1 − conflict_rate) × coverage_factor, 0, 100)
// for a region with findings, and 0 for one without.
func exactConfidence(f, c, g int) int {
	if f == 0 {
		return 0
	}
	rate := new(big.Rat).SetFrac64(int64(c), int64(f))
	coverage := new(big.Rat).Sub(big.NewRat(1, 1), big.NewRat(int64(g), int64(f+g+1)))
	v := new(big.Rat).Sub(big.NewRat(1, 1), rate)
	v.Mul(v, coverage)
	v.Mul(v, big.NewRat(100, 1))
	if v.Sign() < 0 {
		return 0
	}
	floor := new(big.Int).Quo(v.Num(), v.Denom())
	if floor.Cmp(big.NewInt(100)) > 0 {
		return 100
	}
	return int(floor.Int64())
}

func TestRegionConfidence_MatchesExactFormula(t *testing.T) {
	for f := 0; f <= 60; f++ {
		for c := 0; c <= f+2; c++ {
			for g := 0; g <= 12; g++ {
				if got, want := regionConfidence(f, c, g), exactConfidence(f, c, g); got != want {
					t.Fatalf("f=%d c=%d g=%d: confidence %d, want %d", f, c, g, got, want)
				}
			}
		}
	}
}

// 5 findings and 4 unresolved conflicts give exactly 100 × 0.2 × 1 = 20,
// computed in integers, where a float form would truncate to 19.
func TestBuildDigest_ExactConfidenceIsNotTruncatedDown(t *testing.T) {
	store := freshStore(t)
	const findings, conflicts = 5, 4
	var ids []int64
	for i := 0; i < findings; i++ {
		ids = append(ids, addFinding(t, store, "definition", fmt.Sprintf("fact %d", i), intPtr(1), nil, nil))
	}
	for i := 0; i < conflicts; i++ {
		if err := store.Conflicts().AddConflict(1, ids[i], ids[i+1], fmt.Sprintf("disagreement %d", i)); err != nil {
			t.Fatalf("AddConflict: %v", err)
		}
	}
	d, err := BuildDigest(store, Coords{"d1": 1})
	if err != nil {
		t.Fatalf("BuildDigest: %v", err)
	}
	if want := exactConfidence(findings, conflicts, 0); d.Confidence != want {
		t.Fatalf("confidence = %d, want %d", d.Confidence, want)
	}
}
