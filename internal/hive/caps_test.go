package hive_test

import (
	"path/filepath"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/hive"
)

func promoteStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.NewStore(filepath.Join(t.TempDir(), "hive.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func addFinding(t *testing.T, store *db.Store, agent, label, text string) int64 {
	t.Helper()
	d := 0
	f := &db.Finding{Wave: 1, Agent: agent, MSSLabel: label, Finding: text, D1: &d, D2: &d, D3: &d, D4: &d}
	id, err := store.Findings().AddFinding(f)
	if err != nil {
		t.Fatalf("add %s finding: %v", label, err)
	}
	return id
}

// Capping a converged finding records the cap and changes nothing else: the
// finding keeps its label and its dependencies, whatever sits beside it at
// its coordinate. Agreement is not derivation, so no guarantee appears.
func TestApplyCaps_ConvergenceIsNotDerivation(t *testing.T) {
	store := promoteStore(t)
	addFinding(t, store, "a", "definition", "measured 4096 bytes")
	addFinding(t, store, "b", "unknown", "nobody could confirm this")
	candidate := addFinding(t, store, "c", "assumption", "the default page size is 4096")

	results, err := hive.ApplyCaps(store, []hive.Action{
		{Type: "cap_finding", FindingID: candidate},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Capped || results[0].FindingID != candidate {
		t.Fatalf("results = %+v, want one cap of %d", results, candidate)
	}
	var label, deps string
	var caps, guarantees int
	if err := store.ReadDB.QueryRow(
		`SELECT mss_label, COALESCE(depends_on_ids,''),
		        (SELECT COUNT(*) FROM capped_findings WHERE finding_id = f.id),
		        (SELECT COUNT(*) FROM findings WHERE mss_label = 'guarantee')
		 FROM findings f WHERE id=?`, candidate,
	).Scan(&label, &deps, &caps, &guarantees); err != nil {
		t.Fatal(err)
	}
	if label != "assumption" || deps != "" || caps != 1 || guarantees != 0 {
		t.Fatalf("after the cap: label %q, depends_on_ids %q, cap rows %d, guarantees %d; want an assumption with no dependencies, one cap row, no guarantee",
			label, deps, caps, guarantees)
	}
}

// A hive's metrics are read across the whole database, so a second project
// in one DB would reason over the first's findings. The invariant is
// enforced rather than documented away.
func TestExistingProject_DetectsASecondHive(t *testing.T) {
	store := promoteStore(t)
	if other, err := hive.ExistingProject(store, "alpha"); err != nil || other != "" {
		t.Fatalf("empty DB: other=%q err=%v", other, err)
	}
	if _, err := store.WriteDB.Exec(`INSERT INTO hive_state (project) VALUES ('alpha')`); err != nil {
		t.Fatal(err)
	}
	if other, err := hive.ExistingProject(store, "alpha"); err != nil || other != "" {
		t.Fatalf("same project must not collide with itself: other=%q err=%v", other, err)
	}
	other, err := hive.ExistingProject(store, "beta")
	if err != nil {
		t.Fatal(err)
	}
	if other != "alpha" {
		t.Fatalf("other = %q, want alpha", other)
	}
}
