package bench

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// tokenSyntax is how one kind of token is written: the pattern a token
// matches, and how a match becomes the canonical token, with ok false when
// the match names anything outside known.
type tokenSyntax struct {
	re    *regexp.Regexp
	token func(m []string, known map[string]bool) (tok string, ok bool)
}

var tokenSyntaxes = map[TokenKind]tokenSyntax{
	TokensPair:  {regexp.MustCompile(`([a-z][a-z0-9_-]*)\s*⇄\s*([a-z][a-z0-9_-]*)`), pairMatchToken},
	TokensArrow: {regexp.MustCompile(`([a-z][a-z0-9_-]*)\s*→\s*([a-z][a-z0-9_-]*)`), arrowMatchToken},
	TokensName:  {regexp.MustCompile(`@([a-z][a-z0-9_-]*)`), nameMatchToken},
}

// pairMatchToken is a⇄b, names in alphabetical order, for two distinct
// known names.
func pairMatchToken(m []string, known map[string]bool) (string, bool) {
	a, b := m[1], m[2]
	return pairToken(a, b), known[a] && known[b] && a != b
}

// arrowMatchToken is a→b, as written, for two distinct known names.
func arrowMatchToken(m []string, known map[string]bool) (string, bool) {
	return m[1] + "→" + m[2], known[m[1]] && known[m[2]] && m[1] != m[2]
}

// nameMatchToken is @name for a known name.
func nameMatchToken(m []string, known map[string]bool) (string, bool) {
	return "@" + m[1], known[m[1]]
}

// ExtractTokens returns the canonical tokens of kind that text contains,
// sorted and deduplicated. Matching ignores case and the spaces around a
// connector; a token naming anything outside names is dropped. A pair is
// written with its names in alphabetical order whichever order the text used.
func ExtractTokens(kind TokenKind, text string, names []string) []string {
	syn, ok := tokenSyntaxes[kind]
	if !ok {
		return nil
	}
	known := stringSet(names)
	set := map[string]bool{}
	for _, m := range syn.re.FindAllStringSubmatch(strings.ToLower(text), -1) {
		if tok, ok := syn.token(m, known); ok {
			set[tok] = true
		}
	}
	return orderedKeys(set)
}

// stringSet is the strings as a set.
func stringSet(xs []string) map[string]bool {
	set := make(map[string]bool, len(xs))
	for _, x := range xs {
		set[x] = true
	}
	return set
}

// TokenF1 scores the tokens a model named against the ones expected. Both
// empty is a perfect 1; one empty and the other not is 0.
func TokenF1(want, got []string) float64 {
	if len(want) == 0 && len(got) == 0 {
		return 1
	}
	w := stringSet(want)
	tp, distinct := truePositives(w, got)
	if tp == 0 {
		return 0
	}
	p := float64(tp) / float64(distinct)
	r := float64(tp) / float64(len(w))
	return 2 * p * r / (p + r)
}

// truePositives counts the distinct tokens of got that want holds, and the
// distinct tokens of got.
func truePositives(want map[string]bool, got []string) (tp, distinct int) {
	seen := map[string]bool{}
	for _, t := range got {
		if want[t] && !seen[t] {
			tp++
		}
		seen[t] = true
	}
	return tp, len(seen)
}

// Grade scores one answer to an item: the verdict must equal the expected
// one, and when the family asks for tokens, the tokens found in text are
// scored by TokenF1.
func Grade(it Item, verdict, text string) (correct bool, got []string, f1 *float64) {
	correct = strings.ToLower(strings.TrimSpace(verdict)) == it.Want.Verdict
	if it.Tokens == TokensNone {
		return correct, nil, nil
	}
	got = ExtractTokens(it.Tokens, text, it.Names)
	f := TokenF1(it.Want.Tokens, got)
	return correct, got, &f
}

// Arms of a bench item run.
const (
	ArmSwarm = "swarm"
	ArmSolo  = "solo"
)

// NodeStat is one workflow node of a run: its model, status, tokens and
// wall time (whole seconds; the database stores RFC 3339 timestamps).
type NodeStat struct {
	Node        string  `json:"node"`
	Model       string  `json:"model,omitempty"`
	Status      string  `json:"status"`
	TokensIn    int64   `json:"tokens_in"`
	TokensOut   int64   `json:"tokens_out"`
	WallSeconds float64 `json:"wall_seconds"`
}

