package comb

import (
	"path/filepath"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

func storeForTemplateTest(t *testing.T) *db.Store {
	t.Helper()
	s, err := db.NewStore(filepath.Join(t.TempDir(), "tpl.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestResolve_ForagerMissing(t *testing.T) {
	s := storeForTemplateTest(t)
	got := Resolve(s, "forager:nonexistent")
	if got != "" {
		t.Errorf("missing forager = %q; want empty", got)
	}
}

func TestResolve_ForagerPresent(t *testing.T) {
	s := storeForTemplateTest(t)
	if err := s.Comb().Upsert(&db.CombRow{
		VantageKey:  "forager:optimist",
		VantageKind: db.VantageForager,
		Confidence:  80, Narrative: "all systems go",
	}); err != nil {
		t.Fatal(err)
	}
	got := Resolve(s, "forager:optimist")
	if got == "" {
		t.Error("expected non-empty narrative for present forager")
	}
}

func TestResolve_MalformedRegionKey(t *testing.T) {
	s := storeForTemplateTest(t)
	got := Resolve(s, "totally invalid key")
	if got != "" {
		t.Errorf("malformed = %q; want empty", got)
	}
}

func TestResolve_RegionFallback(t *testing.T) {
	s := storeForTemplateTest(t)
	// Insert a region at "d1=0" but not at "d1=0;d2=3". Resolve("d1=0;d2=3")
	// should fall back to the d1=0 narrative.
	if err := s.Comb().Upsert(&db.CombRow{
		VantageKey:  "d1=0",
		VantageKind: db.VantageRegion,
		Narrative:   "wide region",
		Confidence:  50,
	}); err != nil {
		t.Fatal(err)
	}
	got := Resolve(s, "d1=0;d2=3")
	if got == "" {
		t.Error("expected fallback to d1=0 region")
	}
}

func TestAsNullCoords_FullSet(t *testing.T) {
	c := Coords{"d1": 1, "d3": 3, "d5": 5, "d8": 8}
	d := c.AsNullCoords()
	if !d[0].Valid || d[0].Int64 != 1 {
		t.Errorf("d[0] = %+v; want valid 1", d[0])
	}
	if d[1].Valid {
		t.Errorf("d[1] should be invalid (d2 missing)")
	}
	if !d[2].Valid || d[2].Int64 != 3 {
		t.Errorf("d[2] = %+v; want valid 3", d[2])
	}
	if !d[7].Valid || d[7].Int64 != 8 {
		t.Errorf("d[7] = %+v; want valid 8", d[7])
	}
}

func TestAsNullCoords_InvalidKeysSkipped(t *testing.T) {
	c := Coords{"d9": 99, "garbage": 1, "d1": 5}
	d := c.AsNullCoords()
	// Only d1 should be valid; d9 and garbage are skipped.
	if !d[0].Valid || d[0].Int64 != 5 {
		t.Errorf("d[0] = %+v; want valid 5", d[0])
	}
	for i := 1; i < 8; i++ {
		if d[i].Valid {
			t.Errorf("d[%d] should be invalid (no valid key)", i)
		}
	}
}

func TestResolve_ForagerLookupDBError(t *testing.T) {
	s := storeForTemplateTest(t)
	if err := s.Comb().Upsert(&db.CombRow{
		VantageKey: "forager:x", VantageKind: db.VantageForager,
	}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	got := Resolve(s, "forager:x")
	if got != "" {
		t.Errorf("expected empty after DB close; got %q", got)
	}
}

func TestResolve_RegionLookupDBError(t *testing.T) {
	s := storeForTemplateTest(t)
	if err := s.Comb().Upsert(&db.CombRow{
		VantageKey: "d1=0", VantageKind: db.VantageRegion,
	}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	got := Resolve(s, "d1=0")
	if got != "" {
		t.Errorf("expected empty after DB close; got %q", got)
	}
}
