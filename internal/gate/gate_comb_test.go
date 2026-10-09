package gate

import (
	"context"
	"reflect"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/comb"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// A conflict the gate auto-resolves is resolved for the Comb too. The gate
// sets its resolution alone, where ConflictsRepo.Resolve also sets
// resolved_by_wave. So each region it touched is stale after the gate,
// digests as if the conflict were gone, and is fresh and uncontested after a
// refresh.
func TestGateAutoResolve_ResolvesTheCombsConflict(t *testing.T) {
	store := newGateStore(t)
	var ids []int64
	for d2 := 0; d2 < 2; d2++ {
		one := 1
		id, err := store.Findings().AddFinding(&db.Finding{Wave: 1, Agent: "a", MSSLabel: "assumption", Finding: "x", D1: &one, D2: &d2})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := store.Conflicts().AddConflict(1, ids[0], ids[1], "[numeric] 50 vs 200"); err != nil {
		t.Fatal(err)
	}
	if _, err := comb.BuildAllRegions(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	regions := []comb.Coords{{}, {"d1": 1}, {"d1": 1, "d2": 0}, {"d1": 1, "d2": 1}}
	for _, r := range regions {
		row, err := store.Comb().Get(comb.RegionKey(r))
		if err != nil || row == nil || !row.Contested {
			t.Fatalf("fixture: %q is %+v (%v); want contested", comb.RegionKey(r), row, err)
		}
	}

	pipeResolveConflicts(store, 1, true)
	var resolution, byWave any
	if err := store.ReadDB.QueryRow(`SELECT resolution, resolved_by_wave FROM conflicts`).Scan(&resolution, &byWave); err != nil {
		t.Fatal(err)
	}
	if resolution == nil || byWave != nil {
		t.Fatalf("fixture: the gate left resolution %v, resolved_by_wave %v; want a resolution alone", resolution, byWave)
	}
	after := map[string]*comb.Digest{}
	for _, r := range regions {
		key := comb.RegionKey(r)
		if c, err := comb.Classify(store, r); err != nil || c != comb.StaleStale {
			t.Errorf("%q after the gate resolved its conflict: %q %v; want stale", key, c, err)
		}
		if after[key], _ = comb.BuildDigest(store, r); after[key] == nil {
			t.Fatalf("%q: no digest", key)
		}
	}

	if _, err := comb.BuildAllRegions(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	for _, r := range regions {
		key := comb.RegionKey(r)
		if c, err := comb.Classify(store, r); err != nil || c != comb.StaleFresh {
			t.Errorf("%q after a refresh: %q %v; want fresh", key, c, err)
		}
		if row, err := store.Comb().Get(key); err != nil || row == nil || row.Contested {
			t.Errorf("%q after a refresh: %+v (%v); want uncontested", key, row, err)
		}
	}

	if _, err := store.WriteDB.Exec(`DELETE FROM conflicts`); err != nil {
		t.Fatal(err)
	}
	for _, r := range regions {
		key := comb.RegionKey(r)
		gone, err := comb.BuildDigest(store, r)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(gone, after[key]) {
			t.Errorf("%q: with the conflict the gate resolved %+v; with none %+v", key, *after[key], *gone)
		}
	}
}
