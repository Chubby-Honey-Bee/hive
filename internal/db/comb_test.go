package db

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func TestFindingVantageKey(t *testing.T) {
	cases := []struct {
		id   int64
		want string
	}{
		{0, "finding:0"},
		{1, "finding:1"},
		{42, "finding:42"},
		{-1, "finding:-1"},
		{9999999, fmt.Sprintf("finding:%d", 9999999)},
	}
	for _, tc := range cases {
		got := FindingVantageKey(tc.id)
		if got != tc.want {
			t.Errorf("FindingVantageKey(%d) = %q; want %q", tc.id, got, tc.want)
		}
	}
}

// TestCombRepo_Get covers the three branches of CombRepo.Get:
//
//  1. key not present → (nil, nil)
//  2. key present     → returns the row with correct field mapping
//  3. the contested boolean round-trips through int storage
func TestCombRepo_Get(t *testing.T) {
	store := newTestStore(t)
	repo := store.Comb()

	t.Run("missing key returns nil nil", func(t *testing.T) {
		got, err := repo.Get("forager:no-such-forager")
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil row, got %+v", got)
		}
	})

	t.Run("happy path round-trip", func(t *testing.T) {
		in := &CombRow{
			VantageKey:         "forager:optimist",
			VantageKind:        VantageForager,
			D1:                 sql.NullInt64{Int64: 1, Valid: true},
			Narrative:          "all is well",
			Confidence:         80,
			Contested:          false,
			DominantLabel:      sql.NullString{String: "guarantee", Valid: true},
			EvidenceCount:      5,
			OpenQuestionsCount: 2,
			DigestMethod:       "sha256",
			RawJSON:            sql.NullString{String: `{"k":"v"}`, Valid: true},
		}
		if err := repo.Upsert(in); err != nil {
			t.Fatalf("Upsert: %v", err)
		}

		got, err := repo.Get("forager:optimist")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got == nil {
			t.Fatal("expected row, got nil")
		}

		if got.VantageKey != in.VantageKey {
			t.Errorf("VantageKey: got %q want %q", got.VantageKey, in.VantageKey)
		}
		if got.VantageKind != in.VantageKind {
			t.Errorf("VantageKind: got %q want %q", got.VantageKind, in.VantageKind)
		}
		if got.Narrative != in.Narrative {
			t.Errorf("Narrative: got %q want %q", got.Narrative, in.Narrative)
		}
		if got.Confidence != in.Confidence {
			t.Errorf("Confidence: got %d want %d", got.Confidence, in.Confidence)
		}
		if got.Contested != in.Contested {
			t.Errorf("Contested: got %v want %v", got.Contested, in.Contested)
		}
		if got.EvidenceCount != in.EvidenceCount {
			t.Errorf("EvidenceCount: got %d want %d", got.EvidenceCount, in.EvidenceCount)
		}
	})

	t.Run("contested boolean", func(t *testing.T) {
		in := &CombRow{
			VantageKey:   "d1=0",
			VantageKind:  VantageRegion,
			Narrative:    "contested region",
			Confidence:   50,
			Contested:    true,
			DigestMethod: "sha256",
		}
		if err := repo.Upsert(in); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		got, err := repo.Get("d1=0")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got == nil {
			t.Fatal("expected row, got nil")
		}
		if !got.Contested {
			t.Error("Contested should be true")
		}
	})
}

