package gate

import (
	"database/sql"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

var negationRe = regexp.MustCompile(`(?i)\bdoes\s+not\b|\bdo\s+not\b|\bcannot\b|\bcan't\b|\bno\s+longer\b|\bunavailable\b|\bdiscontinued\b|\bnot\s+available\b|\bnot\s+possible\b|\bnot\s+supported\b|\bisn't\b|\baren't\b|\bwon't\b|\bnever\b|\bno\s+evidence\b|\blacks?\b|\babsent\b`)

var multiplierRe = regexp.MustCompile(`(?i)\$\s*[\d,]+(?:\.\d{1,2})?\s*(billion|million|thousand|B|M|K)`)

var multipliers = map[string]float64{
	"billion": 1e9, "b": 1e9,
	"million": 1e6, "m": 1e6,
	"thousand": 1e3, "k": 1e3,
}

var contextDollarRe = regexp.MustCompile(`(?i)\$\s*([\d,]+(?:\.\d{1,2})?)\s*(?:billion|million|thousand|B|M|K)?`)

// ConflictReport holds the result of conflict detection.
type ConflictReport struct {
	Total    int            `json:"total"`
	New      int            `json:"new"` // not already recorded: written, or on a dry run, would be
	Numeric  int            `json:"numeric"`
	MSSLabel int            `json:"mss_label"`
	Negation int            `json:"negation"`
	Details  []ConflictItem `json:"details"`
}

// ConflictItem is a single detected conflict.
type ConflictItem struct {
	Coord       string `json:"coord"`
	FindingAID  int64  `json:"finding_a_id"`
	FindingBID  int64  `json:"finding_b_id"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

type findingRow struct {
	id                 int64
	wave               int
	agent              string
	finding            string
	mssLabel           string
	d1, d2, d3, d4, d5 *int64
}

// coordKey is a finding's (d1..d5) coordinate, an absent axis read as -1
// (deref), so findings group by the cell they sit in.
type coordKey struct{ d1, d2, d3, d4, d5 int64 }

// keyOf is the coordKey of a coordinate.
func keyOf(d1, d2, d3, d4, d5 *int64) coordKey {
	return coordKey{deref(d1), deref(d2), deref(d3), deref(d4), deref(d5)}
}

// String renders the coordinate as a conflict's coord: "d1,d2,d3,d4,d5".
func (k coordKey) String() string {
	return fmt.Sprintf("%d,%d,%d,%d,%d", k.d1, k.d2, k.d3, k.d4, k.d5)
}

// DetectConflicts finds structural conflicts between findings at the same CDE coordinates.
func DetectConflicts(store *db.Store, wave *int, dryRun bool) (*ConflictReport, error) {
	findings, err := loadConflictFindings(store, wave)
	if err != nil {
		return nil, err
	}
	scan := &conflictScan{store: store, dryRun: dryRun, report: &ConflictReport{}}
	for coord, group := range groupConflictFindings(findings) {
		if err := scan.scanCell(coord.String(), group); err != nil {
			return nil, err
		}
	}
	return scan.report, nil
}

// loadConflictFindings reads the findings DetectConflicts compares: the
// wave's, or every finding when wave is nil.
func loadConflictFindings(store *db.Store, wave *int) ([]findingRow, error) {
	q := "SELECT id, wave, agent, finding, mss_label, d1, d2, d3, d4, d5 FROM findings"
	var args []any
	if wave != nil {
		q += " WHERE wave = ?"
		args = append(args, *wave)
	}
	// By id, so a pair is always visited as (lower, higher) and describes
	// itself the same way on every run — which is what lets record recognise
	// a conflict it already wrote.
	q += " ORDER BY id"

	// WASP scan detector (best-effort): the Q6 conflict pass groups by
	// coordinate in Go — there is no JOIN here — and that is a full scan
	// when unbounded by wave. It is operator evidence; nothing feeds it to a
	// forager, and it never affects conflict detection.
	store.RecordScanIfFullScan(q, "findings", args...)

	rows, err := store.ReadDB.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query findings: %w", err)
	}
	defer rows.Close()
	return scanFindingRows(rows)
}

// scanFindingRows reads the rows loadConflictFindings selects.
func scanFindingRows(rows *sql.Rows) ([]findingRow, error) {
	var findings []findingRow
	for rows.Next() {
		var f findingRow
		if err := rows.Scan(&f.id, &f.wave, &f.agent, &f.finding, &f.mssLabel,
			&f.d1, &f.d2, &f.d3, &f.d4, &f.d5); err != nil {
			return nil, fmt.Errorf("scan finding: %w", err)
		}
		findings = append(findings, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query findings: %w", err)
	}
	return findings, nil
}

// groupConflictFindings groups findings by their (d1..d5) coordinate.
func groupConflictFindings(findings []findingRow) map[coordKey][]findingRow {
	groups := make(map[coordKey][]findingRow)
	for _, f := range findings {
		key := keyOf(f.d1, f.d2, f.d3, f.d4, f.d5)
		groups[key] = append(groups[key], f)
	}
	return groups
}

// conflictKind is one check DetectConflicts runs on each pair: its type,
// the conflicts it finds, and the report count it adds to.
type conflictKind struct {
	typ     string
	detect  func(a, b findingRow) []string
	counter func(r *ConflictReport) *int
}

// conflictKinds are the checks in the order a pair runs them: numeric
// divergence, MSS label disagreement, negation.
var conflictKinds = []conflictKind{
	{
		typ:     "numeric",
		detect:  func(a, b findingRow) []string { return numericDivergence(a, b, 0.20) },
		counter: func(r *ConflictReport) *int { return &r.Numeric },
	},
	{
		typ:     "mss_label",
		detect:  mssLabelDisagreement,
		counter: func(r *ConflictReport) *int { return &r.MSSLabel },
	},
	{
		typ:     "negation",
		detect:  negationConflict,
		counter: func(r *ConflictReport) *int { return &r.Negation },
	},
}

// conflictScan is one DetectConflicts pass: the report it fills, and
// whether it records what it finds or, on a dry run, only asks whether it
// would be new.
type conflictScan struct {
	store  *db.Store
	dryRun bool
	report *ConflictReport
}

// scanCell compares every pair of findings in one coordinate cell.
func (s *conflictScan) scanCell(coord string, group []findingRow) error {
	for i := 0; i < len(group); i++ {
		for j := i + 1; j < len(group); j++ {
			if err := s.comparePair(coord, group[i], group[j]); err != nil {
				return err
			}
		}
	}
	return nil
}

// comparePair runs every conflict check on one pair, in order.
func (s *conflictScan) comparePair(coord string, fa, fb findingRow) error {
	for _, kind := range conflictKinds {
		for _, desc := range kind.detect(fa, fb) {
			if err := s.add(coord, fa, fb, kind, desc); err != nil {
				return err
			}
		}
	}
	return nil
}

// add reports one conflict and records it.
func (s *conflictScan) add(coord string, fa, fb findingRow, kind conflictKind, desc string) error {
	*kind.counter(s.report)++
	s.report.Total++
	s.report.Details = append(s.report.Details, ConflictItem{coord, fa.id, fb.id, kind.typ, desc})
	return s.record(fa, fb, "["+kind.typ+"] "+desc)
}

// record writes a conflict once (ConflictsRepo.RecordOnce); a dry run only
// asks whether it would be new. A conflict is recorded under the later of
// its two findings' waves, which is the --wave wave when one is given, so
// the wave gate, which counts conflicts by wave, sees a conflict the dreamer
// found first, though record-once ignores the wave.
func (s *conflictScan) record(fa, fb findingRow, desc string) error {
	isNew, err := s.isNew(fa, fb, desc)
	if isNew && err == nil {
		s.report.New++
	}
	return err
}

// isNew writes the conflict unless it is recorded already, or on a dry run
// only checks, and reports whether it was not recorded before.
func (s *conflictScan) isNew(fa, fb findingRow, desc string) (bool, error) {
	if s.dryRun {
		seen, err := s.store.Conflicts().Recorded(fa.id, fb.id, desc)
		return !seen, err
	}
	return s.store.Conflicts().RecordOnce(max(fa.wave, fb.wave), fa.id, fb.id, desc)
}

// mssLabelDisagreement is the conflict between a guarantee and an
// assumption at the same coordinate.
func mssLabelDisagreement(fa, fb findingRow) []string {
	if !labelsDisagree(fa.mssLabel, fb.mssLabel) {
		return nil
	}
	return []string{fmt.Sprintf("MSS label disagreement: finding %d is '%s', finding %d is '%s'",
		fa.id, fa.mssLabel, fb.id, fb.mssLabel)}
}

// labelsDisagree reports whether one label is a guarantee and the other an
// assumption.
func labelsDisagree(a, b string) bool {
	return (a == "guarantee" && b == "assumption") || (a == "assumption" && b == "guarantee")
}

func numericDivergence(a, b findingRow, threshold float64) []string {
	numsA := extractNumbersWithContext(a.finding)
	numsB := extractNumbersWithContext(b.finding)
	var conflicts []string

	for _, na := range numsA {
		for _, nb := range numsB {
			if desc, ok := divergenceOf(na, nb, threshold); ok {
				conflicts = append(conflicts, desc)
			}
		}
	}
	return conflicts
}

// divergenceOf describes two numbers of the same kind, in matching
// contexts, that differ by more than threshold.
func divergenceOf(na, nb numWithCtx, threshold float64) (string, bool) {
	if !comparableNumbers(na, nb) {
		return "", false
	}
	denom := math.Max(math.Abs(na.val), math.Abs(nb.val))
	if denom == 0 {
		return "", false
	}
	div := math.Abs(na.val-nb.val) / denom
	if div > threshold {
		return fmt.Sprintf(
			"Numeric divergence (%s): %.2f vs %.2f (%.0f%% difference)",
			na.typ, na.val, nb.val, div*100,
		), true
	}
	return "", false
}

// comparableNumbers reports whether two numbers are of one kind, not both
// zero, and said about the same thing.
func comparableNumbers(na, nb numWithCtx) bool {
	return na.typ == nb.typ && !bothZero(na.val, nb.val) && contextsMatch(na.ctx, nb.ctx)
}

// bothZero reports whether a and b are both zero.
func bothZero(a, b float64) bool {
	return a == 0 && b == 0
}

type numWithCtx struct {
	typ string
	val float64
	ctx string
}

func extractNumbersWithContext(text string) []numWithCtx {
	var result []numWithCtx
	for _, m := range contextDollarRe.FindAllStringSubmatchIndex(text, -1) {
		result = append(result, numWithCtx{"dollar", dollarValue(text, m), numberContext(text, m)})
	}
	for _, m := range percentRe.FindAllStringSubmatchIndex(text, -1) {
		var v float64
		fmt.Sscanf(text[m[2]:m[3]], "%f", &v)
		result = append(result, numWithCtx{"percent", v, numberContext(text, m)})
	}
	return result
}

// dollarValue is the amount of a contextDollarRe match, its multiplier
// (billion, million, thousand) applied.
func dollarValue(text string, m []int) float64 {
	raw := strings.ReplaceAll(text[m[2]:m[3]], ",", "")
	var v float64
	fmt.Sscanf(raw, "%f", &v)
	return v * dollarMultiplier(text[m[0]:m[1]])
}

// dollarMultiplier is the multiplier a dollar amount names, 1 when none.
func dollarMultiplier(amount string) float64 {
	mm := multiplierRe.FindStringSubmatch(amount)
	if len(mm) <= 1 {
		return 1
	}
	if mult, ok := multipliers[strings.ToLower(mm[1])]; ok {
		return mult
	}
	return 1
}

// numberContext is the lower-cased text within 30 bytes of a match.
func numberContext(text string, m []int) string {
	start := max(m[0]-30, 0)
	end := min(m[1]+30, len(text))
	return strings.ToLower(text[start:end])
}

var stopwords = map[string]bool{
	"the": true, "a": true, "an": true, "is": true, "are": true, "was": true,
	"for": true, "on": true, "at": true, "to": true, "in": true, "of": true,
	"and": true, "or": true, "with": true, "from": true, "by": true, "up": true,
	"about": true, "into": true, "per": true, "its": true, "it": true,
	"not": true, "does": true, "cannot": true, "can": true, "has": true,
	"have": true, "been": true, "will": true, "that": true, "this": true,
	"which": true, "but": true, "also": true, "than": true,
}

func contentWords(text string) map[string]bool {
	clean := regexp.MustCompile(`[^\w\s]`)
	words := make(map[string]bool)
	for _, w := range strings.Fields(clean.ReplaceAllString(strings.ToLower(text), "")) {
		if len(w) > 3 && !stopwords[w] {
			words[w] = true
		}
	}
	return words
}

// sharedWords lists the words in both sets, in no order.
func sharedWords(a, b map[string]bool) []string {
	var shared []string
	for w := range a {
		if b[w] {
			shared = append(shared, w)
		}
	}
	return shared
}

// unionSize is the number of distinct words in either set.
func unionSize(a, b map[string]bool) int {
	union := len(a)
	for w := range b {
		if !a[w] {
			union++
		}
	}
	return union
}

func contextsMatch(a, b string) bool {
	wordsA := contentWords(a)
	wordsB := contentWords(b)
	if len(wordsA) == 0 || len(wordsB) == 0 {
		return false
	}
	overlap := len(sharedWords(wordsA, wordsB))
	return overlap >= 2 || float64(overlap)/float64(unionSize(wordsA, wordsB)) > 0.4
}

func negationConflict(a, b findingRow) []string {
	textA := strings.ToLower(a.finding)
	textB := strings.ToLower(b.finding)

	aNeg := negationRe.MatchString(textA)
	bNeg := negationRe.MatchString(textB)

	if aNeg == bNeg {
		return nil
	}

	// Check subject overlap
	shared, ok := sharedSubject(contentWords(textA), contentWords(textB))
	if !ok {
		return nil
	}

	negatorID := a.id
	asserterID := b.id
	if bNeg {
		negatorID = b.id
		asserterID = a.id
	}

	return []string{
		fmt.Sprintf("Semantic conflict: finding %d negates while finding %d asserts positively. Shared terms: %s",
			negatorID, asserterID, strings.Join(firstSorted(shared, 5), ", ")),
	}
}

// sharedSubject is the words two findings share, when they share enough of
// them to be about one subject.
func sharedSubject(wordsA, wordsB map[string]bool) ([]string, bool) {
	if len(wordsA) == 0 || len(wordsB) == 0 {
		return nil, false
	}
	shared := sharedWords(wordsA, wordsB)
	jaccard := float64(len(shared)) / float64(unionSize(wordsA, wordsB))
	if weakOverlap(len(shared), jaccard) {
		return nil, false
	}
	return shared, true
}

// weakOverlap reports whether fewer than three shared words that make up
// less than 0.3 of the union leave two findings on different subjects.
func weakOverlap(shared int, jaccard float64) bool {
	return shared < 3 && jaccard < 0.3
}

// firstSorted is the first n words in sorted order. Sorted, so the
// description is the same on every run, whatever the map's order.
func firstSorted(words []string, n int) []string {
	sort.Strings(words)
	if len(words) > n {
		words = words[:n]
	}
	return words
}