// Row is one run of one item on one arm: the unit `chb bench decide` reads.
type Row struct {
	Config     string   `json:"config"`
	Provider   string   `json:"provider"`
	LensModel  string   `json:"lens_model,omitempty"`
	QueenModel string   `json:"queen_model,omitempty"`
	Arm        string   `json:"arm"`
	Item       string   `json:"item"`
	ItemHash   string   `json:"item_hash"`
	Pair       string   `json:"pair"`
	Family     string   `json:"family"`
	Seed       int64    `json:"seed"`
	Variant    string   `json:"variant"`
	Rep        int      `json:"rep"`
	Want       string   `json:"want"`
	Got        string   `json:"got"`
	Correct    bool     `json:"correct"`
	WantTokens []string `json:"want_tokens,omitempty"`
	GotTokens  []string `json:"got_tokens,omitempty"`
	TokenF1    *float64 `json:"token_f1,omitempty"` // absent when the family names nothing
	// Completed: the run exited 0 and yielded a verdict.
	Completed bool `json:"completed"`
	// TreeOK: the run's private tree hashed the same before and after.
	TreeOK bool `json:"tree_ok"`
	// Lenses and FullVerdicts: forager outputs in a swarm run, and how many
	// of them Queen could read in full rather than as the digest.
	Lenses       int `json:"lenses,omitempty"`
	FullVerdicts int `json:"full_verdicts,omitempty"`
	// LensAnswers: each lens of a swarm run, by forager, with the verdict
	// its node returned in the run ("" when none) and the model its row
	// records ("" when none), read from the run's node rows.
	LensAnswers map[string]LensAnswer `json:"lens_answers,omitempty"`
	// Diversity: the run's lens diversity by the artifact's check
	// (low, not_low, unknown or not_checked), read from the same rows; ""
	// when the run left no run row.
	Diversity string `json:"diversity,omitempty"`
	// Outputs and SchemaValid: outputs checked against their contract, and
	// how many held it (required keys present, verdict in the enum).
	Outputs     int `json:"outputs"`
	SchemaValid int `json:"schema_valid"`
	// FinishLength counts calls cut off by the output cap. Null means the
	// run did not record finish reasons, so the guard cannot be evaluated.
	FinishLength *int       `json:"finish_length"`
	WallSeconds  float64    `json:"wall_seconds"`
	TokensIn     int64      `json:"tokens_in"`
	TokensOut    int64      `json:"tokens_out"`
	Nodes        []NodeStat `json:"nodes,omitempty"`
	Error        string     `json:"error,omitempty"`
}

// LensAnswer is one lens's verdict in a swarm run and the model its row
// records.
type LensAnswer struct {
	Verdict string `json:"verdict"`
	Model   string `json:"model"`
}

// NewRow fills the item's half of a row.
func NewRow(it Item, arm string, rep int) Row {
	return Row{
		Arm: arm, Item: it.ID, ItemHash: it.Hash(), Pair: it.Pair, Family: it.Family,
		Seed: it.Seed, Variant: it.Variant, Rep: rep, Want: it.Want.Verdict, WantTokens: it.Want.Tokens,
	}
}

// WriteRows writes rows as JSON lines.
func WriteRows(w io.Writer, rows []Row) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return nil
}

// ErrNotMeasured is why ReadRows refuses a file WriteNotMeasured wrote.
var ErrNotMeasured = errors.New("these rows are not a measurement")

// WriteNotMeasured writes rows as WriteRows does, after a first line
// {"not_measured": why}, which makes ReadRows refuse the file. A bench case
// writes its rows so when it stopped at an outage or no run completed: they
// are kept to read, but they are not a result of the configuration.
func WriteNotMeasured(w io.Writer, why string, rows []Row) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(map[string]string{"not_measured": why}); err != nil {
		return err
	}
	return WriteRows(w, rows)
}

// ReadRows reads JSON lines written by WriteRows; blank lines are skipped.
// It refuses a file with a not_measured line (WriteNotMeasured), with an
// error that wraps ErrNotMeasured and says why.
func ReadRows(r io.Reader) ([]Row, error) {
	var out []Row
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for line := 1; sc.Scan(); line++ {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		row, err := decodeRowLine(sc.Bytes())
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		out = append(out, row)
	}
	return out, sc.Err()
}

// decodeRowLine decodes one line of rows, refusing a not_measured line
// with an error that wraps ErrNotMeasured and says why.
func decodeRowLine(b []byte) (Row, error) {
	if why, ok := notMeasuredLine(b); ok {
		return Row{}, fmt.Errorf("%w: %s", ErrNotMeasured, why)
	}
	var row Row
	err := json.Unmarshal(b, &row)
	return row, err
}

