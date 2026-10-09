package db

import (
	"testing"
)

// foragerBondRecord is a test helper that records a fired bond with runID=0
// (NULL) so there is no FK dependency on workflow_runs.
func foragerBondRecord(t *testing.T, repo *ForagerBondsRepo, from, to string, kind BondKind) int64 {
	t.Helper()
	id, err := repo.RecordFired(0, from, to, kind, 1.0, nil)
	if err != nil {
		t.Fatalf("RecordFired(%q→%q %s): %v", from, to, kind, err)
	}
	return id
}

func TestForagerBondsRepo_RecordFired(t *testing.T) {
	t.Run("happy path returns valid id", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.ForagerBonds()
		id, err := repo.RecordFired(0, "optimist", "skeptic", BondCites, 1.0, nil)
		if err != nil {
			t.Fatalf("RecordFired: %v", err)
		}
		if id <= 0 {
			t.Errorf("expected positive id, got %d", id)
		}
	})

	t.Run("weight=0 defaults to 1.0", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.ForagerBonds()
		_, err := repo.RecordFired(0, "a", "b", BondContradicts, 0, nil)
		if err != nil {
			t.Fatalf("RecordFired with weight=0: %v", err)
		}
		rows, err := repo.ListByPair("a", "b")
		if err != nil {
			t.Fatalf("ListByPair: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("expected 1 row, got %d", len(rows))
		}
		if rows[0].Weight != 1.0 {
			t.Errorf("expected weight=1.0 after default, got %f", rows[0].Weight)
		}
	})

	t.Run("invalid bond kind returns error", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.ForagerBonds()
		_, err := repo.RecordFired(0, "x", "y", BondKind("bogus"), 1.0, nil)
		if err == nil {
			t.Fatal("expected error for invalid bond kind, got nil")
		}
	})

	t.Run("empty from returns error", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.ForagerBonds()
		_, err := repo.RecordFired(0, "", "y", BondCites, 1.0, nil)
		if err == nil {
			t.Fatal("expected error for empty from, got nil")
		}
	})

	t.Run("empty to returns error", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.ForagerBonds()
		_, err := repo.RecordFired(0, "x", "", BondResonates, 1.0, nil)
		if err == nil {
			t.Fatal("expected error for empty to, got nil")
		}
	})

	t.Run("all three bond kinds are accepted", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.ForagerBonds()
		for _, kind := range []BondKind{BondCites, BondContradicts, BondResonates} {
			id, err := repo.RecordFired(0, "src", "dst", kind, 0.5, nil)
			if err != nil {
				t.Errorf("RecordFired kind=%s: %v", kind, err)
			}
			if id <= 0 {
				t.Errorf("expected positive id for kind=%s, got %d", kind, id)
			}
		}
	})

	t.Run("closed writeDB returns exec error", func(t *testing.T) {
		store := newTestStore(t)
		store.WriteDB.Close()
		repo := store.ForagerBonds()
		_, err := repo.RecordFired(0, "optimist", "skeptic", BondCites, 1.0, nil)
		if err == nil {
			t.Fatal("expected error when writeDB is closed, got nil")
		}
	})
}

// foragerBondCreateRun inserts a minimal workflow_runs row and returns its ID,
// satisfying the FK constraint on forager_bonds.run_id.
func foragerBondCreateRun(t *testing.T, store *Store) int64 {
	t.Helper()
	runID, err := store.Workflows().CreateWorkflowRun("test-swarm", 1, "---", "{}", nil)
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	return runID
}

