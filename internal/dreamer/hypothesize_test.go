package dreamer

import (
	"context"
	"testing"
	"time"
)

// TestHypothesize_NoGaps covers the len(stale)==0 early-return branch
// when the gaps table is completely empty.
func TestHypothesize_NoGaps(t *testing.T) {
	store := freshStore(t)

	opts := DefaultOptions()
	opts.GapAgeMin = 24 * time.Hour
	opts.Now = time.Now()

	res, err := passHypothesize(context.Background(), store, opts)
	if err != nil {
		t.Fatalf("passHypothesize: %v", err)
	}
	if res.Status != "completed" {
		t.Fatalf("expected status=completed, got %q", res.Status)
	}
	if res.Touched != 0 {
		t.Fatalf("expected Touched=0, got %d", res.Touched)
	}
	if n, ok := res.Notes["stale_gaps"]; !ok || n != 0 {
		t.Fatalf("expected Notes[stale_gaps]=0, got %v", res.Notes)
	}
}

// TestHypothesize_TooRecentGap covers the case where a gap exists but is
// newer than opts.GapAgeMin — it should not appear in the stale list.
func TestHypothesize_TooRecentGap(t *testing.T) {
	store := freshStore(t)
	mustAddGap(t, store, "brand new gap we don't know about yet", "critical")

	// Do NOT backdate — the gap was just inserted (now).
	opts := DefaultOptions()
	opts.GapAgeMin = 7 * 24 * time.Hour // 7-day window
	opts.Now = time.Now()

	res, err := passHypothesize(context.Background(), store, opts)
	if err != nil {
		t.Fatalf("passHypothesize: %v", err)
	}
	if res.Status != "completed" {
		t.Fatalf("expected status=completed, got %q", res.Status)
	}
	if res.Touched != 0 {
		t.Fatalf("recent gap should not be promoted, got Touched=%d", res.Touched)
	}
	if n, ok := res.Notes["stale_gaps"]; !ok || n != 0 {
		t.Fatalf("expected stale_gaps=0, got %v", res.Notes)
	}
}

// TestHypothesize_WrongPriorityGap covers the case where an old gap exists
// but its priority is neither 'critical' nor 'important' — SQL WHERE filters
// it out, so stale_gaps should be 0.
func TestHypothesize_WrongPriorityGap(t *testing.T) {
	store := freshStore(t)
	// The db.AddGap signature accepts any priority string.
	if err := store.Gaps().AddGap(1, "test",
		"low priority old gap", "minor",
		intP(0), nil, nil, nil); err != nil {
		t.Fatalf("AddGap: %v", err)
	}
	// Backdate it so age is not the filter.
	if _, err := store.WriteDB.Exec(
		`UPDATE gaps SET created_at = datetime('now', '-30 days')`,
	); err != nil {
		t.Fatalf("backdate gap: %v", err)
	}

	opts := DefaultOptions()
	opts.GapAgeMin = 24 * time.Hour
	opts.Now = time.Now()

	res, err := passHypothesize(context.Background(), store, opts)
	if err != nil {
		t.Fatalf("passHypothesize: %v", err)
	}
	if res.Touched != 0 {
		t.Fatalf("wrong-priority gap should not be promoted, got Touched=%d", res.Touched)
	}
	if n, ok := res.Notes["stale_gaps"]; !ok || n != 0 {
		t.Fatalf("expected stale_gaps=0, got %v", res.Notes)
	}
}

// TestHypothesize_DryRun covers the opts.DryRun=true branch: emitted is
// incremented but AddFollowup is not called, and status is "dry_run".
func TestHypothesize_DryRun(t *testing.T) {
	store := freshStore(t)
	mustAddGap(t, store, "We don't know the tariff rate per SKU", "critical")
	if _, err := store.WriteDB.Exec(
		`UPDATE gaps SET created_at = datetime('now', '-30 days')`,
	); err != nil {
		t.Fatalf("backdate gap: %v", err)
	}

	opts := DefaultOptions()
	opts.GapAgeMin = 24 * time.Hour
	opts.Now = time.Now()
	opts.DryRun = true

	res, err := passHypothesize(context.Background(), store, opts)
	if err != nil {
		t.Fatalf("passHypothesize dry-run: %v", err)
	}
	if res.Status != "dry_run" {
		t.Fatalf("expected status=dry_run, got %q", res.Status)
	}
	if res.Touched != 1 {
		t.Fatalf("expected Touched=1 (counted but not written), got %d", res.Touched)
	}

	// No actual followup rows should exist in the DB.
	var count int
	if err := store.ReadDB.QueryRow(`SELECT COUNT(*) FROM followups`).Scan(&count); err != nil {
		t.Fatalf("count followups: %v", err)
	}
	if count != 0 {
		t.Fatalf("dry-run must not write followups, found %d", count)
	}
}

