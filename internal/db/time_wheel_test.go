package db

import (
	"testing"
)

// TestTimeWheelRepo_Begin covers all branches of TimeWheelRepo.Begin:
//
//  1. Empty label returns an error immediately.
//  2. Invalid tick kind returns an error immediately.
//  3. Happy path: new (kind, label) pair is inserted and a positive ID is returned.
//  4. Idempotent: calling Begin with the same (kind, label) returns the
//     existing ID without inserting a duplicate row.
//  5. Read-probe error: readDB closed before the SELECT causes an error that
//     is not sql.ErrNoRows, so Begin propagates it.
//  6. Write error: writeDB closed after a clean read (no existing row) causes
//     the INSERT to fail and Begin propagates the error.
func TestTimeWheelRepo_Begin(t *testing.T) {
	t.Run("empty label returns error", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.TimeWheel()
		_, err := repo.Begin("", TickSwarm, 0, 0, "")
		if err == nil {
			t.Fatal("expected error for empty label; got nil")
		}
	})

	t.Run("invalid kind returns error", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.TimeWheel()
		_, err := repo.Begin("some-label", "bogus-kind", 0, 0, "")
		if err == nil {
			t.Fatal("expected error for invalid tick kind; got nil")
		}
	})

	t.Run("happy path inserts new tick", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.TimeWheel()
		id, err := repo.Begin("my-label", TickWave, 0, 0, "some notes")
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if id <= 0 {
			t.Fatalf("expected positive ID, got %d", id)
		}
		// Verify the row was actually persisted.
		row, err := repo.Get(id)
		if err != nil {
			t.Fatalf("Get(%d): %v", id, err)
		}
		if row == nil {
			t.Fatalf("Get(%d) returned nil", id)
		}
		if row.Label != "my-label" {
			t.Errorf("label: want %q, got %q", "my-label", row.Label)
		}
		if row.Kind != TickWave {
			t.Errorf("kind: want %q, got %q", TickWave, row.Kind)
		}
		if !row.Notes.Valid || row.Notes.String != "some notes" {
			t.Errorf("notes: want %q, got %v", "some notes", row.Notes)
		}
	})

	t.Run("idempotent: same kind+label returns existing ID", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.TimeWheel()
		id1, err := repo.Begin("dup-label", TickSession, 0, 0, "")
		if err != nil {
			t.Fatalf("first Begin: %v", err)
		}
		id2, err := repo.Begin("dup-label", TickSession, 0, 0, "")
		if err != nil {
			t.Fatalf("second Begin (idempotent): %v", err)
		}
		if id1 != id2 {
			t.Errorf("idempotent: want same ID %d, got %d", id1, id2)
		}
		// Confirm only one row exists.
		var count int
		store.ReadDB.QueryRow(
			`SELECT COUNT(*) FROM time_wheel WHERE kind=? AND label=?`,
			string(TickSession), "dup-label",
		).Scan(&count)
		if count != 1 {
			t.Errorf("expected 1 row, got %d", count)
		}
	})

	t.Run("read probe error propagates", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.TimeWheel()
		// Close only the read DB to force an error in the SELECT probe.
		if err := store.ReadDB.Close(); err != nil {
			t.Fatalf("close ReadDB: %v", err)
		}
		_, err := repo.Begin("read-err-label", TickDay, 0, 0, "")
		if err == nil {
			t.Fatal("expected error after ReadDB closed; got nil")
		}
	})

	t.Run("write insert error propagates", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.TimeWheel()
		// Close the whole store (both DBs). The read probe will fail too,
		// so we expect any non-nil error — either from the probe or the
		// insert, both of which flow through Begin's error paths.
		store.Close()
		_, err := repo.Begin("write-err-label", TickManual, 0, 0, "")
		if err == nil {
			t.Fatal("expected error after store closed; got nil")
		}
	})

	t.Run("insert error propagates when only writeDB is closed", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.TimeWheel()
		// Close only the write DB so that:
		//   1. readDB.QueryRow succeeds and returns sql.ErrNoRows (no existing row)
		//   2. writeDB.Exec fails → Begin must return an error
		// This reaches Begin's insert error, which the whole-store-close
		// test cannot, since its readDB fails first.
		if err := store.WriteDB.Close(); err != nil {
			t.Fatalf("close WriteDB: %v", err)
		}
		_, err := repo.Begin("insert-err-label", TickRipen, 0, 0, "")
		if err == nil {
			t.Fatal("expected error after WriteDB closed; got nil")
		}
	})
}

