package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/calibration"
	"github.com/Chubby-Honey-Bee/hive/internal/comb"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/spf13/cobra"
)

// newCombCmd returns the `chb comb` parent command. The hive's
// persistent shared belief surface lives behind these subcommands.
func newCombCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "comb",
		Short: "Persistent per-coordinate-region belief digest (the hive's memory)",
		Long: `The comb is the hive's shared memory: a deterministic
digest of findings/conflicts/gaps under each coordinate prefix,
refreshed on demand and queryable by every agent.

Subcommands:
  refresh    Recompute every region from current findings
  query      Get the digest for a specific region (most-specific-fallback)
  synthesize Render the per-region digest as a markdown synthesis doc
  status     Show last refresh timestamp + counts (contested, stale, capped cells)`,
	}
	cmd.AddCommand(
		newCombRefreshCmd(),
		newCombQueryCmd(),
		newCombSynthesizeCmd(),
		newCombStatusCmd(),
		// Time Wheel surface (comb_history.go) — at/diff/history/wheel.
		newCombAtCmd(),
		newCombDiffCmd(),
		newCombHistoryCmd(),
		newCombWheelCmd(),
		// Embedding sidecar (comb_embed.go) — embed/embed-status/similar.
		newCombEmbedCmd(),
		newCombEmbedStatusCmd(),
		newCombSimilarCmd(),
	)
	return cmd
}

func newCombRefreshCmd() *cobra.Command {
	var region string
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Recompute the belief digest for every region (or one region)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCombRefresh(cmd.Context(), region)
		},
	}
	cmd.Flags().StringVar(&region, "region", "", "specific region key (e.g., 'd1=0;d2=3')")
	return cmd
}

// runCombRefresh recomputes the digest of one region, or of every region
// when region is empty.
func runCombRefresh(ctx context.Context, region string) error {
	if err := store.Init(); err != nil {
		return err
	}
	if region != "" {
		return refreshCombRegion(ctx, region)
	}
	n, err := comb.BuildAllRegions(ctx, store)
	if err != nil {
		return err
	}
	fmt.Printf("comb refreshed: %d regions\n", n)
	return nil
}

// refreshCombRegion recomputes and writes one region's digest.
func refreshCombRegion(ctx context.Context, region string) error {
	coords, err := comb.ParseRegionKey(region)
	if err != nil {
		return err
	}
	d, err := comb.BuildDigest(store, coords)
	if err != nil {
		return err
	}
	// The writer the all-regions path uses, so the row is the one
	// the staleness rule compares with.
	if err := comb.WriteRegionVantage(ctx, store, d, "comb.refresh"); err != nil {
		return err
	}
	fmt.Printf("comb refreshed: 1 region (%s)\n", d.VantageKey)
	return nil
}

// combQueryDimNames are the coordinate flags chb comb query takes.
var combQueryDimNames = [4]string{"d1", "d2", "d3", "d4"}

// combQueryDims is chb comb query's coordinate flags: each dimension's value
// and whether it is set.
type combQueryDims struct {
	value [4]int
	set   [4]bool
}

// markChanged treats a dimension whose value flag was given as set.
func (q *combQueryDims) markChanged(cmd *cobra.Command) {
	for i, name := range combQueryDimNames {
		if cmd.Flags().Changed(name) {
			q.set[i] = true
		}
	}
}

// coords is the region the set dimensions name.
func (q *combQueryDims) coords() comb.Coords {
	coords := comb.Coords{}
	for i, name := range combQueryDimNames {
		if q.set[i] {
			coords[name] = q.value[i]
		}
	}
	return coords
}

func newCombQueryCmd() *cobra.Command {
	var (
		q        combQueryDims
		emitJSON bool
	)
	cmd := &cobra.Command{
		Use:   "query",
		Short: "Get the digest for a region (falls back to wider regions)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCombQuery(q.coords(), emitJSON)
		},
	}
	cmd.Flags().IntVar(&q.value[0], "d1", 0, "")
	cmd.Flags().IntVar(&q.value[1], "d2", 0, "")
	cmd.Flags().IntVar(&q.value[2], "d3", 0, "")
	cmd.Flags().IntVar(&q.value[3], "d4", 0, "")
	cmd.Flags().BoolVar(&emitJSON, "json", false, "emit JSON instead of plain text")
	cmd.Flags().BoolVar(&q.set[0], "set-d1", false, "interpret --d1 as set (otherwise omitted)")
	cmd.Flags().BoolVar(&q.set[1], "set-d2", false, "interpret --d2 as set")
	cmd.Flags().BoolVar(&q.set[2], "set-d3", false, "interpret --d3 as set")
	cmd.Flags().BoolVar(&q.set[3], "set-d4", false, "interpret --d4 as set")
	cmd.PreRun = func(cmd *cobra.Command, args []string) {
		// Auto-detect: if a flag was changed, treat that dim as set.
		q.markChanged(cmd)
	}
	return cmd
}