// TestCombRepo_List covers CombRepo.List and CombRepo.ListKind:
//
//  1. empty table → empty slice (not nil error)
//  2. multiple rows of mixed kinds → List returns all, ListKind filters
//  3. order is (vantage_kind, vantage_key) as documented
func TestCombRepo_List(t *testing.T) {
	store := newTestStore(t)
	repo := store.Comb()

	t.Run("empty table returns empty slice", func(t *testing.T) {
		rows, err := repo.List()
		if err != nil {
			t.Fatalf("List on empty table: %v", err)
		}
		if len(rows) != 0 {
			t.Fatalf("expected 0 rows, got %d", len(rows))
		}
	})

	// Insert three rows: two regions and one forager.
	inputs := []*CombRow{
		{VantageKey: "d1=2", VantageKind: VantageRegion, Narrative: "region two", Confidence: 60, DigestMethod: "sha256"},
		{VantageKey: "d1=1", VantageKind: VantageRegion, Narrative: "region one", Confidence: 70, DigestMethod: "sha256"},
		{VantageKey: "forager:skeptic", VantageKind: VantageForager, Narrative: "skeptic", Confidence: 50, DigestMethod: "sha256"},
	}
	for _, in := range inputs {
		if err := repo.Upsert(in); err != nil {
			t.Fatalf("Upsert(%q): %v", in.VantageKey, err)
		}
	}

	t.Run("List returns all rows ordered by vantage_kind then vantage_key", func(t *testing.T) {
		rows, err := repo.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(rows) != 3 {
			t.Fatalf("expected 3 rows, got %d", len(rows))
		}
		// ORDER BY vantage_kind, vantage_key:
		// "forager" < "region" lexicographically; within region "d1=1" < "d1=2".
		wantOrder := []string{"forager:skeptic", "d1=1", "d1=2"}
		for i, want := range wantOrder {
			if rows[i].VantageKey != want {
				t.Errorf("row[%d].VantageKey = %q; want %q", i, rows[i].VantageKey, want)
			}
		}
	})

	t.Run("ListKind region returns only region rows", func(t *testing.T) {
		rows, err := repo.ListKind(VantageRegion)
		if err != nil {
			t.Fatalf("ListKind(region): %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("expected 2 region rows, got %d", len(rows))
		}
		for _, r := range rows {
			if r.VantageKind != VantageRegion {
				t.Errorf("unexpected kind %q in ListKind(region) result", r.VantageKind)
			}
		}
	})

	t.Run("ListKind forager returns only forager rows", func(t *testing.T) {
		rows, err := repo.ListKind(VantageForager)
		if err != nil {
			t.Fatalf("ListKind(forager): %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("expected 1 forager row, got %d", len(rows))
		}
		if rows[0].VantageKey != "forager:skeptic" {
			t.Errorf("VantageKey = %q; want forager:skeptic", rows[0].VantageKey)
		}
	})

	t.Run("ListKind unknown kind returns empty slice", func(t *testing.T) {
		rows, err := repo.ListKind("nonexistent")
		if err != nil {
			t.Fatalf("ListKind(nonexistent): %v", err)
		}
		if len(rows) != 0 {
			t.Fatalf("expected 0 rows for unknown kind, got %d", len(rows))
		}
	})

	t.Run("List field mapping round-trips correctly", func(t *testing.T) {
		rows, err := repo.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		// Find the d1=1 region row and verify fields.
		var found *CombRow
		for _, r := range rows {
			if r.VantageKey == "d1=1" {
				found = r
				break
			}
		}
		if found == nil {
			t.Fatal("d1=1 row not found in List output")
		}
		if found.VantageKind != VantageRegion {
			t.Errorf("VantageKind = %q; want region", found.VantageKind)
		}
		if found.Narrative != "region one" {
			t.Errorf("Narrative = %q; want 'region one'", found.Narrative)
		}
		if found.Confidence != 70 {
			t.Errorf("Confidence = %d; want 70", found.Confidence)
		}
	})
}

// TestCombUpsert_Branches covers the branches inside Upsert that are not
// exercised by the TestCombRepo_Get / TestCombRepo_List tests above:
//   - empty VantageKind → defaults to "region" (the if-branch)
//   - on-conflict path → updates existing row and deduplicates
func TestCombUpsert_Branches(t *testing.T) {
	t.Run("empty VantageKind defaults to region", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.Comb()
		row := &CombRow{
			VantageKey: "global",
			// VantageKind intentionally left empty — exercises the if-branch
			Narrative: "defaults test",
		}
		if err := repo.Upsert(row); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		// Upsert mutates the caller's struct in-place.
		if row.VantageKind != VantageRegion {
			t.Errorf("row.VantageKind after Upsert = %q; want %q", row.VantageKind, VantageRegion)
		}
		got, err := repo.Get("global")
		if err != nil {
			t.Fatalf("Get after default: %v", err)
		}
		if got == nil {
			t.Fatal("expected row, got nil")
		}
		if got.VantageKind != VantageRegion {
			t.Errorf("stored VantageKind = %q; want %q", got.VantageKind, VantageRegion)
		}
	})

	t.Run("on-conflict updates existing row without creating duplicate", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.Comb()
		key := "d1=42"
		if err := repo.Upsert(&CombRow{VantageKey: key, VantageKind: VantageRegion, Narrative: "v1", Confidence: 10}); err != nil {
			t.Fatalf("first Upsert: %v", err)
		}
		if err := repo.Upsert(&CombRow{VantageKey: key, VantageKind: VantageRegion, Narrative: "v2", Confidence: 99}); err != nil {
			t.Fatalf("second Upsert: %v", err)
		}
		got, err := repo.Get(key)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got == nil {
			t.Fatal("expected row, got nil")
		}
		if got.Narrative != "v2" {
			t.Errorf("Narrative = %q; want v2", got.Narrative)
		}
		if got.Confidence != 99 {
			t.Errorf("Confidence = %d; want 99", got.Confidence)
		}
		var count int
		store.ReadDB.QueryRow("SELECT COUNT(*) FROM comb_state WHERE vantage_key=?", key).Scan(&count)
		if count != 1 {
			t.Errorf("expected exactly 1 row after upsert, got %d", count)
		}
	})
}

