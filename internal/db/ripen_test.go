package db

import (
	"database/sql"
	"testing"
)

// TestRipenRepo_BeginPass covers the happy path and the error path for BeginPass.
func TestRipenRepo_BeginPass(t *testing.T) {
	t.Run("happy path returns positive id", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.Ripen()

		id, err := repo.BeginPass("prune")
		if err != nil {
			t.Fatalf("BeginPass: unexpected error: %v", err)
		}
		if id <= 0 {
			t.Fatalf("BeginPass: expected positive id, got %d", id)
		}
	})

	t.Run("multiple passes get distinct ids", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.Ripen()

		id1, err := repo.BeginPass("prune")
		if err != nil {
			t.Fatalf("BeginPass(prune): %v", err)
		}
		id2, err := repo.BeginPass("reprove")
		if err != nil {
			t.Fatalf("BeginPass(reprove): %v", err)
		}
		if id1 == id2 {
			t.Fatalf("expected distinct ids, got id1=%d id2=%d", id1, id2)
		}
	})

	t.Run("row is readable after BeginPass", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.Ripen()

		_, err := repo.BeginPass("contradict")
		if err != nil {
			t.Fatalf("BeginPass: %v", err)
		}
		passes, err := repo.RecentPasses(10)
		if err != nil {
			t.Fatalf("RecentPasses: %v", err)
		}
		if len(passes) != 1 {
			t.Fatalf("expected 1 pass, got %d", len(passes))
		}
		if passes[0].PassName != "contradict" {
			t.Errorf("PassName = %q; want %q", passes[0].PassName, "contradict")
		}
		if passes[0].Status != "completed" {
			t.Errorf("Status = %q; want %q", passes[0].Status, "completed")
		}
	})

	t.Run("error path when db is closed", func(t *testing.T) {
		// Build a repo whose writeDB is already closed — forces an IO error.
		badDB, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("sql.Open: %v", err)
		}
		badDB.Close() // deliberately close before use

		repo := NewRipenRepo(badDB, badDB)
		_, err = repo.BeginPass("prune")
		if err == nil {
			t.Fatal("expected error from closed db, got nil")
		}
	})
}

// TestRipenRepo_CompletePass covers all branches in CompletePass.
func TestRipenRepo_CompletePass(t *testing.T) {
	tests := []struct {
		name        string
		notes       map[string]any
		status      string
		touched     int
		costX10K    int64
		wantStatus  string
		wantTouched int
		wantCost    int64
		wantNoteKey string
		wantNoteVal any
	}{
		{
			name:        "happy path with notes",
			notes:       map[string]any{"merged": float64(2)},
			status:      "completed",
			touched:     5,
			costX10K:    999,
			wantStatus:  "completed",
			wantTouched: 5,
			wantCost:    999,
			wantNoteKey: "merged",
			wantNoteVal: float64(2),
		},
		{
			name:        "nil notes uses empty json",
			notes:       nil,
			status:      "skipped",
			touched:     0,
			costX10K:    0,
			wantStatus:  "skipped",
			wantTouched: 0,
			wantCost:    0,
		},
		{
			name:        "halted status",
			notes:       map[string]any{"reason": "qmp"},
			status:      "halted",
			touched:     1,
			costX10K:    42,
			wantStatus:  "halted",
			wantTouched: 1,
			wantCost:    42,
			wantNoteKey: "reason",
			wantNoteVal: "qmp",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			repo := store.Ripen()

			id, err := repo.BeginPass("prune")
			if err != nil {
				t.Fatalf("BeginPass: %v", err)
			}

			if err := repo.CompletePass(id, tc.status, tc.touched, tc.costX10K, tc.notes); err != nil {
				t.Fatalf("CompletePass: unexpected error: %v", err)
			}

			passes, err := repo.RecentPasses(1)
			if err != nil {
				t.Fatalf("RecentPasses: %v", err)
			}
			if len(passes) != 1 {
				t.Fatalf("expected 1 pass, got %d", len(passes))
			}
			p := passes[0]
			if p.Status != tc.wantStatus {
				t.Errorf("Status = %q; want %q", p.Status, tc.wantStatus)
			}
			if p.TouchedCount != tc.wantTouched {
				t.Errorf("TouchedCount = %d; want %d", p.TouchedCount, tc.wantTouched)
			}
			if p.CostUSDx10000 != tc.wantCost {
				t.Errorf("CostUSDx10000 = %d; want %d", p.CostUSDx10000, tc.wantCost)
			}
			if tc.wantNoteKey != "" {
				if v, ok := p.Notes[tc.wantNoteKey]; !ok || v != tc.wantNoteVal {
					t.Errorf("Notes[%s] = %v; want %v", tc.wantNoteKey, v, tc.wantNoteVal)
				}
			}
			if !p.CompletedAt.Valid {
				t.Error("expected CompletedAt to be set after CompletePass")
			}
		})
	}

	t.Run("error path when writeDB is closed", func(t *testing.T) {
		badDB, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("sql.Open: %v", err)
		}
		badDB.Close()

		repo := NewRipenRepo(badDB, badDB)
		err = repo.CompletePass(1, "completed", 0, 0, nil)
		if err == nil {
			t.Fatal("expected error from closed writeDB, got nil")
		}
	})
}