// runCombQuery prints the digest for the region coords names, falling back
// to wider regions.
func runCombQuery(coords comb.Coords, emitJSON bool) error {
	result, err := lookupWithFallback(store, coords)
	if err != nil {
		return err
	}
	if emitJSON {
		b, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	renderQueryHuman(result)
	return nil
}

func newCombSynthesizeCmd() *cobra.Command {
	var (
		wave int
		out  string
	)
	cmd := &cobra.Command{
		Use:   "synthesize",
		Short: "Render the Comb as a markdown synthesis document",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCombSynthesize(cmd.OutOrStdout(), wave, out)
		},
	}
	cmd.Flags().IntVar(&wave, "wave", 0, "wave number (header only)")
	cmd.Flags().StringVar(&out, "out", "", "output path; stdout when empty")
	return cmd
}

// runCombSynthesize renders the region digests as markdown to w, or writes
// them to out and says so on w.
func runCombSynthesize(w io.Writer, wave int, out string) error {
	rows, staleKeys, err := combRegionsWithStaleness()
	if err != nil {
		return err
	}
	md := renderSynthesisMarkdown(rows, staleKeys, wave)
	if out == "" {
		fmt.Fprint(w, md)
		return nil
	}
	if err := os.WriteFile(out, []byte(md), 0644); err != nil {
		return fmt.Errorf("write %s: %w", out, err)
	}
	fmt.Fprintf(w, "Comb synthesis written to %s (%d regions)\n", out, len(rows))
	return nil
}

// combRegionsWithStaleness lists the region vantages, and maps the key of
// each vantage to whether it is stale.
func combRegionsWithStaleness() ([]*db.CombRow, map[string]bool, error) {
	// Regions only: List returns forager vantages too, which are not
	// regions.
	rows, err := store.Comb().ListKind(db.VantageRegion)
	if err != nil {
		return nil, nil, err
	}
	staleKeys, err := comb.StaleVantages(store.ReadConn())
	if err != nil {
		return nil, nil, err
	}
	return rows, staleKeys, nil
}

func newCombStatusCmd() *cobra.Command {
	var emitJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show Comb summary (last refresh, contested, stale, capped cells)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCombStatus(emitJSON)
		},
	}
	cmd.Flags().BoolVar(&emitJSON, "json", false, "emit JSON")
	return cmd
}

// combStatus is what chb comb status reports.
type combStatus struct {
	vantages    int
	byKind      map[string]int
	contested   int
	stale       int
	lastRefresh string
	capped      []comb.Coords
	cappedCells int
}

// runCombStatus prints the Comb's summary.
func runCombStatus(emitJSON bool) error {
	if err := store.Init(); err != nil {
		return err
	}
	st, err := readCombStatus()
	if err != nil {
		return err
	}
	if emitJSON {
		st.printStatusJSON()
		return nil
	}
	st.printStatusLine()
	return nil
}

// readCombStatus counts the vantages by kind, the contested and stale ones
// and the capped cells, and reads the last refresh.
func readCombStatus() (combStatus, error) {
	rows, err := store.Comb().List()
	if err != nil {
		return combStatus{}, err
	}
	staleKeys, err := comb.StaleVantages(store.ReadConn())
	if err != nil {
		return combStatus{}, err
	}
	st := tallyCombVantages(rows, staleKeys)
	st.lastRefresh, _ = store.Comb().LastRefreshAt()
	if st.capped, err = comb.CappedCoords(store.ReadConn()); err != nil {
		return combStatus{}, err
	}
	st.cappedCells = comb.CappedCells(st.capped)
	return st, nil
}

// tallyCombVantages counts the vantages by kind and the contested and stale
// ones. List returns every vantage, of every kind, so the total counts
// vantages: a comb holding 9 forager vantages and 3 regions has 12 vantages
// and 3 regions.
func tallyCombVantages(rows []*db.CombRow, staleKeys map[string]bool) combStatus {
	st := combStatus{vantages: len(rows), byKind: map[string]int{}}
	for _, r := range rows {
		st.byKind[string(r.VantageKind)]++
		if r.Contested {
			st.contested++
		}
		if staleKeys[r.VantageKey] {
			st.stale++
		}
	}
	return st
}

