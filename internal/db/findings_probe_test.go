package db

import (
	"path/filepath"
	"testing"
)

func TestProbe_NoFilters(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "probe.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	d1 := 1
	d2 := 2
	for _, c := range []*Finding{
		{Wave: 1, Agent: "a", MSSLabel: "definition", D1: &d1, Finding: "x"},
		{Wave: 1, Agent: "a", MSSLabel: "assumption", D1: &d1, D2: &d2, Finding: "y"},
		{Wave: 2, Agent: "a", MSSLabel: "definition", Finding: "z"},
	} {
		if _, err := store.Findings().AddFinding(c); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	rows, err := store.Findings().Probe(map[string]any{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("Probe (no filter) = %d rows; want 3", len(rows))
	}
}

func TestProbe_DimensionFilters(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "probe.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	d1 := 0
	d2 := 3
	for _, c := range []*Finding{
		{Wave: 1, Agent: "a", MSSLabel: "definition", D1: &d1, Finding: "match-d1"},
		{Wave: 1, Agent: "a", MSSLabel: "definition", D1: &d1, D2: &d2, Finding: "match-both"},
		{Wave: 1, Agent: "a", MSSLabel: "definition", Finding: "no-coords"},
	} {
		if _, err := store.Findings().AddFinding(c); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	rows, err := store.Findings().Probe(map[string]any{"d1": 0}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Probe d1=0: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("Probe d1=0 = %d rows; want 2", len(rows))
	}

	rows, err = store.Findings().Probe(map[string]any{"d1": 0, "d2": 3}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Probe d1=0,d2=3: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("Probe d1+d2 = %d rows; want 1", len(rows))
	}
}

func TestProbe_DimensionFloat64Coercion(t *testing.T) {
	// JSON-decoded numerics arrive as float64 — Probe must coerce.
	store, err := NewStore(filepath.Join(t.TempDir(), "probe.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	d1 := 5
	if _, err := store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "a", MSSLabel: "definition", D1: &d1, Finding: "test",
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := store.Findings().Probe(map[string]any{"d1": float64(5)}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Probe d1=5.0: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("Probe d1=float64(5) = %d rows; want 1", len(rows))
	}
}

func TestProbe_DimensionSliceIN(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "probe.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	d1Vals := []int{1, 2, 3, 4}
	for _, v := range d1Vals {
		v := v
		if _, err := store.Findings().AddFinding(&Finding{
			Wave: 1, Agent: "a", MSSLabel: "definition", D1: &v, Finding: "d1=" + string(rune('0'+v)),
		}); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := store.Findings().Probe(map[string]any{
		"d1": []any{float64(1), float64(3)},
	}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Probe d1 IN (1,3): %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("Probe d1 IN (1,3) = %d rows; want 2", len(rows))
	}
}

func TestProbe_LimitApplied(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "probe.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	for i := 0; i < 10; i++ {
		if _, err := store.Findings().AddFinding(&Finding{
			Wave: 1, Agent: "a", MSSLabel: "definition", Finding: "row",
		}); err != nil {
			t.Fatal(err)
		}
	}

	limit := 3
	rows, err := store.Findings().Probe(nil, nil, nil, nil, &limit)
	if err != nil {
		t.Fatalf("Probe limit=3: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("Probe limit=3 = %d rows; want 3", len(rows))
	}
}

func TestProbe_LabelFilter(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "probe.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	for _, lab := range []string{"definition", "assumption", "definition", "unknown"} {
		if _, err := store.Findings().AddFinding(&Finding{
			Wave: 1, Agent: "a", MSSLabel: lab, Finding: lab + "-row",
		}); err != nil {
			t.Fatal(err)
		}
	}

	label := "definition"
	rows, err := store.Findings().Probe(nil, nil, &label, nil, nil)
	if err != nil {
		t.Fatalf("Probe label=definition: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("Probe label=definition = %d rows; want 2", len(rows))
	}
}

func TestProbe_WaveFilter(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "probe_wave.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	for _, w := range []int{1, 1, 2, 3} {
		if _, err := store.Findings().AddFinding(&Finding{
			Wave: w, Agent: "a", MSSLabel: "definition", Finding: "wave-row",
		}); err != nil {
			t.Fatal(err)
		}
	}

	wave := 1
	rows, err := store.Findings().Probe(map[string]any{}, &wave, nil, nil, nil)
	if err != nil {
		t.Fatalf("Probe wave=1: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("Probe wave=1 = %d rows; want 2", len(rows))
	}
}

func TestProbe_ConvergenceLevelFilter(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "probe_conv.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	// Insert 4 findings (all default convergence_level='low').
	var ids []int64
	for i := 0; i < 4; i++ {
		id, err := store.Findings().AddFinding(&Finding{
			Wave: 1, Agent: "a", MSSLabel: "definition", Finding: "conv-row",
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}

	// Promote first 2 to 'high' via direct SQL (AddFinding uses DEFAULT 'low').
	for _, id := range ids[:2] {
		if _, err := store.WriteDB.Exec("UPDATE findings SET convergence_level='high' WHERE id=?", id); err != nil {
			t.Fatal(err)
		}
	}

	level := "high"
	rows, err := store.Findings().Probe(map[string]any{}, nil, nil, &level, nil)
	if err != nil {
		t.Fatalf("Probe convergence_level=high: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("Probe convergence_level=high = %d rows; want 2", len(rows))
	}
}

func TestProbe_ZeroLimitFallsBackToDefault(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "probe_zerolimit.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	for i := 0; i < 5; i++ {
		if _, err := store.Findings().AddFinding(&Finding{
			Wave: 1, Agent: "a", MSSLabel: "definition", Finding: "row",
		}); err != nil {
			t.Fatal(err)
		}
	}

	// limit=0 should trigger the DefaultProbeLimit path (not the *limit > 0 branch)
	zero := 0
	rows, err := store.Findings().Probe(nil, nil, nil, nil, &zero)
	if err != nil {
		t.Fatalf("Probe limit=0: %v", err)
	}
	// all 5 rows returned (DefaultProbeLimit >= 5)
	if len(rows) != 5 {
		t.Errorf("Probe limit=0 (default) = %d rows; want 5", len(rows))
	}
}

func TestProbe_SliceWithNonFloat64Items(t *testing.T) {
	// Covers the default case in the []any inner type switch (non-float64 items).
	store, err := NewStore(filepath.Join(t.TempDir(), "probe_slice_int.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	for _, v := range []int{10, 20, 30} {
		v := v
		if _, err := store.Findings().AddFinding(&Finding{
			Wave: 1, Agent: "a", MSSLabel: "definition", D1: &v, Finding: "row",
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Pass plain int values inside []any (not float64) — hits the default: branch.
	rows, err := store.Findings().Probe(map[string]any{
		"d1": []any{10, 20},
	}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Probe d1 IN (10,20) with int items: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("Probe d1 IN (10,20) = %d rows; want 2", len(rows))
	}
}
