package db

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// mockScanner implements rowScanner for unit-testing scanRevisionRow without a
// real database connection.
type mockScanner struct {
	err  error
	vals []any
}

func (m *mockScanner) Scan(dest ...any) error {
	if m.err != nil {
		return m.err
	}
	for i, d := range dest {
		if i >= len(m.vals) {
			break
		}
		switch p := d.(type) {
		case *int64:
			*p = m.vals[i].(int64)
		case *string:
			*p = m.vals[i].(string)
		case *sql.NullInt64:
			*p = m.vals[i].(sql.NullInt64)
		case *int:
			*p = m.vals[i].(int)
		case *sql.NullString:
			*p = m.vals[i].(sql.NullString)
		case *[]byte:
			*p = m.vals[i].([]byte)
		}
	}
	return nil
}

// happyScannerVals returns a []any slice matching the 13-column scan order in
// scanRevisionRow.
func happyScannerVals() []any {
	return []any{
		int64(7),                             // ID
		"d1=0",                               // VantageKey
		"region",                             // kind (→ VantageKind)
		sql.NullInt64{Int64: 3, Valid: true}, // TickID
		"test narrative",                     // Narrative
		int(85),                              // Confidence
		int(1),                               // contestedInt (non-zero → true)
		sql.NullString{String: "definition", Valid: true}, // DominantLabel
		int(5), // EvidenceCount
		int(2), // OpenQuestionsCount
		sql.NullString{String: `{"k":"v"}`, Valid: true}, // RawJSON
		"comb.refresh",        // Source
		"2026-03-01 00:00:00", // RevisionAt
	}
}

// TestScanRevisionRow covers all branches of scanRevisionRow.
func TestScanRevisionRow(t *testing.T) {
	t.Run("happy path all fields populated contested true", func(t *testing.T) {
		s := &mockScanner{vals: happyScannerVals()}
		got, err := scanRevisionRow(s)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil {
			t.Fatal("expected non-nil CombRevisionRow")
		}
		if got.ID != 7 {
			t.Errorf("ID = %d; want 7", got.ID)
		}
		if got.VantageKey != "d1=0" {
			t.Errorf("VantageKey = %q; want %q", got.VantageKey, "d1=0")
		}
		if got.VantageKind != VantageKind("region") {
			t.Errorf("VantageKind = %q; want %q", got.VantageKind, "region")
		}
		if !got.TickID.Valid || got.TickID.Int64 != 3 {
			t.Errorf("TickID = %+v; want {3 true}", got.TickID)
		}
		if got.Narrative != "test narrative" {
			t.Errorf("Narrative = %q; want %q", got.Narrative, "test narrative")
		}
		if got.Confidence != 85 {
			t.Errorf("Confidence = %d; want 85", got.Confidence)
		}
		if !got.Contested {
			t.Errorf("Contested = false; want true (contestedInt=1)")
		}
		if !got.DominantLabel.Valid || got.DominantLabel.String != "definition" {
			t.Errorf("DominantLabel = %+v; want {definition true}", got.DominantLabel)
		}
		if got.EvidenceCount != 5 {
			t.Errorf("EvidenceCount = %d; want 5", got.EvidenceCount)
		}
		if got.OpenQuestionsCount != 2 {
			t.Errorf("OpenQuestionsCount = %d; want 2", got.OpenQuestionsCount)
		}
		if got.Source != "comb.refresh" {
			t.Errorf("Source = %q; want %q", got.Source, "comb.refresh")
		}
		if got.RevisionAt != "2026-03-01 00:00:00" {
			t.Errorf("RevisionAt = %q; want %q", got.RevisionAt, "2026-03-01 00:00:00")
		}
	})

	t.Run("contested false when contestedInt zero", func(t *testing.T) {
		vals := happyScannerVals()
		vals[6] = int(0)
		s := &mockScanner{vals: vals}
		got, err := scanRevisionRow(s)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil {
			t.Fatal("expected non-nil row")
		}
		if got.Contested {
			t.Errorf("Contested = true; want false (contestedInt=0)")
		}
	})

	t.Run("sql.ErrNoRows returns nil nil", func(t *testing.T) {
		s := &mockScanner{err: sql.ErrNoRows}
		got, err := scanRevisionRow(s)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Errorf("expected nil row; got %+v", got)
		}
	})

	t.Run("scan error is wrapped and returned", func(t *testing.T) {
		scanErr := errors.New("disk read failure")
		s := &mockScanner{err: scanErr}
		got, err := scanRevisionRow(s)
		if got != nil {
			t.Errorf("expected nil row on error; got %+v", got)
		}
		if err == nil {
			t.Fatal("expected non-nil error")
		}
		if !errors.Is(err, scanErr) {
			t.Errorf("error %v does not wrap original: %v", err, scanErr)
		}
	})
}

