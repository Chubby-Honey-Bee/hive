package db

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func freshEmbedStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "embed.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestPackUnpackFloat32_RoundTrip(t *testing.T) {
	in := []float32{0.1, -2.5, 3.14159, 0, 1e-30, 1e30}
	packed := PackFloat32(in)
	out, err := UnpackFloat32(packed)
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("len mismatch: %d vs %d", len(in), len(out))
	}
	for i := range in {
		if in[i] != out[i] {
			t.Errorf("at %d: in=%v out=%v", i, in[i], out[i])
		}
	}
}

func TestUnpackFloat32_RejectsBadLength(t *testing.T) {
	if _, err := UnpackFloat32([]byte{0x01, 0x02, 0x03}); err == nil {
		t.Fatal("expected error on length not multiple of 4")
	}
}

func TestCombEmbeddings_UpsertGet(t *testing.T) {
	store := freshEmbedStore(t)
	row := &CombEmbeddingRow{
		VantageKey: "forager:optimist", VantageKind: VantageForager,
		Model: "stub/v1", Dim: 4, Embedding: []float32{0.1, 0.2, 0.3, 0.4},
	}
	if err := store.CombEmbeddings().Upsert(row); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := store.CombEmbeddings().Get("forager:optimist", "stub/v1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected row, got nil")
	}
	if len(got.Embedding) != 4 {
		t.Fatalf("dim mismatch: %d", len(got.Embedding))
	}
}

func TestCombEmbeddings_DimMismatchRejected(t *testing.T) {
	store := freshEmbedStore(t)
	row := &CombEmbeddingRow{
		VantageKey: "x", VantageKind: VantageRegion,
		Model: "stub/v1", Dim: 5, Embedding: []float32{1, 2, 3},
	}
	if err := store.CombEmbeddings().Upsert(row); err == nil {
		t.Fatalf("expected dim-mismatch error")
	}
}

func TestCombEmbeddings_UniqueByVantageAndModel(t *testing.T) {
	store := freshEmbedStore(t)
	row := &CombEmbeddingRow{
		VantageKey: "x", VantageKind: VantageRegion,
		Model: "stub/v1", Dim: 4, Embedding: []float32{1, 2, 3, 4},
	}
	if err := store.CombEmbeddings().Upsert(row); err != nil {
		t.Fatal(err)
	}
	// Re-upsert with different vector should overwrite, not fail.
	row.Embedding = []float32{4, 3, 2, 1}
	if err := store.CombEmbeddings().Upsert(row); err != nil {
		t.Fatalf("overwrite upsert: %v", err)
	}
	got, _ := store.CombEmbeddings().Get("x", "stub/v1")
	if got.Embedding[0] != 4 {
		t.Fatalf("expected overwrite, got %v", got.Embedding)
	}
	// Same key + different model creates a parallel row.
	row.Model = "openai/text-embedding-3-small"
	row.Dim = 4
	if err := store.CombEmbeddings().Upsert(row); err != nil {
		t.Fatalf("parallel-model upsert: %v", err)
	}
	if got, _ := store.CombEmbeddings().Get("x", "openai/text-embedding-3-small"); got == nil {
		t.Fatalf("expected parallel-model row to exist")
	}
}

func TestCombEmbeddings_ListByModel(t *testing.T) {
	store := freshEmbedStore(t)
	repo := store.CombEmbeddings()

	// Seed: two forager rows for "stub/v1", one region row for "stub/v1",
	// and one forager row for a different model that must never appear.
	seed := []*CombEmbeddingRow{
		{VantageKey: "forager:optimist", VantageKind: VantageForager, Model: "stub/v1", Embedding: []float32{0.1, 0.2}},
		{VantageKey: "forager:skeptic", VantageKind: VantageForager, Model: "stub/v1", Embedding: []float32{0.3, 0.4}},
		{VantageKey: "d1=0", VantageKind: VantageRegion, Model: "stub/v1", Embedding: []float32{0.5, 0.6}},
		{VantageKey: "forager:other", VantageKind: VantageForager, Model: "other/model", Embedding: []float32{0.7, 0.8}},
	}
	for _, r := range seed {
		if err := repo.Upsert(r); err != nil {
			t.Fatalf("seed upsert %s: %v", r.VantageKey, err)
		}
	}

	tests := []struct {
		name       string
		model      string
		kindFilter VantageKind
		wantCount  int
	}{
		{"all kinds for stub/v1", "stub/v1", "", 3},
		{"forager kind only", "stub/v1", VantageForager, 2},
		{"region kind only", "stub/v1", VantageRegion, 1},
		{"kind with no matches", "stub/v1", VantageFinding, 0},
		{"model not in DB", "nonexistent/model", "", 0},
		{"other model unfiltered", "other/model", "", 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := repo.ListByModel(tc.model, tc.kindFilter)
			if err != nil {
				t.Fatalf("ListByModel: %v", err)
			}
			if len(rows) != tc.wantCount {
				t.Fatalf("want %d rows, got %d", tc.wantCount, len(rows))
			}
			for _, r := range rows {
				if r.Model != tc.model {
					t.Errorf("expected model %q, got %q", tc.model, r.Model)
				}
				if tc.kindFilter != "" && r.VantageKind != tc.kindFilter {
					t.Errorf("expected kind %q, got %q", tc.kindFilter, r.VantageKind)
				}
				if len(r.Embedding) == 0 {
					t.Errorf("embedding should not be empty for %s", r.VantageKey)
				}
			}
		})
	}
}

