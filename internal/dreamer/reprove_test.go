package dreamer

import (
	"context"
	"testing"
	"time"
)

// TestReprove_NoGuarantees covers the len(guarantees)==0 early-return branch.
func TestReprove_NoGuarantees(t *testing.T) {
	store := freshStore(t)
	mustAddFinding(t, store, "assumption", "the sky is blue", nil)
	mustAddFinding(t, store, "definition", "price is 10", nil)

	res, err := passReprove(context.Background(), store, DefaultOptions())
	if err != nil {
		t.Fatalf("passReprove: %v", err)
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

// TestReprove_InvalidJSONDeps covers the json.Unmarshal-error→continue branch.
func TestReprove_InvalidJSONDeps(t *testing.T) {
	store := freshStore(t)
	// Write a guarantee with malformed JSON in depends_on_ids via raw SQL
	// (the normal API validates this, so we bypass it).
	if _, err := store.WriteDB.Exec(
		`INSERT INTO findings (wave, agent, mss_label, finding, d1, depends_on_ids)
		 VALUES (1, 'test', 'guarantee', 'broken deps guarantee', 0, 'not-valid-json')`,
	); err != nil {
		t.Fatalf("insert bad-deps guarantee: %v", err)
	}

	res, err := passReprove(context.Background(), store, DefaultOptions())
	if err != nil {
		t.Fatalf("passReprove with invalid JSON deps: %v", err)
	}
	if res.Status != "completed" {
		t.Fatalf("expected status=completed, got %q", res.Status)
	}
	// Bad-JSON row is silently skipped; nothing flagged.
	if res.Touched != 0 {
		t.Fatalf("expected Touched=0 (skipped bad row), got %d", res.Touched)
	}
}

// TestReprove_EmptyJSONDeps covers the len(depIDs)==0→continue branch.
func TestReprove_EmptyJSONDeps(t *testing.T) {
	store := freshStore(t)
	// A guarantee with an empty JSON array as deps — should be skipped.
	if _, err := store.WriteDB.Exec(
		`INSERT INTO findings (wave, agent, mss_label, finding, d1, depends_on_ids)
		 VALUES (1, 'test', 'guarantee', 'empty deps guarantee', 0, '[]')`,
	); err != nil {
		t.Fatalf("insert empty-deps guarantee: %v", err)
	}

	res, err := passReprove(context.Background(), store, DefaultOptions())
	if err != nil {
		t.Fatalf("passReprove: %v", err)
	}
	if res.Touched != 0 {
		t.Fatalf("expected Touched=0 for empty-deps guarantee, got %d", res.Touched)
	}
}

// TestReprove_DepNotModified covers the depWasModifiedAfter→false branch:
// a guarantee exists with a dep, but no signals post-date it.
func TestReprove_DepNotModified(t *testing.T) {
	store := freshStore(t)
	depA := mustAddFinding(t, store, "definition", "fact A", nil)
	mustAddFinding(t, store, "guarantee", "C follows from A", []int64{depA})

	// No signals at all — depWasModifiedAfter returns false for every dep.
	res, err := passReprove(context.Background(), store, DefaultOptions())
	if err != nil {
		t.Fatalf("passReprove: %v", err)
	}
	if res.Status != "completed" {
		t.Fatalf("expected status=completed, got %q", res.Status)
	}
	if res.Touched != 0 {
		t.Fatalf("expected Touched=0 (dep not modified), got %d", res.Touched)
	}
	if got := countSignals(t, store, "alarm"); got != 0 {
		t.Fatalf("expected no alarm signals, got %d", got)
	}
}

// TestReprove_DryRun covers the opts.DryRun branch: emitted counter increments
// but no alarm signal is written to the DB.
func TestReprove_DryRun(t *testing.T) {
	store := freshStore(t)
	depA := mustAddFinding(t, store, "definition", "the moon is grey", nil)
	mustAddFinding(t, store, "guarantee", "moon affects tides", []int64{depA})

	// SQLite CURRENT_TIMESTAMP has 1-second resolution — sleep so the signal
	// lands strictly after the guarantee's created_at.
	time.Sleep(1500 * time.Millisecond)
	srcType := "finding"
	if _, err := store.Signals().EmitSignal(
		"stop_signal", &srcType, &depA,
		intP(0), nil, nil, nil,
		map[string]any{"reason": "test"}, nil,
	); err != nil {
		t.Fatalf("emit signal: %v", err)
	}

	opts := DefaultOptions()
	opts.DryRun = true
	res, err := passReprove(context.Background(), store, opts)
	if err != nil {
		t.Fatalf("passReprove dry-run: %v", err)
	}
	if res.Status != "dry_run" {
		t.Fatalf("expected status=dry_run, got %q", res.Status)
	}
	if res.Touched < 1 {
		t.Fatalf("expected Touched>=1 in dry-run, got %d", res.Touched)
	}
	// DryRun must not emit alarm signals.
	if got := countSignals(t, store, "alarm"); got != 0 {
		t.Fatalf("dry-run must not emit alarm signals, got %d", got)
	}
}

// TestReprove_MaxPerPass covers the emitted>=MaxPerPass→break branch.
func TestReprove_MaxPerPass(t *testing.T) {
	store := freshStore(t)

	// Create 3 deps and 3 guarantees (one dep per guarantee).
	deps := make([]int64, 3)
	for i := range deps {
		deps[i] = mustAddFinding(t, store, "definition", "fact", nil)
	}
	for i := range deps {
		mustAddFinding(t, store, "guarantee", "guarantee X", []int64{deps[i]})
	}

	// Sleep so signals land strictly after every guarantee's created_at.
	time.Sleep(1500 * time.Millisecond)
	srcType := "finding"
	for i := range deps {
		if _, err := store.Signals().EmitSignal(
			"alarm", &srcType, &deps[i],
			intP(0), nil, nil, nil,
			map[string]any{"reason": "test"}, nil,
		); err != nil {
			t.Fatalf("emit signal for dep %d: %v", i, err)
		}
	}

	opts := DefaultOptions()
	opts.MaxPerPass = 2
	res, err := passReprove(context.Background(), store, opts)
	if err != nil {
		t.Fatalf("passReprove: %v", err)
	}
	if res.Touched != 2 {
		t.Fatalf("expected MaxPerPass=2 to cap Touched at 2, got %d", res.Touched)
	}
	if n, ok := res.Notes["emitted"]; !ok || n != 2 {
		t.Fatalf("expected Notes[emitted]=2, got %v", res.Notes)
	}
}