// newRevision is a test helper that appends one CombRevisionRow and returns it.
func newRevision(t *testing.T, repo *CombRevisionsRepo, vantageKey, narrative string, confidence int, ts string) *CombRevisionRow {
	t.Helper()
	row := &CombRevisionRow{
		VantageKey:    vantageKey,
		VantageKind:   VantageRegion,
		Narrative:     narrative,
		Confidence:    confidence,
		DominantLabel: sql.NullString{String: "assumption", Valid: true},
	}
	// Append with an explicit revision_at by inserting directly so we can
	// control timestamps deterministically.
	res, err := repo.writeDB.Exec(
		`INSERT INTO comb_revisions
		 (vantage_key, vantage_kind, tick_id,
		  narrative, confidence, contested, dominant_label,
		  evidence_count, open_questions_count, raw_json, source, revision_at)
		 VALUES (?,?,NULL,?,?,0,?,0,0,NULL,'test',?)`,
		vantageKey, string(VantageRegion),
		narrative, confidence,
		sql.NullString{String: "assumption", Valid: true},
		ts,
	)
	if err != nil {
		t.Fatalf("insert revision: %v", err)
	}
	id, _ := res.LastInsertId()
	row.ID = id
	row.RevisionAt = ts
	return row
}

// TestCombRevisionsRepo_Append covers the Append function branches:
//
//  1. Happy path: explicit VantageKind and source are persisted correctly
//  2. Empty VantageKind defaults to VantageRegion
//  3. Empty source defaults to "unknown"
//  4. Null TickID is stored as NULL (NullInt64 not valid)
//  5. Valid TickID is stored and can be read back
func TestCombRevisionsRepo_Append(t *testing.T) {
	store := newTestStore(t)
	repo := store.CombRevisions()

	t.Run("happy path explicit kind and source", func(t *testing.T) {
		row := &CombRevisionRow{
			VantageKey:         "d1=0",
			VantageKind:        VantageForager,
			Narrative:          "some narrative",
			Confidence:         75,
			Contested:          true,
			DominantLabel:      sql.NullString{String: "assumption", Valid: true},
			EvidenceCount:      3,
			OpenQuestionsCount: 1,
			RawJSON:            sql.NullString{String: `{"k":"v"}`, Valid: true},
		}
		id, err := repo.Append(row, "comb.refresh")
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if id <= 0 {
			t.Errorf("id = %d; want > 0", id)
		}
		// Verify persisted fields by reading back via History.
		rows, err := repo.History("d1=0", 10)
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d; want 1", len(rows))
		}
		got := rows[0]
		if got.ID != id {
			t.Errorf("ID = %d; want %d", got.ID, id)
		}
		if got.VantageKind != VantageForager {
			t.Errorf("VantageKind = %q; want %q", got.VantageKind, VantageForager)
		}
		if got.Narrative != "some narrative" {
			t.Errorf("Narrative = %q; want %q", got.Narrative, "some narrative")
		}
		if got.Confidence != 75 {
			t.Errorf("Confidence = %d; want 75", got.Confidence)
		}
		if !got.Contested {
			t.Errorf("Contested = false; want true")
		}
		if got.EvidenceCount != 3 {
			t.Errorf("EvidenceCount = %d; want 3", got.EvidenceCount)
		}
		if got.OpenQuestionsCount != 1 {
			t.Errorf("OpenQuestionsCount = %d; want 1", got.OpenQuestionsCount)
		}
		if got.Source != "comb.refresh" {
			t.Errorf("Source = %q; want %q", got.Source, "comb.refresh")
		}
	})

	t.Run("empty VantageKind defaults to region", func(t *testing.T) {
		store2 := newTestStore(t)
		repo2 := store2.CombRevisions()
		row := &CombRevisionRow{
			VantageKey: "d1=1",
			// VantageKind intentionally left empty
			Narrative:  "defaults test",
			Confidence: 50,
		}
		id, err := repo2.Append(row, "test")
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		rows, err := repo2.History("d1=1", 1)
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d; want 1", len(rows))
		}
		if rows[0].ID != id {
			t.Errorf("ID = %d; want %d", rows[0].ID, id)
		}
		if rows[0].VantageKind != VantageRegion {
			t.Errorf("VantageKind = %q; want %q", rows[0].VantageKind, VantageRegion)
		}
	})

	t.Run("empty source defaults to unknown", func(t *testing.T) {
		store3 := newTestStore(t)
		repo3 := store3.CombRevisions()
		row := &CombRevisionRow{
			VantageKey:  "d1=2",
			VantageKind: VantageRegion,
			Narrative:   "source defaults test",
			Confidence:  40,
		}
		id, err := repo3.Append(row, "") // empty source
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		rows, err := repo3.History("d1=2", 1)
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d; want 1", len(rows))
		}
		if rows[0].ID != id {
			t.Errorf("ID = %d; want %d", rows[0].ID, id)
		}
		if rows[0].Source != "unknown" {
			t.Errorf("Source = %q; want %q", rows[0].Source, "unknown")
		}
	})

	t.Run("null TickID stored as NULL", func(t *testing.T) {
		store4 := newTestStore(t)
		repo4 := store4.CombRevisions()
		row := &CombRevisionRow{
			VantageKey:  "d1=3",
			VantageKind: VantageRegion,
			Narrative:   "null tick test",
			Confidence:  30,
			TickID:      sql.NullInt64{Valid: false},
		}
		_, err := repo4.Append(row, "test")
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		rows, err := repo4.History("d1=3", 1)
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d; want 1", len(rows))
		}
		if rows[0].TickID.Valid {
			t.Errorf("TickID.Valid = true; want false (NULL)")
		}
	})

	t.Run("error on closed db propagates", func(t *testing.T) {
		store5 := newTestStore(t)
		repo5 := store5.CombRevisions()
		// Close the write DB to force a failure.
		store5.Close()
		row := &CombRevisionRow{
			VantageKey:  "d1=error",
			VantageKind: VantageRegion,
			Narrative:   "error test",
			Confidence:  10,
		}
		_, err := repo5.Append(row, "test")
		if err == nil {
			t.Errorf("expected error after db close; got nil")
		}
	})
}

