package cli

import (
	"context"
	"fmt"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/comb"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
)

// The Comb discovers regions over d1..d8, and coverage reads all eight, so a
// forager whose finding carries d5..d8 is not reported blind in the region
// its finding created.
func TestPerspectiveGaps_CoverageReadsAllEightDimensions(t *testing.T) {
	s := useTestStore(t)
	type written struct {
		agent  string
		coords map[string]int
	}
	findings := []written{
		{"forager-skeptic", map[string]int{"d1": 0, "d2": 1, "d3": 0, "d4": 0, "d5": 2}},
		{"forager-skeptic", map[string]int{"d1": 2}},
	}
	for _, f := range findings {
		row := &db.Finding{Wave: 1, Agent: f.agent, MSSLabel: "definition", Finding: fmt.Sprint(f.coords)}
		dims := []**int{&row.D1, &row.D2, &row.D3, &row.D4, &row.D5, &row.D6, &row.D7, &row.D8}
		for i, p := range dims {
			if v, ok := f.coords[fmt.Sprintf("d%d", i+1)]; ok {
				*p = &v
			}
		}
		if _, err := s.Findings().AddFinding(row); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := comb.BuildAllRegions(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	regions, err := s.Comb().ListKind(db.VantageRegion)
	if err != nil {
		t.Fatal(err)
	}

	// A region covers a finding when its coordinates are the finding's first
	// k pinned dimensions, in d1..d8 order.
	within := func(region, finding map[string]int) bool {
		k := 0
		for i := 1; i <= 8; i++ {
			dim := fmt.Sprintf("d%d", i)
			v, pinned := finding[dim]
			if !pinned {
				continue
			}
			if k == len(region) {
				return true
			}
			if rv, ok := region[dim]; !ok || rv != v {
				return false
			}
			k++
		}
		return k == len(region)
	}
	lens := []foragers.Forager{{Name: "skeptic"}, {Name: "optimist"}}
	want := map[string]bool{}
	sawD5 := false
	for _, r := range regions {
		rc, err := comb.ParseRegionKey(r.VantageKey)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := rc["d5"]; ok {
			sawD5 = true
		}
		for _, w := range lens {
			covered := false
			for _, f := range findings {
				if f.agent == "forager-"+w.Name && within(rc, f.coords) {
					covered = true
				}
			}
			if !covered {
				want[r.VantageKey+"|"+w.Name] = true
			}
		}
	}
	if !sawD5 {
		t.Fatalf("setup: no region carries d5, so the test cannot see it: %d regions", len(regions))
	}

	gaps, err := computePerspectiveGaps(s, lens, false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, g := range gaps {
		got[g.RegionKey+"|"+g.Forager] = true
	}
	for k := range got {
		if !want[k] {
			t.Errorf("reported gap %q: the forager wrote a finding in that region", k)
		}
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing gap %q", k)
		}
	}
}