// TestScanEmbedRows exercises scanEmbedRows directly to cover the three
// branches: happy path, rows.Scan failure, and UnpackFloat32 failure.
func TestScanEmbedRows(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		store := freshEmbedStore(t)
		row := &CombEmbeddingRow{
			VantageKey: "forager:test", VantageKind: VantageForager,
			Model: "stub/v1", Dim: 3, Embedding: []float32{0.1, 0.2, 0.3},
		}
		if err := store.CombEmbeddings().Upsert(row); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		rows, err := store.ReadDB.Query(
			`SELECT id, vantage_key, vantage_kind, model, dim,
			        embedding, source_text, created_at
			 FROM comb_embeddings WHERE vantage_key = ?`, "forager:test",
		)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatal("expected at least one row")
		}
		got, err := scanEmbedRows(rows)
		if err != nil {
			t.Fatalf("scanEmbedRows: %v", err)
		}
		if got.VantageKey != "forager:test" {
			t.Errorf("VantageKey: got %q, want %q", got.VantageKey, "forager:test")
		}
		if got.VantageKind != VantageForager {
			t.Errorf("VantageKind: got %q, want %q", got.VantageKind, VantageForager)
		}
		if len(got.Embedding) != 3 {
			t.Errorf("Embedding len: got %d, want 3", len(got.Embedding))
		}
	})

	t.Run("scan error from wrong column count", func(t *testing.T) {
		store := freshEmbedStore(t)
		if err := store.CombEmbeddings().Upsert(&CombEmbeddingRow{
			VantageKey: "d1=0", VantageKind: VantageRegion,
			Model: "stub/v1", Dim: 2, Embedding: []float32{1, 2},
		}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		// Query with only one column; scanEmbedRows expects eight — Scan must fail.
		rows, err := store.ReadDB.Query(`SELECT id FROM comb_embeddings`)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatal("expected at least one row")
		}
		_, err = scanEmbedRows(rows)
		if err == nil {
			t.Fatal("expected scan error, got nil")
		}
	})

	t.Run("unpack error from corrupted blob", func(t *testing.T) {
		store := freshEmbedStore(t)
		// Insert a row whose embedding blob is 3 bytes — not a multiple of 4.
		_, err := store.WriteDB.Exec(
			`INSERT INTO comb_embeddings
			 (vantage_key, vantage_kind, model, dim, embedding)
			 VALUES ('bad-blob', 'region', 'stub/v1', 3, ?)`,
			[]byte{0x01, 0x02, 0x03},
		)
		if err != nil {
			t.Fatalf("insert corrupted blob: %v", err)
		}
		rows, err := store.ReadDB.Query(
			`SELECT id, vantage_key, vantage_kind, model, dim,
			        embedding, source_text, created_at
			 FROM comb_embeddings WHERE vantage_key = 'bad-blob'`,
		)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatal("expected at least one row")
		}
		_, err = scanEmbedRows(rows)
		if err == nil {
			t.Fatal("expected unpack error, got nil")
		}
	})
}

// TestScanEmbedRow exercises scanEmbedRow directly via mockScanner,
// covering: happy path, sql.ErrNoRows (nil,nil), generic scan error,
// and a corrupted blob that triggers UnpackFloat32 failure.
func TestScanEmbedRow(t *testing.T) {
	validBlob := PackFloat32([]float32{0.1, 0.2, 0.3})

	happyVals := []any{
		int64(1),
		"forager:optimist",
		"forager",
		"stub/v1",
		int(3),
		validBlob,
		sql.NullString{String: "source text", Valid: true},
		"2026-01-01 00:00:00",
	}

	tests := []struct {
		name     string
		scanner  rowScanner
		wantNil  bool
		wantErr  bool
		wantKey  string
		wantKind VantageKind
		wantDim  int
	}{
		{
			name:     "happy path",
			scanner:  &mockScanner{vals: happyVals},
			wantKey:  "forager:optimist",
			wantKind: VantageForager,
			wantDim:  3,
		},
		{
			name:    "sql.ErrNoRows returns nil nil",
			scanner: &mockScanner{err: sql.ErrNoRows},
			wantNil: true,
		},
		{
			name:    "other scan error returns error",
			scanner: &mockScanner{err: errors.New("db closed")},
			wantErr: true,
		},
		{
			name: "corrupted blob triggers unpack error",
			scanner: &mockScanner{vals: []any{
				int64(2),
				"region:x",
				"region",
				"stub/v1",
				int(3),
				[]byte{0x01, 0x02, 0x03}, // 3 bytes — not a multiple of 4
				sql.NullString{},
				"2026-01-01 00:00:00",
			}},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := scanEmbedRow(tc.scanner)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantNil {
				if got != nil {
					t.Fatalf("expected nil result, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil result, got nil")
			}
			if got.VantageKey != tc.wantKey {
				t.Errorf("VantageKey: got %q, want %q", got.VantageKey, tc.wantKey)
			}
			if got.VantageKind != tc.wantKind {
				t.Errorf("VantageKind: got %q, want %q", got.VantageKind, tc.wantKind)
			}
			if len(got.Embedding) != tc.wantDim {
				t.Errorf("Embedding len: got %d, want %d", len(got.Embedding), tc.wantDim)
			}
		})
	}
}