// notMeasuredLine is why a line WriteNotMeasured wrote says the rows are
// not a measurement.
func notMeasuredLine(b []byte) (string, bool) {
	var mark struct {
		NotMeasured *string `json:"not_measured"`
	}
	if json.Unmarshal(b, &mark) != nil || mark.NotMeasured == nil {
		return "", false
	}
	return *mark.NotMeasured, true
}

// ClassScore is accuracy on the items expecting one verdict.
type ClassScore struct {
	N       int `json:"n"`
	Correct int `json:"correct"`
}

// Summary scores a set of rows — normally one configuration on one arm.
// Every row counts as one observation, so repetitions weigh like items.
type Summary struct {
	Runs      int     `json:"runs"`
	Completed int     `json:"completed"`
	Correct   int     `json:"correct"`
	Accuracy  float64 `json:"accuracy"`
	// ClassBalanced is the mean of support and oppose accuracy. Abstain
	// items are judged by Fabricated: a run has too few of them to weigh
	// like a class.
	ClassBalanced float64               `json:"class_balanced_accuracy"`
	Classes       map[string]ClassScore `json:"classes"`
	// Pairs counts (pair, rep) groups holding both twins; PairsPassed those
	// where both were answered correctly.
	Pairs       int     `json:"pairs"`
	PairsPassed int     `json:"pairs_passed"`
	PairScore   float64 `json:"pair_score"`
	TokenItems  int     `json:"token_items"`
	TokenF1     float64 `json:"token_f1"`
	// Fabricated counts abstain items answered support or oppose.
	Fabricated int `json:"fabricated"`
	// LensErrors is how the lenses' errors fell together (CorrelatedErrors).
	LensErrors LensErrors `json:"lens_errors"`
	// Diversity counts swarm runs by their lens diversity state
	// (Row.Diversity); a run with none counts as not_recorded.
	// DiversityLowWrong counts the low runs answered wrong.
	Diversity         map[string]int `json:"diversity"`
	DiversityLowWrong int            `json:"diversity_low_wrong"`
	TokensIn          int64          `json:"tokens_in"`
	TokensOut         int64          `json:"tokens_out"`
	WallSeconds       float64        `json:"wall_seconds"`
}

// diversityLow is the Row.Diversity state of a run whose lenses that
// answered all ran on one model and gave one verdict (workflow.DiversityLow).
const diversityLow = "low"

// CoErrors counts lens pairs in one run, both with a verdict (Compared),
// those where at least one lens erred (Pairs) and those where both did
// (Together). Rate is Together/Pairs, nil when no lens erred. Independent
// is the rate the same pairs would have if each lens erred independently
// of the others, at its own error rate over the rows; nil when no lens
// erred.
type CoErrors struct {
	Compared    int      `json:"compared"`
	Pairs       int      `json:"pairs"`
	Together    int      `json:"together"`
	Rate        *float64 `json:"rate"`
	Independent *float64 `json:"independent"`
}

// ModelVerdicts counts the lens runs its row records on one model, and
// those that returned a verdict.
type ModelVerdicts struct {
	Model    string `json:"model"`
	Lenses   int    `json:"lenses"`
	Verdicts int    `json:"verdicts"`
}

// LensErrors is the correlated-error rate of a set of swarm runs, over
// every lens pair (All) and over the pairs whose two lenses ran on
// different recorded models (CrossModel). Runs counts the runs with two or
// more lens verdicts, the runs whose pairs are counted. Models counts each
// recorded model's lens runs and verdicts, sorted by model; a lens with
// no recorded model is left out of it.
type LensErrors struct {
	Runs       int             `json:"runs"`
	Models     []ModelVerdicts `json:"models"`
	All        CoErrors        `json:"all"`
	CrossModel CoErrors        `json:"cross_model"`
}

// CorrelatedErrors measures how often the lenses err together. A lens errs
// on a run when its verdict is not the item's expected verdict; a lens
// with no verdict is left out. For every pair of lenses in one run, both
// with a verdict, the pair counts when at least one erred, and counts as
// together when both did. A pair is cross-model when both lenses have a
// recorded model and the two differ. A lens's error rate p is that of its
// forager on its model over the rows; the independent rate is Σ pᵢpⱼ /
// Σ (pᵢ + pⱼ − pᵢpⱼ) over the same pairs. It assumes a lens's chance of
// erring is the same on every item, so errors that come from the item — a
// hard item, or one model's bias toward one verdict — read as correlation.
func CorrelatedErrors(rows []Row) LensErrors {
	rates, models := lensTallies(rows)
	var e LensErrors
	var pairs lensPairs
	for _, r := range rows {
		names := verdictLenses(r)
		if len(names) >= 2 {
			e.Runs++
		}
		pairs.addRun(r, names, rates)
	}
	e.All, e.CrossModel = pairs.all.result(), pairs.cross.result()
	e.Models = sortedModels(models)
	return e
}

