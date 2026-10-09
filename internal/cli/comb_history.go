package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Chubby-Honey-Bee/hive/internal/calibration"
	"github.com/Chubby-Honey-Bee/hive/internal/comb"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/spf13/cobra"
)

// newCombAtCmd implements `chb comb at` — read what the Comb
// believed about a vantage at a given moment (or tick). Bounded probe;
// uses the (vantage_key, revision_at DESC) index.
//
//	chb comb at --vantage d1=0 --time 2026-04-12T12:00:00Z
//	chb comb at --vantage forager:optimist --tick 14
func newCombAtCmd() *cobra.Command {
	var (
		vantage string
		atTime  string
		tickID  int64
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "at",
		Short: "Read the Comb's belief about a vantage at a prior tick or timestamp (Time Wheel)",
		Long: `Bounded Time Wheel probe. Returns the revision of the vantage that
was current at the given moment (or tick). When both --time and --tick
are set, --tick wins; when neither is set, the call errors so a missed
flag doesn't silently return today's belief.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCombAt(vantage, atTime, tickID, jsonOut)
		},
	}
	cmd.Flags().StringVar(&vantage, "vantage", "", "vantage key (region prefix or forager:<name>)")
	cmd.Flags().StringVar(&atTime, "time", "", "moment in RFC3339 (e.g. 2026-04-12T12:00:00Z)")
	cmd.Flags().Int64Var(&tickID, "tick", 0, "Time Wheel tick id (alternative to --time)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON instead of formatted text")
	return cmd
}

// runCombAt prints the vantage's revision current at the tick, or at the
// time when no tick is given.
func runCombAt(vantage, atTime string, tickID int64, jsonOut bool) error {
	if err := checkCombAtFlags(vantage, atTime, tickID); err != nil {
		return err
	}
	if err := store.Init(); err != nil {
		return err
	}
	row, scores, err := combRevisionAt(vantage, atTime, tickID)
	if err != nil {
		return err
	}
	return printCombRevisionAt(vantage, row, scores, jsonOut)
}

// checkCombAtFlags refuses a missing --vantage, and a call with neither
// --time nor --tick, so a missed flag does not silently return today's
// belief.
func checkCombAtFlags(vantage, atTime string, tickID int64) error {
	if vantage == "" {
		return fmt.Errorf("--vantage required (e.g. d1=0 or forager:optimist)")
	}
	if atTime == "" && tickID == 0 {
		return fmt.Errorf("either --time <RFC3339> or --tick <id> is required")
	}
	return nil
}

// combRevisionAt reads the vantage's revision, and the calibration scores,
// current at the tick when one is given, else at the RFC3339 time.
func combRevisionAt(vantage, atTime string, tickID int64) (*db.CombRevisionRow, []*db.ScoreRow, error) {
	if tickID != 0 {
		return combRevisionAtTick(vantage, tickID)
	}
	// Accept RFC3339 input; normalise to SQLite timestamp form.
	ts, err := time.Parse(time.RFC3339, atTime)
	if err != nil {
		return nil, nil, fmt.Errorf("--time: %w (use RFC3339, e.g. 2026-04-12T12:00:00Z)", err)
	}
	return combRevisionAtTime(vantage, ts.UTC().Format("2006-01-02 15:04:05"))
}

// combRevisionAtTick reads the vantage's revision and the scores current at
// the tick.
func combRevisionAtTick(vantage string, tickID int64) (*db.CombRevisionRow, []*db.ScoreRow, error) {
	row, err := store.CombRevisions().AtTick(vantage, tickID)
	if err != nil {
		return nil, nil, err
	}
	scores, err := store.Calibration().ScoresAtTick(tickID)
	return row, scores, err
}

// combRevisionAtTime reads the vantage's revision and the scores current at
// a SQLite timestamp.
func combRevisionAtTime(vantage, at string) (*db.CombRevisionRow, []*db.ScoreRow, error) {
	row, err := store.CombRevisions().At(vantage, at)
	if err != nil {
		return nil, nil, err
	}
	scores, err := store.Calibration().ScoresAtTime(at)
	return row, scores, err
}

// printCombRevisionAt prints the revision found, or says that none predates
// the requested moment.
func printCombRevisionAt(vantage string, row *db.CombRevisionRow, scores []*db.ScoreRow, jsonOut bool) error {
	if row == nil {
		fmt.Printf("no revision of %q predates the requested moment\n", vantage)
		return nil
	}
	return printCombRevision(row, calibration.ScoresFrom(scores), jsonOut)
}

// newCombDiffCmd implements `chb comb diff` — diff a vantage's
// belief between two moments / ticks. Surfaces confidence delta,
// label flips, and the narrative diff.
func newCombDiffCmd() *cobra.Command {
	var (
		vantage  string
		fromTime string
		toTime   string
		jsonOut  bool
	)
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Diff the Comb's belief about a vantage between two moments (Time Wheel)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCombDiff(vantage, fromTime, toTime, jsonOut)
		},
	}
	cmd.Flags().StringVar(&vantage, "vantage", "", "vantage key (region prefix or forager:<name>)")
	cmd.Flags().StringVar(&fromTime, "from", "", "earlier moment (RFC3339)")
	cmd.Flags().StringVar(&toTime, "to", "", "later moment (RFC3339)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON instead of formatted text")
	return cmd
}

// runCombDiff prints how the vantage's belief changed between the two
// moments.
func runCombDiff(vantage, fromTime, toTime string, jsonOut bool) error {
	if combDiffFlagsMissing(vantage, fromTime, toTime) {
		return fmt.Errorf("--vantage, --from, --to all required (RFC3339 timestamps)")
	}
	if err := store.Init(); err != nil {
		return err
	}
	base, cur, err := combRevisionsBetween(vantage, fromTime, toTime)
	if err != nil {
		return err
	}
	return printCombDiff(computeCombDiff(vantage, base, cur), jsonOut)
}

// combDiffFlagsMissing reports whether --vantage, --from or --to is unset.
func combDiffFlagsMissing(vantage, fromTime, toTime string) bool {
	return vantage == "" || fromTime == "" || toTime == ""
}

// combRevisionsBetween reads the vantage's revisions current at the two
// RFC3339 moments.
func combRevisionsBetween(vantage, fromTime, toTime string) (*db.CombRevisionRow, *db.CombRevisionRow, error) {
	from, err := time.Parse(time.RFC3339, fromTime)
	if err != nil {
		return nil, nil, fmt.Errorf("--from: %w", err)
	}
	to, err := time.Parse(time.RFC3339, toTime)
	if err != nil {
		return nil, nil, fmt.Errorf("--to: %w", err)
	}
	return store.CombRevisions().Diff(
		vantage,
		from.UTC().Format("2006-01-02 15:04:05"),
		to.UTC().Format("2006-01-02 15:04:05"),
	)
}

// printCombDiff prints the diff as JSON or as text.
func printCombDiff(d *combDiff, jsonOut bool) error {
	if jsonOut {
		return jsonPrint(d)
	}
	renderCombDiff(d)
	return nil
}

// newCombHistoryCmd implements `chb comb history` — list the full
// revision arc for a vantage, most-recent-first. The Timekeeper
// reads this to find inflection points.
func newCombHistoryCmd() *cobra.Command {
	var (
		vantage string
		limit   int
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "history",
		Short: "List every revision of a vantage, newest first (Time Wheel)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCombHistory(vantage, limit, jsonOut)
		},
	}
	cmd.Flags().StringVar(&vantage, "vantage", "", "vantage key")
	cmd.Flags().IntVar(&limit, "limit", 50, "max revisions to return")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

// runCombHistory prints the vantage's revisions, newest first.
func runCombHistory(vantage string, limit int, jsonOut bool) error {
	if vantage == "" {
		return fmt.Errorf("--vantage required")
	}
	if err := store.Init(); err != nil {
		return err
	}
	rows, err := store.CombRevisions().History(vantage, limit)
	if err != nil {
		return err
	}
	return printCombHistory(vantage, rows, jsonOut)
}

// printCombHistory prints the revisions as JSON or one line each.
func printCombHistory(vantage string, rows []*db.CombRevisionRow, jsonOut bool) error {
	if jsonOut {
		return jsonPrint(rows)
	}
	fmt.Printf("history %s — %d revisions\n", vantage, len(rows))
	for _, r := range rows {
		printRevisionLine(r)
	}
	return nil
}

// newCombWheelCmd implements `chb comb wheel` — list the Time
// Wheel's recent ticks. The Timekeeper's substrate.
func newCombWheelCmd() *cobra.Command {
	var (
		kind    string
		limit   int
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "wheel",
		Short: "List Time Wheel ticks",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCombWheel(kind, limit, jsonOut)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "filter by tick kind (swarm|wave|session|day|manual|ripen|calibrate)")
	cmd.Flags().IntVar(&limit, "limit", 50, "max ticks to return")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

// runCombWheel prints the Time Wheel's most recent ticks, of one kind when
// kind is set.
func runCombWheel(kind string, limit int, jsonOut bool) error {
	if err := store.Init(); err != nil {
		return err
	}
	if err := flagOneOf("--kind", kind, "swarm", "wave", "session", "day", "manual", "ripen", "calibrate"); err != nil {
		return err
	}
	ticks, err := store.TimeWheel().Recent(db.TickKind(kind), limit)
	if err != nil {
		return err
	}
	return printCombWheel(ticks, jsonOut)
}

// printCombWheel prints the ticks as JSON or one line each.
func printCombWheel(ticks []*db.TickRow, jsonOut bool) error {
	if jsonOut {
		return jsonPrint(ticks)
	}
	fmt.Printf("Time Wheel — %d ticks\n", len(ticks))
	for _, t := range ticks {
		ended := "open"
		if t.EndedAt.Valid {
			ended = t.EndedAt.String
		}
		fmt.Printf("  #%-4d %-8s %-25s %s → %s\n",
			t.ID, t.Kind, t.Label, t.StartedAt, ended)
	}
	return nil
}

// combDiff is the structured representation of a vantage diff between
// two moments — what changed, what stayed, and the narrative deltas.
type combDiff struct {
	Vantage         string `json:"vantage"`
	BaselineAt      string `json:"baseline_at,omitempty"`
	CurrentAt       string `json:"current_at,omitempty"`
	BaselineRev     int64  `json:"baseline_revision_id,omitempty"`
	CurrentRev      int64  `json:"current_revision_id,omitempty"`
	ConfidenceDelta int    `json:"confidence_delta"`
	LabelFrom       string `json:"label_from,omitempty"`
	LabelTo         string `json:"label_to,omitempty"`
	ContestedFrom   bool   `json:"contested_from"`
	ContestedTo     bool   `json:"contested_to"`
	NarrativeFrom   string `json:"narrative_from,omitempty"`
	NarrativeTo     string `json:"narrative_to,omitempty"`
}

func computeCombDiff(vantage string, base, cur *db.CombRevisionRow) *combDiff {
	d := &combDiff{Vantage: vantage}
	d.setBaseline(base)
	d.setCurrent(cur)
	d.ConfidenceDelta = combConfidenceDelta(base, cur)
	return d
}

// setBaseline records the baseline revision, when there is one.
func (d *combDiff) setBaseline(base *db.CombRevisionRow) {
	if base == nil {
		return
	}
	d.BaselineRev = base.ID
	d.BaselineAt = base.RevisionAt
	d.NarrativeFrom = base.Narrative
	d.ContestedFrom = base.Contested
	if base.DominantLabel.Valid {
		d.LabelFrom = base.DominantLabel.String
	}
}

// setCurrent records the current revision, when there is one.
func (d *combDiff) setCurrent(cur *db.CombRevisionRow) {
	if cur == nil {
		return
	}
	d.CurrentRev = cur.ID
	d.CurrentAt = cur.RevisionAt
	d.NarrativeTo = cur.Narrative
	d.ContestedTo = cur.Contested
	if cur.DominantLabel.Valid {
		d.LabelTo = cur.DominantLabel.String
	}
}

// combConfidenceDelta is the current confidence less the baseline's: the
// current confidence itself with no baseline, and 0 with no current
// revision.
func combConfidenceDelta(base, cur *db.CombRevisionRow) int {
	switch {
	case base != nil && cur != nil:
		return cur.Confidence - base.Confidence
	case cur != nil:
		return cur.Confidence
	}
	return 0
}

func renderCombDiff(d *combDiff) {
	fmt.Printf("comb diff %s\n", d.Vantage)
	fmt.Printf("  baseline: %s (rev #%d)\n", d.BaselineAt, d.BaselineRev)
	fmt.Printf("  current : %s (rev #%d)\n", d.CurrentAt, d.CurrentRev)
	sign := "+"
	if d.ConfidenceDelta < 0 {
		sign = "-"
	}
	fmt.Printf("  confidence: %s%d%%\n", sign, abs(d.ConfidenceDelta))
	renderCombDiffChanges(d)
}

// renderCombDiffChanges prints what flipped between the revisions: the
// dominant label, the contested flag and the narrative.
func renderCombDiffChanges(d *combDiff) {
	if d.LabelFrom != d.LabelTo {
		fmt.Printf("  label flip: %s → %s\n", strDefault(d.LabelFrom, "—"), strDefault(d.LabelTo, "—"))
	}
	if d.ContestedFrom != d.ContestedTo {
		fmt.Printf("  contested: %v → %v\n", d.ContestedFrom, d.ContestedTo)
	}
	if d.NarrativeFrom != d.NarrativeTo {
		fmt.Printf("  narrative was: %s\n", strings.TrimSpace(d.NarrativeFrom))
		fmt.Printf("  narrative is : %s\n", strings.TrimSpace(d.NarrativeTo))
	}
}

// printCombRevision prints a revision with, when its dominant label had a
// calibrated score in the region's scopes under the scores current then,
// the calibrated confidence beside the stored one.
func printCombRevision(r *db.CombRevisionRow, scores calibration.Scores, jsonOut bool) error {
	out := struct {
		*db.CombRevisionRow
		CalibratedConfidence *int `json:"calibrated_confidence,omitempty"`
	}{CombRevisionRow: r, CalibratedConfidence: revisionCalibratedConfidence(r, scores)}
	if jsonOut {
		return jsonPrint(out)
	}
	fmt.Printf("revision #%d  vantage=%s  kind=%s  source=%s  at=%s\n",
		r.ID, r.VantageKey, r.VantageKind, r.Source, r.RevisionAt)
	fmt.Printf("confidence=%d%% contested=%v evidence=%d open=%d\n",
		r.Confidence, r.Contested, r.EvidenceCount, r.OpenQuestionsCount)
	if out.CalibratedConfidence != nil {
		fmt.Printf("calibrated_confidence=%d%% (%s)\n", *out.CalibratedConfidence, calibration.CorrelationalNote)
	}
	fmt.Printf("narrative: %s\n", strings.TrimSpace(r.Narrative))
	return nil
}

// revisionCalibratedConfidence is a region revision's confidence calibrated
// by its dominant label's score in the region's scopes; nil for a forager
// vantage, a revision with no dominant label, or a label with no calibrated
// score there.
func revisionCalibratedConfidence(r *db.CombRevisionRow, scores calibration.Scores) *int {
	if !r.DominantLabel.Valid || comb.IsForagerVantage(r.VantageKey) {
		return nil
	}
	coords, err := comb.ParseRegionKey(r.VantageKey)
	if err != nil {
		return nil
	}
	d := coords.AsNullCoords()
	return labelCalibratedConfidence(scores, r.Confidence, r.DominantLabel.String, d[0], d[1])
}

func printRevisionLine(r *db.CombRevisionRow) {
	dom := "—"
	if r.DominantLabel.Valid {
		dom = r.DominantLabel.String
	}
	fmt.Printf("  #%-5d %s  conf=%-3d  dom=%-10s  src=%-22s  %s\n",
		r.ID, r.RevisionAt, r.Confidence, dom, truncFor(r.Source, 22),
		truncFor(strings.TrimSpace(r.Narrative), 60))
}

func jsonPrint(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func strDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// truncFor cuts s to fewer than n bytes plus "…". The cut backs off to a
// rune boundary (clip), so a cut through a multi-byte rune, such as the em
// dashes queen output is full of, prints no invalid UTF-8.
func truncFor(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return clip(s, n-1) + "…"
}

// clip cuts s to at most n bytes, backing off to a rune boundary so the cut
// splits no multi-byte character; s of at most n bytes is returned whole.
// Every cut chb makes in text it prints goes through it.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