// TestNullIfEmpty covers both branches of the nullIfEmpty helper.
func TestNullIfEmpty(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"", nil},
		{"hello", "hello"},
		{" ", " "}, // whitespace-only is non-empty
		{"0", "0"}, // string "0" is non-empty
	}
	for _, tc := range cases {
		got := nullIfEmpty(tc.in)
		if got != tc.want {
			t.Errorf("nullIfEmpty(%q) = %v (%T), want %v (%T)", tc.in, got, got, tc.want, tc.want)
		}
	}
}

// TestNullIfZero covers both branches of the nullIfZero helper.
func TestNullIfZero(t *testing.T) {
	cases := []struct {
		in   int64
		want any
	}{
		{0, nil},
		{1, int64(1)},
		{-1, int64(-1)},
		{9223372036854775807, int64(9223372036854775807)}, // MaxInt64
	}
	for _, tc := range cases {
		got := nullIfZero(tc.in)
		if got != tc.want {
			t.Errorf("nullIfZero(%d) = %v (%T), want %v (%T)", tc.in, got, got, tc.want, tc.want)
		}
	}
}

// TestValidTickKind covers all branches of the validTickKind helper.
func TestValidTickKind(t *testing.T) {
	cases := []struct {
		kind  TickKind
		valid bool
	}{
		{TickSwarm, true},
		{TickWave, true},
		{TickSession, true},
		{TickDay, true},
		{TickManual, true},
		{TickRipen, true},
		{"", false},
		{"unknown", false},
		{"COUNCIL", false}, // case-sensitive
		{"wavex", false},
	}
	for _, tc := range cases {
		got := validTickKind(tc.kind)
		if got != tc.valid {
			t.Errorf("validTickKind(%q) = %v, want %v", tc.kind, got, tc.valid)
		}
	}
}

// TestTimeWheelRepo_Recent covers the Recent method: happy paths (all kinds,
// filtered by kind), the default-limit branch (limit <=0), and an empty result.
func TestTimeWheelRepo_Recent(t *testing.T) {
	store := newTestStore(t)
	tw := store.TimeWheel()

	// Insert a few ticks of different kinds.
	id1, err := tw.Begin("tick-swarm-1", TickSwarm, 0, 0, "")
	if err != nil {
		t.Fatalf("Begin swarm tick: %v", err)
	}
	id2, err := tw.Begin("tick-wave-1", TickWave, 0, 1, "wave note")
	if err != nil {
		t.Fatalf("Begin wave tick: %v", err)
	}
	id3, err := tw.Begin("tick-swarm-2", TickSwarm, 0, 0, "")
	if err != nil {
		t.Fatalf("Begin second swarm tick: %v", err)
	}
	_ = id1
	_ = id2
	_ = id3

	t.Run("all kinds returns all ticks", func(t *testing.T) {
		rows, err := tw.Recent("", 10)
		if err != nil {
			t.Fatalf("Recent(\"\", 10): %v", err)
		}
		if len(rows) != 3 {
			t.Errorf("expected 3 ticks, got %d", len(rows))
		}
	})

	t.Run("filter by kind swarm", func(t *testing.T) {
		rows, err := tw.Recent(TickSwarm, 10)
		if err != nil {
			t.Fatalf("Recent(TickSwarm, 10): %v", err)
		}
		if len(rows) != 2 {
			t.Errorf("expected 2 swarm ticks, got %d", len(rows))
		}
		for _, r := range rows {
			if r.Kind != TickSwarm {
				t.Errorf("expected kind %q, got %q", TickSwarm, r.Kind)
			}
		}
	})

	t.Run("filter by kind wave", func(t *testing.T) {
		rows, err := tw.Recent(TickWave, 10)
		if err != nil {
			t.Fatalf("Recent(TickWave, 10): %v", err)
		}
		if len(rows) != 1 {
			t.Errorf("expected 1 wave tick, got %d", len(rows))
		}
		if len(rows) > 0 && rows[0].Kind != TickWave {
			t.Errorf("expected kind %q, got %q", TickWave, rows[0].Kind)
		}
	})

	t.Run("limit respected", func(t *testing.T) {
		rows, err := tw.Recent("", 2)
		if err != nil {
			t.Fatalf("Recent(\"\", 2): %v", err)
		}
		if len(rows) != 2 {
			t.Errorf("expected 2 ticks with limit=2, got %d", len(rows))
		}
	})

	t.Run("limit<=0 defaults to 100 (returns all 3)", func(t *testing.T) {
		rows, err := tw.Recent("", 0)
		if err != nil {
			t.Fatalf("Recent(\"\", 0): %v", err)
		}
		if len(rows) != 3 {
			t.Errorf("expected 3 ticks with default limit, got %d", len(rows))
		}
	})

	t.Run("no rows for unknown kind", func(t *testing.T) {
		rows, err := tw.Recent("nonexistent-kind", 10)
		if err != nil {
			t.Fatalf("Recent(unknown, 10): %v", err)
		}
		if len(rows) != 0 {
			t.Errorf("expected 0 ticks for unknown kind, got %d", len(rows))
		}
	})

	t.Run("most recent first ordering", func(t *testing.T) {
		rows, err := tw.Recent(TickSwarm, 10)
		if err != nil {
			t.Fatalf("Recent(TickSwarm): %v", err)
		}
		// The second swarm tick (id3) was inserted after id1, so it
		// should appear first in DESC order.
		if len(rows) == 2 && rows[0].ID < rows[1].ID {
			t.Errorf("expected DESC id order: got rows[0].ID=%d < rows[1].ID=%d",
				rows[0].ID, rows[1].ID)
		}
	})

	t.Run("query error propagates when readDB is closed", func(t *testing.T) {
		store2 := newTestStore(t)
		tw2 := store2.TimeWheel()
		if err := store2.ReadDB.Close(); err != nil {
			t.Fatalf("close ReadDB: %v", err)
		}
		_, err := tw2.Recent("", 10)
		if err == nil {
			t.Fatal("expected error when readDB is closed; got nil")
		}
	})
}

