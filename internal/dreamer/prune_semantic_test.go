package dreamer

import (
	"context"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/embed"
	"github.com/Chubby-Honey-Bee/hive/internal/mss"
)

// upsertFindingEmbedding writes a stub embedding for a finding under
// the canonical "finding:<id>" vantage_key. Used by paraphrase tests
// to seed the comb_embeddings table with values the prune pass can
// consume.
func upsertFindingEmbedding(t *testing.T, store *db.Store, id int64, text string, prov embed.Provider) {
	t.Helper()
	vec, err := prov.Embed(context.Background(), text)
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	row := &db.CombEmbeddingRow{
		VantageKey:  db.FindingVantageKey(id),
		VantageKind: db.VantageFinding,
		Model:       prov.Name(),
		Dim:         len(vec),
		Embedding:   vec,
	}
	row.SourceText.String = text
	row.SourceText.Valid = true
	if err := store.CombEmbeddings().Upsert(row); err != nil {
		t.Fatalf("upsert embedding: %v", err)
	}
}

func TestSemanticPruneCandidates_FindsParaphraseJaccardMisses(t *testing.T) {
	// Two findings with low lexical overlap but nearly identical
	// stub-embedding signature. We craft strings where the stub
	// embedding (FNV-1a per token, summed + normalised) produces
	// near-identical vectors. The simplest way: identical token bag
	// in different order — Jaccard catches this too. To escape Jaccard,
	// we need *different* tokens. Use a token-overlap pair where
	// Jaccard < 0.7 but stub similarity > 0.85.
	//
	// Stub embed sums per-token contributions; two strings sharing
	// any single token will score near-identical (one token dominates
	// the others). So a single shared salient token + different
	// fillers passes both: Jaccard < 0.7 (only one token shared) and
	// stub cosine > 0.85 (the shared token's contribution dominates).
	store := freshStore(t)
	a := mustAddFinding(t, store, "assumption", "fragmentation", nil)
	b := mustAddFinding(t, store, "assumption", "alpha beta gamma delta epsilon zeta", nil)
	prov := embed.NewStubProvider()
	upsertFindingEmbedding(t, store, a, "fragmentation", prov)
	upsertFindingEmbedding(t, store, b, "fragmentation", prov) // intentionally identical text → cosine 1

	cands, err := semanticPruneCandidates(context.Background(), store, DefaultOptions())
	if err != nil {
		t.Fatalf("semanticPruneCandidates: %v", err)
	}
	if len(cands) == 0 {
		t.Fatalf("expected ≥1 paraphrase candidate, got 0")
	}
	// At least one pair must include both seeded findings.
	found := false
	for _, c := range cands {
		if (c.IDA == a && c.IDB == b) || (c.IDA == b && c.IDB == a) {
			found = true
			if c.Similarity < 0.85 {
				t.Errorf("expected similarity ≥ 0.85, got %v", c.Similarity)
			}
		}
	}
	if !found {
		t.Fatalf("paraphrase pair (%d, %d) not in candidates: %v", a, b, cands)
	}
}

func TestSemanticPruneCandidates_NoEmbeddings_ReturnsNilCleanly(t *testing.T) {
	store := freshStore(t)
	mustAddFinding(t, store, "assumption", "x", nil)
	mustAddFinding(t, store, "assumption", "y", nil)

	cands, err := semanticPruneCandidates(context.Background(), store, DefaultOptions())
	if err != nil {
		t.Fatalf("semanticPruneCandidates: %v", err)
	}
	if cands != nil {
		t.Fatalf("expected nil candidates when no embeddings exist, got %v", cands)
	}
}