func TestForagerBondsRepo_CountByKind(t *testing.T) {
	t.Run("empty table returns empty map", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.ForagerBonds()
		counts, err := repo.CountByKind()
		if err != nil {
			t.Fatalf("CountByKind on empty table: %v", err)
		}
		if len(counts) != 0 {
			t.Errorf("expected empty map, got %v", counts)
		}
	})

	t.Run("single bond kind counted correctly", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.ForagerBonds()
		foragerBondRecord(t, repo, "optimist", "skeptic", BondCites)
		counts, err := repo.CountByKind()
		if err != nil {
			t.Fatalf("CountByKind: %v", err)
		}
		if counts[BondCites] != 1 {
			t.Errorf("expected counts[cites]=1, got %d", counts[BondCites])
		}
		if len(counts) != 1 {
			t.Errorf("expected 1 kind in map, got %d: %v", len(counts), counts)
		}
	})

	t.Run("multiple bonds same kind accumulate", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.ForagerBonds()
		foragerBondRecord(t, repo, "a", "b", BondResonates)
		foragerBondRecord(t, repo, "c", "d", BondResonates)
		foragerBondRecord(t, repo, "e", "f", BondResonates)
		counts, err := repo.CountByKind()
		if err != nil {
			t.Fatalf("CountByKind: %v", err)
		}
		if counts[BondResonates] != 3 {
			t.Errorf("expected counts[resonates]=3, got %d", counts[BondResonates])
		}
	})

	t.Run("mixed kinds counted independently", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.ForagerBonds()
		foragerBondRecord(t, repo, "a", "b", BondCites)
		foragerBondRecord(t, repo, "c", "d", BondCites)
		foragerBondRecord(t, repo, "e", "f", BondContradicts)
		foragerBondRecord(t, repo, "g", "h", BondResonates)
		foragerBondRecord(t, repo, "i", "j", BondResonates)
		counts, err := repo.CountByKind()
		if err != nil {
			t.Fatalf("CountByKind: %v", err)
		}
		cases := []struct {
			kind BondKind
			want int
		}{
			{BondCites, 2},
			{BondContradicts, 1},
			{BondResonates, 2},
		}
		for _, c := range cases {
			if counts[c.kind] != c.want {
				t.Errorf("counts[%s]=%d; want %d", c.kind, counts[c.kind], c.want)
			}
		}
		if len(counts) != 3 {
			t.Errorf("expected 3 kinds in map, got %d: %v", len(counts), counts)
		}
	})

	t.Run("closed readDB returns query error", func(t *testing.T) {
		store := newTestStore(t)
		// Close the read connection before querying to exercise the error branch.
		store.ReadDB.Close()
		repo := store.ForagerBonds()
		_, err := repo.CountByKind()
		if err == nil {
			t.Fatal("expected error when readDB is closed, got nil")
		}
	})
}

func TestForagerBondsRepo_ListByPair(t *testing.T) {
	t.Run("empty table returns empty slice", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.ForagerBonds()
		bonds, err := repo.ListByPair("optimist", "skeptic")
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if len(bonds) != 0 {
			t.Fatalf("expected 0 bonds, got %d", len(bonds))
		}
	})

	t.Run("happy path returns matching bonds only", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.ForagerBonds()

		foragerBondRecord(t, repo, "optimist", "skeptic", BondCites)
		// Bond with different pair must NOT appear.
		foragerBondRecord(t, repo, "pragmatist", "empiricist", BondContradicts)

		bonds, err := repo.ListByPair("optimist", "skeptic")
		if err != nil {
			t.Fatalf("ListByPair: %v", err)
		}
		if len(bonds) != 1 {
			t.Fatalf("expected 1 bond, got %d", len(bonds))
		}
		if bonds[0].From != "optimist" || bonds[0].To != "skeptic" {
			t.Errorf("unexpected from/to: %q → %q", bonds[0].From, bonds[0].To)
		}
		if bonds[0].Kind != BondCites {
			t.Errorf("expected BondCites, got %q", bonds[0].Kind)
		}
	})

	t.Run("multiple bonds for same pair accumulate and order by id desc", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.ForagerBonds()

		id1 := foragerBondRecord(t, repo, "dreamer", "surveyor", BondCites)
		id2 := foragerBondRecord(t, repo, "dreamer", "surveyor", BondResonates)
		id3 := foragerBondRecord(t, repo, "dreamer", "surveyor", BondContradicts)

		bonds, err := repo.ListByPair("dreamer", "surveyor")
		if err != nil {
			t.Fatalf("ListByPair: %v", err)
		}
		if len(bonds) != 3 {
			t.Fatalf("expected 3 bonds, got %d", len(bonds))
		}
		// Ordered by observed_at DESC, id DESC — inserted same-second → id DESC wins.
		if bonds[0].ID != id3 || bonds[1].ID != id2 || bonds[2].ID != id1 {
			t.Errorf("wrong order: ids = %d,%d,%d; want %d,%d,%d",
				bonds[0].ID, bonds[1].ID, bonds[2].ID, id3, id2, id1)
		}
	})

	t.Run("pair filter is exact — reversed pair returns nothing", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.ForagerBonds()

		foragerBondRecord(t, repo, "optimist", "skeptic", BondCites)

		bonds, err := repo.ListByPair("skeptic", "optimist")
		if err != nil {
			t.Fatalf("ListByPair (reversed): %v", err)
		}
		if len(bonds) != 0 {
			t.Fatalf("expected 0 bonds for reversed pair, got %d", len(bonds))
		}
	})

	t.Run("closed readDB returns error", func(t *testing.T) {
		store := newTestStore(t)
		store.ReadDB.Close()
		repo := store.ForagerBonds()
		_, err := repo.ListByPair("a", "b")
		if err == nil {
			t.Fatal("expected error when readDB is closed, got nil")
		}
	})
}

