package dreamer

import (
	"context"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/embed"
)

// The arm takes its model from the latest finding embedding, so a region
// embedding written later under another model does not hide the finding
// vectors or their candidate pair.
func TestSemanticPruneCandidates_ModelComesFromFindingEmbeddings(t *testing.T) {
	store := freshStore(t)
	prov := embed.NewStubProvider()
	a := mustAddFinding(t, store, "assumption", "fragmentation", nil)
	b := mustAddFinding(t, store, "assumption", "alpha beta gamma delta epsilon zeta", nil)
	upsertFindingEmbedding(t, store, a, "fragmentation", prov)
	upsertFindingEmbedding(t, store, b, "fragmentation", prov)
	if err := store.CombEmbeddings().Upsert(&db.CombEmbeddingRow{
		VantageKey: "d1=0", VantageKind: db.VantageRegion,
		Model: "other/v2", Dim: 3, Embedding: []float32{1, 0, 0},
	}); err != nil {
		t.Fatal(err)
	}
	// Written after the finding embeddings, as a later `comb embed` would be.
	if _, err := store.WriteDB.Exec(`UPDATE comb_embeddings SET created_at = datetime('now', '+1 minute') WHERE vantage_key = 'd1=0'`); err != nil {
		t.Fatal(err)
	}

	cands, err := semanticPruneCandidates(context.Background(), store, DefaultOptions())
	if err != nil {
		t.Fatalf("semanticPruneCandidates: %v", err)
	}
	if len(cands) != 1 || !((cands[0].IDA == a && cands[0].IDB == b) || (cands[0].IDA == b && cands[0].IDB == a)) {
		t.Fatalf("candidates %v, want the one pair (%d, %d) under the finding model", cands, a, b)
	}
}

// The cap counts pairs signalled, after skipping pairs already signalled, so
// later runs reach the pairs past the first MaxPerPass rather than
// recomputing the same first pairs and skipping them all.
func TestPrune_EmbeddingCapDoesNotStrandLaterPairs(t *testing.T) {
	store := freshStore(t)
	prov := embed.NewStubProvider()
	// Words of three letters or fewer carry no Jaccard tokens, so only the
	// embedding arm (identical text, cosine 1) sees these as duplicates.
	const text = "the cat sat on a mat"
	const n = 4
	for i := 0; i < n; i++ {
		upsertFindingEmbedding(t, store, mustAddFinding(t, store, "assumption", text, nil), text, prov)
	}
	pairs := n * (n - 1) / 2

	opts := DefaultOptions()
	opts.MaxPerPass = 2
	for run := 1; run <= 4; run++ {
		if _, err := passPrune(context.Background(), store, opts); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		want := min(pairs, run*opts.MaxPerPass)
		if got := countSignals(t, store, "stop_signal"); got != want {
			t.Fatalf("after run %d: %d stop_signals, want %d", run, got, want)
		}
	}
}