// combStateWithStale is comb_state with a stale column between digest_method
// and raw_json, a column the schema does not declare.
const combStateWithStale = `CREATE TABLE comb_state (
	vantage_key TEXT PRIMARY KEY,
	vantage_kind TEXT NOT NULL DEFAULT 'region'
		CHECK(vantage_kind IN ('region','forager')),
	d1 INTEGER, d2 INTEGER, d3 INTEGER, d4 INTEGER,
	d5 INTEGER, d6 INTEGER, d7 INTEGER, d8 INTEGER,
	narrative TEXT NOT NULL DEFAULT '',
	confidence INTEGER CHECK(confidence BETWEEN 0 AND 100) NOT NULL DEFAULT 0,
	contested INTEGER NOT NULL DEFAULT 0,
	dominant_label TEXT,
	evidence_count INTEGER NOT NULL DEFAULT 0,
	open_questions_count INTEGER NOT NULL DEFAULT 0,
	digest_method TEXT NOT NULL DEFAULT 'heuristic',
	stale INTEGER NOT NULL DEFAULT 0,
	raw_json TEXT,
	last_revised_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
)`

// storeWithStaleColumn opens a store on a database whose comb_state carries
// the stale column and its index, created before the store applies the
// schema, which leaves an existing table as it is.
func storeWithStaleColumn(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stale-column.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{combStateWithStale, `CREATE INDEX idx_comb_stale ON comb_state(stale)`} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	return store
}

// A comb_state that carries a column the schema does not declare, a stale
// flag here, set on every row, reads and writes as one without it: every
// query names its columns, and none reads the extra one.
func TestCombRepo_ExtraColumnReadsAndWrites(t *testing.T) {
	store := storeWithStaleColumn(t)
	repo := store.Comb()
	row := &CombRow{VantageKey: "d1=0", VantageKind: VantageRegion, Narrative: "first", Confidence: 40, Contested: true}
	if err := repo.Upsert(row); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if _, err := store.WriteDB.Exec(`UPDATE comb_state SET stale = 1`); err != nil {
		t.Fatal(err)
	}
	row.Narrative = "second"
	if err := repo.Upsert(row); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	if err := repo.Upsert(&CombRow{VantageKey: "forager:optimist", VantageKind: VantageForager, Narrative: "support"}); err != nil {
		t.Fatalf("forager Upsert: %v", err)
	}
	got, err := repo.Get("d1=0")
	if err != nil || got == nil || got.Narrative != "second" || got.Confidence != 40 || !got.Contested {
		t.Fatalf("Get = %+v, %v; want the second write", got, err)
	}
	rows, err := repo.List()
	if err != nil || len(rows) != 2 {
		t.Fatalf("List = %d rows, %v; want 2", len(rows), err)
	}
	if n, err := repo.CountContested(); err != nil || n != 1 {
		t.Fatalf("CountContested = %d, %v; want 1", n, err)
	}
	if at, err := repo.LastRefreshAt(); err != nil || at == "" {
		t.Fatalf("LastRefreshAt = %q, %v; want the region's write", at, err)
	}
}