// lensKey is one lens: its forager on its recorded model.
type lensKey struct{ forager, model string }

// errorTally counts one lens's verdicts and the wrong ones.
type errorTally struct{ n, wrong int }

// lensRates is each lens's error tally over a set of rows.
type lensRates map[lensKey]*errorTally

// rate is the lens's error rate.
func (lr lensRates) rate(k lensKey) float64 {
	return float64(lr[k].wrong) / float64(lr[k].n)
}

// lensTallies counts each lens's verdicts and errors, and each recorded
// model's lens runs and verdicts.
func lensTallies(rows []Row) (lensRates, map[string]*ModelVerdicts) {
	rates := lensRates{}
	models := map[string]*ModelVerdicts{}
	for _, r := range rows {
		for name, a := range r.LensAnswers {
			countModelVerdict(models, a)
			rates.count(lensKey{name, a.Model}, a.Verdict, r.Want)
		}
	}
	return rates, models
}

// countModelVerdict counts a lens run under its recorded model, and
// whether it returned a verdict; a lens with no recorded model is skipped.
func countModelVerdict(models map[string]*ModelVerdicts, a LensAnswer) {
	if a.Model == "" {
		return
	}
	m := models[a.Model]
	if m == nil {
		m = &ModelVerdicts{Model: a.Model}
		models[a.Model] = m
	}
	m.Lenses++
	if a.Verdict != "" {
		m.Verdicts++
	}
}

// count tallies the lens's verdict against want; no verdict counts nothing.
func (lr lensRates) count(k lensKey, verdict, want string) {
	if verdict == "" {
		return
	}
	t := lr[k]
	if t == nil {
		t = &errorTally{}
		lr[k] = t
	}
	t.n++
	if verdict != want {
		t.wrong++
	}
}