// printStatusJSON prints the status as one JSON object.
func (st combStatus) printStatusJSON() {
	out := map[string]any{
		"vantages":        st.vantages,
		"by_kind":         st.byKind,
		"regions":         st.byKind[string(db.VantageRegion)],
		"contested":       st.contested,
		"stale":           st.stale,
		"capped_cells":    st.cappedCells,
		"capped_findings": len(st.capped),
		"last_refresh":    st.lastRefresh,
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
}

// printStatusLine prints the status as one line, the kinds in name order.
func (st combStatus) printStatusLine() {
	kinds := make([]string, 0, len(st.byKind))
	for k := range st.byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	var parts []string
	for _, k := range kinds {
		parts = append(parts, fmt.Sprintf("%s=%d", k, st.byKind[k]))
	}
	fmt.Printf("comb: vantages=%d (%s) contested=%d stale=%d capped_cells=%d capped_findings=%d last_refresh=%s\n",
		st.vantages, strings.Join(parts, " "), st.contested, st.stale, st.cappedCells, len(st.capped), displayTime(st.lastRefresh))
}

// queryResult is the shape returned by `comb query`.
type queryResult struct {
	Region     string `json:"region"`
	CoveredBy  string `json:"covered_by,omitempty"`
	Found      bool   `json:"found"`
	Confidence int    `json:"confidence"`
	// CalibratedConfidence is Confidence rescaled by the dominant label's
	// calibrated hit rate (comb.md § Calibrated confidence); absent when
	// the label has no calibrated score in the region's scopes.
	CalibratedConfidence *int           `json:"calibrated_confidence,omitempty"`
	Contested            bool           `json:"contested"`
	DominantLabel        string         `json:"dominant_label,omitempty"`
	EvidenceCount        int            `json:"evidence_count"`
	OpenQuestions        int            `json:"open_questions"`
	Narrative            string         `json:"narrative"`
	Staleness            comb.Staleness `json:"staleness"`
	Coords               map[string]int `json:"coords"`
}

func lookupWithFallback(store *db.Store, target comb.Coords) (*queryResult, error) {
	want := comb.RegionKey(target)
	for _, region := range comb.CoveringRegions(target) {
		row, err := store.Comb().Get(comb.RegionKey(region))
		if err != nil {
			return nil, err
		}
		if row != nil {
			return combQueryResult(store, want, region, row)
		}
	}
	// nothing found at all
	return &queryResult{
		Region:    want,
		Found:     false,
		Coords:    target,
		Staleness: comb.StaleMissing,
		Narrative: "(no digest available — run `chb comb refresh`)",
	}, nil
}

// combQueryResult is the query's answer from the digest of region, the
// narrowest region covering the target (want) that has one.
func combQueryResult(store *db.Store, want string, region comb.Coords, row *db.CombRow) (*queryResult, error) {
	stale, _ := comb.Classify(store, region)
	coordOut := map[string]int{}
	for k, v := range region {
		coordOut[k] = v
	}
	out := &queryResult{
		Region:        want,
		Found:         true,
		Confidence:    row.Confidence,
		Contested:     row.Contested,
		EvidenceCount: row.EvidenceCount,
		OpenQuestions: row.OpenQuestionsCount,
		Narrative:     row.Narrative,
		Staleness:     stale,
		Coords:        coordOut,
	}
	if err := applyCombDominantLabel(store, out, row); err != nil {
		return nil, err
	}
	if foundKey := comb.RegionKey(region); foundKey != want {
		out.CoveredBy = foundKey
	}
	return out, nil
}

// applyCombDominantLabel sets the result's dominant label, when the digest
// has one, and the confidence that label's calibrated score gives.
func applyCombDominantLabel(store *db.Store, out *queryResult, row *db.CombRow) error {
	if !row.DominantLabel.Valid {
		return nil
	}
	out.DominantLabel = row.DominantLabel.String
	scores, err := store.Calibration().ListScores(calibration.KindLabel, nil)
	if err != nil {
		return err
	}
	out.CalibratedConfidence = labelCalibratedConfidence(calibration.ScoresFrom(scores), row.Confidence, out.DominantLabel, row.D1, row.D2)
	return nil
}

// labelCalibratedConfidence is confidence rescaled by label's calibrated
// score in the scopes of (d1, d2); nil when the label has no calibrated
// score there.
func labelCalibratedConfidence(scores calibration.Scores, confidence int, label string, d1, d2 sql.NullInt64) *int {
	v, ok := calibration.CalibratedConfidence(confidence, label, scores.LabelScore(label, calibration.ScopesFor(d1, d2)))
	if !ok {
		return nil
	}
	return &v
}

func renderQueryHuman(r *queryResult) {
	fmt.Printf("region: %s\n", strDefault(r.Region, "global"))
	if !r.Found {
		fmt.Printf("status: %s\n", r.Staleness)
		fmt.Printf("note:   %s\n", r.Narrative)
		return
	}
	if r.CoveredBy != "" {
		fmt.Printf("covered_by: %s\n", r.CoveredBy)
	}
	fmt.Printf("staleness:  %s\n", r.Staleness)
	fmt.Printf("confidence: %d%%  contested=%v  evidence=%d  open=%d  dominant=%s\n",
		r.Confidence, r.Contested, r.EvidenceCount, r.OpenQuestions,
		strDefault(r.DominantLabel, "—"))
	if r.CalibratedConfidence != nil {
		fmt.Printf("calibrated_confidence: %d%%  (%s)\n", *r.CalibratedConfidence, calibration.CorrelationalNote)
	}
	fmt.Printf("\n%s\n", r.Narrative)
}

// renderSynthesisMarkdown renders the region digests as markdown, marking
// each stale or not by staleKeys.
func renderSynthesisMarkdown(rows []*db.CombRow, staleKeys map[string]bool, wave int) string {
	var b strings.Builder
	writeSynthesisTitle(&b, wave)
	if len(rows) == 0 {
		fmt.Fprint(&b, "_The hive has no digest yet. Run `chb comb refresh`._\n")
		return b.String()
	}
	// global first, then specific regions sorted
	global, specific := splitGlobalRegion(rows)
	if global != nil {
		fmt.Fprintf(&b, "## Whole comb\n\n%s\n\n", global.Narrative)
	}
	writeContestedRegions(&b, specific)
	writeRegionDigestTable(&b, specific, staleKeys)
	return b.String()
}

// writeSynthesisTitle writes the synthesis heading, naming the wave when
// one is given.
func writeSynthesisTitle(b *strings.Builder, wave int) {
	if wave > 0 {
		fmt.Fprintf(b, "# Comb Synthesis — Wave %d\n\n", wave)
		return
	}
	fmt.Fprint(b, "# Comb Synthesis\n\n")
}

// splitGlobalRegion separates the whole-comb digest, if any, from the
// specific regions, which it returns sorted by key.
func splitGlobalRegion(rows []*db.CombRow) (*db.CombRow, []*db.CombRow) {
	var global *db.CombRow
	specific := make([]*db.CombRow, 0, len(rows))
	for _, r := range rows {
		if r.VantageKey == "" {
			global = r
			continue
		}
		specific = append(specific, r)
	}
	sort.Slice(specific, func(i, j int) bool {
		return specific[i].VantageKey < specific[j].VantageKey
	})
	return global, specific
}

// writeContestedRegions lists the contested regions, nothing when there are
// none.
func writeContestedRegions(b *strings.Builder, specific []*db.CombRow) {
	contestedRows := contestedCombRows(specific)
	if len(contestedRows) == 0 {
		return
	}
	fmt.Fprint(b, "## Contested regions\n\n")
	for _, r := range contestedRows {
		fmt.Fprintf(b, "- **%s** — %s\n", r.VantageKey, r.Narrative)
	}
	fmt.Fprintln(b)
}

// contestedCombRows keeps the contested rows, in order.
func contestedCombRows(rows []*db.CombRow) []*db.CombRow {
	contested := []*db.CombRow{}
	for _, r := range rows {
		if r.Contested {
			contested = append(contested, r)
		}
	}
	return contested
}

// writeRegionDigestTable writes the per-region digest table, each region
// marked stale or not by staleKeys.
func writeRegionDigestTable(b *strings.Builder, specific []*db.CombRow, staleKeys map[string]bool) {
	fmt.Fprint(b, "## Per-region digest\n\n")
	fmt.Fprint(b, "| Region | Confidence | Evidence | Open | Dominant | Contested | Stale |\n")
	fmt.Fprint(b, "|--------|------------|----------|------|----------|-----------|-------|\n")
	for _, r := range specific {
		dom := "—"
		if r.DominantLabel.Valid {
			dom = r.DominantLabel.String
		}
		fmt.Fprintf(b, "| `%s` | %d%% | %d | %d | %s | %v | %v |\n",
			r.VantageKey, r.Confidence, r.EvidenceCount, r.OpenQuestionsCount,
			dom, r.Contested, staleKeys[r.VantageKey])
	}
}

func displayTime(t string) string {
	if t == "" {
		return "(never)"
	}
	if len(t) > 19 {
		return t[:19]
	}
	return t
}