// TestCombRepo_Upsert_ErrorPath: an Upsert whose write fails returns the
// error. Closing writeDB before the call makes its Exec fail.
func TestCombRepo_Upsert_ErrorPath(t *testing.T) {
	store := newTestStore(t)
	if err := store.WriteDB.Close(); err != nil {
		t.Fatalf("close WriteDB: %v", err)
	}
	repo := store.Comb()
	err := repo.Upsert(&CombRow{
		VantageKey:  "d1=999",
		VantageKind: VantageRegion,
		Narrative:   "should fail",
	})
	if err == nil {
		t.Fatal("expected error after WriteDB closed, got nil")
	}
}

// TestCombRepo_LastRefreshAt covers the reachable branches of LastRefreshAt:
//  1. empty table → MAX returns SQL NULL → !t.Valid → returns ("", nil)
//  2. only a forager row → returns ("", nil)
//  3. multiple rows → returns the maximum region last_revised_at
//  4. readDB closed → Scan returns an error → propagated to caller
//
// Note: sql.ErrNoRows is unreachable for MAX() queries; SQLite always
// returns a single row (possibly NULL), so that branch is dead code but
// still compiles correctly.
func TestCombRepo_LastRefreshAt(t *testing.T) {
	t.Run("empty table returns empty string", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.Comb()
		got, err := repo.LastRefreshAt()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("expected empty string for empty table, got %q", got)
		}
	})

	t.Run("a forager row alone is not a refresh", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.Comb()
		if err := repo.Upsert(&CombRow{
			VantageKey:  "forager:optimist",
			VantageKind: VantageForager,
			Narrative:   "all good",
			Confidence:  70,
		}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		got, err := repo.LastRefreshAt()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("got %q with only a forager row; want empty", got)
		}
	})

	t.Run("multiple rows returns the latest region last_revised_at", func(t *testing.T) {
		store := newTestStore(t)
		repo := store.Comb()
		revised := map[string]string{
			"d1=1":            "2026-09-01 10:00:00",
			"d1=2":            "2026-09-02 10:00:00",
			"forager:skeptic": "2026-09-03 10:00:00",
		}
		want := ""
		for key, at := range revised {
			kind := VantageRegion
			if key == "forager:skeptic" {
				kind = VantageForager
			}
			if err := repo.Upsert(&CombRow{
				VantageKey:  key,
				VantageKind: kind,
				Narrative:   "narrative for " + key,
				Confidence:  50,
			}); err != nil {
				t.Fatalf("Upsert(%q): %v", key, err)
			}
			if _, err := store.WriteDB.Exec(`UPDATE comb_state SET last_revised_at = ? WHERE vantage_key = ?`, at, key); err != nil {
				t.Fatal(err)
			}
			if kind == VantageRegion && at > want {
				want = at
			}
		}

		got, err := repo.LastRefreshAt()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != want {
			t.Errorf("got %q; want %q", got, want)
		}
		if got == "" {
			t.Error("expected non-empty last_revised_at for populated table")
		}
	})

	t.Run("error path when readDB is closed", func(t *testing.T) {
		badDB, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("sql.Open: %v", err)
		}
		badDB.Close()
		repo := NewCombRepo(badDB, badDB)
		_, err = repo.LastRefreshAt()
		if err == nil {
			t.Fatal("expected error from closed readDB, got nil")
		}
	})
}