// TestTimeWheelRepo_CountByKind covers CountByKind: empty wheel, single-kind,
// and multi-kind with correctness of per-kind counts.
func TestTimeWheelRepo_CountByKind(t *testing.T) {
	t.Run("empty wheel returns empty map", func(t *testing.T) {
		store := newTestStore(t)
		tw := store.TimeWheel()
		counts, err := tw.CountByKind()
		if err != nil {
			t.Fatalf("CountByKind on empty wheel: %v", err)
		}
		if len(counts) != 0 {
			t.Errorf("expected empty map, got %v", counts)
		}
	})

	t.Run("single kind counted correctly", func(t *testing.T) {
		store := newTestStore(t)
		tw := store.TimeWheel()
		if _, err := tw.Begin("tick-1", TickSwarm, 0, 0, ""); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if _, err := tw.Begin("tick-2", TickSwarm, 0, 0, ""); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		counts, err := tw.CountByKind()
		if err != nil {
			t.Fatalf("CountByKind: %v", err)
		}
		if counts[TickSwarm] != 2 {
			t.Errorf("expected swarm=2, got %v", counts)
		}
		if len(counts) != 1 {
			t.Errorf("expected exactly 1 key, got %v", counts)
		}
	})

	t.Run("multiple kinds counted independently", func(t *testing.T) {
		store := newTestStore(t)
		tw := store.TimeWheel()
		inserts := []struct {
			label string
			kind  TickKind
		}{
			{"a", TickSwarm},
			{"b", TickSwarm},
			{"c", TickWave},
			{"d", TickSession},
			{"e", TickSession},
			{"f", TickSession},
		}
		for _, tc := range inserts {
			if _, err := tw.Begin(tc.label, tc.kind, 0, 0, ""); err != nil {
				t.Fatalf("Begin(%q, %q): %v", tc.label, tc.kind, err)
			}
		}
		counts, err := tw.CountByKind()
		if err != nil {
			t.Fatalf("CountByKind: %v", err)
		}
		want := map[TickKind]int{
			TickSwarm:   2,
			TickWave:    1,
			TickSession: 3,
		}
		for k, wantN := range want {
			if counts[k] != wantN {
				t.Errorf("kind %q: want %d, got %d (full map: %v)", k, wantN, counts[k], counts)
			}
		}
		if len(counts) != len(want) {
			t.Errorf("expected %d kinds, got %d: %v", len(want), len(counts), counts)
		}
	})
}

