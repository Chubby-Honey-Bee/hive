package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/mss"
)

// TestUpdateFinding covers the branches in FindingsRepo.UpdateFinding.
func TestUpdateFinding(t *testing.T) {
	t.Run("finding not found returns error", func(t *testing.T) {
		s := newTestStore(t)
		err := s.Findings().UpdateFinding(99999, map[string]any{"finding": "x"})
		if err == nil {
			t.Fatal("expected error for non-existent finding ID")
		}
	})

	t.Run("empty updates returns nil (no-op)", func(t *testing.T) {
		s := newTestStore(t)
		id := addTestFinding(t, s, "definition", "original text", nil)
		if err := s.Findings().UpdateFinding(id, map[string]any{}); err != nil {
			t.Fatalf("empty updates: unexpected error: %v", err)
		}
	})

	t.Run("happy path: update finding text", func(t *testing.T) {
		s := newTestStore(t)
		id := addTestFinding(t, s, "definition", "original text", nil)
		if err := s.Findings().UpdateFinding(id, map[string]any{"finding": "updated text"}); err != nil {
			t.Fatalf("UpdateFinding: %v", err)
		}
		rows, err := s.Findings().Probe(map[string]any{}, nil, nil, nil, nil)
		if err != nil {
			t.Fatalf("Probe: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("expected 1 row, got %d", len(rows))
		}
		if got := rows[0]["finding"]; got != "updated text" {
			t.Errorf("finding = %q; want %q", got, "updated text")
		}
	})

	t.Run("happy path: update multiple fields", func(t *testing.T) {
		s := newTestStore(t)
		id := addTestFinding(t, s, "definition", "original", nil)
		err := s.Findings().UpdateFinding(id, map[string]any{
			"finding":     "changed",
			"evidence":    "some evidence",
			"source_urls": "https://example.com",
		})
		if err != nil {
			t.Fatalf("UpdateFinding: %v", err)
		}
	})

	t.Run("mss_label non-string returns type error", func(t *testing.T) {
		s := newTestStore(t)
		id := addTestFinding(t, s, "definition", "text", nil)
		err := s.Findings().UpdateFinding(id, map[string]any{"mss_label": 42})
		if err == nil {
			t.Fatal("expected error for non-string mss_label")
		}
	})

	t.Run("update mss_label to assumption succeeds", func(t *testing.T) {
		s := newTestStore(t)
		id := addTestFinding(t, s, "definition", "text", nil)
		if err := s.Findings().UpdateFinding(id, map[string]any{"mss_label": "assumption"}); err != nil {
			t.Fatalf("UpdateFinding to assumption: %v", err)
		}
	})

	t.Run("promote to guarantee with valid string deps", func(t *testing.T) {
		s := newTestStore(t)
		depID := addTestFinding(t, s, "definition", "dep finding", nil)
		id := addTestFinding(t, s, "assumption", "to be promoted", nil)

		depsJSON, _ := json.Marshal([]int64{depID})
		err := s.Findings().UpdateFinding(id, map[string]any{
			"mss_label":      "guarantee",
			"depends_on_ids": string(depsJSON),
		})
		if err != nil {
			t.Fatalf("promote via string deps: %v", err)
		}
	})

	t.Run("promote to guarantee with valid []int64 deps", func(t *testing.T) {
		s := newTestStore(t)
		depID := addTestFinding(t, s, "definition", "dep finding", nil)
		id := addTestFinding(t, s, "assumption", "to be promoted", nil)

		err := s.Findings().UpdateFinding(id, map[string]any{
			"mss_label":      "guarantee",
			"depends_on_ids": []int64{depID},
		})
		if err != nil {
			t.Fatalf("promote via []int64 deps: %v", err)
		}
	})

	t.Run("promote to guarantee with missing deps rejected", func(t *testing.T) {
		s := newTestStore(t)
		id := addTestFinding(t, s, "assumption", "no deps", nil)
		err := s.Findings().UpdateFinding(id, map[string]any{
			"mss_label": "guarantee",
		})
		if err == nil {
			t.Fatal("expected error when promoting to guarantee without deps")
		}
	})

	t.Run("promote to guarantee depending on unknown rejected (laundering)", func(t *testing.T) {
		s := newTestStore(t)
		unknownID := addTestFinding(t, s, "unknown", "gap", nil)
		id := addTestFinding(t, s, "assumption", "to be promoted", nil)
		depsJSON, _ := json.Marshal([]int64{unknownID})
		err := s.Findings().UpdateFinding(id, map[string]any{
			"mss_label":      "guarantee",
			"depends_on_ids": string(depsJSON),
		})
		if err == nil {
			t.Fatal("expected laundering error")
		}
	})

	t.Run("depends_on_ids as []int64 stored correctly", func(t *testing.T) {
		s := newTestStore(t)
		depID := addTestFinding(t, s, "definition", "dep", nil)
		id := addTestFinding(t, s, "definition", "target", nil)
		// Update only depends_on_ids (not label) via []int64 branch.
		err := s.Findings().UpdateFinding(id, map[string]any{
			"depends_on_ids": []int64{depID},
		})
		if err != nil {
			t.Fatalf("UpdateFinding with []int64 deps: %v", err)
		}
	})

	t.Run("depends_on_ids as scalar string stored correctly (setClauses default branch)", func(t *testing.T) {
		// Passing depends_on_ids as a plain string (not []int64) exercises the
		// `default:` arm inside the setClauses loop, which appends the value as-is.
		s := newTestStore(t)
		depID := addTestFinding(t, s, "definition", "dep", nil)
		id := addTestFinding(t, s, "definition", "target", nil)
		depsJSON, _ := json.Marshal([]int64{depID})
		err := s.Findings().UpdateFinding(id, map[string]any{
			"depends_on_ids": string(depsJSON), // string — not []int64
		})
		if err != nil {
			t.Fatalf("UpdateFinding with string depends_on_ids: %v", err)
		}
	})

	t.Run("no-op update on existing guarantee passes validation", func(t *testing.T) {
		// Verifies that re-updating a guarantee finding with an empty updates map
		// returns nil (no regression in the read-then-noop path).
		s := newTestStore(t)
		depID := addTestFinding(t, s, "definition", "dep", nil)
		id := addTestFinding(t, s, "assumption", "will be promoted", nil)
		depsJSON, _ := json.Marshal([]int64{depID})
		if err := s.Findings().UpdateFinding(id, map[string]any{
			"mss_label":      "guarantee",
			"depends_on_ids": string(depsJSON),
		}); err != nil {
			t.Fatalf("initial promote: %v", err)
		}
		// Empty update on a guarantee finding must also succeed.
		if err := s.Findings().UpdateFinding(id, map[string]any{}); err != nil {
			t.Fatalf("empty update on guarantee: %v", err)
		}
	})

	t.Run("update evidence and source_urls only", func(t *testing.T) {
		// Exercises the branch where multiple allowed columns are updated but
		// mss_label and depends_on_ids are untouched (guarantee block skipped).
		s := newTestStore(t)
		id := addTestFinding(t, s, "assumption", "original", nil)
		err := s.Findings().UpdateFinding(id, map[string]any{
			"evidence":    "new evidence text",
			"source_urls": "https://example.org/source",
		})
		if err != nil {
			t.Fatalf("UpdateFinding evidence+source_urls: %v", err)
		}
	})

	t.Run("cycle in guarantee deps rejected", func(t *testing.T) {
		s := newTestStore(t)
		// A <- B <- A would be a cycle; build a simpler self-reference case.
		// Insert A as definition.
		aID := addTestFinding(t, s, "definition", "A", nil)
		// Insert B as guarantee depending on A.
		bID := addTestFinding(t, s, "definition", "B", nil)
		bDeps, _ := json.Marshal([]int64{aID})
		if err := s.Findings().UpdateFinding(bID, map[string]any{
			"mss_label":      "guarantee",
			"depends_on_ids": string(bDeps),
		}); err != nil {
			t.Fatalf("setup B→A guarantee: %v", err)
		}
		// Now try to update A to be a guarantee that depends on B — creating a cycle.
		aDeps, _ := json.Marshal([]int64{bID})
		err := s.Findings().UpdateFinding(aID, map[string]any{
			"mss_label":      "guarantee",
			"depends_on_ids": string(aDeps),
		})
		if err == nil {
			t.Fatal("expected cycle error when creating A→B→A cycle")
		}
	})
}

// labelOf reads a finding's stored label and depends_on_ids.
func labelOf(t *testing.T, s *Store, id int64) (string, *string) {
	t.Helper()
	var label string
	var deps *string
	if err := s.ReadDB.QueryRow("SELECT mss_label, depends_on_ids FROM findings WHERE id=?", id).Scan(&label, &deps); err != nil {
		t.Fatal(err)
	}
	return label, deps
}

// Relabelling a finding unknown while a guarantee rests on it, directly or
// through a chain of any labels, would launder that guarantee, so the write
// is refused: the error names every such guarantee and the cascade that
// reverts them first. Lean's M8 takes this as the premise the Go code now
// supplies. Once the cascade has reverted them, the relabel is accepted.
func TestUpdateFinding_RelabelToUnknownUnderAGuarantee(t *testing.T) {
	t.Run("a chain of guarantees is named, ascending", func(t *testing.T) {
		s := newTestStore(t)
		a := addTestFinding(t, s, "definition", "A", nil)
		b := addTestFinding(t, s, "guarantee", "B rests on A", []int64{a})
		c := addTestFinding(t, s, "guarantee", "C rests on B", []int64{b})

		err := s.Findings().UpdateFinding(a, map[string]any{"mss_label": "unknown"})
		if !errors.Is(err, mss.ErrLaundering) {
			t.Fatalf("relabel under guarantees: err = %v, want ErrLaundering", err)
		}
		want := fmt.Sprintf("MSS error (finding %d, label unknown): guarantees %d, %d rest on it; revert them first: chb db-write cascade_revert %d", a, b, c, a)
		if err.Error() != want {
			t.Errorf("error\n got %q\nwant %q", err, want)
		}
		if label, _ := labelOf(t, s, a); label != "definition" {
			t.Errorf("label after refused relabel = %q, want definition", label)
		}

		err = s.Findings().UpdateFinding(b, map[string]any{"mss_label": "unknown"})
		want = fmt.Sprintf("MSS error (finding %d, label unknown): guarantee %d rests on it; revert it first: chb db-write cascade_revert %d", b, c, b)
		if err == nil || err.Error() != want {
			t.Errorf("one dependent: error\n got %v\nwant %q", err, want)
		}

		reverted, err := s.CascadeRevert(a)
		if err != nil || len(reverted) != 2 {
			t.Fatalf("CascadeRevert(%d) = %v, %v; want B and C reverted", a, reverted, err)
		}
		if err := s.Findings().UpdateFinding(a, map[string]any{"mss_label": "unknown"}); err != nil {
			t.Fatalf("relabel after the cascade: %v", err)
		}
		if label, _ := labelOf(t, s, a); label != "unknown" {
			t.Errorf("label after accepted relabel = %q, want unknown", label)
		}
	})

	t.Run("a guarantee resting through an assumption is named, the assumption is not", func(t *testing.T) {
		s := newTestStore(t)
		x := addTestFinding(t, s, "definition", "X", nil)
		m := addTestFinding(t, s, "assumption", "M rests on X", []int64{x})
		g := addTestFinding(t, s, "guarantee", "G rests on M", []int64{m})

		err := s.Findings().UpdateFinding(x, map[string]any{"mss_label": "unknown"})
		want := fmt.Sprintf("MSS error (finding %d, label unknown): guarantee %d rests on it; revert it first: chb db-write cascade_revert %d", x, g, x)
		if err == nil || err.Error() != want {
			t.Errorf("error\n got %v\nwant %q", err, want)
		}
	})

	t.Run("no guarantee resting on it: accepted", func(t *testing.T) {
		s := newTestStore(t)
		x := addTestFinding(t, s, "definition", "X", nil)
		addTestFinding(t, s, "assumption", "M rests on X", []int64{x})
		if err := s.Findings().UpdateFinding(x, map[string]any{"mss_label": "unknown"}); err != nil {
			t.Fatalf("relabel with only an assumption resting on it: %v", err)
		}
		if label, _ := labelOf(t, s, x); label != "unknown" {
			t.Errorf("label = %q, want unknown", label)
		}
	})

	t.Run("relabel to assumption under a guarantee: accepted", func(t *testing.T) {
		s := newTestStore(t)
		a := addTestFinding(t, s, "definition", "A", nil)
		addTestFinding(t, s, "guarantee", "B rests on A", []int64{a})
		if err := s.Findings().UpdateFinding(a, map[string]any{"mss_label": "assumption"}); err != nil {
			t.Fatalf("a guarantee may rest on an assumption: %v", err)
		}
	})
}

// A finding of any label given a dependency that does not exist is refused on
// update with the error a guarantee gets (Lean's UpdateCheckPasses asks
// existence of every label), and nothing is written.
func TestUpdateFinding_MissingDependencyRefusedForEveryLabel(t *testing.T) {
	s := newTestStore(t)
	dep := addTestFinding(t, s, "definition", "dep", nil)
	asm := addTestFinding(t, s, "assumption", "asm", nil)
	missing := dep + 1000

	for form, v := range map[string]any{
		"string":  fmt.Sprintf("[%d, %d]", dep, missing),
		"[]int64": []int64{dep, missing},
	} {
		err := s.Findings().UpdateFinding(asm, map[string]any{"depends_on_ids": v})
		if !errors.Is(err, mss.ErrMissingDeps) {
			t.Fatalf("%s form: err = %v, want ErrMissingDeps", form, err)
		}
		want := fmt.Sprintf("MSS error (finding %d, label assumption): depends_on_ids references non-existent finding %d", asm, missing)
		if err.Error() != want {
			t.Errorf("%s form: error\n got %q\nwant %q", form, err, want)
		}
		if _, deps := labelOf(t, s, asm); deps != nil {
			t.Errorf("%s form: depends_on_ids written as %q after a refused update", form, *deps)
		}
	}

	if err := s.Findings().UpdateFinding(asm, map[string]any{"depends_on_ids": []int64{dep}}); err != nil {
		t.Fatalf("existing dependency on an assumption: %v", err)
	}
	if _, deps := labelOf(t, s, asm); deps == nil || *deps != fmt.Sprintf("[%d]", dep) {
		t.Errorf("depends_on_ids = %v, want [%d]", deps, dep)
	}
}

// A dependency change of any label that would close a cycle is refused with
// the cycle named, as M9's premise asks of every label; the same change
// without the back edge is accepted.
func TestUpdateFinding_CycleThroughAssumptionsRefused(t *testing.T) {
	s := newTestStore(t)
	a1 := addTestFinding(t, s, "assumption", "A1", nil)
	a2 := addTestFinding(t, s, "assumption", "A2 rests on A1", []int64{a1})
	a3 := addTestFinding(t, s, "assumption", "A3 rests on A2", []int64{a2})
	d := addTestFinding(t, s, "definition", "D", nil)

	for name, deps := range map[string][]int64{"back edge": {d, a3}, "self": {a1}} {
		err := s.Findings().UpdateFinding(a1, map[string]any{"depends_on_ids": deps})
		if !errors.Is(err, mss.ErrCycleDetected) {
			t.Fatalf("%s: err = %v, want ErrCycleDetected", name, err)
		}
		cycle := fmt.Sprintf("%d → %d → %d → %d", a1, a3, a2, a1)
		if name == "self" {
			cycle = fmt.Sprintf("%d → %d", a1, a1)
		}
		want := fmt.Sprintf("MSS error (finding %d, label assumption): would create dependency cycle %s", a1, cycle)
		if err.Error() != want {
			t.Errorf("%s: error\n got %q\nwant %q", name, err, want)
		}
		if _, stored := labelOf(t, s, a1); stored != nil {
			t.Errorf("%s: depends_on_ids written as %q after a refused update", name, *stored)
		}
	}

	if err := s.Findings().UpdateFinding(a1, map[string]any{"depends_on_ids": []int64{d}}); err != nil {
		t.Fatalf("the same change without the back edge: %v", err)
	}
	if _, stored := labelOf(t, s, a1); stored == nil || *stored != fmt.Sprintf("[%d]", d) {
		t.Errorf("depends_on_ids = %v, want [%d]", stored, d)
	}
}