func TestForagerVantageKey(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain name", "optimist", "forager:optimist"},
		{"already prefixed", "forager:skeptic", "forager:skeptic"},
		{"leading whitespace", "  pragmatist", "forager:pragmatist"},
		{"trailing whitespace", "historian  ", "forager:historian"},
		{"whitespace + prefix", "  forager:empiricist  ", "forager:empiricist"},
		{"empty string", "", "forager:"},
		{"double prefix", "forager:forager:dreamer", "forager:forager:dreamer"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := ForagerVantageKey(tc.input)
			if got != tc.want {
				t.Errorf("ForagerVantageKey(%q) = %q; want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestCombRowToCoordsMap covers ToCoordsMap:
//  1. all dimensions valid → all eight keys present with correct values
//  2. all dimensions invalid → empty map returned
//  3. mixed valid/invalid → only valid dimensions included (branch per dim)
//  4. zero-value Int64 with Valid=true → dimension is included
func TestCombRowToCoordsMap(t *testing.T) {
	nv := func(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
	ni := sql.NullInt64{Valid: false}

	cases := []struct {
		name     string
		row      CombRow
		wantKeys []string
		wantVals map[string]int64
	}{
		{
			name: "all valid returns all eight dimensions",
			row: CombRow{
				D1: nv(1), D2: nv(2), D3: nv(3), D4: nv(4),
				D5: nv(5), D6: nv(6), D7: nv(7), D8: nv(8),
			},
			wantKeys: []string{"d1", "d2", "d3", "d4", "d5", "d6", "d7", "d8"},
			wantVals: map[string]int64{
				"d1": 1, "d2": 2, "d3": 3, "d4": 4,
				"d5": 5, "d6": 6, "d7": 7, "d8": 8,
			},
		},
		{
			name:     "all invalid returns empty map",
			row:      CombRow{D1: ni, D2: ni, D3: ni, D4: ni, D5: ni, D6: ni, D7: ni, D8: ni},
			wantKeys: []string{},
			wantVals: map[string]int64{},
		},
		{
			name:     "only d1 and d3 valid",
			row:      CombRow{D1: nv(10), D2: ni, D3: nv(30), D4: ni, D5: ni, D6: ni, D7: ni, D8: ni},
			wantKeys: []string{"d1", "d3"},
			wantVals: map[string]int64{"d1": 10, "d3": 30},
		},
		{
			name:     "only d8 valid",
			row:      CombRow{D1: ni, D2: ni, D3: ni, D4: ni, D5: ni, D6: ni, D7: ni, D8: nv(99)},
			wantKeys: []string{"d8"},
			wantVals: map[string]int64{"d8": 99},
		},
		{
			name:     "zero-value Int64 with Valid=true is included",
			row:      CombRow{D1: sql.NullInt64{Int64: 0, Valid: true}, D2: ni, D3: ni, D4: ni, D5: ni, D6: ni, D7: ni, D8: ni},
			wantKeys: []string{"d1"},
			wantVals: map[string]int64{"d1": 0},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := tc.row.ToCoordsMap()

			if len(got) != len(tc.wantKeys) {
				t.Errorf("len(ToCoordsMap()) = %d; want %d", len(got), len(tc.wantKeys))
			}

			wantSet := make(map[string]bool, len(tc.wantKeys))
			for _, k := range tc.wantKeys {
				wantSet[k] = true
				v, ok := got[k]
				if !ok {
					t.Errorf("key %q missing from result", k)
					continue
				}
				if !v.Valid {
					t.Errorf("key %q: Valid=false; want true", k)
				}
				if v.Int64 != tc.wantVals[k] {
					t.Errorf("key %q: Int64=%d; want %d", k, v.Int64, tc.wantVals[k])
				}
			}

			for k := range got {
				if !wantSet[k] {
					t.Errorf("unexpected key %q in result", k)
				}
			}
		})
	}
}

// TestCombRow_ToCoordsMap covers the branches of CombRow.ToCoordsMap:
//  1. all 8 dimensions valid  → map with 8 entries
//  2. subset valid            → sparse map with only valid entries
//  3. none valid              → empty map (not nil)
//  4. zero value is preserved when Valid=true
func TestCombRow_ToCoordsMap(t *testing.T) {
	nv := func(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
	ni := func() sql.NullInt64 { return sql.NullInt64{} }

	cases := []struct {
		name     string
		row      CombRow
		wantKeys []string
		wantVals map[string]int64
	}{
		{
			name: "all 8 dimensions valid",
			row: CombRow{
				D1: nv(1), D2: nv(2), D3: nv(3), D4: nv(4),
				D5: nv(5), D6: nv(6), D7: nv(7), D8: nv(8),
			},
			wantKeys: []string{"d1", "d2", "d3", "d4", "d5", "d6", "d7", "d8"},
			wantVals: map[string]int64{"d1": 1, "d2": 2, "d3": 3, "d4": 4, "d5": 5, "d6": 6, "d7": 7, "d8": 8},
		},
		{
			name: "sparse: only d1 and d3 valid",
			row: CombRow{
				D1: nv(10), D2: ni(), D3: nv(30), D4: ni(),
				D5: ni(), D6: ni(), D7: ni(), D8: ni(),
			},
			wantKeys: []string{"d1", "d3"},
			wantVals: map[string]int64{"d1": 10, "d3": 30},
		},
		{
			name:     "none valid returns empty map",
			row:      CombRow{},
			wantKeys: []string{},
			wantVals: map[string]int64{},
		},
		{
			name:     "zero value is preserved when Valid=true",
			row:      CombRow{D1: nv(0), D2: ni()},
			wantKeys: []string{"d1"},
			wantVals: map[string]int64{"d1": 0},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := tc.row.ToCoordsMap()
			if got == nil {
				t.Fatal("ToCoordsMap returned nil; want non-nil map")
			}
			if len(got) != len(tc.wantKeys) {
				t.Errorf("map len = %d; want %d", len(got), len(tc.wantKeys))
			}
			for _, k := range tc.wantKeys {
				v, ok := got[k]
				if !ok {
					t.Errorf("key %q missing from map", k)
					continue
				}
				if !v.Valid {
					t.Errorf("key %q: NullInt64.Valid is false; want true", k)
				}
				if v.Int64 != tc.wantVals[k] {
					t.Errorf("key %q: Int64 = %d; want %d", k, v.Int64, tc.wantVals[k])
				}
			}
			wantSet := make(map[string]bool, len(tc.wantKeys))
			for _, k := range tc.wantKeys {
				wantSet[k] = true
			}
			for k := range got {
				if !wantSet[k] {
					t.Errorf("unexpected key %q in map", k)
				}
			}
		})
	}
}

// TestCombRepo_CountContested covers CountContested:
//  1. empty table  → 0, nil
//  2. one row, contested=false → 0
//  3. one contested row → 1
//  4. mixed rows → only contested rows counted
func TestCombRepo_CountContested(t *testing.T) {
	cases := []struct {
		name      string
		rows      []*CombRow
		wantCount int
	}{
		{
			name:      "empty table returns 0",
			rows:      nil,
			wantCount: 0,
		},
		{
			name: "single non-contested row returns 0",
			rows: []*CombRow{
				{VantageKey: "d1=1", VantageKind: VantageRegion, Narrative: "n", Confidence: 50, Contested: false, DigestMethod: "sha256"},
			},
			wantCount: 0,
		},
		{
			name: "single contested row returns 1",
			rows: []*CombRow{
				{VantageKey: "d1=1", VantageKind: VantageRegion, Narrative: "n", Confidence: 50, Contested: true, DigestMethod: "sha256"},
			},
			wantCount: 1,
		},
		{
			name: "mixed rows counts only contested",
			rows: []*CombRow{
				{VantageKey: "d1=1", VantageKind: VantageRegion, Narrative: "n", Confidence: 50, Contested: true, DigestMethod: "sha256"},
				{VantageKey: "d1=2", VantageKind: VantageRegion, Narrative: "n", Confidence: 60, Contested: false, DigestMethod: "sha256"},
				{VantageKey: "d1=3", VantageKind: VantageRegion, Narrative: "n", Confidence: 70, Contested: true, DigestMethod: "sha256"},
			},
			wantCount: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			repo := store.Comb()
			for _, row := range tc.rows {
				if err := repo.Upsert(row); err != nil {
					t.Fatalf("Upsert(%q): %v", row.VantageKey, err)
				}
			}
			got, err := repo.CountContested()
			if err != nil {
				t.Fatalf("CountContested: %v", err)
			}
			if got != tc.wantCount {
				t.Errorf("CountContested = %d; want %d", got, tc.wantCount)
			}
		})
	}
}

// TestCombRow_Line covers every branch of (*CombRow).Line.
//
// Branches covered:
//
//  1. non-empty Narrative → used as-is (after TrimSpace)
//  2. whitespace-only Narrative → treated as empty → synthetic body
//  3. empty Narrative + DominantLabel valid → dom shown as label string
//  4. empty Narrative + DominantLabel invalid → dom shown as "—"
//  5. Contested=true → " [contested]" appended
//  6. stale → " [stale]" appended
//  7. both Contested and stale → both suffixes
//  8. len(out) == 512 → not truncated
//  9. len(out) > 512 → capped to exactly 512 bytes
func TestCombRow_Line(t *testing.T) {
	nullStr := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	repeat := func(b byte, n int) string {
		buf := make([]byte, n)
		for i := range buf {
			buf[i] = b
		}
		return string(buf)
	}

	cases := []struct {
		name  string
		row   CombRow
		stale bool
		want  string
	}{
		{
			name: "non-empty narrative returned verbatim",
			row:  CombRow{VantageKey: "forager:optimist", Narrative: "all is well"},
			want: "all is well",
		},
		{
			name: "whitespace-only narrative treated as empty",
			row: CombRow{
				VantageKey:    "d1=7",
				Narrative:     "   \t  ",
				EvidenceCount: 3,
				Confidence:    50,
				DominantLabel: nullStr("definition"),
			},
			want: "vantage=d1=7 evidence=3 open=0 dominant=definition confidence=50%",
		},
		{
			name: "empty narrative with valid DominantLabel",
			row: CombRow{
				VantageKey:         "d1=3",
				DominantLabel:      nullStr("assumption"),
				EvidenceCount:      7,
				OpenQuestionsCount: 2,
				Confidence:         55,
			},
			want: "vantage=d1=3 evidence=7 open=2 dominant=assumption confidence=55%",
		},
		{
			name: "empty narrative with invalid DominantLabel uses em-dash",
			row: CombRow{
				VantageKey:         "d1=2",
				EvidenceCount:      3,
				OpenQuestionsCount: 1,
				Confidence:         72,
			},
			want: "vantage=d1=2 evidence=3 open=1 dominant=\u2014 confidence=72%",
		},
		{
			name: "contested flag appended",
			row:  CombRow{VantageKey: "d1=2", Narrative: "some finding", Contested: true},
			want: "some finding [contested]",
		},
		{
			name:  "stale appended",
			row:   CombRow{VantageKey: "d1=3", Narrative: "some finding"},
			stale: true,
			want:  "some finding [stale]",
		},
		{
			name:  "both contested and stale appended",
			row:   CombRow{VantageKey: "d1=4", Narrative: "some finding", Contested: true},
			stale: true,
			want:  "some finding [contested] [stale]",
		},
		{
			name: "narrative exactly 512 chars is not truncated",
			row:  CombRow{VantageKey: "d1=5", Narrative: repeat('x', 512)},
			want: repeat('x', 512),
		},
		{
			name: "narrative longer than 512 chars is capped",
			row:  CombRow{VantageKey: "d1=6", Narrative: repeat('x', 600)},
			want: repeat('x', 512),
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := tc.row.Line(tc.stale)
			if got != tc.want {
				t.Errorf("Line(%v) = %q; want %q", tc.stale, got, tc.want)
			}
			if len(got) > 512 {
				t.Errorf("output length %d exceeds 512 cap", len(got))
			}
		})
	}
}