// verdictLenses is the run's lenses that returned a verdict, sorted.
func verdictLenses(r Row) []string {
	names := make([]string, 0, len(r.LensAnswers))
	for name, a := range r.LensAnswers {
		if a.Verdict != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// pairErrors gathers one set of lens pairs: the counts CoErrors reports,
// and the sums Σ pᵢpⱼ and Σ (pᵢ + pⱼ − pᵢpⱼ) of its independent rate.
type pairErrors struct {
	counts       CoErrors
	both, either float64
}

// lensPairs gathers every pair of lenses in a run, and apart from them the
// pairs on different recorded models.
type lensPairs struct{ all, cross pairErrors }

// addRun counts every pair of the run's lenses names, in order.
func (lp *lensPairs) addRun(r Row, names []string, rates lensRates) {
	for i, ni := range names {
		for _, nj := range names[i+1:] {
			lp.addPair(r, ni, nj, rates)
		}
	}
}

// addPair counts the pair of r's lenses ni and nj among all pairs, and
// among the cross-model pairs when their recorded models differ.
func (lp *lensPairs) addPair(r Row, ni, nj string, rates lensRates) {
	ai, aj := r.LensAnswers[ni], r.LensAnswers[nj]
	pi, pj := rates.rate(lensKey{ni, ai.Model}), rates.rate(lensKey{nj, aj.Model})
	both, either := pi*pj, pi+pj-pi*pj
	wrong := erred(r.Want, ai, aj)
	lp.all.add(wrong, both, either)
	if crossModel(ai, aj) {
		lp.cross.add(wrong, both, either)
	}
}

// erred counts the lenses whose verdict is not want.
func erred(want string, lenses ...LensAnswer) int {
	n := 0
	for _, a := range lenses {
		if a.Verdict != want {
			n++
		}
	}
	return n
}

// crossModel: both lenses have a recorded model, and the two differ.
func crossModel(ai, aj LensAnswer) bool {
	return ai.Model != "" && aj.Model != "" && ai.Model != aj.Model
}

// add counts one pair, wrong of whose two lenses erred, with its
// independent-rate terms.
func (s *pairErrors) add(wrong int, both, either float64) {
	s.both, s.either = s.both+both, s.either+either
	s.counts.Compared++
	if wrong > 0 {
		s.counts.Pairs++
	}
	if wrong == 2 {
		s.counts.Together++
	}
}

// result is the counts with their rate and independent rate.
func (s *pairErrors) result() CoErrors {
	c := s.counts
	if c.Pairs > 0 {
		rate := float64(c.Together) / float64(c.Pairs)
		c.Rate = &rate
	}
	if s.either > 0 {
		ind := s.both / s.either
		c.Independent = &ind
	}
	return c
}

// sortedModels is the models' counts, sorted by model; nil when there are
// none.
func sortedModels(models map[string]*ModelVerdicts) []ModelVerdicts {
	var out []ModelVerdicts
	for _, m := range models {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

// Summarize scores rows.
func Summarize(rows []Row) Summary {
	s := Summary{Classes: map[string]ClassScore{}, Diversity: map[string]int{}}
	twins := map[pairRep]map[string]bool{}
	for _, r := range rows {
		s.add(r)
		recordTwin(twins, r)
	}
	s.Accuracy = share(s.Correct, s.Runs)
	s.ClassBalanced = classBalanced(s.Classes)
	s.Pairs, s.PairsPassed = countTwinPairs(twins)
	s.PairScore = share(s.PairsPassed, s.Pairs)
	if s.TokenItems > 0 {
		s.TokenF1 /= float64(s.TokenItems)
	}
	s.LensErrors = CorrelatedErrors(rows)
	return s
}

// add counts one row into the summary's totals; TokenF1 holds the sum of
// the rows' token F1 until Summarize divides it.
func (s *Summary) add(r Row) {
	s.Runs++
	if r.Completed {
		s.Completed++
	}
	s.addClass(r)
	if fabricated(r) {
		s.Fabricated++
	}
	s.addDiversity(r)
	if r.TokenF1 != nil {
		s.TokenItems++
		s.TokenF1 += *r.TokenF1
	}
	s.TokensIn += r.TokensIn
	s.TokensOut += r.TokensOut
	s.WallSeconds += r.WallSeconds
}

// addClass counts the row under its expected verdict.
func (s *Summary) addClass(r Row) {
	c := s.Classes[r.Want]
	c.N++
	if r.Correct {
		s.Correct++
		c.Correct++
	}
	s.Classes[r.Want] = c
}

// fabricated: an abstain item answered support or oppose.
func fabricated(r Row) bool {
	return r.Want == Abstain && (r.Got == Support || r.Got == Oppose)
}

// addDiversity counts a swarm run under its lens diversity state.
func (s *Summary) addDiversity(r Row) {
	if r.Arm != ArmSwarm {
		return
	}
	d := diversityState(r)
	s.Diversity[d]++
	if d == diversityLow && !r.Correct {
		s.DiversityLowWrong++
	}
}

// diversityState is the run's lens diversity, not_recorded when it has none.
func diversityState(r Row) string {
	if r.Diversity == "" {
		return "not_recorded"
	}
	return r.Diversity
}

// pairRep is one repetition of one twin pair.
type pairRep struct {
	pair string
	rep  int
}

// recordTwin records whether the row's twin was answered right.
func recordTwin(twins map[pairRep]map[string]bool, r Row) {
	k := pairRep{r.Pair, r.Rep}
	if twins[k] == nil {
		twins[k] = map[string]bool{}
	}
	twins[k][r.Variant] = r.Correct
}

// countTwinPairs counts the pair repetitions that hold both twins, and
// those where both were answered right.
func countTwinPairs(twins map[pairRep]map[string]bool) (pairs, passed int) {
	for _, v := range twins {
		complete, right := bothTwins(v)
		if !complete {
			continue
		}
		pairs++
		if right {
			passed++
		}
	}
	return pairs, passed
}

// bothTwins reports whether a pair repetition holds both twins, and
// whether both were answered right.
func bothTwins(v map[string]bool) (complete, right bool) {
	a, okA := v["a"]
	b, okB := v["b"]
	return okA && okB, a && b
}

// classBalanced is the mean of the support and oppose classes' accuracy,
// over those present.
func classBalanced(classes map[string]ClassScore) float64 {
	sum, n := 0.0, 0
	for _, want := range []string{Support, Oppose} {
		if c, ok := classes[want]; ok {
			sum += float64(c.Correct) / float64(c.N)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// share is num/den, 0 when den is 0.
func share(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return float64(num) / float64(den)
}
