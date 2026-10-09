package comb

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// Digest is the in-memory shape of one comb_state row for a region
// vantage. Forager vantages skip this builder entirely — they're written
// directly from the runner using BuildForagerVantage.
type Digest struct {
	VantageKey         string // canonical region key for region vantages
	Coords             Coords
	Narrative          string
	Confidence         int
	Contested          bool
	DominantLabel      string
	EvidenceCount      int
	OpenQuestionsCount int
	DigestMethod       string
}

// BuildDigest computes the digest for one region from the live findings
// + conflicts + gaps + followups tables. The function is deterministic;
// re-running it on the same DB returns byte-equal results.
//
// Confidence formula (heuristic, doc'd here so future drift is obvious):
//
//	conflict_rate = unresolved_conflicts / findings
//	coverage_factor = 1 - (critical_gaps / (findings + critical_gaps + 1))
//	confidence = clamp(100 * (1 - conflict_rate) * coverage_factor, 0, 100)
//	                 for findings > 0, and 0 for a region with no findings
//
// Contested: an unresolved conflict touches the region. A conflict is the
// store's record of findings that contradict each other; the label mix is
// not. A region of honest assumptions is not in dispute.
//
// It reads the region's tally with regionTallies, so the staleness rule
// computes the digest a refresh would write with this code.
func BuildDigest(store *db.Store, region Coords) (*Digest, error) {
	ts, err := regionTallies(store.ReadConn(), region, []Coords{region})
	if err != nil {
		return nil, err
	}
	return ts[pinsOf(region, orderedDims())].digest(region), nil
}

func (t regionTally) digest(region Coords) *Digest {
	d := &Digest{
		VantageKey:         RegionKey(region),
		Coords:             region,
		DigestMethod:       "heuristic",
		EvidenceCount:      t.findings,
		OpenQuestionsCount: t.openGaps + t.followups,
		Confidence:         regionConfidence(t.findings, t.conflicts, t.criticalGaps),
		Contested:          t.conflicts > 0,
	}
	d.DominantLabel, _ = pickDominant(t.labels)
	d.Narrative = renderNarrative(d, t.conflicts, t.criticalGaps)
	return d
}

// regionConfidence is clamp(100 × (1 − c/f) × (1 − g/(f+g+1)), 0, 100),
// rounded down, for f findings, c unresolved conflicts and g critical gaps:
// 100·(f−c)·(f+1) / (f·(f+g+1)). A region with no findings has no belief to
// be confident in and scores 0. It is computed in integers: a float form
// lands just below exact integers, which int() truncates, so 5 findings with
// 4 conflicts would store 19 instead of 20.
func regionConfidence(f, c, g int) int {
	if f == 0 {
		return 0
	}
	conf := 100 * (f - c) * (f + 1) / (f * (f + g + 1))
	if conf < 0 {
		conf = 0
	}
	if conf > 100 {
		conf = 100
	}
	return conf
}

// BuildAllRegions walks the findings table to discover every region
// prefix that has at least one finding (plus a global empty-key row),
// builds a digest for each, and upserts. Returns the number of rows
// touched. Idempotent — re-running on an unchanged DB does the same
// row-for-row work but produces the same data, with `last_revised_at`
// bumped on every row.
//
// Each upsert also appends a comb_revisions row with source =
// "comb.refresh" and publishes EventVantageWritten, so the chronomantic
// history and the bus's subscribers both reflect every refresh. ctx is observed by
// the publish, so a cancelled caller stops emitting.
func BuildAllRegions(ctx context.Context, store *db.Store) (int, error) {
	regions, err := discoverRegions(store.ReadConn())
	if err != nil {
		return 0, err
	}
	touched := 0
	for _, r := range refreshWork(regions) {
		if err := writeRefresh(ctx, store, r); err != nil {
			return touched, err
		}
		touched++
	}
	return touched, nil
}

// refreshWork is the global empty-key region, then every prefix of regions,
// each once. The global region is always included so the comb has a
// top-level digest even before any coordinate-pinned findings.
func refreshWork(regions []Coords) []Coords {
	seen := map[string]bool{"": true}
	work := []Coords{{}}
	for _, r := range regions {
		for _, p := range Prefixes(r) {
			if k := RegionKey(p); !seen[k] {
				seen[k] = true
				work = append(work, p)
			}
		}
	}
	return work
}

// writeRefresh builds the digest of region r and writes it as a refresh.
func writeRefresh(ctx context.Context, store *db.Store, r Coords) error {
	d, err := BuildDigest(store, r)
	if err != nil {
		return fmt.Errorf("build digest for %q: %w", RegionKey(r), err)
	}
	return WriteRegionVantage(ctx, store, d, "comb.refresh")
}

// discoverRegions returns every distinct (d1..d8) tuple seen in findings,
// from which every region prefix that has at least one finding is derived.
func discoverRegions(read *sql.DB) ([]Coords, error) {
	return collectCoords(read, `SELECT DISTINCT d1, d2, d3, d4, d5, d6, d7, d8 FROM findings`)
}

// pickDominant returns the label with the highest count.
// Ties broken by epistemic *strength*, not lex order: definition >
// guarantee > assumption > unknown. So a tie between one definition and
// one assumption resolves to definition (the stronger label),
// reflecting the comb's bias toward firmly-grounded findings.
func pickDominant(counts map[string]int) (string, int) {
	bestKey, best := "", -1
	for _, k := range []string{"definition", "guarantee", "assumption", "unknown"} {
		if n, ok := counts[k]; ok && n > best {
			bestKey, best = k, n
		}
	}
	return bestKey, max(best, 0)
}

// renderNarrative builds the ≤512-char descriptive line that agents see
// when they consume {comb.<region>}.
func renderNarrative(d *Digest, unresolvedConflicts, criticalGaps int) string {
	regionLabel := cmp.Or(d.VantageKey, "global")
	if d.EvidenceCount == 0 {
		return clip(fmt.Sprintf("region=%s no findings yet — open question.", regionLabel))
	}
	s := fmt.Sprintf("region=%s evidence=%d dominant=%s confidence=%d%% open=%d",
		regionLabel, d.EvidenceCount, cmp.Or(d.DominantLabel, "—"), d.Confidence, d.OpenQuestionsCount)
	return clip(s + narrativeMarks(d.Contested, unresolvedConflicts, criticalGaps))
}

// narrativeMarks is the tail of a narrative: its unresolved conflicts and
// critical gaps when it has any, and the warning on a contested region.
func narrativeMarks(contested bool, unresolvedConflicts, criticalGaps int) string {
	var b strings.Builder
	if unresolvedConflicts > 0 {
		fmt.Fprintf(&b, " conflicts=%d", unresolvedConflicts)
	}
	if criticalGaps > 0 {
		fmt.Fprintf(&b, " critical_gaps=%d", criticalGaps)
	}
	if contested {
		b.WriteString(" — CONTESTED, treat findings here as provisional")
	}
	return b.String()
}

// clip is s cut to at most 512 bytes, backing off to a rune boundary so the
// cut splits no multi-byte character.
func clip(s string) string {
	if len(s) <= 512 {
		return s
	}
	n := 512
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func orderedDims() []string {
	return []string{"d1", "d2", "d3", "d4", "d5", "d6", "d7", "d8"}
}
