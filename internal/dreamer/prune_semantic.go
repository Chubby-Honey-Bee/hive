package dreamer

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/embed"
	"github.com/Chubby-Honey-Bee/hive/internal/mss"
)

// embeddingThreshold is the cosine cutoff above which two assumption
// findings are flagged as semantically duplicated. 0.85 is a
// conservative bound for sentence-level embeddings — borrowed from
// retrieval-augmented-generation literature where 0.80–0.90 is the
// typical "this is obviously the same thing" range.
const embeddingThreshold = 0.85

// semanticPruneCandidates returns near-duplicate ASSUMPTION pairs
// detected via cosine similarity over the most-recent embedding model
// in comb_embeddings. Returns zero candidates (and no error) when no
// embeddings exist — the prune pass falls back to Jaccard-only.
//
// This is the embedding-cosine arm of the MSS Independence rule, which
// is specifically about assumptions ("no assumption derivable from
// other assumptions + definitions"). It deliberately mirrors
// mss.IndependenceAudit's discipline:
//
//   - only assumption findings are compared (a definition/definition or
//     guarantee/guarantee pair is NOT a redundant-assumption candidate);
//   - findings are bucketed by their (d1..d5) coordinate and compared
//     only within a bucket — the WASP "bounded work" promise, so the
//     comparison count stays O(per-coordinate²), not O(all-findings²);
//   - candidates carry the finding text so an operator can review them.
//
// The only difference from IndependenceAudit is the similarity metric:
// cosine over the finding's embedding instead of Jaccard over its words.
//
// Embeddings are looked up by the canonical finding vantage key
// (db.FindingVantageKey(id) → "finding:<id>"). When a workspace hasn't
// run `chb comb embed --findings` (most workspaces only embed region +
// forager vantages), the join finds nothing and this returns nil cleanly.
func semanticPruneCandidates(ctx context.Context, store *db.Store, opts Options) ([]mss.RedundancyCandidate, error) {
	embByFinding, err := findingEmbeddings(store)
	if err != nil || len(embByFinding) < 2 {
		return nil, err
	}
	groups, err := assumptionBuckets(ctx, store, embByFinding)
	if err != nil {
		return nil, err
	}
	return similarPairs(groups, embByFinding), nil
}

// similarPairs compares only within a coordinate bucket — the bounded-work
// promise. Every pair is returned: passPrune applies MaxPerPass after
// skipping pairs already signalled, where a cap here would stop at the same
// first pairs every run, all already signalled, and later runs would never
// reach the rest.
func similarPairs(groups map[bucketKey][]assumptionRow, embByFinding map[int64][]float32) []mss.RedundancyCandidate {
	var cands []mss.RedundancyCandidate
	for _, group := range groups {
		cands = appendSimilarPairs(cands, group, embByFinding)
	}
	return cands
}

// findingEmbeddings indexes the finding embeddings of the latest model by
// the finding id encoded in their vantage key; none when there are no
// embeddings.
func findingEmbeddings(store *db.Store) (map[int64][]float32, error) {
	model, err := latestEmbeddingModel(store)
	if err != nil {
		return nil, nil // no embeddings — nothing to do
	}
	embRows, err := store.CombEmbeddings().ListByModel(model, db.VantageFinding)
	if err != nil {
		return nil, fmt.Errorf("list embeddings: %w", err)
	}
	return indexByFinding(embRows), nil
}

// indexByFinding maps each embedding to the finding id encoded in its
// vantage key. Non-finding vantages (region / forager) yield 0 and are
// dropped — they are not assumption findings and cannot be prune candidates.
func indexByFinding(embRows []*db.CombEmbeddingRow) map[int64][]float32 {
	embByFinding := make(map[int64][]float32, len(embRows))
	for _, r := range embRows {
		if id := vantageKeyToFindingID(r.VantageKey); id != 0 {
			embByFinding[id] = r.Embedding
		}
	}
	return embByFinding
}

// assumptionRow is one assumption finding with its d1..d5 coordinate, an
// absent axis read as -1.
type assumptionRow struct {
	id                 int64
	text               string
	d1, d2, d3, d4, d5 int64
}

// bucketKey is the d1..d5 coordinate the embedding arm buckets by.
type bucketKey struct{ a, b, c, d, e int64 }

// assumptionBuckets reads the assumption findings that have an embedding in
// embByFinding — only those can be compared — bucketed by coordinate,
// mirroring mss.IndependenceAudit so the embedding arm groups identically.
func assumptionBuckets(ctx context.Context, store *db.Store, embByFinding map[int64][]float32) (map[bucketKey][]assumptionRow, error) {
	rows, err := store.ReadConn().QueryContext(ctx, `
		SELECT id, finding, COALESCE(d1, -1), COALESCE(d2, -1), COALESCE(d3, -1), COALESCE(d4, -1), COALESCE(d5, -1)
		FROM findings WHERE mss_label = 'assumption'
		ORDER BY d1, d2, d3, d4, d5, id
	`)
	if err != nil {
		return nil, fmt.Errorf("list assumptions: %w", err)
	}
	defer rows.Close()
	groups := make(map[bucketKey][]assumptionRow)
	if err := scanRows(rows, func(rows *sql.Rows) error {
		return bucketAssumption(rows, embByFinding, groups)
	}); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return groups, nil
}

