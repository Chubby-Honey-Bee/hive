package dreamer

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Chubby-Honey-Bee/hive/internal/comb"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/mss"
)

// passPrune surfaces near-duplicate assumption findings and emits one
// stop_signal per pair. Two candidate sources:
//
//  1. Jaccard similarity over content words (delegates to
//     mss.IndependenceAudit). Catches lexical overlap.
//  2. Embedding cosine similarity ≥ embeddingThreshold, when an
//     embedding model is registered for the workspace. Catches
//     paraphrase that Jaccard misses.
//
// Both sources contribute to the candidate list; pairs that fire on both are
// flagged with payload.method = "both" (the strongest signal). A pair
// already signalled is skipped. Dry-run counts the pairs it would signal;
// otherwise they are written, with or without --apply.
//
// Nothing is collapsed or deleted, --apply included: merging two findings is
// a judgement about which claim survives, not something a background
// consolidation loop should do unasked. The pass surfaces the pairs; a
// person or an agent decides.
func passPrune(ctx context.Context, store *db.Store, opts Options) (Result, error) {
	jaccardCands, err := mss.IndependenceAudit(store.ReadConn(), opts.JaccardMin)
	if err != nil {
		return Result{Status: "failed"}, fmt.Errorf("independence audit: %w", err)
	}

	// Optional second source: embedding cosine. The semanticPruneCandidates
	// helper is defined in prune_semantic.go so the prune pass can be
	// audited for the deterministic core in isolation. Returns zero
	// candidates when no embeddings exist for the workspace.
	semCands, semErr := semanticPruneCandidates(ctx, store, opts)
	if semErr != nil {
		// A failure here is non-fatal — Jaccard alone keeps the pass
		// useful. Surface via Notes so the operator sees we tried.
		semCands = nil
	}

	cands := mergeCandidateSources(jaccardCands, semCands)
	if len(cands) == 0 {
		return Result{
			Status: "completed",
			Notes: map[string]any{
				"candidates":    0,
				"threshold":     opts.JaccardMin,
				"jaccard":       len(jaccardCands),
				"embedding":     len(semCands),
				"embedding_err": errString(semErr),
			},
		}, nil
	}
	p := &pruner{
		store:        store,
		opts:         opts,
		candidates:   len(cands),
		jaccard:      len(jaccardCands),
		embedding:    len(semCands),
		embeddingErr: semErr,
	}
	return p.walk(ctx, cands)
}

// pruner is one prune pass over the candidate pairs: how many each source
// gave, and the pairs it has signalled so far.
type pruner struct {
	store        *db.Store
	opts         Options
	candidates   int
	jaccard      int
	embedding    int
	embeddingErr error
	emitted      int
	signalled    int
}

// walk visits the candidate pairs in turn while the pass may signal
// another, and reports the pass.
func (p *pruner) walk(ctx context.Context, cands []mss.RedundancyCandidate) (Result, error) {
	for _, c := range cands {
		ok, err := admit(ctx, p.emitted, p.opts.MaxPerPass)
		if !ok {
			return p.report(err)
		}
		if err := p.visit(c); err != nil {
			return Result{Status: "failed"}, err
		}
	}
	return p.report(nil)
}

// visit signals the pair c unless the run is dry or a stop_signal already
// stands for it. A standing stop_signal is not re-emitted: emitting the same
// pairs on every ripen would grow signals without bound and hand the hive
// the same redundancy each cadence. The check comes before the cap, so pairs
// beyond the first MaxPerPass are reached on later runs.
func (p *pruner) visit(c mss.RedundancyCandidate) error {
	done, err := pairAlreadySignalled(p.store.ReadConn(), c.IDA, c.IDB)
	if err != nil {
		return err
	}
	if done {
		p.signalled++
		return nil
	}
	if p.opts.DryRun {
		p.emitted++
		return nil
	}
	return p.signal(c)
}

// signal emits a stop_signal for the pair c at the older finding's
// coordinates, so downstream HIVE plans suppress further investigation
// there.
func (p *pruner) signal(c mss.RedundancyCandidate) error {
	coordsA, _ := readFindingCoords(p.store.ReadConn(), c.IDA)
	td1, td2, td3, td4 := coordsToInts(coordsA)
	if _, err := p.store.Signals().EmitSignal(
		"stop_signal",
		ptrString("audit"),
		&c.IDA,
		td1, td2, td3, td4,
		map[string]any{
			"reason":     "redundancy",
			"pair":       []int64{c.IDA, c.IDB},
			"similarity": c.Similarity,
			"source":     "dreamer.prune",
		},
		nil,
	); err != nil {
		return fmt.Errorf("emit stop_signal: %w", err)
	}
	p.emitted++
	return nil
}

// report is the pass's outcome once its walk stops: failed with the pairs
// already signalled when cut is the deadline's error, else complete.
func (p *pruner) report(cut error) (Result, error) {
	if cut != nil {
		return Result{Status: "failed", Touched: p.emitted}, cut
	}
	return Result{
		Status:  passStatus(p.opts),
		Touched: p.emitted,
		Notes: map[string]any{
			"candidates":    p.candidates,
			"emitted":       p.emitted,
			"signalled":     p.signalled,
			"threshold":     p.opts.JaccardMin,
			"applied":       p.opts.Apply,
			"jaccard":       p.jaccard,
			"embedding":     p.embedding,
			"embedding_err": errString(p.embeddingErr),
		},
	}, nil
}

// pairAlreadySignalled reports whether prune has emitted a stop_signal for
// this pair, in either order.
func pairAlreadySignalled(read *sql.DB, a, b int64) (bool, error) {
	var one int
	err := read.QueryRow(`
		SELECT 1 FROM signals
		WHERE signal_type = 'stop_signal' AND source_type = 'audit'
		  AND json_extract(payload_json, '$.source') = 'dreamer.prune'
		  AND ((source_id = ? AND json_extract(payload_json, '$.pair[1]') = ?)
		    OR (source_id = ? AND json_extract(payload_json, '$.pair[1]') = ?))
		LIMIT 1`, a, b, b, a).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func ptrString(s string) *string { return &s }

func coordsToInts(c comb.Coords) (d1, d2, d3, d4 *int) {
	return coordPtr(c, "d1"), coordPtr(c, "d2"), coordPtr(c, "d3"), coordPtr(c, "d4")
}

// coordPtr is c's value on axis dim, or nil when c leaves it absent.
func coordPtr(c comb.Coords, dim string) *int {
	if v, ok := c[dim]; ok {
		return ptrInt(v)
	}
	return nil
}

func ptrInt(v int) *int { return &v }