func TestSemanticPruneCandidates_NonFindingVantagesIgnored(t *testing.T) {
	// Embeddings under non-finding vantage keys (region, forager) must
	// not produce prune candidates — vantageKeyToFindingID returns 0
	// for them and the helper skips.
	store := freshStore(t)
	prov := embed.NewStubProvider()
	regionVec, _ := prov.Embed(context.Background(), "region narrative")
	_ = store.CombEmbeddings().Upsert(&db.CombEmbeddingRow{
		VantageKey:  "d1=0",
		VantageKind: db.VantageRegion,
		Model:       prov.Name(),
		Dim:         len(regionVec),
		Embedding:   regionVec,
	})
	foragerVec, _ := prov.Embed(context.Background(), "region narrative") // same content
	_ = store.CombEmbeddings().Upsert(&db.CombEmbeddingRow{
		VantageKey:  "forager:optimist",
		VantageKind: db.VantageForager,
		Model:       prov.Name(),
		Dim:         len(foragerVec),
		Embedding:   foragerVec,
	})

	cands, err := semanticPruneCandidates(context.Background(), store, DefaultOptions())
	if err != nil {
		t.Fatalf("semanticPruneCandidates: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("expected 0 candidates from non-finding vantages, got %d: %v", len(cands), cands)
	}
}

// mustAddFindingAt adds a finding with an explicit label, text, and d1
// coordinate so the Independence tests can place findings in (or across)
// coordinate buckets.
func mustAddFindingAt(t *testing.T, store *db.Store, label, text string, d1 int) int64 {
	t.Helper()
	f := &db.Finding{Wave: 1, Agent: "test", MSSLabel: label, Finding: text, D1: intP(d1)}
	id, err := store.Findings().AddFinding(f)
	if err != nil {
		t.Fatalf("AddFinding(%s): %v", label, err)
	}
	return id
}

func TestSemanticPruneCandidates_OnlyAssumptions(t *testing.T) {
	// Two DEFINITIONS with identical embeddings at the same coordinate must
	// NOT be flagged — the Independence rule is about assumptions, so the
	// embedding arm filters mss_label='assumption' just like
	// mss.IndependenceAudit.
	store := freshStore(t)
	prov := embed.NewStubProvider()
	a := mustAddFindingAt(t, store, "definition", "shared token", 0)
	b := mustAddFindingAt(t, store, "definition", "shared token", 0)
	upsertFindingEmbedding(t, store, a, "shared token", prov)
	upsertFindingEmbedding(t, store, b, "shared token", prov)

	cands, err := semanticPruneCandidates(context.Background(), store, DefaultOptions())
	if err != nil {
		t.Fatalf("semanticPruneCandidates: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("definitions must not be redundant-assumption candidates, got %d: %v", len(cands), cands)
	}
}

func TestSemanticPruneCandidates_BoundedByCoordinate(t *testing.T) {
	// Two assumptions with identical embeddings but DIFFERENT d1
	// coordinates live in different buckets and must not be compared —
	// the WASP bounded-work promise the embedding arm inherits from
	// mss.IndependenceAudit's coordinate grouping.
	store := freshStore(t)
	prov := embed.NewStubProvider()
	a := mustAddFindingAt(t, store, "assumption", "shared token", 0)
	b := mustAddFindingAt(t, store, "assumption", "shared token", 1)
	upsertFindingEmbedding(t, store, a, "shared token", prov)
	upsertFindingEmbedding(t, store, b, "shared token", prov)

	cands, err := semanticPruneCandidates(context.Background(), store, DefaultOptions())
	if err != nil {
		t.Fatalf("semanticPruneCandidates: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("cross-coordinate assumptions must not be compared, got %d: %v", len(cands), cands)
	}
}

func TestSemanticPruneCandidates_PopulatesText(t *testing.T) {
	// A same-coordinate assumption pair is flagged AND carries the
	// finding text so an operator can review the candidate.
	store := freshStore(t)
	prov := embed.NewStubProvider()
	a := mustAddFindingAt(t, store, "assumption", "shared token", 0)
	b := mustAddFindingAt(t, store, "assumption", "shared token", 0)
	upsertFindingEmbedding(t, store, a, "shared token", prov)
	upsertFindingEmbedding(t, store, b, "shared token", prov)

	cands, err := semanticPruneCandidates(context.Background(), store, DefaultOptions())
	if err != nil {
		t.Fatalf("semanticPruneCandidates: %v", err)
	}
	if len(cands) == 0 {
		t.Fatalf("expected 1 same-coordinate assumption candidate, got 0")
	}
	if cands[0].FindingA == "" || cands[0].FindingB == "" {
		t.Errorf("candidate text not populated: %+v", cands[0])
	}
}

func TestVantageKeyToFindingID(t *testing.T) {
	cases := []struct {
		key  string
		want int64
	}{
		{"finding:42", 42},
		{"finding:0", 0}, // 0 is the sentinel "not a finding key"; round-trips to 0
		{"finding:1", 1},
		{"finding:abc", 0},
		{"region:42", 0},
		{"forager:42", 0},
		{"finding:", 0},
		{"", 0},
	}
	for _, c := range cases {
		if got := vantageKeyToFindingID(c.key); got != c.want {
			t.Errorf("vantageKeyToFindingID(%q) = %d, want %d", c.key, got, c.want)
		}
	}
}

func TestMergeCandidateSources_Unions(t *testing.T) {
	jaccard := []mss.RedundancyCandidate{
		{IDA: 1, IDB: 2, Similarity: 0.75},
	}
	embedding := []mss.RedundancyCandidate{
		{IDA: 2, IDB: 1, Similarity: 0.92}, // same pair, higher score
		{IDA: 3, IDB: 4, Similarity: 0.88}, // new pair
	}
	merged := mergeCandidateSources(jaccard, embedding)
	if len(merged) != 2 {
		t.Fatalf("expected 2 merged, got %d", len(merged))
	}
	if merged[0].Similarity != 0.92 {
		t.Errorf("expected max similarity 0.92 for shared pair, got %v", merged[0].Similarity)
	}
}