// TestHypothesize_MaxPerPass: only MaxPerPass followups are created even if
// more gaps qualify.
func TestHypothesize_MaxPerPass(t *testing.T) {
	store := freshStore(t)
	for _, desc := range []string{
		"gap alpha we don't understand",
		"gap beta still unknown",
		"gap gamma remains open",
	} {
		mustAddGap(t, store, desc, "important")
	}
	if _, err := store.WriteDB.Exec(
		`UPDATE gaps SET created_at = datetime('now', '-30 days')`,
	); err != nil {
		t.Fatalf("backdate gaps: %v", err)
	}

	opts := DefaultOptions()
	opts.GapAgeMin = 24 * time.Hour
	opts.Now = time.Now()
	opts.MaxPerPass = 2 // only process 2 of the 3 gaps

	res, err := passHypothesize(context.Background(), store, opts)
	if err != nil {
		t.Fatalf("passHypothesize: %v", err)
	}
	if res.Touched != 2 {
		t.Fatalf("MaxPerPass=2 should cap Touched at 2, got %d", res.Touched)
	}
	// stale_gaps counts every qualifying gap; the cap bounds what is created.
	if n, ok := res.Notes["stale_gaps"]; !ok || n != 3 {
		t.Fatalf("expected stale_gaps=3, got %v", res.Notes)
	}
}

// A gap younger than the window, created on the cutoff's calendar date, was
// promoted: the RFC3339 cutoff's 'T' sorts after the space in SQLite's
// created_at. Six days old against a seven-day window must stay put.
func TestHypothesize_GapOnTheCutoffDateButYoungerIsNotPromoted(t *testing.T) {
	store := freshStore(t)
	mustAddGap(t, store, "six days old", "critical")
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	// Cutoff is 2026-09-10 12:00:00; the gap is on that date, six hours later.
	if _, err := store.WriteDB.Exec(`UPDATE gaps SET created_at = '2026-09-10 18:00:00'`); err != nil {
		t.Fatal(err)
	}
	opts := DefaultOptions()
	opts.GapAgeMin = 7 * 24 * time.Hour
	opts.Now = now

	res, err := passHypothesize(context.Background(), store, opts)
	if err != nil {
		t.Fatalf("passHypothesize: %v", err)
	}
	if res.Touched != 0 {
		t.Errorf("a gap younger than the window was promoted (Touched=%d)", res.Touched)
	}
}

// A cancelled per-pass deadline context stops the pass.
func TestHypothesize_HonoursCancelledContext(t *testing.T) {
	store := freshStore(t)
	mustAddGap(t, store, "old gap", "critical")
	if _, err := store.WriteDB.Exec(`UPDATE gaps SET created_at = '2020-01-01 00:00:00'`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	opts := DefaultOptions()
	opts.Now = time.Now()
	if _, err := passHypothesize(ctx, store, opts); err == nil {
		t.Error("a cancelled pass ran to completion")
	}
}

func TestFollowupExists_NoMatch(t *testing.T) {
	store := freshStore(t)
	got, err := followupExists(store.ReadConn(), "ghost", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("followupExists: %v", err)
	}
	if got {
		t.Errorf("expected false; got true")
	}
}

func TestFollowupExists_ExactMatchAllNullCoords(t *testing.T) {
	store := freshStore(t)
	if err := store.Followups().AddFollowup(1, "agent", "matches", "minor", nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	got, err := followupExists(store.ReadConn(), "matches", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("followupExists: %v", err)
	}
	if !got {
		t.Error("expected true for matching question with NULL coords")
	}
}

func TestFollowupExists_CoordMatch(t *testing.T) {
	store := freshStore(t)
	d1 := 0
	d2 := 3
	if err := store.Followups().AddFollowup(1, "agent", "with-coords", "minor", &d1, &d2, nil, nil); err != nil {
		t.Fatal(err)
	}
	got, err := followupExists(store.ReadConn(), "with-coords", &d1, &d2, nil, nil)
	if err != nil {
		t.Fatalf("followupExists: %v", err)
	}
	if !got {
		t.Error("expected true for matching coords")
	}
}

func TestFollowupExists_DBClosed(t *testing.T) {
	store := freshStore(t)
	store.Close()
	_, err := followupExists(store.ReadConn(), "x", nil, nil, nil, nil)
	if err == nil {
		t.Error("expected error from closed DB")
	}
}