// TestRipenRepo_RecentPasses covers all branches in RecentPasses.
func TestRipenRepo_RecentPasses(t *testing.T) {
	t.Run("empty table returns empty slice", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.Ripen()

		passes, err := repo.RecentPasses(10)
		if err != nil {
			t.Fatalf("RecentPasses on empty table: %v", err)
		}
		if len(passes) != 0 {
			t.Fatalf("expected 0 passes, got %d", len(passes))
		}
	})

	t.Run("returns rows in descending order up to limit", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.Ripen()

		names := []string{"prune", "reprove", "contradict", "hypothesize", "settle"}
		for _, name := range names {
			if _, err := repo.BeginPass(name); err != nil {
				t.Fatalf("BeginPass(%s): %v", name, err)
			}
		}

		passes, err := repo.RecentPasses(3)
		if err != nil {
			t.Fatalf("RecentPasses: %v", err)
		}
		if len(passes) != 3 {
			t.Fatalf("expected 3 passes (limit), got %d", len(passes))
		}
		// Rows should come back most-recent-first (id DESC when started_at ties).
		if passes[0].ID <= passes[1].ID || passes[1].ID <= passes[2].ID {
			t.Errorf("expected descending IDs, got %d %d %d",
				passes[0].ID, passes[1].ID, passes[2].ID)
		}
	})

	t.Run("zero limit defaults to 50", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.Ripen()

		// Insert 60 rows; with default limit=50 we should get back exactly 50.
		for i := 0; i < 60; i++ {
			if _, err := repo.BeginPass("prune"); err != nil {
				t.Fatalf("BeginPass: %v", err)
			}
		}

		passes, err := repo.RecentPasses(0)
		if err != nil {
			t.Fatalf("RecentPasses(0): %v", err)
		}
		if len(passes) != 50 {
			t.Fatalf("expected 50 (default limit), got %d", len(passes))
		}
	})

	t.Run("negative limit also defaults to 50", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.Ripen()

		for i := 0; i < 60; i++ {
			if _, err := repo.BeginPass("prune"); err != nil {
				t.Fatalf("BeginPass: %v", err)
			}
		}

		passes, err := repo.RecentPasses(-5)
		if err != nil {
			t.Fatalf("RecentPasses(-5): %v", err)
		}
		if len(passes) != 50 {
			t.Fatalf("expected 50 (default limit), got %d", len(passes))
		}
	})

	t.Run("row fields are populated correctly", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.Ripen()

		id, err := repo.BeginPass("settle")
		if err != nil {
			t.Fatalf("BeginPass: %v", err)
		}
		notes := map[string]any{"key": "value", "count": float64(3)}
		if err := repo.CompletePass(id, "completed", 7, 123, notes); err != nil {
			t.Fatalf("CompletePass: %v", err)
		}

		passes, err := repo.RecentPasses(1)
		if err != nil {
			t.Fatalf("RecentPasses: %v", err)
		}
		if len(passes) != 1 {
			t.Fatalf("expected 1 pass, got %d", len(passes))
		}
		p := passes[0]
		if p.PassName != "settle" {
			t.Errorf("PassName = %q; want settle", p.PassName)
		}
		if p.Status != "completed" {
			t.Errorf("Status = %q; want completed", p.Status)
		}
		if p.TouchedCount != 7 {
			t.Errorf("TouchedCount = %d; want 7", p.TouchedCount)
		}
		if p.CostUSDx10000 != 123 {
			t.Errorf("CostUSDx10000 = %d; want 123", p.CostUSDx10000)
		}
		if v, ok := p.Notes["key"]; !ok || v != "value" {
			t.Errorf("Notes[key] = %v; want value", p.Notes["key"])
		}
		if !p.CompletedAt.Valid {
			t.Error("expected CompletedAt to be set after CompletePass")
		}
	})

	t.Run("empty notes json leaves Notes map empty", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.Ripen()

		// BeginPass inserts notes_json='{}'; do not call CompletePass.
		if _, err := repo.BeginPass("hypothesize"); err != nil {
			t.Fatalf("BeginPass: %v", err)
		}

		passes, err := repo.RecentPasses(1)
		if err != nil {
			t.Fatalf("RecentPasses: %v", err)
		}
		if len(passes) != 1 {
			t.Fatalf("expected 1 pass, got %d", len(passes))
		}
		if len(passes[0].Notes) != 0 {
			t.Errorf("expected empty Notes map, got %v", passes[0].Notes)
		}
	})

	t.Run("error path when readDB is closed", func(t *testing.T) {
		badDB, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("sql.Open: %v", err)
		}
		badDB.Close()

		repo := NewRipenRepo(badDB, badDB)
		_, err = repo.RecentPasses(10)
		if err == nil {
			t.Fatal("expected error from closed readDB, got nil")
		}
	})
}