// TestTimeWheelRepo_Current covers all branches of TimeWheelRepo.Current:
//
//  1. No open tick of the requested kind → returns nil, nil (sql.ErrNoRows path).
//  2. Happy path: one open tick → returned with correct fields.
//  3. Multiple open ticks of the same kind → most-recent (highest id) is returned.
//  4. Only closed ticks exist for the kind → returns nil, nil.
//  5. Open ticks of a different kind are not returned.
func TestTimeWheelRepo_Current(t *testing.T) {
	t.Run("no open tick returns nil nil", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.TimeWheel()
		got, err := repo.Current(TickSwarm)
		if err != nil {
			t.Fatalf("Current on empty wheel: %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil, got %+v", got)
		}
	})

	t.Run("happy path returns open tick", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.TimeWheel()
		id, err := repo.Begin("open-swarm", TickSwarm, 0, 0, "note")
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		got, err := repo.Current(TickSwarm)
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		if got == nil {
			t.Fatal("expected non-nil TickRow, got nil")
		}
		if got.ID != id {
			t.Errorf("ID: want %d, got %d", id, got.ID)
		}
		if got.Kind != TickSwarm {
			t.Errorf("Kind: want %q, got %q", TickSwarm, got.Kind)
		}
		if got.Label != "open-swarm" {
			t.Errorf("Label: want %q, got %q", "open-swarm", got.Label)
		}
		if got.EndedAt.Valid {
			t.Error("EndedAt should be NULL for open tick")
		}
	})

	t.Run("most recent open tick is returned", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.TimeWheel()
		_, err := repo.Begin("first-wave", TickWave, 0, 1, "")
		if err != nil {
			t.Fatalf("Begin first: %v", err)
		}
		id2, err := repo.Begin("second-wave", TickWave, 0, 2, "")
		if err != nil {
			t.Fatalf("Begin second: %v", err)
		}
		got, err := repo.Current(TickWave)
		if err != nil {
			t.Fatalf("Current: %v", err)
		}
		if got == nil {
			t.Fatal("expected non-nil TickRow")
		}
		if got.ID != id2 {
			t.Errorf("expected most-recent id=%d, got id=%d", id2, got.ID)
		}
	})

	t.Run("only closed ticks returns nil nil", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.TimeWheel()
		id, err := repo.Begin("closed-session", TickSession, 0, 0, "")
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if err := repo.End(id); err != nil {
			t.Fatalf("End: %v", err)
		}
		got, err := repo.Current(TickSession)
		if err != nil {
			t.Fatalf("Current after End: %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil after closing tick, got %+v", got)
		}
	})

	t.Run("open tick of different kind not returned", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.TimeWheel()
		if _, err := repo.Begin("day-tick", TickDay, 0, 0, ""); err != nil {
			t.Fatalf("Begin TickDay: %v", err)
		}
		got, err := repo.Current(TickWave)
		if err != nil {
			t.Fatalf("Current(TickWave): %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil for TickWave when only TickDay open, got %+v", got)
		}
	})
}

