package dreamer

import (
	"context"
	"encoding/json"
	"testing"
)

// TestSettle_NoGuarantees covers the len(rowsBuf)==0 early-return branch.
func TestSettle_NoGuarantees(t *testing.T) {
	store := freshStore(t)
	// Only assumptions and definitions — no guarantees at all.
	mustAddFinding(t, store, "assumption", "the sky is blue", nil)
	mustAddFinding(t, store, "definition", "price is 10", nil)

	res, err := passSettle(context.Background(), store, DefaultOptions())
	if err != nil {
		t.Fatalf("passSettle: %v", err)
	}
	if res.Status != "completed" {
		t.Fatalf("expected status=completed, got %q", res.Status)
	}
	if res.Touched != 0 {
		t.Fatalf("expected Touched=0, got %d", res.Touched)
	}
	if n, ok := res.Notes["guarantees"]; !ok || n != 0 {
		t.Fatalf("expected Notes[guarantees]=0, got %v", res.Notes)
	}
}

// TestSettle_GuaranteeWithAllKnownDeps covers the !hasUnknown→continue branch.
func TestSettle_GuaranteeWithAllKnownDeps(t *testing.T) {
	store := freshStore(t)
	depA := mustAddFinding(t, store, "definition", "fact A", nil)
	depB := mustAddFinding(t, store, "assumption", "assumption B", nil)
	// Guarantee whose deps are all non-unknown — should not be flagged.
	mustAddFinding(t, store, "guarantee", "C follows from A and B", []int64{depA, depB})

	res, err := passSettle(context.Background(), store, DefaultOptions())
	if err != nil {
		t.Fatalf("passSettle: %v", err)
	}
	if res.Status != "completed" {
		t.Fatalf("expected status=completed, got %q", res.Status)
	}
	if res.Touched != 0 {
		t.Fatalf("expected 0 flagged (no unknown deps), got %d", res.Touched)
	}
	if got := countSignals(t, store, "alarm"); got != 0 {
		t.Fatalf("expected no alarm signals, got %d", got)
	}
}

// TestSettle_DryRun covers the opts.DryRun→emitted++ but no signals branch.
func TestSettle_DryRun(t *testing.T) {
	store := freshStore(t)
	depA := mustAddFinding(t, store, "definition", "fact A", nil)
	depUnknown := mustAddFinding(t, store, "unknown", "we don't know B", nil)
	gid := mustAddFinding(t, store, "guarantee", "A implies C", []int64{depA})
	depsBoth, _ := json.Marshal([]int64{depA, depUnknown})
	if _, err := store.WriteDB.Exec(
		`UPDATE findings SET depends_on_ids = ? WHERE id = ?`, string(depsBoth), gid,
	); err != nil {
		t.Fatalf("inject unknown dep: %v", err)
	}

	opts := DefaultOptions()
	opts.DryRun = true
	res, err := passSettle(context.Background(), store, opts)
	if err != nil {
		t.Fatalf("passSettle dry-run: %v", err)
	}
	if res.Status != "dry_run" {
		t.Fatalf("expected status=dry_run, got %q", res.Status)
	}
	if res.Touched < 1 {
		t.Fatalf("expected Touched>=1, got %d", res.Touched)
	}
	// DryRun must not emit alarm signals.
	if got := countSignals(t, store, "alarm"); got != 0 {
		t.Fatalf("dry-run must not emit alarm signals, got %d", got)
	}
}

// TestSettle_MaxPerPass covers the emitted>=MaxPerPass→break branch.
func TestSettle_MaxPerPass(t *testing.T) {
	store := freshStore(t)
	depUnknown := mustAddFinding(t, store, "unknown", "we don't know X", nil)

	// Add 3 guarantees each depending on the unknown dep.
	for i := 0; i < 3; i++ {
		depA := mustAddFinding(t, store, "definition", "fact", nil)
		gid := mustAddFinding(t, store, "guarantee", "implies something", []int64{depA})
		deps, _ := json.Marshal([]int64{depA, depUnknown})
		if _, err := store.WriteDB.Exec(
			`UPDATE findings SET depends_on_ids = ? WHERE id = ?`, string(deps), gid,
		); err != nil {
			t.Fatalf("inject unknown dep for gid %d: %v", gid, err)
		}
	}

	opts := DefaultOptions()
	opts.MaxPerPass = 2 // process at most 2
	res, err := passSettle(context.Background(), store, opts)
	if err != nil {
		t.Fatalf("passSettle: %v", err)
	}
	if res.Touched != 2 {
		t.Fatalf("expected MaxPerPass=2 to cap Touched at 2, got %d", res.Touched)
	}
}

// TestSettle_InvalidJSONDeps covers the json.Unmarshal→continue branch.
func TestSettle_InvalidJSONDeps(t *testing.T) {
	store := freshStore(t)
	// Write a guarantee with invalid JSON in depends_on_ids via raw SQL.
	if _, err := store.WriteDB.Exec(
		`INSERT INTO findings (wave, agent, mss_label, finding, d1, depends_on_ids)
		 VALUES (1, 'test', 'guarantee', 'broken deps guarantee', 0, 'not-valid-json')`,
	); err != nil {
		t.Fatalf("insert bad-deps guarantee: %v", err)
	}

	// Should not error — the bad-JSON row is silently skipped.
	res, err := passSettle(context.Background(), store, DefaultOptions())
	if err != nil {
		t.Fatalf("passSettle with invalid JSON deps: %v", err)
	}
	if res.Status != "completed" {
		t.Fatalf("expected status=completed, got %q", res.Status)
	}
	// The broken row should be skipped, so nothing is flagged.
	if res.Touched != 0 {
		t.Fatalf("expected Touched=0 (skipped bad row), got %d", res.Touched)
	}
}

func TestAlarmAlreadyOpenFor_NoRow(t *testing.T) {
	store := freshStore(t)
	got, err := alarmAlreadyOpenFor(store.ReadConn(), 99999)
	if err != nil {
		t.Fatalf("alarmAlreadyOpenFor: %v", err)
	}
	if got {
		t.Errorf("expected false for missing finding")
	}
}

func TestAlarmAlreadyOpenFor_DBClosed(t *testing.T) {
	store := freshStore(t)
	store.Close()
	_, err := alarmAlreadyOpenFor(store.ReadConn(), 1)
	if err == nil {
		t.Error("expected error after DB close")
	}
}
