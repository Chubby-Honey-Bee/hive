package bench

import (
	"bytes"
	"errors"
	"math"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestExtractTokens_CanonicalFormsOnly(t *testing.T) {
	names := []string{"architect", "framer", "skeptic", "steward"}
	text := "Orphans: `Framer ⇄ architect`; skeptic⇄steward, and ghost⇄framer.\n" +
		"architect⇄architect is no pair. Bonds: steward→skeptic, skeptic -> architect.\n" +
		"Lacking: @Architect, @nobody, email@framer."
	cases := []struct {
		kind TokenKind
		want []string
	}{
		// Names are sorted within a pair, case is ignored, and a name
		// outside the roster or a self-pair is dropped.
		{TokensPair, []string{"architect⇄framer", "skeptic⇄steward"}},
		// Only the arrow the question asks for counts; "->" does not.
		{TokensArrow, []string{"steward→skeptic"}},
		// @name counts wherever it appears, if the name is on the roster.
		{TokensName, []string{"@architect", "@framer"}},
		{TokensNone, nil},
	}
	for _, c := range cases {
		got := ExtractTokens(c.kind, text, names)
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %v, want %v", c.kind, got, c.want)
		}
	}
}

// TokenF1 is the harmonic mean of precision and recall over distinct tokens.
func TestTokenF1(t *testing.T) {
	cases := []struct{ want, got []string }{
		{nil, nil},
		{[]string{"a"}, nil},
		{nil, []string{"a"}},
		{[]string{"a", "b"}, []string{"a", "b"}},
		{[]string{"a", "b", "c", "d"}, []string{"a", "x"}},
		{[]string{"a", "b"}, []string{"a", "a", "c"}},
	}
	for _, c := range cases {
		want := independentF1(c.want, c.got)
		if got := TokenF1(c.want, c.got); math.Abs(got-want) > 1e-12 {
			t.Errorf("TokenF1(%v, %v) = %v, want %v", c.want, c.got, got, want)
		}
	}
}

func independentF1(want, got []string) float64 {
	w, g := map[string]bool{}, map[string]bool{}
	for _, x := range want {
		w[x] = true
	}
	for _, x := range got {
		g[x] = true
	}
	if len(w) == 0 && len(g) == 0 {
		return 1
	}
	tp := 0.0
	for x := range g {
		if w[x] {
			tp++
		}
	}
	if tp == 0 {
		return 0
	}
	p, r := tp/float64(len(g)), tp/float64(len(w))
	return 2 * p * r / (p + r)
}

