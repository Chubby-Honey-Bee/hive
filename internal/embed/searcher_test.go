package embed

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// TestNewSearcher pins the constructor's field assignment.
func TestNewSearcher(t *testing.T) {
	store := freshStore(t)
	s := NewSearcher(store, "stub")
	if s == nil {
		t.Fatal("NewSearcher returned nil")
	}
	if s.store != store {
		t.Error("store field not set correctly")
	}
	if s.model != "stub" {
		t.Errorf("model = %q; want %q", s.model, "stub")
	}
}

func TestNullStringValue(t *testing.T) {
	cases := []struct {
		name string
		ns   sql.NullString
		want string
	}{
		{"valid non-empty", sql.NullString{String: "hello", Valid: true}, "hello"},
		{"valid empty", sql.NullString{String: "", Valid: true}, ""},
		{"invalid (NULL)", sql.NullString{String: "ignored", Valid: false}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nullStringValue(tc.ns)
			if got != tc.want {
				t.Errorf("nullStringValue(%+v) = %q; want %q", tc.ns, got, tc.want)
			}
		})
	}
}

func freshStore(t *testing.T) *db.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "embed.db")
	store, err := db.NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// seed populates the embeddings table with N stub embeddings.
func seed(t *testing.T, store *db.Store, prov Provider, vantages []string) {
	t.Helper()
	ctx := context.Background()
	for _, v := range vantages {
		vec, err := prov.Embed(ctx, v)
		if err != nil {
			t.Fatalf("embed: %v", err)
		}
		row := &db.CombEmbeddingRow{
			VantageKey:  v,
			VantageKind: db.VantageRegion,
			Model:       prov.Name(),
			Dim:         len(vec),
			Embedding:   vec,
		}
		if err := store.CombEmbeddings().Upsert(row); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
}

func TestSearcher_NearestK_OrdersByScore(t *testing.T) {
	store := freshStore(t)
	prov := NewStubProvider()
	seed(t, store, prov, []string{"alpha", "beta", "gamma", "delta", "epsilon"})

	s := NewSearcher(store, prov.Name())
	queryVec, _ := prov.Embed(context.Background(), "alpha")
	matches, err := s.NearestK(queryVec, 3, "", "")
	if err != nil {
		t.Fatalf("NearestK: %v", err)
	}
	if len(matches) != 3 {
		t.Fatalf("expected 3 matches, got %d", len(matches))
	}
	// "alpha" must be the top match (cosine 1.0 with itself).
	if matches[0].VantageKey != "alpha" || matches[0].Score < 0.999 {
		t.Errorf("top match should be alpha @ ~1.0, got %s @ %v", matches[0].VantageKey, matches[0].Score)
	}
	// Scores must be monotonically non-increasing.
	for i := 1; i < len(matches); i++ {
		if matches[i].Score > matches[i-1].Score {
			t.Errorf("scores not sorted: %v before %v", matches[i-1].Score, matches[i].Score)
		}
	}
}

func TestSearcher_ExcludesAnchorByKey(t *testing.T) {
	store := freshStore(t)
	prov := NewStubProvider()
	seed(t, store, prov, []string{"alpha", "beta", "gamma"})

	s := NewSearcher(store, prov.Name())
	queryVec, _ := prov.Embed(context.Background(), "alpha")
	matches, err := s.NearestK(queryVec, 3, "", "alpha")
	if err != nil {
		t.Fatalf("NearestK: %v", err)
	}
	for _, m := range matches {
		if m.VantageKey == "alpha" {
			t.Fatalf("alpha should be excluded by anchor filter")
		}
	}
}

func TestSearcher_KindFilter(t *testing.T) {
	store := freshStore(t)
	prov := NewStubProvider()
	ctx := context.Background()
	// Mix kinds: 2 region, 1 forager.
	for i, v := range []string{"region:a", "region:b", "forager:x"} {
		vec, _ := prov.Embed(ctx, v)
		kind := db.VantageRegion
		if i == 2 {
			kind = db.VantageForager
		}
		_ = store.CombEmbeddings().Upsert(&db.CombEmbeddingRow{
			VantageKey: v, VantageKind: kind, Model: prov.Name(),
			Dim: len(vec), Embedding: vec,
		})
	}
	s := NewSearcher(store, prov.Name())
	queryVec, _ := prov.Embed(ctx, "region:a")
	matches, err := s.NearestK(queryVec, 5, db.VantageForager, "")
	if err != nil {
		t.Fatalf("NearestK: %v", err)
	}
	for _, m := range matches {
		if m.VantageKind != db.VantageForager {
			t.Errorf("kind filter violated: got %s for %s", m.VantageKind, m.VantageKey)
		}
	}
}

// TestSearcher_NearestK_ZeroOrNegativeK covers the k<=0 early-return branch.
func TestSearcher_NearestK_ZeroOrNegativeK(t *testing.T) {
	store := freshStore(t)
	prov := NewStubProvider()
	seed(t, store, prov, []string{"alpha"})
	s := NewSearcher(store, prov.Name())
	queryVec, _ := prov.Embed(context.Background(), "alpha")

	for _, k := range []int{0, -1, -100} {
		matches, err := s.NearestK(queryVec, k, "", "")
		if err != nil {
			t.Errorf("k=%d: unexpected error: %v", k, err)
		}
		if matches != nil {
			t.Errorf("k=%d: expected nil matches, got %v", k, matches)
		}
	}
}

// TestSearcher_load_CacheHitFastPath verifies that a second call to load
// returns the cached slice without re-querying ListByModel when the row
// count has not changed.
func TestSearcher_load_CacheHitFastPath(t *testing.T) {
	store := freshStore(t)
	prov := NewStubProvider()
	seed(t, store, prov, []string{"alpha", "beta", "gamma"})
	s := NewSearcher(store, prov.Name())

	// First call — cold cache, populates cachedAll.
	rows1, err := s.load()
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if len(rows1) != 3 {
		t.Fatalf("first load: expected 3 rows, got %d", len(rows1))
	}

	// Second call — cache hit, count unchanged; must return the same slice.
	rows2, err := s.load()
	if err != nil {
		t.Fatalf("second load (cache hit): %v", err)
	}
	if len(rows2) != 3 {
		t.Fatalf("second load: expected 3 rows, got %d", len(rows2))
	}
	// Pointer equality confirms the fast-return path was taken.
	if len(rows1) > 0 && len(rows2) > 0 && &rows1[0] != &rows2[0] {
		t.Error("cache-hit path should return the same backing slice")
	}
}

// TestSearcher_load_CacheHitReloadPath verifies that load reloads data when
// the row count increases between two calls (even without an explicit Invalidate).
func TestSearcher_load_CacheHitReloadPath(t *testing.T) {
	store := freshStore(t)
	prov := NewStubProvider()
	seed(t, store, prov, []string{"alpha", "beta"})
	s := NewSearcher(store, prov.Name())

	// Prime the cache.
	rows1, err := s.load()
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if len(rows1) != 2 {
		t.Fatalf("first load: expected 2 rows, got %d", len(rows1))
	}

	// Add a row directly — count diverges from cachedRows.
	seed(t, store, prov, []string{"gamma"})

	// Second call — cache hit but count changed; must reload.
	rows2, err := s.load()
	if err != nil {
		t.Fatalf("second load (count-changed): %v", err)
	}
	if len(rows2) != 3 {
		t.Fatalf("second load: expected 3 rows after reload, got %d", len(rows2))
	}
}

// TestSearcher_load_CountQueryError verifies that a DB error during the
// freshness count query is propagated correctly.
func TestSearcher_load_CountQueryError(t *testing.T) {
	store := freshStore(t)
	prov := NewStubProvider()
	seed(t, store, prov, []string{"alpha"})
	s := NewSearcher(store, prov.Name())

	// Prime the cache so the next call hits the count-check branch.
	if _, err := s.load(); err != nil {
		t.Fatalf("first load: %v", err)
	}

	// Close the DB to induce a query error on the next call.
	store.Close()

	_, err := s.load()
	if err == nil {
		t.Fatal("expected error after DB closed, got nil")
	}
}

// TestSearcher_load_ListByModelError verifies that a ListByModel error on a
// cold cache is propagated correctly.
func TestSearcher_load_ListByModelError(t *testing.T) {
	store := freshStore(t)
	prov := NewStubProvider()
	// No rows seeded — ensure cold-cache path is taken.
	s := NewSearcher(store, prov.Name())

	// First load on a fresh store should succeed (empty result is fine).
	rows, err := s.load()
	if err != nil {
		t.Fatalf("load on empty store: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows on empty store, got %d", len(rows))
	}

	// Close the DB; a new searcher has a cold cache, so its load goes to
	// ListByModel and must surface the error.
	store.Close()
	cold := NewSearcher(store, prov.Name())

	_, err = cold.load()
	if err == nil {
		t.Fatal("expected error from ListByModel after DB closed, got nil")
	}
}

// TestSearcher_NearestK_MismatchedDim verifies that rows with a different
// embedding dimension are silently skipped (no panic, no error).
func TestSearcher_NearestK_MismatchedDim(t *testing.T) {
	store := freshStore(t)
	prov := NewStubProvider()
	seed(t, store, prov, []string{"alpha", "beta"})

	// Insert a row whose embedding has the wrong dimension.
	badRow := &db.CombEmbeddingRow{
		VantageKey:  "bad-dim",
		VantageKind: db.VantageRegion,
		Model:       prov.Name(),
		Dim:         2,
		Embedding:   []float32{1.0, 0.0}, // 2-dim vs stub's 64-dim
	}
	if err := store.CombEmbeddings().Upsert(badRow); err != nil {
		t.Fatalf("upsert bad-dim row: %v", err)
	}

	s := NewSearcher(store, prov.Name())
	queryVec, _ := prov.Embed(context.Background(), "alpha")
	matches, err := s.NearestK(queryVec, 5, "", "")
	if err != nil {
		t.Fatalf("NearestK with mismatched-dim row: %v", err)
	}
	// bad-dim must not appear in results.
	for _, m := range matches {
		if m.VantageKey == "bad-dim" {
			t.Errorf("bad-dim row should have been skipped, but appeared in matches")
		}
	}
	// alpha and beta should still be returned.
	if len(matches) != 2 {
		t.Errorf("expected 2 matches (alpha, beta), got %d", len(matches))
	}
}
