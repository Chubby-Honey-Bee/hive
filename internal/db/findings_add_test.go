package db

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/mss"
)

// Every label's dependencies must exist, not only a guarantee's: Lean's
// WriteCheckPasses asks it of every label, and write_preserves_deps_valid
// rests on it. The refusal has the shape the guarantee case gives, and no
// row is written.
func TestAddFinding_MissingDependencyRefusedForEveryLabel(t *testing.T) {
	s := newTestStore(t)
	dep := addTestFinding(t, s, "definition", "base", nil)
	missing := dep + 1000
	for _, label := range []string{"definition", "assumption", "unknown"} {
		deps := fmt.Sprintf("[%d, %d]", dep, missing)
		_, err := s.Findings().AddFinding(&Finding{
			Wave: 1, Agent: "a", MSSLabel: label, D1: intPtr(0), Finding: label + " with a phantom dep",
			DependsOnIDs: &deps,
		})
		if !errors.Is(err, mss.ErrMissingDeps) {
			t.Fatalf("%s: err = %v, want ErrMissingDeps", label, err)
		}
		want := fmt.Sprintf("MSS error (finding 0, label %s): depends_on_ids references non-existent finding %d", label, missing)
		if err.Error() != want {
			t.Errorf("%s: error\n got %q\nwant %q", label, err, want)
		}
	}
	var n int
	if err := s.ReadDB.QueryRow("SELECT COUNT(*) FROM findings").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d findings after three refused writes, want 1", n)
	}

	if id := addTestFinding(t, s, "assumption", "rests on base", []int64{dep}); id <= dep {
		t.Errorf("assumption with an existing dependency: id = %d", id)
	}
}

func TestAddFinding_NullDepsString(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	d1 := 0
	nullDeps := "null"
	_, err = store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "a", MSSLabel: "guarantee", D1: &d1, Finding: "x",
		DependsOnIDs: &nullDeps,
	})
	if err == nil {
		t.Fatal("expected error for guarantee with deps='null'")
	}
}

func TestAddFinding_EmptyDepsArray(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	d1 := 0
	emptyArr := "[]"
	_, err = store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "a", MSSLabel: "guarantee", D1: &d1, Finding: "x",
		DependsOnIDs: &emptyArr,
	})
	if err == nil {
		t.Fatal("expected error for guarantee with deps='[]'")
	}
}

func TestAddFinding_AssumptionMissingSourceWarns(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	d1 := 0
	id, err := store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "a", MSSLabel: "assumption", D1: &d1, Finding: "no source",
	})
	if err != nil {
		t.Fatalf("AddFinding: %v", err)
	}
	if id <= 0 {
		t.Errorf("id = %d; want >0", id)
	}
}

func TestAddFinding_DefinitionWithoutSourceOK(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	d1 := 0
	id, err := store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "a", MSSLabel: "definition", D1: &d1, Finding: "no source needed",
	})
	if err != nil {
		t.Fatalf("AddFinding: %v", err)
	}
	if id <= 0 {
		t.Errorf("id = %d; want >0", id)
	}
}

func TestAddFinding_NilDepsOnGuarantee(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	d1 := 0
	_, err = store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "a", MSSLabel: "guarantee", D1: &d1, Finding: "x",
		// DependsOnIDs nil
	})
	if err == nil {
		t.Fatal("expected error for guarantee with nil deps")
	}
}

// TestAddFinding_InvalidMSSLabel covers the !label.Valid() branch.
func TestAddFinding_InvalidMSSLabel(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	_, err = store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "a", MSSLabel: "bogus", Finding: "bad label",
	})
	if err == nil {
		t.Fatal("expected error for invalid MSS label")
	}
}

// TestAddFinding_EmptyStringDepsOnGuarantee covers the *f.DependsOnIDs == "" branch.
func TestAddFinding_EmptyStringDepsOnGuarantee(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	d1 := 0
	emptyStr := ""
	_, err = store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "a", MSSLabel: "guarantee", D1: &d1, Finding: "x",
		DependsOnIDs: &emptyStr,
	})
	if err == nil {
		t.Fatal("expected error for guarantee with deps=''")
	}
}

// TestAddFinding_GuaranteeMissingDepID covers the ValidateGuaranteeDeps error branch
// (dep ID references a finding that does not exist).
func TestAddFinding_GuaranteeMissingDepID(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	d1 := 0
	deps := "[9999]" // non-existent finding ID
	_, err = store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "a", MSSLabel: "guarantee", D1: &d1, Finding: "x",
		DependsOnIDs: &deps,
	})
	if err == nil {
		t.Fatal("expected error for guarantee depending on non-existent finding")
	}
}

// TestAddFinding_GuaranteeHappyPath covers the happy path: guarantee with a valid dep.
func TestAddFinding_GuaranteeHappyPath(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	d1 := 0
	// Insert a definition as the dep.
	depID, err := store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "a", MSSLabel: "definition", D1: &d1, Finding: "base fact",
	})
	if err != nil {
		t.Fatalf("AddFinding base: %v", err)
	}

	src := "https://example.com"
	deps := fmt.Sprintf("[%d]", depID)
	id, err := store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "a", MSSLabel: "guarantee", D1: &d1,
		Finding:      "derived fact",
		DependsOnIDs: &deps,
		SourceURLs:   &src,
	})
	if err != nil {
		t.Fatalf("AddFinding guarantee: %v", err)
	}
	if id <= 0 {
		t.Errorf("id = %d; want >0", id)
	}
}

// TestAddFinding_GuaranteeNoSourceWarns covers the source-warning branch for guarantees.
func TestAddFinding_GuaranteeNoSourceWarns(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	d1 := 0
	// Insert a definition as the dep.
	depID, err := store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "a", MSSLabel: "definition", D1: &d1, Finding: "base fact",
	})
	if err != nil {
		t.Fatalf("AddFinding base: %v", err)
	}

	deps := fmt.Sprintf("[%d]", depID)
	// No SourceURLs set — should trigger the stderr warning but still succeed.
	id, err := store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "a", MSSLabel: "guarantee", D1: &d1,
		Finding:      "derived without source",
		DependsOnIDs: &deps,
	})
	if err != nil {
		t.Fatalf("AddFinding guarantee (no source): %v", err)
	}
	if id <= 0 {
		t.Errorf("id = %d; want >0", id)
	}
}

// TestAddFinding_ValidateCoordsError covers the cde.ValidateCoords failure branch
// by passing a coord value that exceeds a registered dimension's bounds.
func TestAddFinding_ValidateCoordsError(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Register a dimension with a bounded value set so out-of-range fails validation.
	if err := store.Dimensions().AddDimension("d1_test", "test dim", `["low","high"]`); err != nil {
		t.Fatalf("AddDimension: %v", err)
	}

	outOfRange := 99 // only 0 and 1 are valid for a 2-value dimension
	_, err = store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "a", MSSLabel: "definition", D1: &outOfRange, Finding: "bad coord",
	})
	if err == nil {
		t.Fatal("expected error for out-of-bounds coordinate")
	}
}