// bucketAssumption scans one assumption from rows into its bucket in
// groups, when it has an embedding.
func bucketAssumption(rows *sql.Rows, embByFinding map[int64][]float32, groups map[bucketKey][]assumptionRow) error {
	var r assumptionRow
	if err := rows.Scan(&r.id, &r.text, &r.d1, &r.d2, &r.d3, &r.d4, &r.d5); err != nil {
		return fmt.Errorf("scan assumption: %w", err)
	}
	if _, ok := embByFinding[r.id]; ok {
		k := bucketKey{r.d1, r.d2, r.d3, r.d4, r.d5}
		groups[k] = append(groups[k], r)
	}
	return nil
}

// appendSimilarPairs appends to cands each pair in group whose embeddings'
// cosine reaches embeddingThreshold.
func appendSimilarPairs(cands []mss.RedundancyCandidate, group []assumptionRow, embByFinding map[int64][]float32) []mss.RedundancyCandidate {
	for i := 0; i < len(group); i++ {
		for j := i + 1; j < len(group); j++ {
			if c, ok := similarPair(group[i], group[j], embByFinding); ok {
				cands = append(cands, c)
			}
		}
	}
	return cands
}

// similarPair is the candidate a and b make when the cosine of their
// embeddings reaches embeddingThreshold.
func similarPair(a, b assumptionRow, embByFinding map[int64][]float32) (mss.RedundancyCandidate, bool) {
	score := embed.Cosine(embByFinding[a.id], embByFinding[b.id])
	if score < embeddingThreshold {
		return mss.RedundancyCandidate{}, false
	}
	return mss.RedundancyCandidate{
		IDA:        a.id,
		IDB:        b.id,
		Similarity: float64(score),
		FindingA:   a.text,
		FindingB:   b.text,
	}, true
}

// latestEmbeddingModel returns the model of the most recently written
// finding embedding, the one with the highest id, or an error if there is
// none. A region or question embedding written later under another model
// does not count, and created_at, which ties within a second, does not order
// them.
func latestEmbeddingModel(store *db.Store) (string, error) {
	var model string
	err := store.ReadConn().QueryRow(
		`SELECT model FROM comb_embeddings WHERE vantage_kind = 'finding' ORDER BY id DESC LIMIT 1`,
	).Scan(&model)
	return model, err
}

// mergeCandidateSources unions Jaccard + embedding candidate lists,
// deduplicating by unordered (IDA, IDB) pair. When a pair appears in
// both sources it gets the maximum of the two similarity scores.
// Stable order: Jaccard first, then embedding-only candidates.
func mergeCandidateSources(jaccard, embedding []mss.RedundancyCandidate) []mss.RedundancyCandidate {
	if len(embedding) == 0 {
		return jaccard
	}
	s := candidateSet{
		at:  make(map[[2]int64]int, len(jaccard)+len(embedding)),
		out: make([]mss.RedundancyCandidate, 0, len(jaccard)+len(embedding)),
	}
	for _, c := range jaccard {
		s.put(c)
	}
	for _, c := range embedding {
		s.merge(c)
	}
	return s.out
}

// candidateSet is a candidate list with the index of each unordered pair's
// latest entry.
type candidateSet struct {
	at  map[[2]int64]int
	out []mss.RedundancyCandidate
}

// put appends c and indexes its pair at it.
func (s *candidateSet) put(c mss.RedundancyCandidate) {
	s.at[unorderedPair(c.IDA, c.IDB)] = len(s.out)
	s.out = append(s.out, c)
}

// merge raises the similarity of c's pair to c's when the list holds the
// pair, and puts c otherwise.
func (s *candidateSet) merge(c mss.RedundancyCandidate) {
	idx, hit := s.at[unorderedPair(c.IDA, c.IDB)]
	if !hit {
		s.put(c)
		return
	}
	if c.Similarity > s.out[idx].Similarity {
		s.out[idx].Similarity = c.Similarity
	}
}

// unorderedPair is a and b, the lesser first.
func unorderedPair(a, b int64) [2]int64 {
	if a < b {
		return [2]int64{a, b}
	}
	return [2]int64{b, a}
}

// vantageKeyToFindingID parses a "finding:<n>" vantage key (the
// convention the embed CLI uses for finding-level embeddings).
// Returns 0 for non-finding vantage keys; callers skip those.
func vantageKeyToFindingID(key string) int64 {
	digits, ok := strings.CutPrefix(key, "finding:")
	if !ok || digits == "" {
		return 0
	}
	return decimalValue(digits)
}

// decimalValue is the value of s read as decimal digits, 0 when s holds any
// other character.
func decimalValue(s string) int64 {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

// errString is a tiny helper that returns "" for nil errors, the
// error's message otherwise. Keeps the prune pass's Notes JSON tidy.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