func TestReadRowsRoundTrip(t *testing.T) {
	f := 0.5
	rows := []Row{{Config: "C1", Arm: ArmSwarm, Item: "F1-s1-a", Want: Support, Got: "oppose", TokenF1: &f}, {Config: "C1", Arm: ArmSolo, Item: "AB-s1-b", Want: Abstain}}
	var buf bytes.Buffer
	if err := WriteRows(&buf, rows); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"finish_length":null`) {
		t.Errorf("an unmeasured finish reason must be written as null:\n%s", buf.String())
	}
	back, err := ReadRows(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, rows) {
		t.Fatalf("round trip changed the rows:\n%+v\n%+v", back, rows)
	}
}

// Rows written under a not_measured line are refused, with why, and the
// same rows without it read back.
func TestReadRowsRefusesNotMeasured(t *testing.T) {
	rows := []Row{{Config: "C1", Arm: ArmSwarm, Item: "F1-s1-a", Want: Support}}
	why := "stopped at run 1 of 2 (F1-s1-a swarm r1), which reached no model: connection refused"
	var buf bytes.Buffer
	if err := WriteNotMeasured(&buf, why, rows); err != nil {
		t.Fatal(err)
	}
	marked := buf.String()
	if _, err := ReadRows(strings.NewReader(marked)); !errors.Is(err, ErrNotMeasured) || !strings.Contains(err.Error(), why) {
		t.Errorf("ReadRows = %v; want ErrNotMeasured, saying %q", err, why)
	}
	_, rest, _ := strings.Cut(marked, "\n")
	back, err := ReadRows(strings.NewReader(rest))
	if err != nil || !reflect.DeepEqual(back, rows) {
		t.Errorf("the rows after the line read back as %+v, %v; want %+v", back, err, rows)
	}
}

// policyRows answers every item with policy and grades the answers.
func policyRows(items []Item, policy func(Item) (string, string)) []Row {
	var rows []Row
	for _, it := range items {
		v, text := policy(it)
		r := NewRow(it, ArmSolo, 1)
		r.Got = v
		r.Correct, r.GotTokens, r.TokenF1 = Grade(it, v, text)
		rows = append(rows, r)
	}
	return rows
}

// binomialInterval returns the largest lo with P(X < lo) <= tail and the
// smallest hi with P(X > hi) <= tail, for X ~ Binomial(n, p).
func binomialInterval(n int, p, tail float64) (lo, hi int) {
	pmf := make([]float64, n+1)
	for k := 0; k <= n; k++ {
		lg := lgamma(n+1) - lgamma(k+1) - lgamma(n-k+1)
		pmf[k] = math.Exp(lg + float64(k)*math.Log(p) + float64(n-k)*math.Log(1-p))
	}
	cum := 0.0
	for lo = 0; lo <= n; lo++ {
		if cum+pmf[lo] > tail {
			break
		}
		cum += pmf[lo]
	}
	cum = 0
	for hi = n; hi >= 0; hi-- {
		if cum+pmf[hi] > tail {
			break
		}
		cum += pmf[hi]
	}
	return lo, hi
}

func lgamma(x int) float64 {
	v, _ := math.Lgamma(float64(x))
	return v
}

// The scoring is exact for policies whose score is known by construction:
// a constant verdict is right on exactly one twin of every pair, so it
// passes none; an oracle passes all; a fair coin between support and
// oppose passes a pair with probability 1/4.
func TestPolicies_PairScoresAreExact(t *testing.T) {
	items, err := Generate(liveRoster(t), nil, seedRange(1, 60))
	if err != nil {
		t.Fatal(err)
	}
	constant := func(v string) func(Item) (string, string) {
		return func(Item) (string, string) { return v, "" }
	}
	for _, v := range []string{Support, Oppose, Abstain} {
		if s := Summarize(policyRows(items, constant(v))); s.PairScore != 0 || s.Pairs != len(items)/2 {
			t.Errorf("always-%s: %d/%d pairs passed, want 0/%d", v, s.PairsPassed, s.Pairs, len(items)/2)
		}
	}
	oracle := Summarize(policyRows(items, func(it Item) (string, string) {
		return it.Want.Verdict, strings.Join(it.Want.Tokens, " ")
	}))
	if oracle.PairScore != 1 || oracle.ClassBalanced != 1 || oracle.TokenF1 != 1 {
		t.Errorf("oracle: pair score %v, class-balanced %v, token F1 %v; want 1, 1, 1", oracle.PairScore, oracle.ClassBalanced, oracle.TokenF1)
	}

	var binary []Item
	for _, it := range items {
		if it.Family != "AB" {
			binary = append(binary, it)
		}
	}
	rng := rand.New(rand.NewPCG(7, 7))
	coin := Summarize(policyRows(binary, func(Item) (string, string) {
		if rng.IntN(2) == 0 {
			return Support, ""
		}
		return Oppose, ""
	}))
	lo, hi := binomialInterval(coin.Pairs, 0.25, 0.0005)
	if coin.PairsPassed < lo || coin.PairsPassed > hi {
		t.Errorf("coin: %d/%d pairs passed, outside the 99.9%% binomial interval [%d, %d] around 1/4", coin.PairsPassed, coin.Pairs, lo, hi)
	}
}

// randomLensRows draws swarm rows whose lenses answer at random: a lens
// may be missing or give no verdict, and lenses run on one of models.
func randomLensRows(rng *rand.Rand, n int, names, models []string) []Row {
	verdicts := []string{Support, Oppose, "conditional", Abstain, ""}
	var rows []Row
	for i := 0; i < n; i++ {
		r := Row{Arm: ArmSwarm, Want: []string{Support, Oppose}[rng.IntN(2)], LensAnswers: map[string]LensAnswer{}}
		for _, name := range names {
			if rng.IntN(10) == 0 {
				continue
			}
			r.LensAnswers[name] = LensAnswer{Verdict: verdicts[rng.IntN(len(verdicts))], Model: models[rng.IntN(len(models))]}
		}
		rows = append(rows, r)
	}
	return rows
}

func choose2(n int) int { return n * (n - 1) / 2 }

// countedCoErrors restates CorrelatedErrors by counting instead of
// enumerating pairs. In a run with k answering lenses, w of them wrong,
// C(k,2) pairs are compared, C(k,2) − C(k−w,2) hold an error and C(w,2)
// hold two; over the same pairs Σ pᵢpⱼ = ((Σp)² − Σp²)/2 and
// Σ (pᵢ + pⱼ − pᵢpⱼ) = (k−1)Σp − Σ pᵢpⱼ. The cross-model figures are
// those over the lenses with a recorded model less those within each
// model.
func countedCoErrors(rows []Row) (all, cross [5]float64) {
	type lens struct{ forager, model string }
	n, wrong := map[lens]float64{}, map[lens]float64{}
	for _, r := range rows {
		for name, a := range r.LensAnswers {
			if a.Verdict != "" {
				n[lens{name, a.Model}]++
				if a.Verdict != r.Want {
					wrong[lens{name, a.Model}]++
				}
			}
		}
	}
	// group returns the pairs compared, those with an error, those with
	// two, Σ pᵢpⱼ and Σ (pᵢ + pⱼ − pᵢpⱼ) over the pairs within one set of
	// lenses.
	group := func(ps []float64, w int) [5]float64 {
		k := len(ps)
		var sum, sq float64
		for _, p := range ps {
			sum, sq = sum+p, sq+p*p
		}
		both := (sum*sum - sq) / 2
		return [5]float64{float64(choose2(k)), float64(choose2(k) - choose2(k-w)), float64(choose2(w)), both, float64(k-1)*sum - both}
	}
	for _, r := range rows {
		var ps, recorded []float64
		w, wRecorded := 0, 0
		byModel := map[string][]float64{}
		wByModel := map[string]int{}
		for name, a := range r.LensAnswers {
			if a.Verdict == "" {
				continue
			}
			k := lens{name, a.Model}
			p := wrong[k] / n[k]
			ps = append(ps, p)
			wrongHere := a.Verdict != r.Want
			if wrongHere {
				w++
			}
			if a.Model == "" {
				continue
			}
			recorded = append(recorded, p)
			byModel[a.Model] = append(byModel[a.Model], p)
			if wrongHere {
				wRecorded++
				wByModel[a.Model]++
			}
		}
		g, gr := group(ps, w), group(recorded, wRecorded)
		within := [5]float64{}
		for m, mps := range byModel {
			gm := group(mps, wByModel[m])
			for i := range within {
				within[i] += gm[i]
			}
		}
		for i := range all {
			all[i] += g[i]
			cross[i] += gr[i] - within[i]
		}
	}
	return all, cross
}

// The correlated-error counts, rate and independent rate equal the
// counting restatement on random rows, over all pairs and cross-model; a
// lens with no recorded model is in no cross-model pair. The runs counted
// and each model's lens runs and verdicts equal a direct count.
func TestCorrelatedErrors_MatchCounting(t *testing.T) {
	rng := rand.New(rand.NewPCG(8, 2026))
	names := []string{"architect", "empiricist", "pragmatist", "scholar", "skeptic", "steward", "timekeeper"}
	rows := randomLensRows(rng, 300, names, []string{"qwen3.5:4b", "ministral-3:8b", "qwen3.6:35b-a3b", ""})
	got := CorrelatedErrors(rows)
	all, cross := countedCoErrors(rows)
	check := func(label string, c CoErrors, want [5]float64) {
		t.Helper()
		if float64(c.Compared) != want[0] || float64(c.Pairs) != want[1] || float64(c.Together) != want[2] {
			t.Errorf("%s: compared %d pairs %d together %d, want %v %v %v", label, c.Compared, c.Pairs, c.Together, want[0], want[1], want[2])
		}
		if c.Rate == nil || math.Abs(*c.Rate-want[2]/want[1]) > 1e-12 {
			t.Errorf("%s: rate %v, want %v", label, c.Rate, want[2]/want[1])
		}
		if c.Independent == nil || math.Abs(*c.Independent-want[3]/want[4]) > 1e-9 {
			t.Errorf("%s: independent rate %v, want %v", label, c.Independent, want[3]/want[4])
		}
	}
	check("all", got.All, all)
	check("cross-model", got.CrossModel, cross)
	if got.CrossModel.Pairs >= got.All.Pairs {
		t.Errorf("cross-model pairs %d not below all pairs %d; the rows mix models within runs", got.CrossModel.Pairs, got.All.Pairs)
	}

	runs := 0
	perModel := map[string]ModelVerdicts{}
	for _, r := range rows {
		answered := 0
		for _, a := range r.LensAnswers {
			if a.Verdict != "" {
				answered++
			}
			if a.Model != "" {
				m := perModel[a.Model]
				m.Model, m.Lenses = a.Model, m.Lenses+1
				if a.Verdict != "" {
					m.Verdicts++
				}
				perModel[a.Model] = m
			}
		}
		if answered >= 2 {
			runs++
		}
	}
	if got.Runs != runs {
		t.Errorf("runs %d, want %d with two or more lens verdicts", got.Runs, runs)
	}
	if len(got.Models) != len(perModel) || !slices.IsSortedFunc(got.Models, func(a, b ModelVerdicts) int { return strings.Compare(a.Model, b.Model) }) {
		t.Errorf("models %+v, want the %d recorded models sorted", got.Models, len(perModel))
	}
	for _, m := range got.Models {
		if m != perModel[m.Model] {
			t.Errorf("model %+v, want %+v", m, perModel[m.Model])
		}
	}
}

// Lenses that copy one verdict err together every time they err; lenses of
// which at most one errs per run never do; with no error there is no rate.
func TestCorrelatedErrors_Extremes(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 9))
	names := []string{"a", "b", "c", "d"}
	var copied, alone, right []Row
	for i := 0; i < 50; i++ {
		want := []string{Support, Oppose}[rng.IntN(2)]
		given := []string{Support, Oppose}[rng.IntN(2)]
		loner := names[rng.IntN(len(names))]
		c := Row{Want: want, LensAnswers: map[string]LensAnswer{}}
		a := Row{Want: want, LensAnswers: map[string]LensAnswer{}}
		ok := Row{Want: want, LensAnswers: map[string]LensAnswer{}}
		for _, n := range names {
			c.LensAnswers[n] = LensAnswer{Verdict: given, Model: "m"}
			v := want
			if n == loner {
				v = Abstain
			}
			a.LensAnswers[n] = LensAnswer{Verdict: v, Model: "m"}
			ok.LensAnswers[n] = LensAnswer{Verdict: want, Model: "m"}
		}
		copied, alone, right = append(copied, c), append(alone, a), append(right, ok)
	}
	if e := CorrelatedErrors(copied).All; e.Pairs == 0 || e.Rate == nil || *e.Rate != 1 {
		t.Errorf("copied verdicts: %+v, want rate 1", e)
	}
	if e := CorrelatedErrors(alone).All; e.Pairs == 0 || e.Together != 0 || e.Rate == nil || *e.Rate != 0 {
		t.Errorf("one lens wrong per run: %+v, want rate 0", e)
	}
	if e := CorrelatedErrors(right); e.All.Pairs != 0 || e.All.Rate != nil || e.All.Independent != nil || e.CrossModel.Pairs != 0 {
		t.Errorf("no lens erred: %+v, want no pairs and no rates", e)
	}
}

// When each lens errs independently at its own fixed rate, the measured
// rate lands on the independent rate. The seed is fixed, so the draw is
// the same every run.
func TestCorrelatedErrors_IndependentLensesMeetTheBaseline(t *testing.T) {
	rng := rand.New(rand.NewPCG(11, 17))
	names := []string{"a", "b", "c", "d", "e"}
	errRate := map[string]float64{"a": 0.1, "b": 0.2, "c": 0.3, "d": 0.4, "e": 0.5}
	var rows []Row
	for i := 0; i < 4000; i++ {
		r := Row{Want: Support, LensAnswers: map[string]LensAnswer{}}
		for _, n := range names {
			v := Support
			if rng.Float64() < errRate[n] {
				v = Oppose
			}
			r.LensAnswers[n] = LensAnswer{Verdict: v, Model: "m"}
		}
		rows = append(rows, r)
	}
	e := CorrelatedErrors(rows).All
	if e.Rate == nil || e.Independent == nil || math.Abs(*e.Rate-*e.Independent) > 0.02 {
		t.Fatalf("rate %v, independent %v; want them within 0.02 when lenses err independently", e.Rate, e.Independent)
	}
}

// Summarize carries the correlated-error rate, counts the swarm runs by
// diversity state, a run with none as not_recorded, and counts the low
// runs answered wrong. Solo runs have no lenses and are not counted.
func TestSummarize_LensErrorsAndDiversity(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 5))
	rows := randomLensRows(rng, 40, []string{"a", "b", "c"}, []string{"m1", "m2"})
	rows = append(rows, Row{Arm: ArmSolo, Want: Support}, Row{Arm: ArmSolo, Want: Oppose})
	states := []string{"low", "not_low", "unknown", "not_checked", ""}
	want, lowWrong := map[string]int{}, 0
	for i := range rows {
		rows[i].Correct = rng.IntN(2) == 0
		if rows[i].Arm != ArmSwarm {
			continue
		}
		rows[i].Diversity = states[rng.IntN(len(states))]
		key := rows[i].Diversity
		if key == "" {
			key = "not_recorded"
		}
		want[key]++
		if key == "low" && !rows[i].Correct {
			lowWrong++
		}
	}
	s := Summarize(rows)
	if !reflect.DeepEqual(s.LensErrors, CorrelatedErrors(rows)) {
		t.Errorf("summary lens errors %+v, want %+v", s.LensErrors, CorrelatedErrors(rows))
	}
	if !reflect.DeepEqual(s.Diversity, want) || s.DiversityLowWrong != lowWrong {
		t.Errorf("diversity %v (%d low wrong), want %v (%d)", s.Diversity, s.DiversityLowWrong, want, lowWrong)
	}
}