// TestCombEmbeddings_ListByModel_ErrorPaths covers the two error branches
// inside ListByModel that are not reached by the happy-path table test:
// (1) Query failure when readDB is closed, (2) scanEmbedRows failure when
// the embedding blob is corrupt (triggers the mid-loop error return).
func TestCombEmbeddings_ListByModel_ErrorPaths(t *testing.T) {
	t.Run("query error when readDB is closed", func(t *testing.T) {
		store := freshEmbedStore(t)
		repo := store.CombEmbeddings()
		store.ReadDB.Close()
		_, err := repo.ListByModel("stub/v1", "")
		if err == nil {
			t.Fatal("expected error after readDB closed, got nil")
		}
	})

	t.Run("scan error from corrupted blob stops iteration", func(t *testing.T) {
		store := freshEmbedStore(t)
		repo := store.CombEmbeddings()
		// Insert one good row followed by one row whose embedding blob is corrupt.
		if err := repo.Upsert(&CombEmbeddingRow{
			VantageKey: "good", VantageKind: VantageRegion,
			Model: "stub/v1", Embedding: []float32{1, 2},
		}); err != nil {
			t.Fatalf("upsert good row: %v", err)
		}
		// Bypass Upsert validation to store a 3-byte blob (not a multiple of 4).
		if _, err := store.WriteDB.Exec(
			`INSERT INTO comb_embeddings
			 (vantage_key, vantage_kind, model, dim, embedding)
			 VALUES ('corrupt', 'region', 'stub/v1', 2, ?)`,
			[]byte{0x01, 0x02, 0x03},
		); err != nil {
			t.Fatalf("insert corrupt blob: %v", err)
		}
		_, err := repo.ListByModel("stub/v1", "")
		if err == nil {
			t.Fatal("expected error from corrupt blob, got nil")
		}
	})
}

// TestCombEmbeddings_Upsert_Branches covers the branches inside Upsert that
// are not exercised by the existing happy-path and dim-mismatch tests:
// (1) Dim auto-set when Dim==0, (2) VantageKind defaulting to VantageRegion,
// (3) DB exec error when writeDB is closed.
func TestCombEmbeddings_Upsert_Branches(t *testing.T) {
	t.Run("zero Dim auto-set from embedding length", func(t *testing.T) {
		store := freshEmbedStore(t)
		row := &CombEmbeddingRow{
			VantageKey:  "auto-dim",
			VantageKind: VantageRegion,
			Model:       "stub/v1",
			Dim:         0, // must be inferred from Embedding
			Embedding:   []float32{1, 2, 3},
		}
		if err := store.CombEmbeddings().Upsert(row); err != nil {
			t.Fatalf("expected success with Dim=0, got: %v", err)
		}
		if row.Dim != 3 {
			t.Fatalf("expected Dim auto-set to 3, got %d", row.Dim)
		}
		got, err := store.CombEmbeddings().Get("auto-dim", "stub/v1")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got == nil || len(got.Embedding) != 3 {
			t.Fatalf("expected 3-element embedding, got %v", got)
		}
	})

	t.Run("empty VantageKind defaults to VantageRegion", func(t *testing.T) {
		store := freshEmbedStore(t)
		row := &CombEmbeddingRow{
			VantageKey:  "default-kind",
			VantageKind: "", // must default to VantageRegion
			Model:       "stub/v1",
			Embedding:   []float32{0.5, 0.5},
		}
		if err := store.CombEmbeddings().Upsert(row); err != nil {
			t.Fatalf("expected success with empty VantageKind, got: %v", err)
		}
		if row.VantageKind != VantageRegion {
			t.Fatalf("expected VantageKind=%q, got %q", VantageRegion, row.VantageKind)
		}
		got, err := store.CombEmbeddings().Get("default-kind", "stub/v1")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got == nil || got.VantageKind != VantageRegion {
			t.Fatalf("expected stored VantageKind=%q, got %v", VantageRegion, got)
		}
	})

	t.Run("db exec error when writeDB is closed", func(t *testing.T) {
		store := freshEmbedStore(t)
		store.WriteDB.Close()
		row := &CombEmbeddingRow{
			VantageKey:  "closed-db",
			VantageKind: VantageRegion,
			Model:       "stub/v1",
			Embedding:   []float32{1, 2},
		}
		if err := store.CombEmbeddings().Upsert(row); err == nil {
			t.Fatal("expected error after WriteDB closed, got nil")
		}
	})
}
