package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/comb"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
	"github.com/spf13/cobra"
)

// newSwarmGapsCmd implements `chb gaps` — the
// perspective-as-dimension WASP move applied to the swarm. Walks the
// cartesian product of (subject coordinates) × (registered forager
// perspectives) and surfaces the cells with zero coverage — i.e. "this
// forager has not weighed in on this coordinate region."
//
// Output is sorted by an information-gain heuristic: high-evidence
// regions where a missing forager would change the picture come first.
//
// This is a bounded probe per the WASP guarantee — we don't scan
// findings; we read comb_state (already CDE-indexed) + the foragers
// directory + the forager-vantage rows that exist on the Comb.
func newSwarmGapsCmd() *cobra.Command {
	var (
		jsonOut bool
		limit   int
		topD1   bool
	)
	cmd := &cobra.Command{
		Use:   "gaps",
		Short: "Surface (subject coordinate × forager perspective) cells with zero coverage",
		Long: `Walks the cartesian product of subject-matter regions (currently
populated on the comb) and registered forager perspectives. Cells with
zero coverage are the hive's literal blind spots — the highest-
leverage next dispatches.

Bounded probe: reads comb_state (CDE-indexed) + foragers/<*>.md + the
forager:<name> vantages already present. Never scans findings.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSwarmGaps(jsonOut, limit, topD1)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON instead of text")
	cmd.Flags().IntVar(&limit, "limit", 50, "max gaps to report")
	cmd.Flags().BoolVar(&topD1, "top-d1-only", false, "restrict the walk to the top d1=N regions (fast preview)")
	return cmd
}

// runSwarmGaps prints the uncovered (region × lens) cells, at most limit of
// them when limit is above 0.
func runSwarmGaps(jsonOut bool, limit int, topD1 bool) error {
	if err := store.Init(); err != nil {
		return err
	}
	lens, err := loadLensForagers()
	if err != nil {
		return err
	}
	gaps, err := computePerspectiveGaps(store, lens, topD1)
	if err != nil {
		return err
	}
	printPerspectiveGaps(capPerspectiveGaps(gaps, limit), jsonOut)
	return nil
}

// loadLensForagers loads the forager tree and keeps the lenses.
func loadLensForagers() ([]foragers.Forager, error) {
	all, err := loadForagers()
	if err != nil {
		return nil, err
	}
	lensOnly := []foragers.Forager{}
	for _, w := range all {
		if w.IsLens() {
			lensOnly = append(lensOnly, w)
		}
	}
	return lensOnly, nil
}

// capPerspectiveGaps keeps the first limit gaps when limit is above 0.
func capPerspectiveGaps(gaps []PerspectiveGap, limit int) []PerspectiveGap {
	if limit > 0 && len(gaps) > limit {
		return gaps[:limit]
	}
	return gaps
}

// printPerspectiveGaps prints the gaps as JSON or one line each.
func printPerspectiveGaps(gaps []PerspectiveGap, jsonOut bool) {
	if jsonOut {
		b, _ := json.MarshalIndent(gaps, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Printf("Swarm perspective gaps — %d cells with zero coverage\n", len(gaps))
	for _, g := range gaps {
		fmt.Printf("  %-30s × %-15s   evidence=%d  contested=%v\n",
			g.RegionKey, g.Forager, g.RegionEvidence, g.RegionContested)
	}
}

// PerspectiveGap is one (region × forager) cell with zero coverage.
// Sorted by information-gain heuristic so the most useful next
// dispatches surface first.
type PerspectiveGap struct {
	RegionKey       string `json:"region_key"`
	Forager         string `json:"forager"`
	RegionEvidence  int    `json:"region_evidence"`  // findings count under this region
	RegionContested bool   `json:"region_contested"` // is the region marked contested?
	Score           int    `json:"score"`            // information-gain heuristic (higher = more leverage)
}

// computePerspectiveGaps does the cartesian walk. Bounded — uses the
// comb_state index to enumerate populated regions (instead of scanning
// findings) and reads the per-forager vantage rows directly to detect
// missing coverage.
func computePerspectiveGaps(s *db.Store, lens []foragers.Forager, topD1 bool) ([]PerspectiveGap, error) {
	regions, err := gapRegions(s, topD1)
	if err != nil {
		return nil, err
	}

	// Build the present-pairs lookup: which (region, forager) cells
	// already have a finding.
	covered, err := buildCoveragePairs(s, lens)
	if err != nil {
		return nil, fmt.Errorf("build coverage pairs: %w", err)
	}

	out := make([]PerspectiveGap, 0, len(regions)*len(lens)/2)
	for _, r := range regions {
		out = appendRegionGaps(out, r, lens, covered)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	return out, nil
}

// gapRegions enumerates the populated regions from comb_state — already-built
// digest rows are exactly the regions where the swarm has any presence —
// keeping only the top d1=N regions under topD1.
func gapRegions(s *db.Store, topD1 bool) ([]*db.CombRow, error) {
	regions, err := s.Comb().ListKind(db.VantageRegion)
	if err != nil {
		return nil, fmt.Errorf("list region vantages: %w", err)
	}
	if topD1 {
		regions = filterTopD1(regions)
	}
	return regions, nil
}

// appendRegionGaps appends a gap for every lens with no finding in region r.
func appendRegionGaps(out []PerspectiveGap, r *db.CombRow, lens []foragers.Forager, covered map[string]struct{}) []PerspectiveGap {
	// Score depends only on the region — cache once instead of
	// recomputing for every forager at the same coordinate.
	score := gapScore(r)
	for _, w := range lens {
		if _, ok := covered[r.VantageKey+"|"+w.Name]; ok {
			continue
		}
		out = append(out, PerspectiveGap{
			RegionKey:       r.VantageKey,
			Forager:         w.Name,
			RegionEvidence:  r.EvidenceCount,
			RegionContested: r.Contested,
			Score:           score,
		})
	}
	return out
}

// gapScore weights regions by evidence (more evidence = more
// established context, so a missing perspective is more anomalous) and
// contestedness (contested regions especially benefit from a missing
// forager's lens).
func gapScore(r *db.CombRow) int {
	score := r.EvidenceCount
	if r.Contested {
		score += 5
	}
	if r.OpenQuestionsCount > 0 {
		score += r.OpenQuestionsCount
	}
	return score
}

// buildCoveragePairs returns a set keyed by "<region_key>|<forager>"
// for every (region prefix, forager) pair with at least one finding
// written by that forager inside that prefix. Bounded probe via the
// CDE indexes on findings.
//
// "Forager wrote a finding inside region R" is taken to mean: the
// finding's d1..d8 coordinates, when truncated to the same prefix
// length as R, match R. Each finding contributes coverage to every
// prefix of its coordinate tuple (the same Prefixes walk the Comb
// uses), so a finding at d1=0;d2=3 covers the forager for `""`,
// `d1=0`, and `d1=0;d2=3`.
func buildCoveragePairs(s *db.Store, lens []foragers.Forager) (map[string]struct{}, error) {
	if len(lens) == 0 {
		return map[string]struct{}{}, nil
	}
	args := lensAgentArgs(lens)
	placeholders := strings.Repeat("?,", len(args))
	placeholders = placeholders[:len(placeholders)-1] // trim trailing comma

	q := `
		SELECT DISTINCT d1, d2, d3, d4, d5, d6, d7, d8, agent
		FROM findings
		WHERE agent IN (` + placeholders + `)`

	rows, err := s.ReadConn().Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCoveragePairs(rows)
}

// lensAgentArgs are the agent names the lenses write findings under: a
// single union of the bare-name + forager-prefixed forms in one IN list;
// one parser pass, one index probe.
func lensAgentArgs(lens []foragers.Forager) []any {
	args := make([]any, 0, len(lens)*2)
	for _, w := range lens {
		args = append(args, w.Name)
	}
	for _, w := range lens {
		args = append(args, "forager-"+w.Name)
	}
	return args
}

// scanCoveragePairs reads each (coordinates, agent) row into the covered
// set.
func scanCoveragePairs(rows *sql.Rows) (map[string]struct{}, error) {
	covered := map[string]struct{}{}
	for rows.Next() {
		var (
			ds    [8]sql.NullInt64
			agent string
		)
		if err := rows.Scan(&ds[0], &ds[1], &ds[2], &ds[3], &ds[4], &ds[5], &ds[6], &ds[7], &agent); err != nil {
			return nil, err
		}
		markCoveredPrefixes(covered, ds, strings.TrimPrefix(agent, "forager-"))
	}
	return covered, rows.Err()
}

// markCoveredPrefixes builds the canonical region key from the finding's
// coords, then marks every prefix as covered for the forager (same prefix
// walk the Comb uses for region vantages).
func markCoveredPrefixes(covered map[string]struct{}, ds [8]sql.NullInt64, name string) {
	coords := combCoordsFromFinding(ds)
	for _, p := range comb.Prefixes(coords) {
		covered[comb.RegionKey(p)+"|"+name] = struct{}{}
	}
}

// combCoordsFromFinding builds an comb.Coords from the eight nullable
// d1..d8 columns, as the Comb's region discovery does. NULL columns are
// simply absent from the map (the finding is unpinned on that dimension).
func combCoordsFromFinding(ds [8]sql.NullInt64) comb.Coords {
	out := comb.Coords{}
	for i, n := range ds {
		if n.Valid {
			out[fmt.Sprintf("d%d", i+1)] = int(n.Int64)
		}
	}
	return out
}

// filterTopD1 keeps only the top-level d1=N region rows (fast preview).
func filterTopD1(rows []*db.CombRow) []*db.CombRow {
	out := rows[:0]
	for _, r := range rows {
		if isTopD1Region(r) {
			out = append(out, r)
		}
	}
	return out
}

// isTopD1Region reports whether a region pins d1 and none of d2..d4.
func isTopD1Region(r *db.CombRow) bool {
	return r.D1.Valid && !r.D2.Valid && !r.D3.Valid && !r.D4.Valid
}
