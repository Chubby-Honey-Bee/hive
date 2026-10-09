package db

import "testing"

// A vantage revised in the second its tick opened, once before the tick and
// once after it. Timestamps to the second cannot order the two, so the tick
// decides: a revision anchored to a tick opened after this one was written
// after this one opened, whatever its timestamp, and a revision anchored to
// no tick or to an earlier one, written in that second, counts as before the
// tick.
func TestCombRevisionsRepo_AtTick_SameSecondAsTheTick(t *testing.T) {
	const second = "2026-05-01 12:00:00"
	for _, tc := range []struct {
		name        string
		earlierTick bool // anchor the revision before the tick to an earlier tick, not to none
	}{
		{name: "revision before the tick anchored to none"},
		{name: "revision before the tick anchored to an earlier tick", earlierTick: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			repo := store.CombRevisions()
			const key = "d1=same-second"

			var beforeTick any
			if tc.earlierTick {
				beforeTick = insertTestTick(t, store, "run-0", "swarm", "2026-05-01 11:00:00")
			}
			before := insertTickRevision(t, store, key, "before the tick", beforeTick, second)
			tick := insertTestTick(t, store, "wave-7", "wave", second)
			later := insertTestTick(t, store, "ripen-1", "ripen", second)
			after := insertTickRevision(t, store, key, "after the tick", later, second)

			got, err := repo.AtTick(key, tick)
			if err != nil {
				t.Fatal(err)
			}
			if got == nil || got.ID != before {
				t.Fatalf("AtTick(%d) = %+v, want revision %d, written before the tick opened (revision %d came after it)", tick, got, before, after)
			}

			got, err = repo.AtTick(key, later)
			if err != nil {
				t.Fatal(err)
			}
			if got == nil || got.ID != after {
				t.Fatalf("AtTick(%d) = %+v, want revision %d, the one anchored to that tick", later, got, after)
			}
		})
	}
}

// insertTickRevision writes a revision of key anchored to tick (nil for
// none) at the timestamp at, and returns its id.
func insertTickRevision(t *testing.T, store *Store, key, narrative string, tick any, at string) int64 {
	t.Helper()
	res, err := store.WriteDB.Exec(
		`INSERT INTO comb_revisions (vantage_key, vantage_kind, tick_id, narrative, source, revision_at)
		 VALUES (?, ?, ?, ?, 'test', ?)`,
		key, string(VantageRegion), tick, narrative, at,
	)
	if err != nil {
		t.Fatalf("insert revision: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
