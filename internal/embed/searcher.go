package embed

import (
	"database/sql"
	"fmt"
	"sort"
	"sync"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// Match is one similarity hit returned by Searcher.NearestK.
type Match struct {
	VantageKey  string         `json:"vantage_key"`
	VantageKind db.VantageKind `json:"vantage_kind"`
	Score       float32        `json:"score"`
	SourceText  string         `json:"source_text,omitempty"`
}

// Searcher is the in-memory nearest-neighbour index over
// comb_embeddings. Lazy-loads on first NearestK call; invalidates the
// cache when the row count changes.
//
// Implementation: brute-force cosine over the in-memory slice. The largest
// workspace observed, ~5,500 findings, takes well under 100ms per query; a
// sub-linear index would pay off only past ~50K vectors.
//
// Concurrency: a sync.Mutex guards the cached slice and the row-count
// used for invalidation. Lookups are read-heavy but the cache pointer
// rarely changes, so contention is negligible.
type Searcher struct {
	store *db.Store
	model string

	mu         sync.Mutex
	cachedAll  []*db.CombEmbeddingRow
	cachedRows int
}

// NewSearcher builds a Searcher over the given store, scoped to the
// supplied embedding model. Different models live in different
// vector spaces — never mix them in a single search.
func NewSearcher(store *db.Store, model string) *Searcher {
	return &Searcher{store: store, model: model}
}

// NearestK returns up to `k` matches whose cosine similarity to
// `query` is highest. `kindFilter` is optional ("" = all kinds);
// `excludeKey` is an optional vantage key to drop from results
// (typical use: callers want the K nearest *other* vantages, not
// themselves).
func (s *Searcher) NearestK(query []float32, k int, kindFilter db.VantageKind, excludeKey string) ([]Match, error) {
	if k <= 0 {
		return nil, nil
	}
	rows, err := s.load()
	if err != nil {
		return nil, err
	}
	return s.nearestKBrute(query, rows, k, kindFilter, excludeKey), nil
}

// nearestKBrute is the canonical correctness path — full O(N · dim)
// scan. Stable order on ties (sort.SliceStable).
func (s *Searcher) nearestKBrute(query []float32, rows []*db.CombEmbeddingRow, k int, kindFilter db.VantageKind, excludeKey string) []Match {
	out := make([]Match, 0, k+1)
	for _, r := range rows {
		if !searchable(r, query, kindFilter, excludeKey) {
			continue
		}
		score := Cosine(query, r.Embedding)
		out = append(out, Match{
			VantageKey:  r.VantageKey,
			VantageKind: r.VantageKind,
			Score:       score,
			SourceText:  nullStringValue(r.SourceText),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	if len(out) > k {
		out = out[:k]
	}
	return out
}

// searchable reports whether row r takes part in a search for query: of
// the kind asked for ("" asks for every kind), not the excluded key, and of
// the query's dimension. Rows of mismatched dim are skipped instead of
// panicking — the model column makes this rare, but we tolerate stragglers
// from older models cleanly.
func searchable(r *db.CombEmbeddingRow, query []float32, kindFilter db.VantageKind, excludeKey string) bool {
	return (kindFilter == "" || r.VantageKind == kindFilter) &&
		r.VantageKey != excludeKey &&
		len(r.Embedding) == len(query)
}

// load returns the in-memory embeddings, loading on first call or
// after Invalidate. Re-uses the underlying slice across calls.
func (s *Searcher) load() ([]*db.CombEmbeddingRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fresh, err := s.cacheFresh()
	if err != nil {
		return nil, err
	}
	if fresh {
		return s.cachedAll, nil
	}
	all, err := s.store.CombEmbeddings().ListByModel(s.model, "")
	if err != nil {
		return nil, fmt.Errorf("list embeddings: %w", err)
	}
	s.cachedAll = all
	s.cachedRows = len(all)
	return s.cachedAll, nil
}

// cacheFresh reports whether the cache holds the model's rows: it is loaded
// and, a cheap freshness check, the model's row count has not changed.
// s.mu must be held.
func (s *Searcher) cacheFresh() (bool, error) {
	if s.cachedAll == nil {
		return false, nil
	}
	var n int
	if err := s.store.ReadConn().QueryRow(
		`SELECT COUNT(*) FROM comb_embeddings WHERE model = ?`, s.model,
	).Scan(&n); err != nil {
		return false, fmt.Errorf("count embeddings: %w", err)
	}
	return n == s.cachedRows, nil
}

// nullStringValue unwraps sql.NullString → string (empty when invalid).
func nullStringValue(ns sql.NullString) string {
	if !ns.Valid {
		return ""
	}
	return ns.String
}
