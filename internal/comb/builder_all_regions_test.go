package comb

import (
	"context"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

func TestBuildAllRegions_EmptyDB(t *testing.T) {
	store := builderTestStore(t)
	n, err := BuildAllRegions(context.Background(), store)
	if err != nil {
		t.Fatalf("BuildAllRegions: %v", err)
	}
	// Always at least the global "" region.
	if n < 1 {
		t.Errorf("expected ≥1 region (global); got %d", n)
	}
}

func TestBuildAllRegions_WithFindings(t *testing.T) {
	store := builderTestStore(t)
	for _, c := range []*db.Finding{
		{Wave: 1, Agent: "a", MSSLabel: "definition", D1: intP(0), D2: intP(0), D3: intP(0), D4: intP(0), Finding: "x"},
		{Wave: 1, Agent: "a", MSSLabel: "assumption", D1: intP(1), D2: intP(0), D3: intP(0), D4: intP(0), Finding: "y"},
		{Wave: 1, Agent: "a", MSSLabel: "definition", D1: intP(0), D2: intP(3), D3: intP(0), D4: intP(0), Finding: "z"},
	} {
		if _, err := store.Findings().AddFinding(c); err != nil {
			t.Fatal(err)
		}
	}
	n, err := BuildAllRegions(context.Background(), store)
	if err != nil {
		t.Fatalf("BuildAllRegions: %v", err)
	}
	if n < 4 {
		// global, d1=0, d1=1, d1=0;d2=3 → at least 4
		t.Errorf("expected ≥4 regions; got %d", n)
	}
}

func TestBuildAllRegions_RunsTwice(t *testing.T) {
	store := builderTestStore(t)
	if _, err := store.Findings().AddFinding(&db.Finding{
		Wave: 1, Agent: "a", MSSLabel: "definition",
		D1: intP(0), D2: intP(0), D3: intP(0), D4: intP(0), Finding: "x",
	}); err != nil {
		t.Fatal(err)
	}
	n1, err := BuildAllRegions(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	n2, err := BuildAllRegions(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if n1 != n2 {
		t.Errorf("rerun count differs: %d vs %d", n1, n2)
	}
}
