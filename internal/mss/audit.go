// Audit-time MSS criterion detectors that complement the write-time
// enforcement in invariants.go. The schema CHECK already enforces
// Partition's structural form (every label is one of the four); these
// detectors add the criteria it leaves unenforced:
//
//   - PartitionAudit confirms every row has a recognized label and
//     reports any rows that slipped through (e.g., from external SQLite
//     access bypassing CHECK, schema drift, or future migrations).
//   - IndependenceAudit detects assumption findings whose finding text
//     is suspiciously near-duplicate of another assumption — the most
//     common "redundant assumption" form. This is necessarily a heuristic;
//     true semantic independence is a research-grade problem. The output
//     is a candidate list, not a hard violation.
//
// These are separate from the MSSAudit query in internal/db/mss_audit.go, so
// the mss package owns its own algorithms and callers can run individual
// detectors without paying for the others.

package mss

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// PartitionViolation is a single row whose mss_label is unrecognized.
type PartitionViolation struct {
	FindingID   int64
	Label       string
	FindingText string
}

// PartitionAudit returns every row whose mss_label is not one of the
// four canonical labels. On a healthy DB this returns an empty slice
// (the schema CHECK should make it impossible to insert otherwise),
// so any non-empty result is a real corruption signal.
func PartitionAudit(db *sql.DB) ([]PartitionViolation, error) {
	rows, err := db.Query(`
		SELECT id, COALESCE(mss_label, ''), finding
		FROM findings
		WHERE mss_label NOT IN ('definition','guarantee','assumption','unknown')
		   OR mss_label IS NULL
	`)
	if err != nil {
		return nil, fmt.Errorf("partition audit query: %w", err)
	}
	defer rows.Close()
	return scanPartitionViolations(rows)
}

// scanPartitionViolations reads each (id, label, finding) row of rows.
func scanPartitionViolations(rows *sql.Rows) ([]PartitionViolation, error) {
	var out []PartitionViolation
	for rows.Next() {
		var v PartitionViolation
		if err := rows.Scan(&v.FindingID, &v.Label, &v.FindingText); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, v)
	}
	// A partial read would report fewer partition violations than exist —
	// an audit that looks cleaner than the data.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("partition audit did not complete: %w", err)
	}
	return out, nil
}

// RedundancyCandidate is a pair of assumption findings whose finding
// text overlaps enough that they may encode the same bet. Pairs are
// always reported with the lower ID first.
type RedundancyCandidate struct {
	IDA, IDB   int64
	Similarity float64 // Jaccard over content words, 0..1
	FindingA   string
	FindingB   string
}

// IndependenceAudit finds candidate redundant-assumption pairs. Returns
// pairs whose content-word Jaccard similarity exceeds the threshold.
//
// This is the always-on, zero-dependency tier of a two-tier Independence
// surface: Jaccard here, plus an opt-in embedding-cosine arm in
// internal/dreamer/prune_semantic.go that runs over the SAME
// assumption-only, coordinate-bucketed shape once finding embeddings
// exist. Both are heuristics — true semantic independence requires
// logical inference. Pairs reported here should be reviewed; many will be
// genuinely independent assumptions that happen to share vocabulary
// (e.g. two assumptions about "shipping cost" that mean different
// things). Only this Jaccard tier feeds the gate's mss_audit.
//
// Bounded work: skips coordinate groups with fewer than 2 assumptions
// before computing any similarities.
func IndependenceAudit(db *sql.DB, threshold float64) ([]RedundancyCandidate, error) {
	all, err := readAssumptions(db)
	if err != nil {
		return nil, err
	}
	// Group by (d1..d5); only compare within group. This is the
	// "bounded work" promise — same coordinate ⇒ candidate for being
	// the same bet.
	groups := make(map[bucketKey][]assumptionRow)
	for _, r := range all {
		k := bucketKey{r.d1, r.d2, r.d3, r.d4, r.d5}
		groups[k] = append(groups[k], r)
	}
	var out []RedundancyCandidate
	for _, group := range groups {
		out = appendRedundant(out, group, threshold)
	}
	return out, nil
}

// assumptionRow is one assumption finding with its d1..d5 coordinate, an
// absent axis read as -1.
type assumptionRow struct {
	id                 int64
	text               string
	d1, d2, d3, d4, d5 int64
}

// bucketKey is the d1..d5 coordinate assumptions are grouped by.
type bucketKey struct{ a, b, c, d, e int64 }

// readAssumptions reads every assumption finding in coordinate order.
func readAssumptions(db *sql.DB) ([]assumptionRow, error) {
	rows, err := db.Query(`
		SELECT id, finding, COALESCE(d1, -1), COALESCE(d2, -1), COALESCE(d3, -1), COALESCE(d4, -1), COALESCE(d5, -1)
		FROM findings WHERE mss_label = 'assumption'
		ORDER BY d1, d2, d3, d4, d5, id
	`)
	if err != nil {
		return nil, fmt.Errorf("independence audit query: %w", err)
	}
	defer rows.Close()
	return scanAssumptions(rows)
}

// scanAssumptions reads each assumption row of rows.
func scanAssumptions(rows *sql.Rows) ([]assumptionRow, error) {
	var all []assumptionRow
	for rows.Next() {
		var r assumptionRow
		if err := rows.Scan(&r.id, &r.text, &r.d1, &r.d2, &r.d3, &r.d4, &r.d5); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return all, nil
}

// appendRedundant appends to out each pair in group whose content
// similarity reaches threshold. A group of fewer than 2 has no pair, so no
// similarity is computed for it.
func appendRedundant(out []RedundancyCandidate, group []assumptionRow, threshold float64) []RedundancyCandidate {
	for i := 0; i < len(group); i++ {
		for j := i + 1; j < len(group); j++ {
			if sim := contentSimilarity(group[i].text, group[j].text); sim >= threshold {
				out = append(out, RedundancyCandidate{
					IDA:        group[i].id,
					IDB:        group[j].id,
					Similarity: sim,
					FindingA:   group[i].text,
					FindingB:   group[j].text,
				})
			}
		}
	}
	return out
}

var nonAlphaRe = regexp.MustCompile(`[^a-z0-9 ]`)

// contentSimilarity is a Jaccard score over content words (length > 3,
// lowercased, punctuation stripped). Matches the heuristic used in
// internal/gate/conflict.go's contextsMatch but is duplicated here
// to avoid a dependency cycle and to keep this package self-contained.
func contentSimilarity(a, b string) float64 {
	wa := contentTokens(a)
	wb := contentTokens(b)
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	inter := sharedWords(wa, wb)
	return float64(inter) / float64(len(wa)+len(wb)-inter)
}

// sharedWords counts the words in both wa and wb.
func sharedWords(wa, wb map[string]bool) int {
	n := 0
	for w := range wa {
		if wb[w] {
			n++
		}
	}
	return n
}

func contentTokens(s string) map[string]bool {
	clean := nonAlphaRe.ReplaceAllString(strings.ToLower(s), " ")
	out := make(map[string]bool)
	for _, w := range strings.Fields(clean) {
		if len(w) > 3 {
			out[w] = true
		}
	}
	return out
}