// TestTimeWheelRepo_Get covers all branches of TimeWheelRepo.Get:
//
//  1. Happy path: existing id returns a fully-populated TickRow with correct fields.
//  2. Not-found: non-existent id returns nil, nil (sql.ErrNoRows branch).
//  3. Error path: closed readDB causes the scan to fail and Get propagates the error.
func TestTimeWheelRepo_Get(t *testing.T) {
	t.Run("happy path returns correct TickRow", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.TimeWheel()

		id, err := repo.Begin("get-happy", TickRipen, 0, 3, "get notes")
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}

		got, err := repo.Get(id)
		if err != nil {
			t.Fatalf("Get(%d): %v", id, err)
		}
		if got == nil {
			t.Fatalf("Get(%d) returned nil, want TickRow", id)
		}
		if got.ID != id {
			t.Errorf("ID: want %d, got %d", id, got.ID)
		}
		if got.Label != "get-happy" {
			t.Errorf("Label: want %q, got %q", "get-happy", got.Label)
		}
		if got.Kind != TickRipen {
			t.Errorf("Kind: want %q, got %q", TickRipen, got.Kind)
		}
		if !got.Notes.Valid || got.Notes.String != "get notes" {
			t.Errorf("Notes: want %q valid=true, got %v", "get notes", got.Notes)
		}
		if got.Wave.Valid && got.Wave.Int64 != 3 {
			t.Errorf("Wave: want 3, got %d", got.Wave.Int64)
		}
	})

	t.Run("non-existent id returns nil nil", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.TimeWheel()

		got, err := repo.Get(999999)
		if err != nil {
			t.Fatalf("Get(999999): expected nil error, got %v", err)
		}
		if got != nil {
			t.Fatalf("Get(999999): expected nil row, got %+v", got)
		}
	})

	t.Run("closed readDB returns error", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.TimeWheel()

		id, err := repo.Begin("get-err", TickManual, 0, 0, "")
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}

		if err := store.ReadDB.Close(); err != nil {
			t.Fatalf("close ReadDB: %v", err)
		}

		got, err := repo.Get(id)
		if err == nil {
			t.Fatalf("Get after ReadDB closed: expected error, got nil (row=%+v)", got)
		}
		if got != nil {
			t.Errorf("Get after ReadDB closed: expected nil row, got %+v", got)
		}
	})
}

// TestTimeWheelRepo_End covers the branches of TimeWheelRepo.End:
//
//  1. Happy path: End on an open tick sets ended_at (tick no longer returned
//     by Current after closing).
//  2. Idempotent: calling End twice on the same tick does not error (the WHERE
//     ended_at IS NULL guard makes the second UPDATE a silent no-op).
//  3. Non-existent id: End on an id that does not exist silently succeeds
//     (UPDATE affects 0 rows; the contract is best-effort / idempotent).
//  4. Error path: End after the write DB is closed returns an error.
func TestTimeWheelRepo_End(t *testing.T) {
	t.Run("happy path closes open tick", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.TimeWheel()

		id, err := repo.Begin("wave-end-happy", TickWave, 0, 0, "")
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}

		// Verify the tick is open (Current should return it).
		cur, err := repo.Current(TickWave)
		if err != nil {
			t.Fatalf("Current before End: %v", err)
		}
		if cur == nil || cur.ID != id {
			t.Fatalf("expected open tick id=%d; got %v", id, cur)
		}
		if cur.EndedAt.Valid {
			t.Error("expected ended_at NULL before End")
		}

		// Close the tick.
		if err := repo.End(id); err != nil {
			t.Fatalf("End: %v", err)
		}

		// Verify ended_at is now set via Get.
		row, err := repo.Get(id)
		if err != nil {
			t.Fatalf("Get after End: %v", err)
		}
		if row == nil {
			t.Fatal("Get returned nil after End")
		}
		if !row.EndedAt.Valid {
			t.Error("expected ended_at to be non-NULL after End")
		}

		// Current should no longer return this closed tick.
		cur2, err := repo.Current(TickWave)
		if err != nil {
			t.Fatalf("Current after End: %v", err)
		}
		if cur2 != nil && cur2.ID == id {
			t.Errorf("Current returned closed tick id=%d; want nil or different tick", id)
		}
	})

	t.Run("idempotent second End is a no-op", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.TimeWheel()

		id, err := repo.Begin("wave-end-idempotent", TickWave, 0, 0, "")
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}

		if err := repo.End(id); err != nil {
			t.Fatalf("first End: %v", err)
		}
		// Second call must not return an error.
		if err := repo.End(id); err != nil {
			t.Fatalf("second End (idempotent): %v", err)
		}
	})

	t.Run("non-existent id is a silent no-op", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.TimeWheel()

		const nonExistent = int64(99999)
		if err := repo.End(nonExistent); err != nil {
			t.Fatalf("End on non-existent id: %v", err)
		}
	})

	t.Run("error propagates when write db is closed", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.TimeWheel()

		id, err := repo.Begin("wave-end-err", TickWave, 0, 0, "")
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}

		// Close the store to force a write-DB error.
		store.Close()

		if err := repo.End(id); err == nil {
			t.Error("expected error from End after db close; got nil")
		}
	})
}