// TestScanBondRows_PayloadEmptyString exercises the payloadJSON == "" branch
// inside scanBondRows (line 167: `if payloadJSON != "" && payloadJSON != "{}"`)
// which RecordFired never reaches: it writes '{}' for no payload.
// We insert a row directly with an empty-string payload_json to cover it.
func TestScanBondRows_PayloadEmptyString(t *testing.T) {
	store := newTestStore(t)
	_, err := store.WriteDB.Exec(
		`INSERT INTO forager_bonds (run_id, from_forager, to_forager, bond_kind, weight, fired, payload_json)
		 VALUES (NULL, 'x', 'y', 'cites', 1.0, 0, '')`,
	)
	if err != nil {
		t.Fatalf("direct insert with empty payload_json: %v", err)
	}
	repo := store.ForagerBonds()
	bonds, err := repo.ListByPair("x", "y")
	if err != nil {
		t.Fatalf("ListByPair: %v", err)
	}
	if len(bonds) != 1 {
		t.Fatalf("expected 1 bond, got %d", len(bonds))
	}
	// Empty payload_json should result in a non-nil but empty map.
	if bonds[0].Payload == nil {
		t.Errorf("expected non-nil Payload map for empty payload_json")
	}
	if len(bonds[0].Payload) != 0 {
		t.Errorf("expected empty Payload map, got %v", bonds[0].Payload)
	}
}

func TestForagerBondsRepo_ListByRun(t *testing.T) {
	t.Run("unknown run id returns empty slice", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.ForagerBonds()

		bonds, err := repo.ListByRun(99999)
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if len(bonds) != 0 {
			t.Fatalf("expected 0 bonds for unknown run id, got %d", len(bonds))
		}
	})

	t.Run("returns only bonds for the requested run ordered by id", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.ForagerBonds()

		// Create two real workflow runs to satisfy the FK.
		run1 := foragerBondCreateRun(t, store)
		run2 := foragerBondCreateRun(t, store)

		id1, err := repo.RecordFired(run1, "optimist", "skeptic", BondCites, 1.0, nil)
		if err != nil {
			t.Fatalf("RecordFired bond1: %v", err)
		}
		id2, err := repo.RecordFired(run1, "pragmatist", "empiricist", BondContradicts, 0.8, nil)
		if err != nil {
			t.Fatalf("RecordFired bond2: %v", err)
		}
		// Bond for run2 — must NOT appear in ListByRun(run1).
		_, err = repo.RecordFired(run2, "dreamer", "historian", BondResonates, 1.0, nil)
		if err != nil {
			t.Fatalf("RecordFired bond for run2: %v", err)
		}

		bonds, err := repo.ListByRun(run1)
		if err != nil {
			t.Fatalf("ListByRun(%d): %v", run1, err)
		}
		if len(bonds) != 2 {
			t.Fatalf("expected 2 bonds for run %d, got %d", run1, len(bonds))
		}

		// Results ordered by id ascending.
		if bonds[0].ID != id1 {
			t.Errorf("bonds[0].ID = %d; want %d", bonds[0].ID, id1)
		}
		if bonds[1].ID != id2 {
			t.Errorf("bonds[1].ID = %d; want %d", bonds[1].ID, id2)
		}
		if bonds[0].From != "optimist" {
			t.Errorf("bonds[0].From = %q; want \"optimist\"", bonds[0].From)
		}
		if bonds[0].To != "skeptic" {
			t.Errorf("bonds[0].To = %q; want \"skeptic\"", bonds[0].To)
		}
		if bonds[0].Kind != BondCites {
			t.Errorf("bonds[0].Kind = %q; want %q", bonds[0].Kind, BondCites)
		}
		if !bonds[0].Fired {
			t.Errorf("bonds[0].Fired = false; RecordFired writes a fired bond")
		}
		if !bonds[0].RunID.Valid || bonds[0].RunID.Int64 != run1 {
			t.Errorf("bonds[0].RunID = %+v; want {Valid:true Int64:%d}", bonds[0].RunID, run1)
		}
	})

	t.Run("fired payload round-trips through ListByRun", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.ForagerBonds()

		runID := foragerBondCreateRun(t, store)

		if _, err := repo.RecordFired(runID, "a", "b", BondResonates, 1.0, map[string]any{"signal": "nabla", "score": 0.95}); err != nil {
			t.Fatalf("RecordFired: %v", err)
		}

		bonds, err := repo.ListByRun(runID)
		if err != nil {
			t.Fatalf("ListByRun: %v", err)
		}
		if len(bonds) != 1 {
			t.Fatalf("expected 1 bond, got %d", len(bonds))
		}
		if !bonds[0].Fired {
			t.Errorf("expected Fired=true")
		}
		if v, ok := bonds[0].Payload["signal"]; !ok || v != "nabla" {
			t.Errorf("Payload[signal] = %v; want \"nabla\"", bonds[0].Payload["signal"])
		}
	})
}