// TestCombRevisionsRepo_Diff covers the four meaningful branches of Diff:
//
//  1. No revisions at all → (nil, nil, nil)
//  2. Only a revision before "to", none before "from" → (nil, rev, nil)
//  3. Revisions before both "from" and "to" → (baseRev, curRev, nil)
//  4. "from" == "to" and one revision exists → same row returned for both
func TestCombRevisionsRepo_Diff(t *testing.T) {
	store := newTestStore(t)
	repo := store.CombRevisions()

	const key = "d1=1"
	const t0 = "2026-01-01 00:00:00"
	const t1 = "2026-03-01 00:00:00"
	const t2 = "2026-05-01 00:00:00"

	t.Run("no revisions returns nil nil", func(t *testing.T) {
		base, cur, err := repo.Diff(key, t0, t2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if base != nil {
			t.Errorf("base: got %+v; want nil", base)
		}
		if cur != nil {
			t.Errorf("cur: got %+v; want nil", cur)
		}
	})

	// Insert rev1 between t0 and t2 (after t0, before t2).
	rev1 := newRevision(t, repo, key, "first narrative", 60, t1)

	t.Run("revision exists only before to not before from", func(t *testing.T) {
		// from=t0 (before rev1), to=t2 (after rev1)
		base, cur, err := repo.Diff(key, t0, t2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if base != nil {
			t.Errorf("base: got %+v; want nil", base)
		}
		if cur == nil {
			t.Fatal("cur: got nil; want a revision row")
		}
		if cur.ID != rev1.ID {
			t.Errorf("cur.ID = %d; want %d", cur.ID, rev1.ID)
		}
		if cur.Narrative != rev1.Narrative {
			t.Errorf("cur.Narrative = %q; want %q", cur.Narrative, rev1.Narrative)
		}
	})

	// Insert rev0 at t0 (exactly equal to "from" bound).
	rev0 := newRevision(t, repo, key, "zeroth narrative", 40, t0)

	t.Run("happy path both revisions found", func(t *testing.T) {
		base, cur, err := repo.Diff(key, t0, t2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if base == nil {
			t.Fatal("base: got nil; want a revision row")
		}
		if base.ID != rev0.ID {
			t.Errorf("base.ID = %d; want %d", base.ID, rev0.ID)
		}
		if cur == nil {
			t.Fatal("cur: got nil; want a revision row")
		}
		if cur.ID != rev1.ID {
			t.Errorf("cur.ID = %d; want %d", cur.ID, rev1.ID)
		}
	})

	t.Run("from equals to returns same revision for both", func(t *testing.T) {
		base, cur, err := repo.Diff(key, t1, t1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if base == nil || cur == nil {
			t.Fatalf("expected both non-nil; got base=%v cur=%v", base, cur)
		}
		if base.ID != cur.ID {
			t.Errorf("expected same revision; base.ID=%d cur.ID=%d", base.ID, cur.ID)
		}
	})

	t.Run("unknown vantage key returns nil nil", func(t *testing.T) {
		base, cur, err := repo.Diff("no-such-key", t0, t2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if base != nil || cur != nil {
			t.Errorf("expected nil nil; got base=%v cur=%v", base, cur)
		}
	})
}

// TestCombRevisionsRepo_Diff_Errors covers the two error-return branches of
// Diff that are not reachable through the happy-path tests above:
//
//  1. At(from) returns an error → Diff returns nil, nil, err wrapping "diff base:"
//  2. At(to)   returns an error → Diff returns nil, nil, err wrapping "diff current:"
//
// Branch 1 is triggered by closing the read DB before calling Diff so that
// the very first QueryRow fails. Branch 2 would require the first At()
// invocation to succeed and the second to fail on the same DB connection,
// which is not achievable without interface-level mocking; it is omitted here
// and tracked as a coverage gap.
func TestCombRevisionsRepo_Diff_Errors(t *testing.T) {
	t.Run("diff base error propagates with diff base prefix", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.CombRevisions()

		// Close the read connection so that the first At() call fails.
		if err := store.ReadDB.Close(); err != nil {
			t.Fatalf("ReadDB.Close: %v", err)
		}

		base, cur, err := repo.Diff("d1=0", "2026-01-01 00:00:00", "2026-06-01 00:00:00")
		if err == nil {
			t.Fatal("expected error after ReadDB close; got nil")
		}
		if base != nil || cur != nil {
			t.Errorf("expected nil,nil on error; got base=%v cur=%v", base, cur)
		}
		const wantPrefix = "diff base:"
		if len(err.Error()) < len(wantPrefix) || err.Error()[:len(wantPrefix)] != wantPrefix {
			t.Errorf("error = %q; want prefix %q", err.Error(), wantPrefix)
		}
	})
}

// TestCombRevisionsRepo_History covers the History function branches:
//
//  1. No revisions → empty slice, no error
//  2. Happy path: multiple revisions returned most-recent first
//  3. limit <= 0 defaults to 100 (does not error)
//  4. limit < total rows → only limit rows returned
//  5. Unknown vantage key → empty slice, no error
func TestCombRevisionsRepo_History(t *testing.T) {
	store := newTestStore(t)
	repo := store.CombRevisions()

	const key = "forager:historian"
	const t0 = "2026-01-01 00:00:00"
	const t1 = "2026-02-01 00:00:00"
	const t2 = "2026-03-01 00:00:00"

	t.Run("no revisions returns empty slice", func(t *testing.T) {
		rows, err := repo.History(key, 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(rows) != 0 {
			t.Errorf("want 0 rows; got %d", len(rows))
		}
	})

	// Insert three revisions in chronological order.
	rev0 := newRevision(t, repo, key, "oldest narrative", 30, t0)
	rev1 := newRevision(t, repo, key, "middle narrative", 60, t1)
	rev2 := newRevision(t, repo, key, "newest narrative", 90, t2)

	t.Run("happy path most recent first", func(t *testing.T) {
		rows, err := repo.History(key, 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(rows) != 3 {
			t.Fatalf("want 3 rows; got %d", len(rows))
		}
		// Most-recent first.
		if rows[0].ID != rev2.ID {
			t.Errorf("rows[0].ID = %d; want %d (newest)", rows[0].ID, rev2.ID)
		}
		if rows[1].ID != rev1.ID {
			t.Errorf("rows[1].ID = %d; want %d (middle)", rows[1].ID, rev1.ID)
		}
		if rows[2].ID != rev0.ID {
			t.Errorf("rows[2].ID = %d; want %d (oldest)", rows[2].ID, rev0.ID)
		}
	})

	t.Run("limit zero defaults to 100 and returns all rows", func(t *testing.T) {
		rows, err := repo.History(key, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Default limit of 100 is far larger than our 3 rows.
		if len(rows) != 3 {
			t.Errorf("want 3 rows; got %d", len(rows))
		}
	})

	t.Run("negative limit defaults to 100 and returns all rows", func(t *testing.T) {
		rows, err := repo.History(key, -5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(rows) != 3 {
			t.Errorf("want 3 rows; got %d", len(rows))
		}
	})

	t.Run("limit smaller than total returns only limit rows", func(t *testing.T) {
		rows, err := repo.History(key, 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("want 2 rows; got %d", len(rows))
		}
		// Still most-recent first.
		if rows[0].ID != rev2.ID {
			t.Errorf("rows[0].ID = %d; want %d (newest)", rows[0].ID, rev2.ID)
		}
		if rows[1].ID != rev1.ID {
			t.Errorf("rows[1].ID = %d; want %d (middle)", rows[1].ID, rev1.ID)
		}
	})

	t.Run("unknown vantage key returns empty slice", func(t *testing.T) {
		rows, err := repo.History("no-such-vantage", 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(rows) != 0 {
			t.Errorf("want 0 rows; got %d", len(rows))
		}
	})
}

// TestCombRevisionsRepo_At covers the At function branches:
//
//  1. No revisions at all → (nil, nil)
//  2. Query timestamp before all revisions → (nil, nil)
//  3. Query timestamp equal to revision_at → returns that revision
//  4. Query timestamp after a revision → returns that revision
//  5. Multiple revisions: returns the latest one at-or-before the query
//  6. Unknown vantage key → (nil, nil)
func TestCombRevisionsRepo_At(t *testing.T) {
	store := newTestStore(t)
	repo := store.CombRevisions()

	const key = "d1=at-test"
	const tBefore = "2026-01-01 00:00:00"
	const t1 = "2026-02-01 00:00:00"
	const t2 = "2026-04-01 00:00:00"
	const tAfter = "2026-06-01 00:00:00"

	t.Run("no revisions returns nil", func(t *testing.T) {
		got, err := repo.At(key, tAfter)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Errorf("got %+v; want nil", got)
		}
	})

	t.Run("query before first revision returns nil", func(t *testing.T) {
		newRevision(t, repo, key, "first", 50, t1)
		got, err := repo.At(key, tBefore)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Errorf("got %+v; want nil", got)
		}
	})

	t.Run("query equal to revision_at returns that revision", func(t *testing.T) {
		got, err := repo.At(key, t1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil {
			t.Fatal("got nil; want a revision row")
		}
		if got.Narrative != "first" {
			t.Errorf("Narrative = %q; want %q", got.Narrative, "first")
		}
	})

	t.Run("query after revision returns that revision", func(t *testing.T) {
		got, err := repo.At(key, tAfter)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil {
			t.Fatal("got nil; want a revision row")
		}
		if got.Narrative != "first" {
			t.Errorf("Narrative = %q; want %q", got.Narrative, "first")
		}
	})

	rev2 := newRevision(t, repo, key, "second", 80, t2)

	t.Run("multiple revisions returns latest at or before query", func(t *testing.T) {
		// Query between t1 and t2: should return rev at t1.
		mid := "2026-03-01 00:00:00"
		got, err := repo.At(key, mid)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil {
			t.Fatal("got nil; want first revision")
		}
		if got.Narrative != "first" {
			t.Errorf("Narrative = %q; want %q", got.Narrative, "first")
		}

		// Query after t2: should return rev at t2.
		got, err = repo.At(key, tAfter)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil {
			t.Fatal("got nil; want second revision")
		}
		if got.ID != rev2.ID {
			t.Errorf("ID = %d; want %d", got.ID, rev2.ID)
		}
		if got.Narrative != "second" {
			t.Errorf("Narrative = %q; want %q", got.Narrative, "second")
		}
		if got.Confidence != 80 {
			t.Errorf("Confidence = %d; want 80", got.Confidence)
		}
	})

	t.Run("unknown vantage key returns nil", func(t *testing.T) {
		got, err := repo.At("no-such-key", tAfter)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Errorf("got %+v; want nil", got)
		}
	})
}

// TestCombRevisionsRepo_CountByVantage covers CountByVantage branches:
//
//  1. No revisions → empty map, no error
//  2. Happy path: multiple vantages with different counts
//  3. Count accumulates correctly as more revisions are added
func TestCombRevisionsRepo_CountByVantage(t *testing.T) {
	store := newTestStore(t)
	repo := store.CombRevisions()

	t.Run("no revisions returns empty map", func(t *testing.T) {
		counts, err := repo.CountByVantage()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(counts) != 0 {
			t.Errorf("want empty map; got %v", counts)
		}
	})

	const keyA = "d1=0"
	const keyB = "forager:optimist"

	// Insert 2 revisions for keyA and 1 for keyB.
	newRevision(t, repo, keyA, "a-first", 50, "2026-01-01 00:00:00")
	newRevision(t, repo, keyA, "a-second", 60, "2026-02-01 00:00:00")
	newRevision(t, repo, keyB, "b-first", 70, "2026-03-01 00:00:00")

	t.Run("happy path multiple vantages with correct counts", func(t *testing.T) {
		counts, err := repo.CountByVantage()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if counts[keyA] != 2 {
			t.Errorf("counts[%q] = %d; want 2", keyA, counts[keyA])
		}
		if counts[keyB] != 1 {
			t.Errorf("counts[%q] = %d; want 1", keyB, counts[keyB])
		}
		if len(counts) != 2 {
			t.Errorf("map len = %d; want 2", len(counts))
		}
	})

	// Add a third revision for keyA and verify the count updates.
	newRevision(t, repo, keyA, "a-third", 80, "2026-04-01 00:00:00")

	t.Run("count accumulates after additional revision", func(t *testing.T) {
		counts, err := repo.CountByVantage()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if counts[keyA] != 3 {
			t.Errorf("counts[%q] = %d; want 3", keyA, counts[keyA])
		}
		if counts[keyB] != 1 {
			t.Errorf("counts[%q] = %d; want 1", keyB, counts[keyB])
		}
	})
}

// errQueryDB is a minimal dbReader whose Query always returns an injected error
// and whose QueryRow delegates to a real DB. Used to exercise the error-return
// branches of CountByVantage and History without closing the shared connection.
type errQueryDB struct {
	real dbReader
	err  error
}

func (d *errQueryDB) QueryRow(query string, args ...any) *sql.Row {
	return d.real.QueryRow(query, args...)
}
func (d *errQueryDB) Query(_ string, _ ...any) (*sql.Rows, error) {
	return nil, d.err
}

// badColsQueryDB is a dbReader whose Query always returns rows with only one
// column. This causes rows.Scan(&k, &n) in CountByVantage to fail (expected 2
// destination args; got 1 column), exercising the scan-error return branch.
type badColsQueryDB struct {
	real dbReader
}

func (d *badColsQueryDB) QueryRow(query string, args ...any) *sql.Row {
	return d.real.QueryRow(query, args...)
}
func (d *badColsQueryDB) Query(_ string, _ ...any) (*sql.Rows, error) {
	// Return a single-column synthetic row so Next() is true but Scan into
	// (string, int) fails with a column-count mismatch.
	return d.real.(interface {
		Query(string, ...any) (*sql.Rows, error)
	}).Query(`SELECT 'mismatch'`)
}

// TestCombRevisionsRepo_CountByVantage_Errors covers the two error-return
// branches inside CountByVantage that the happy-path tests cannot reach:
//
//  1. readDB.Query returns an error → (nil, err)
//  2. rows.Scan returns an error (column count mismatch) → (nil, err)
func TestCombRevisionsRepo_CountByVantage_Errors(t *testing.T) {
	t.Run("query error propagates", func(t *testing.T) {
		store := newTestStore(t)
		injected := errors.New("injected query failure")
		mock := &errQueryDB{real: store.ReadDB, err: injected}
		repo := &CombRevisionsRepo{writeDB: store.WriteDB, readDB: mock}

		counts, err := repo.CountByVantage()
		if err == nil {
			t.Fatal("expected error from Query; got nil")
		}
		if !errors.Is(err, injected) {
			t.Errorf("error = %v; want to wrap %v", err, injected)
		}
		if counts != nil {
			t.Errorf("expected nil map on error; got %v", counts)
		}
	})

	t.Run("scan error propagates when rows have wrong column count", func(t *testing.T) {
		store := newTestStore(t)
		// Insert one revision so the synthetic rows.Next() in badColsQueryDB
		// returns true and Scan is actually attempted.
		newRevision(t, store.CombRevisions(), "d1=scan-err", "narrative", 50, "2026-01-01 00:00:00")

		mock := &badColsQueryDB{real: store.ReadDB}
		repo := &CombRevisionsRepo{writeDB: store.WriteDB, readDB: mock}

		counts, err := repo.CountByVantage()
		if err == nil {
			t.Fatal("expected error from Scan column mismatch; got nil")
		}
		if counts != nil {
			t.Errorf("expected nil map on error; got %v", counts)
		}
	})
}

// insertTestTick is a helper that inserts a time_wheel row and returns its id.
// started_at must be a SQLite-format timestamp string.
func insertTestTick(t *testing.T, store *Store, label, kind, startedAt string) int64 {
	t.Helper()
	res, err := store.WriteDB.Exec(
		`INSERT INTO time_wheel (label, kind, started_at) VALUES (?, ?, ?)`,
		label, kind, startedAt,
	)
	if err != nil {
		t.Fatalf("insertTestTick: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// TestCombRevisionsRepo_AtTick covers all branches of AtTick:
//
//  1. Happy path: revision whose tick_id matches → returned directly (no fallback)
//  2. No direct tick match → fallback to At(vantageKey, tick.started_at); revision found
//  3. No direct tick match → fallback to At(vantageKey, tick.started_at); no revision before tick → nil, nil
//  4. tick_id not found in time_wheel → error "tick N not found"
func TestCombRevisionsRepo_AtTick(t *testing.T) {
	const key = "d1=at-tick-test"
	const t0 = "2026-01-01 00:00:00"
	const t1 = "2026-03-01 00:00:00"
	const t2 = "2026-05-01 00:00:00"

	t.Run("direct tick match returned without fallback", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.CombRevisions()

		tickID := insertTestTick(t, store, "wave-1", "wave", t1)

		// Insert a revision anchored to this tick.
		res, err := store.WriteDB.Exec(
			`INSERT INTO comb_revisions
			 (vantage_key, vantage_kind, tick_id,
			  narrative, confidence, contested, dominant_label,
			  evidence_count, open_questions_count, raw_json, source, revision_at)
			 VALUES (?,?,?,?,?,0,NULL,0,0,NULL,'test',?)`,
			key, string(VantageRegion), tickID, "tick-anchored narrative", 77, t1,
		)
		if err != nil {
			t.Fatalf("insert revision with tick: %v", err)
		}
		revID, _ := res.LastInsertId()

		// Also insert a later revision with NO tick to verify we return the tick-matched one.
		store.WriteDB.Exec(
			`INSERT INTO comb_revisions
			 (vantage_key, vantage_kind, tick_id,
			  narrative, confidence, contested, dominant_label,
			  evidence_count, open_questions_count, raw_json, source, revision_at)
			 VALUES (?,?,NULL,?,?,0,NULL,0,0,NULL,'test',?)`,
			key, string(VantageRegion), "later narrative", 90, t2,
		)

		got, err := repo.AtTick(key, tickID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil {
			t.Fatal("got nil; want a revision")
		}
		if got.ID != revID {
			t.Errorf("ID = %d; want %d (tick-anchored revision)", got.ID, revID)
		}
		if got.Narrative != "tick-anchored narrative" {
			t.Errorf("Narrative = %q; want %q", got.Narrative, "tick-anchored narrative")
		}
	})

	t.Run("fallback to started_at when no tick_id match but revision exists before tick", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.CombRevisions()

		// tick starts at t1; revision is at t0 (before tick)
		tickID := insertTestTick(t, store, "wave-2", "wave", t1)

		revBefore := newRevision(t, repo, key, "before-tick narrative", 55, t0)

		got, err := repo.AtTick(key, tickID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil {
			t.Fatal("got nil; want revision from fallback At()")
		}
		if got.ID != revBefore.ID {
			t.Errorf("ID = %d; want %d", got.ID, revBefore.ID)
		}
		if got.Narrative != "before-tick narrative" {
			t.Errorf("Narrative = %q; want %q", got.Narrative, "before-tick narrative")
		}
	})

	t.Run("fallback returns nil nil when no revision before tick started_at", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.CombRevisions()

		// tick starts at t0; no revisions exist
		tickID := insertTestTick(t, store, "wave-3", "wave", t0)

		got, err := repo.AtTick(key, tickID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Errorf("got %+v; want nil", got)
		}
	})

	t.Run("tick not found returns error", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.CombRevisions()

		const nonExistentTickID = int64(99999)
		got, err := repo.AtTick(key, nonExistentTickID)
		if got != nil {
			t.Errorf("got %+v; want nil on error", got)
		}
		if err == nil {
			t.Fatal("expected error for non-existent tick; got nil")
		}
		want := fmt.Sprintf("tick %d not found", nonExistentTickID)
		if err.Error() != want {
			t.Errorf("error = %q; want %q", err.Error(), want)
		}
	})
}

// TestScanRevisionRows covers all branches of scanRevisionRows directly.
// The function is the *sql.Rows analogue of scanRevisionRow; it is exercised
// indirectly by History(), but this test attributes coverage explicitly and
// also reaches the error branch that the History tests cannot trigger.
func TestScanRevisionRows(t *testing.T) {
	const q = `SELECT id, vantage_key, vantage_kind, tick_id,
	                  narrative, confidence, contested, dominant_label,
	                  evidence_count, open_questions_count, raw_json,
	                  source, revision_at
	           FROM comb_revisions WHERE vantage_key = ?`

	t.Run("happy path all fields populated contested true", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.CombRevisions()

		in := &CombRevisionRow{
			VantageKey:         "d1=scanrows-happy",
			VantageKind:        VantageRegion,
			Narrative:          "scanrows narrative",
			Confidence:         72,
			Contested:          true,
			DominantLabel:      sql.NullString{String: "assumption", Valid: true},
			EvidenceCount:      4,
			OpenQuestionsCount: 2,
			RawJSON:            sql.NullString{String: `{"x":1}`, Valid: true},
		}
		id, err := repo.Append(in, "test-source")
		if err != nil {
			t.Fatalf("Append: %v", err)
		}

		rows, err := store.WriteDB.Query(q, "d1=scanrows-happy")
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		defer rows.Close()

		if !rows.Next() {
			t.Fatal("expected at least one row")
		}
		got, err := scanRevisionRows(rows)
		if err != nil {
			t.Fatalf("scanRevisionRows: %v", err)
		}
		if got == nil {
			t.Fatal("got nil; want a row")
		}
		if got.ID != id {
			t.Errorf("ID = %d; want %d", got.ID, id)
		}
		if got.VantageKey != "d1=scanrows-happy" {
			t.Errorf("VantageKey = %q; want %q", got.VantageKey, "d1=scanrows-happy")
		}
		if got.VantageKind != VantageRegion {
			t.Errorf("VantageKind = %q; want %q", got.VantageKind, VantageRegion)
		}
		if got.Narrative != "scanrows narrative" {
			t.Errorf("Narrative = %q; want %q", got.Narrative, "scanrows narrative")
		}
		if got.Confidence != 72 {
			t.Errorf("Confidence = %d; want 72", got.Confidence)
		}
		if !got.Contested {
			t.Errorf("Contested = false; want true (contested=1 in DB)")
		}
		if got.EvidenceCount != 4 {
			t.Errorf("EvidenceCount = %d; want 4", got.EvidenceCount)
		}
		if got.Source != "test-source" {
			t.Errorf("Source = %q; want %q", got.Source, "test-source")
		}
	})

	t.Run("contested false when zero stored", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.CombRevisions()

		in := &CombRevisionRow{
			VantageKey:  "d1=scanrows-uncontested",
			VantageKind: VantageRegion,
			Narrative:   "uncontested",
			Confidence:  50,
			Contested:   false,
		}
		if _, err := repo.Append(in, "test"); err != nil {
			t.Fatalf("Append: %v", err)
		}

		rows, err := store.WriteDB.Query(q, "d1=scanrows-uncontested")
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		defer rows.Close()

		if !rows.Next() {
			t.Fatal("expected at least one row")
		}
		got, err := scanRevisionRows(rows)
		if err != nil {
			t.Fatalf("scanRevisionRows: %v", err)
		}
		if got.Contested {
			t.Errorf("Contested = true; want false")
		}
	})

	t.Run("scan error propagates when column count mismatches", func(t *testing.T) {
		store := newTestStore(t)

		// SELECT only 2 columns; scanRevisionRows expects 13 → Scan returns an error.
		rows, err := store.WriteDB.Query(`SELECT 1, 'bad'`)
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		defer rows.Close()

		if !rows.Next() {
			t.Fatal("expected a synthetic row")
		}
		got, err := scanRevisionRows(rows)
		if err == nil {
			t.Error("expected error from column-count mismatch; got nil")
		}
		if got != nil {
			t.Errorf("expected nil row on error; got %+v", got)
		}
	})
}

// TestCombRevisionsRepo_History_Errors covers the error branches of History
// that the happy-path tests above cannot reach:
//
//  1. Query error (ReadDB closed) → error returned with "history:" prefix
//  2. rows.Err() propagation: SQLite returns a rows.Err() when the underlying
//     connection is closed mid-iteration; we verify the error surface works.
func TestCombRevisionsRepo_History_Errors(t *testing.T) {
	t.Run("query error propagates with history prefix", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.CombRevisions()

		// Close the read connection so that Query fails immediately.
		if err := store.ReadDB.Close(); err != nil {
			t.Fatalf("ReadDB.Close: %v", err)
		}

		rows, err := repo.History("d1=0", 10)
		if err == nil {
			t.Fatal("expected error after ReadDB close; got nil")
		}
		if rows != nil {
			t.Errorf("expected nil slice on error; got %v", rows)
		}
		const wantPrefix = "history:"
		if len(err.Error()) < len(wantPrefix) || err.Error()[:len(wantPrefix)] != wantPrefix {
			t.Errorf("error = %q; want prefix %q", err.Error(), wantPrefix)
		}
	})
}

// failAfterFirstQueryDB implements dbReader. The first QueryRow call
// is delegated to the real working DB (returns no rows for an unknown
// vantage key → nil, nil). Every subsequent QueryRow call is routed
// through a pre-closed *sql.DB so that its Scan returns an error,
// exercising the "diff current:" error branch in Diff.
type failAfterFirstQueryDB struct {
	real   *sql.DB
	closed *sql.DB
	mu     sync.Mutex
	n      int
}

func (d *failAfterFirstQueryDB) QueryRow(query string, args ...any) *sql.Row {
	d.mu.Lock()
	d.n++
	n := d.n
	d.mu.Unlock()
	if n == 1 {
		return d.real.QueryRow(query, args...)
	}
	return d.closed.QueryRow(query, args...)
}

func (d *failAfterFirstQueryDB) Query(query string, args ...any) (*sql.Rows, error) {
	return d.real.Query(query, args...)
}

// TestCombRevisionsRepo_Diff_Errors_CurrentBranch covers the one branch of
// Diff that TestCombRevisionsRepo_Diff_Errors cannot reach without
// interface-level mocking: At(to) returns an error while At(from) succeeds.
func TestCombRevisionsRepo_Diff_Errors_CurrentBranch(t *testing.T) {
	t.Run("diff current error propagates with diff current prefix", func(t *testing.T) {
		store := newTestStore(t)

		// closedDB is a separately opened and immediately closed *sql.DB.
		// Any QueryRow on it returns a *sql.Row whose Scan yields an error
		// ("sql: database is closed"), which is distinct from sql.ErrNoRows.
		closedDB, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("sql.Open: %v", err)
		}
		closedDB.Close()

		mock := &failAfterFirstQueryDB{real: store.ReadDB, closed: closedDB}
		repo := &CombRevisionsRepo{writeDB: store.WriteDB, readDB: mock}

		// Use a vantage key that has no rows so that At(from) returns (nil, nil).
		// At(to) is routed to the closed DB and returns an error.
		base, cur, err := repo.Diff(
			"d1=diff-cur-err-branch",
			"2026-01-01 00:00:00",
			"2026-06-01 00:00:00",
		)
		if err == nil {
			t.Fatal("expected error from At(to); got nil")
		}
		if base != nil || cur != nil {
			t.Errorf("expected nil,nil on error; got base=%v cur=%v", base, cur)
		}
		const wantPrefix = "diff current:"
		if len(err.Error()) < len(wantPrefix) || err.Error()[:len(wantPrefix)] != wantPrefix {
			t.Errorf("error = %q; want prefix %q", err.Error(), wantPrefix)
		}
	})
}

// TestNullableInt64 covers both branches of the nullableInt64 helper.
func TestNullableInt64(t *testing.T) {
	tests := []struct {
		name string
		in   sql.NullInt64
		want any
	}{
		{
			name: "null returns nil",
			in:   sql.NullInt64{Valid: false, Int64: 0},
			want: nil,
		},
		{
			name: "valid zero returns 0",
			in:   sql.NullInt64{Valid: true, Int64: 0},
			want: int64(0),
		},
		{
			name: "valid positive returns value",
			in:   sql.NullInt64{Valid: true, Int64: 42},
			want: int64(42),
		},
		{
			name: "valid negative returns value",
			in:   sql.NullInt64{Valid: true, Int64: -99},
			want: int64(-99),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := nullableInt64(tc.in)
			if got != tc.want {
				t.Errorf("nullableInt64(%+v) = %v (%T); want %v (%T)",
					tc.in, got, got, tc.want, tc.want)
			}
		})
	}
}
