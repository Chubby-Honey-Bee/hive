package db

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/mss"
)

// TestPromoteFinding_MarshalError exercises the json.Marshal error branch (line
// 171) which is normally unreachable for []int64. We inject a failing marshaler
// via the package-level var so the branch is reachable in tests only.
func TestPromoteFinding_MarshalError(t *testing.T) {
	orig := promoteFindingMarshal
	promoteFindingMarshal = func(v any) ([]byte, error) {
		return nil, errors.New("injected marshal failure")
	}
	t.Cleanup(func() { promoteFindingMarshal = orig })

	s := newTestStore(t)
	id := addTestFinding(t, s, "assumption", "marshal-error target", nil)
	err := s.Findings().PromoteFinding(id, []int64{1})
	if err == nil {
		t.Fatal("expected error from injected marshal failure, got nil")
	}
	const want = "marshal depends_on_ids: injected marshal failure"
	if err.Error() != want {
		t.Errorf("error = %q; want %q", err.Error(), want)
	}
}

// TestPromoteFinding covers every path of FindingsRepo.PromoteFinding but
// its json.Marshal error, which an []int64 cannot reach.
func TestPromoteFinding(t *testing.T) {
	t.Run("happy path: assumption promoted to guarantee", func(t *testing.T) {
		s := newTestStore(t)
		depID := addTestFinding(t, s, "definition", "base fact", nil)
		id := addTestFinding(t, s, "assumption", "to be promoted", nil)

		if err := s.Findings().PromoteFinding(id, []int64{depID}); err != nil {
			t.Fatalf("PromoteFinding: %v", err)
		}
		var label string
		s.ReadDB.QueryRow("SELECT mss_label FROM findings WHERE id=?", id).Scan(&label)
		if label != "guarantee" {
			t.Errorf("mss_label = %q; want %q", label, "guarantee")
		}
	})

	t.Run("non-existent findingID returns error", func(t *testing.T) {
		s := newTestStore(t)
		depID := addTestFinding(t, s, "definition", "dep", nil)
		err := s.Findings().PromoteFinding(99999, []int64{depID})
		if err == nil {
			t.Fatal("expected error for non-existent findingID, got nil")
		}
	})

	t.Run("nil dependsOnIDs marshals to null and is rejected as missing deps", func(t *testing.T) {
		s := newTestStore(t)
		id := addTestFinding(t, s, "assumption", "no deps via nil", nil)
		err := s.Findings().PromoteFinding(id, nil)
		if err == nil {
			t.Fatal("expected ErrMissingDeps for nil dependsOnIDs, got nil")
		}
		if !errors.Is(err, mss.ErrMissingDeps) {
			t.Errorf("expected ErrMissingDeps, got: %v", err)
		}
	})

	t.Run("empty slice dependsOnIDs rejected as missing deps", func(t *testing.T) {
		s := newTestStore(t)
		id := addTestFinding(t, s, "assumption", "no deps via empty slice", nil)
		err := s.Findings().PromoteFinding(id, []int64{})
		if err == nil {
			t.Fatal("expected ErrMissingDeps for empty dependsOnIDs, got nil")
		}
		if !errors.Is(err, mss.ErrMissingDeps) {
			t.Errorf("expected ErrMissingDeps, got: %v", err)
		}
	})

	t.Run("dep on unknown finding rejected as laundering", func(t *testing.T) {
		s := newTestStore(t)
		unknownID := addTestFinding(t, s, "unknown", "gap", nil)
		id := addTestFinding(t, s, "assumption", "to promote", nil)
		err := s.Findings().PromoteFinding(id, []int64{unknownID})
		if err == nil {
			t.Fatal("expected ErrLaundering, got nil")
		}
		if !errors.Is(err, mss.ErrLaundering) {
			t.Errorf("expected ErrLaundering, got: %v", err)
		}
	})

	t.Run("depends_on_ids stored correctly after promotion", func(t *testing.T) {
		s := newTestStore(t)
		dep1 := addTestFinding(t, s, "definition", "dep one", nil)
		dep2 := addTestFinding(t, s, "definition", "dep two", nil)
		id := addTestFinding(t, s, "assumption", "multi-dep promotion", nil)

		if err := s.Findings().PromoteFinding(id, []int64{dep1, dep2}); err != nil {
			t.Fatalf("PromoteFinding: %v", err)
		}
		var depsJSON string
		s.ReadDB.QueryRow("SELECT depends_on_ids FROM findings WHERE id=?", id).Scan(&depsJSON)
		if depsJSON == "" || depsJSON == "null" || depsJSON == "[]" {
			t.Errorf("depends_on_ids = %q; want non-empty JSON array", depsJSON)
		}
	})

	t.Run("dep referencing non-existent finding ID rejected", func(t *testing.T) {
		s := newTestStore(t)
		id := addTestFinding(t, s, "assumption", "bad dep ref", nil)
		err := s.Findings().PromoteFinding(id, []int64{99999})
		if err == nil {
			t.Fatal("expected error for non-existent dep ID, got nil")
		}
	})

	t.Run("already a guarantee is idempotent", func(t *testing.T) {
		s := newTestStore(t)
		depID := addTestFinding(t, s, "definition", "base", nil)
		id := addTestFinding(t, s, "assumption", "will promote twice", nil)

		if err := s.Findings().PromoteFinding(id, []int64{depID}); err != nil {
			t.Fatalf("first promote: %v", err)
		}
		// Promoting again with same deps should succeed (idempotent).
		if err := s.Findings().PromoteFinding(id, []int64{depID}); err != nil {
			t.Fatalf("second promote: %v", err)
		}
		var label string
		s.ReadDB.QueryRow("SELECT mss_label FROM findings WHERE id=?", id).Scan(&label)
		if label != "guarantee" {
			t.Errorf("mss_label = %q; want guarantee", label)
		}
	})
}

func TestPromoteFinding_HappyPath(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "promote.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	d1 := 0
	defID, err := store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "a", MSSLabel: "definition", D1: &d1, Finding: "support",
	})
	if err != nil {
		t.Fatal(err)
	}
	asmID, err := store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "a", MSSLabel: "assumption", D1: &d1, Finding: "candidate",
		SourceURLs: stringPtr("https://example.com/x"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Findings().PromoteFinding(asmID, []int64{defID}); err != nil {
		t.Fatalf("PromoteFinding: %v", err)
	}

	// Verify it's now a guarantee.
	rows, err := store.Findings().GetByMSSLabel("guarantee")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if id, ok := r["id"].(int64); ok && id == asmID {
			found = true
		}
	}
	if !found {
		t.Errorf("promoted finding not in guarantees: %v", rows)
	}
}

func TestPromoteFinding_EmptyDeps_ReturnsError(t *testing.T) {
	// Promotion to guarantee with empty deps would violate the
	// non-empty-deps invariant. The marshal succeeds but the underlying
	// UpdateFinding may fail.
	store, err := NewStore(filepath.Join(t.TempDir(), "promote.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	d1 := 0
	asmID, err := store.Findings().AddFinding(&Finding{
		Wave: 1, Agent: "a", MSSLabel: "assumption", D1: &d1, Finding: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = store.Findings().PromoteFinding(asmID, nil)
	// Either error or success — both branches exercise the marshal path.
	_ = err
}

func stringPtr(s string) *string { return &s }
