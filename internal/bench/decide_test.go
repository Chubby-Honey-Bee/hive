package bench

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shapeItem is one item of a fixture laid out the way Generate lays out a
// run: per seed, one twin pair per family. Each binary family's pair holds
// one support and one oppose twin; the AB pair holds a support or oppose
// twin and an abstain twin. At four seeds that is 26 support, 26 oppose
// and 4 abstain items, the mix the shipped suite produces.
type shapeItem struct {
	family, variant, want string
	seed                  int64
	binary                int // index among the binary pairs; -1 for AB
}

func (s shapeItem) pair() string { return fmt.Sprintf("%s-s%d", s.family, s.seed) }
func (s shapeItem) id() string   { return s.pair() + "-" + s.variant }

func benchShape(seeds []int64) []shapeItem {
	var out []shapeItem
	binary := 0
	for _, fam := range []string{"F1", "F2", "F3", "F4", "F6", "F8", "AB"} {
		for i, seed := range seeds {
			switch {
			case fam == "AB":
				first := Support
				if i%2 == 1 {
					first = Oppose
				}
				out = append(out, shapeItem{fam, "a", first, seed, -1}, shapeItem{fam, "b", Abstain, seed, -1})
			case fam < "F4":
				out = append(out, shapeItem{fam, "a", Support, seed, binary}, shapeItem{fam, "b", Oppose, seed, binary})
				binary++
			default:
				out = append(out, shapeItem{fam, "a", Oppose, seed, binary}, shapeItem{fam, "b", Support, seed, binary})
				binary++
			}
		}
	}
	return out
}

// policy answers an item: the verdict given and the token F1 earned.
type policy func(shapeItem) (string, float64)

func other(want string) string {
	if want == Support {
		return Oppose
	}
	return Support
}

func perfect(s shapeItem) (string, float64) { return s.want, 1 }

// constant answers v to everything and names tokens right only when v is
// the expected verdict.
func constant(v string) policy {
	return func(s shapeItem) (string, float64) {
		if v == s.want {
			return v, 1
		}
		return v, 0
	}
}

// wrongTwins is right on every item except the support twins of the first
// kS binary pairs and the oppose twins of the kO pairs after them, which
// get the other verdict. With tokens set it names every item's tokens
// right, so only its verdicts differ from a perfect answer.
func wrongTwins(kS, kO int, tokens bool) policy {
	return func(s shapeItem) (string, float64) {
		wrong := s.binary >= 0 && (s.want == Support && s.binary < kS || s.want == Oppose && s.binary >= kS && s.binary < kS+kO)
		switch {
		case !wrong:
			return s.want, 1
		case tokens:
			return other(s.want), 1
		default:
			return other(s.want), 0
		}
	}
}

// rowsFor answers every item by p. Item hashes depend on the item alone, so
// configurations pair, and selection and confirmation items differ.
func rowsFor(config, arm string, items []shapeItem, p policy, wall float64) []Row {
	var rows []Row
	zero := 0
	for _, s := range items {
		got, f1 := p(s)
		r := Row{
			Config: config, Arm: arm, Item: s.id(), ItemHash: "h-" + s.id(), Pair: s.pair(), Family: s.family,
			Seed: s.seed, Variant: s.variant, Rep: 1, Want: s.want, Got: got, Correct: got == s.want,
			Completed: true, TreeOK: true, Lenses: 7, FullVerdicts: 7, Outputs: 7, SchemaValid: 7,
			FinishLength: &zero, WallSeconds: wall,
		}
		if s.family != "AB" {
			r.TokenF1 = &f1
		}
		rows = append(rows, r)
	}
	return rows
}

var (
	selSeeds  = []int64{1, 2, 3, 4}
	confSeeds = []int64{101, 102, 103, 104}
	selItems  = benchShape(selSeeds)
	confItems = benchShape(confSeeds)
)

func fixtureRule() Rule {
	r := DefaultRule()
	r.Reference, r.Negative = "R", "N"
	r.Configs = map[string]ConfigSpec{"C": {MemoryGB: 10}}
	return r
}

// config builds a configuration's swarm and solo rows on the selection seeds.
func config(name string, swarm, solo policy, wall float64) []Row {
	return append(rowsFor(name, ArmSwarm, selItems, swarm, wall), rowsFor(name, ArmSolo, selItems, solo, wall/7)...)
}

var (
	// ref and neg are a perfect reference and a negative control that
	// always says support: it fails the oppose floor and fabricates on
	// every abstain item.
	ref = config("R", perfect, constant(Support), 100)
	neg = config("N", constant(Support), constant(Support), 30)
)

func resultFor(d Decision, name string) ConfigResult {
	for _, c := range d.Configs {
		if c.Config == name {
			return c
		}
	}
	return ConfigResult{}
}

func marginFor(st Stage, metric string) Margin {
	for _, m := range st.Margins {
		if m.Metric == metric {
			return m
		}
	}
	return Margin{}
}

func join(parts ...[]Row) []Row {
	var out []Row
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// twinDiffsOf computes, from the policies alone, each binary pair's
// candidate-minus-reference difference on its support and oppose twin, in
// accuracy and in token F1.
func twinDiffsOf(items []shapeItem, c, r policy) (acc, f1 [][2]float64) {
	type diffs struct{ acc, f1 [2]float64 }
	byPair := map[int]*diffs{}
	var order []int
	for _, s := range items {
		if s.binary < 0 {
			continue
		}
		d := byPair[s.binary]
		if d == nil {
			d = &diffs{}
			byPair[s.binary] = d
			order = append(order, s.binary)
		}
		cv, cf := c(s)
		rv, rf := r(s)
		i := 0
		if s.want == Oppose {
			i = 1
		}
		d.acc[i] = b2f(cv == s.want) - b2f(rv == s.want)
		d.f1[i] = cf - rf
	}
	for _, k := range order {
		acc = append(acc, byPair[k].acc)
		f1 = append(f1, byPair[k].f1)
	}
	return acc, f1
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// recompute is the margin written out from its definition: over m pairs
// of (x, y) differences, the difference is (mean x + mean y)/2; each
// twin's variance is floored at delta(1-delta); the covariance is the
// sample correlation times the floored deviations; the pair's variance is
// floored at delta(1-2 delta)/2; SE is its square root over m; the bounds
// are the difference ∓ the 1-alpha quantile of t on m-1 degrees of
// freedom times SE.
func recompute(pairs [][2]float64, delta, alpha float64) (diff, lower, upper float64, status string) {
	m := float64(len(pairs))
	z := studentTQuantile(1-alpha, len(pairs)-1)
	var sx, sy float64
	for _, p := range pairs {
		sx += p[0]
		sy += p[1]
	}
	mx, my := sx/m, sy/m
	var vxx, vyy, vxy float64
	for _, p := range pairs {
		vxx += (p[0] - mx) * (p[0] - mx) / (m - 1)
		vyy += (p[1] - my) * (p[1] - my) / (m - 1)
		vxy += (p[0] - mx) * (p[1] - my) / (m - 1)
	}
	rho := 0.0
	if vxx > 0 && vyy > 0 {
		rho = vxy / math.Sqrt(vxx) / math.Sqrt(vyy)
	}
	fx, fy := math.Max(vxx, delta*(1-delta)), math.Max(vyy, delta*(1-delta))
	v := math.Max((fx+fy+2*rho*math.Sqrt(fx)*math.Sqrt(fy))/4, delta*(1-2*delta)/2)
	se := math.Sqrt(v / m)
	diff = (mx + my) / 2
	lower, upper = diff-z*se, diff+z*se
	switch {
	case lower > -delta:
		status = Pass
	case upper < -delta:
		status = Fail
	default:
		status = Inconclusive
	}
	return diff, lower, upper, status
}

// checkMargin compares a margin with its recomputation and asserts the
// status the fixture was built to reach.
func checkMargin(t *testing.T, m Margin, pairs [][2]float64, rule Rule, want string) {
	t.Helper()
	diff, lower, upper, status := recompute(pairs, rule.Delta, rule.Alpha)
	if m.Pairs != len(pairs) || math.Abs(m.Diff-diff) > 1e-12 || math.Abs(m.Lower-lower) > 1e-12 || math.Abs(m.Upper-upper) > 1e-12 || m.Status != status {
		t.Fatalf("%s margin %+v; recomputed over %d pairs: diff %v bounds [%v, %v] status %s", m.Metric, m, len(pairs), diff, lower, upper, status)
	}
	if m.Status != want {
		t.Fatalf("%s margin is %s; the fixture was built to make it %s", m.Metric, m.Status, want)
	}
}

// The six outcomes the rule must reach, each for the reason the fixture was
// built around, on items shaped like the shipped suite's.
func TestDecide_SixFixtures(t *testing.T) {
	rule := fixtureRule()
	good := wrongTwins(1, 1, true)
	confirm := join(rowsFor("R", ArmSwarm, confItems, perfect, 100), rowsFor("C", ArmSwarm, confItems, good, 50))

	t.Run("accept", func(t *testing.T) {
		d := Decide(join(ref, neg, config("C", good, constant(Support), 50), confirm), rule)
		c := resultFor(d, "C")
		if d.Verdict != "decided" || c.Outcome != OutcomeAccept {
			t.Fatalf("verdict %s %v, C %s %v; want decided, accept", d.Verdict, d.Reasons, c.Outcome, c.Reasons)
		}
		acc, f1 := twinDiffsOf(selItems, good, perfect)
		checkMargin(t, marginFor(c.Selection, MetricAccuracy), acc, rule, Pass)
		checkMargin(t, marginFor(c.Selection, MetricTokenF1), f1, rule, Pass)
		if len(d.Choices) != 1 || d.Choices[0].Config != "C" || d.Choices[0].Outcome != OutcomeAccept {
			t.Fatalf("choices %+v, want C accepted", d.Choices)
		}
		if resultFor(d, "N").Outcome != OutcomeRejected {
			t.Fatalf("the negative control was not rejected: %+v", resultFor(d, "N"))
		}
	})

	t.Run("guard failure", func(t *testing.T) {
		// Wrong on 9 oppose twins: oppose accuracy 17/26 is under the floor.
		d := Decide(join(ref, neg, config("C", wrongTwins(0, 9, true), constant(Support), 50)), rule)
		c := resultFor(d, "C")
		if c.Outcome != OutcomeRejectGuard || !strings.Contains(strings.Join(c.Reasons, " "), GuardOppose) {
			t.Fatalf("C %s %v; want reject-guard naming %q", c.Outcome, c.Reasons, GuardOppose)
		}
	})

	t.Run("margin failure", func(t *testing.T) {
		// Wrong on 7 support and 7 oppose twins: each class keeps 19/26,
		// above the floor, and every token is named right, so only the
		// accuracy margin can reject it.
		p := wrongTwins(7, 7, true)
		d := Decide(join(ref, neg, config("C", p, constant(Support), 50)), rule)
		c := resultFor(d, "C")
		if c.Outcome != OutcomeRejectMargin {
			t.Fatalf("C %s %v; want reject-margin", c.Outcome, c.Reasons)
		}
		if failed := guardNames(c.Selection, Fail); len(failed) > 0 {
			t.Fatalf("guards %v failed; the fixture must fail only the margin", failed)
		}
		acc, f1 := twinDiffsOf(selItems, p, perfect)
		checkMargin(t, marginFor(c.Selection, MetricAccuracy), acc, rule, Fail)
		checkMargin(t, marginFor(c.Selection, MetricTokenF1), f1, rule, Pass)
	})

	t.Run("inconclusive", func(t *testing.T) {
		p := wrongTwins(3, 3, true)
		d := Decide(join(ref, neg, config("C", p, constant(Support), 50)), rule)
		c := resultFor(d, "C")
		if c.Outcome != OutcomeInconclusive {
			t.Fatalf("C %s %v; want inconclusive", c.Outcome, c.Reasons)
		}
		acc, f1 := twinDiffsOf(selItems, p, perfect)
		checkMargin(t, marginFor(c.Selection, MetricAccuracy), acc, rule, Inconclusive)
		checkMargin(t, marginFor(c.Selection, MetricTokenF1), f1, rule, Pass)
	})

	t.Run("lift at or below zero", func(t *testing.T) {
		d := Decide(join(ref, neg, config("C", good, perfect, 50)), rule)
		c := resultFor(d, "C")
		if c.Outcome != OutcomeOneCall || c.Lift == nil || *c.Lift > 0 {
			t.Fatalf("C %s lift %v; want one-call with lift <= 0", c.Outcome, c.Lift)
		}
	})

	t.Run("void: the negative control passed", func(t *testing.T) {
		d := Decide(join(ref, config("N", perfect, perfect, 30), config("C", good, constant(Support), 50)), rule)
		if d.Verdict != "void" || resultFor(d, "N").Outcome != OutcomeNotRejected {
			t.Fatalf("verdict %s, N %s; want void, not-rejected", d.Verdict, resultFor(d, "N").Outcome)
		}
		if len(d.Choices) != 0 {
			t.Fatalf("a void run chose %+v", d.Choices)
		}
	})
}

// A negative control that answers well but fails a process guard is not
// rejected: the guard says nothing about whether the items separate a bad
// model from the reference.
func TestDecide_NegativeControlNeedsAQualityFailure(t *testing.T) {
	sloppy := config("N", perfect, perfect, 30)
	for i := range sloppy {
		sloppy[i].SchemaValid = 0
	}
	d := Decide(join(ref, sloppy, config("C", wrongTwins(1, 1, true), constant(Support), 50)), fixtureRule())
	n := resultFor(d, "N")
	if !contains(guardNames(n.Selection, Fail), GuardSchema) {
		t.Fatalf("N guards failed %v; the fixture must fail %q", guardNames(n.Selection, Fail), GuardSchema)
	}
	if d.Verdict != "void" || n.Outcome != OutcomeNotRejected {
		t.Fatalf("verdict %s, N %s; want void, not-rejected", d.Verdict, n.Outcome)
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// Class-balanced accuracy is the mean of support and oppose accuracy:
// neither the pooled rate nor a mean that gives the abstain class a third.
func TestSummarize_ClassBalancedIsSupportAndOppose(t *testing.T) {
	var rows []Row
	add := func(want string, n, correct int) {
		for i := 0; i < n; i++ {
			rows = append(rows, Row{Want: want, Correct: i < correct, Pair: fmt.Sprintf("%s%d", want, i), Variant: "a"})
		}
	}
	add(Support, 30, 30)
	add(Oppose, 10, 5)
	add(Abstain, 4, 0)
	s := Summarize(rows)
	want := (30.0/30 + 5.0/10) / 2
	pooled := float64(30+5+0) / float64(30+10+4)
	withAbstain := (30.0/30 + 5.0/10 + 0.0/4) / 3
	if math.Abs(s.ClassBalanced-want) > 1e-12 {
		t.Fatalf("class-balanced %v, want %v (pooled would be %v, with abstain as a class %v)", s.ClassBalanced, want, pooled, withAbstain)
	}
}

// The pair is the unit. Two candidates err on the same number of twins;
// one errs on both twins of fewer pairs. Their differences are equal but
// the correlated one is less certain, and the margin's bounds match the
// pair-level recomputation, not a count of independent items.
func TestMargin_ThePairIsTheUnit(t *testing.T) {
	rule := DefaultRule()
	build := func(both, one int) []twinDiff {
		var ps []twinDiff
		for i := 0; i < 24; i++ {
			switch {
			case i < both:
				ps = append(ps, twinDiff{-1, -1})
			case i < both+one:
				ps = append(ps, twinDiff{-1, 0})
			case i < both+2*one:
				ps = append(ps, twinDiff{0, -1})
			default:
				ps = append(ps, twinDiff{0, 0})
			}
		}
		return ps
	}
	together, apart := build(5, 0), build(0, 5)
	mt, ma := margin(MetricAccuracy, together, rule.Delta, rule.Alpha), margin(MetricAccuracy, apart, rule.Delta, rule.Alpha)
	if mt.Diff != ma.Diff {
		t.Fatalf("diffs %v and %v; the fixtures must err on as many twins", mt.Diff, ma.Diff)
	}
	for _, c := range []struct {
		m  Margin
		ps []twinDiff
	}{{mt, together}, {ma, apart}} {
		var pairs [][2]float64
		for _, p := range c.ps {
			pairs = append(pairs, [2]float64{p.support, p.oppose})
		}
		_, lower, upper, _ := recompute(pairs, rule.Delta, rule.Alpha)
		if math.Abs(c.m.Lower-lower) > 1e-12 || math.Abs(c.m.Upper-upper) > 1e-12 {
			t.Fatalf("bounds [%v, %v], recomputed [%v, %v]", c.m.Lower, c.m.Upper, lower, upper)
		}
	}
	if mt.Diff-mt.Lower <= ma.Diff-ma.Lower {
		t.Fatalf("errors on both twins of a pair gave a half-width %v, not above the %v of errors on separate pairs", mt.Diff-mt.Lower, ma.Diff-ma.Lower)
	}
}

// At Bench-1's size, 24 support/oppose twin pairs, a candidate exactly
// delta worse than a reference that never errs passes the accuracy margin
// with a probability of at most alpha, however its errors fall on the two
// twins of a pair. The probability is exact: the sum, over every count of
// the four twin outcomes, of its multinomial probability where the margin
// passes.
func TestMargin_LevelAtTheMargin(t *testing.T) {
	const m = 24
	rule := DefaultRule()
	e := rule.Delta
	outcomes := [4]twinDiff{{0, 0}, {-1, 0}, {0, -1}, {-1, -1}}
	for _, s := range []struct {
		name string
		p    [4]float64 // P(both right), P(support twin wrong only), P(oppose twin wrong only), P(both wrong)
	}{
		{"independent twins", [4]float64{(1 - e) * (1 - e), e * (1 - e), e * (1 - e), e * e}},
		{"only oppose twins err", [4]float64{1 - 2*e, 0, 2 * e, 0}},
		{"both twins err together", [4]float64{1 - e, 0, 0, e}},
		{"never both twins", [4]float64{1 - 2*e, e, e, 0}},
	} {
		mean := 0.0
		for i, o := range outcomes {
			mean += s.p[i] * (o.support + o.oppose) / 2
		}
		if math.Abs(mean+rule.Delta) > 1e-12 {
			t.Fatalf("%s: true difference %v, not -delta", s.name, mean)
		}
		level := 0.0
		for n3 := 0; n3 <= m; n3++ {
			for n2 := 0; n2 <= m-n3; n2++ {
				for n1 := 0; n1 <= m-n3-n2; n1++ {
					counts := [4]int{m - n1 - n2 - n3, n1, n2, n3}
					lp, _ := math.Lgamma(m + 1)
					possible := true
					var pairs []twinDiff
					for i, k := range counts {
						if k > 0 && s.p[i] == 0 {
							possible = false
							break
						}
						lk, _ := math.Lgamma(float64(k + 1))
						lp -= lk
						if k > 0 {
							lp += float64(k) * math.Log(s.p[i])
						}
						for j := 0; j < k; j++ {
							pairs = append(pairs, outcomes[i])
						}
					}
					if possible && margin(MetricAccuracy, pairs, rule.Delta, rule.Alpha).Status == Pass {
						level += math.Exp(lp)
					}
				}
			}
		}
		t.Logf("%s: P(pass) = %.4f", s.name, level)
		if level > rule.Alpha {
			t.Errorf("%s: a candidate delta worse passes with probability %.4f, above alpha = %.3f", s.name, level, rule.Alpha)
		}
	}
}

// When the reference errs too, the pass rate is simulated: 40,000 draws of
// 24 twin pairs from a fixed seed. At the margin it stays below 1.5 alpha
// in every case tried. Away from the margin the test keeps at least 60%
// power in the two cases tried. The rates are logged; the spec quotes them.
func TestMargin_SimulatedLevelAndPower(t *testing.T) {
	const m, draws = 24, 40000
	rule := DefaultRule()
	rng := rand.New(rand.NewPCG(24, 15))
	right := func(errRate float64) float64 { return b2f(rng.Float64() >= errRate) }
	for _, s := range []struct {
		name     string
		ref      float64 // the reference's error rate on each twin, independently
		sup, opp float64 // the candidate's error rate on its support and oppose twins
		together bool    // one draw decides both of the candidate's twins
		atMargin bool    // true: bound caps the pass rate; false: it floors it
		bound    float64
	}{
		{"reference 5%, candidate 20%, independent twins", 0.05, 0.20, 0.20, false, true, 1.5 * rule.Alpha},
		{"reference 10%, candidate 25%, independent twins", 0.10, 0.25, 0.25, false, true, 1.5 * rule.Alpha},
		{"reference 10%, candidate 25%, both twins together", 0.10, 0.25, 0.25, true, true, 1.5 * rule.Alpha},
		{"reference 10%, candidate 40% on oppose twins only", 0.10, 0.10, 0.40, false, true, 1.5 * rule.Alpha},
		{"reference never errs, candidate 5%, independent twins", 0, 0.05, 0.05, false, false, 0.6},
		{"both err 10%, independent twins", 0.10, 0.10, 0.10, false, false, 0.6},
	} {
		if diff := ((s.ref - s.sup) + (s.ref - s.opp)) / 2; s.atMargin && math.Abs(diff+rule.Delta) > 1e-12 {
			t.Fatalf("%s: true difference %v, not -delta", s.name, diff)
		}
		passed := 0
		pairs := make([]twinDiff, m)
		for d := 0; d < draws; d++ {
			for i := range pairs {
				var cs, co float64
				if s.together {
					u := rng.Float64()
					cs, co = b2f(u >= s.sup), b2f(u >= s.opp)
				} else {
					cs, co = right(s.sup), right(s.opp)
				}
				pairs[i] = twinDiff{cs - right(s.ref), co - right(s.ref)}
			}
			if margin(MetricAccuracy, pairs, rule.Delta, rule.Alpha).Status == Pass {
				passed++
			}
		}
		rate := float64(passed) / draws
		t.Logf("%s: P(pass) = %.4f", s.name, rate)
		if s.atMargin && rate > s.bound {
			t.Errorf("%s: a candidate delta worse passes with probability %.4f, above %.3f", s.name, rate, s.bound)
		}
		if !s.atMargin && rate < s.bound {
			t.Errorf("%s: power %.4f, below %.2f", s.name, rate, s.bound)
		}
	}
}

// Without its confirmation runs a passing choice is pending; with a failed
// confirmation it is inconclusive and the next configuration is tried.
func TestDecide_ConfirmationGatesTheChoice(t *testing.T) {
	rule := fixtureRule()
	rule.Configs = map[string]ConfigSpec{"C": {MemoryGB: 10}, "D": {MemoryGB: 10}}
	good := wrongTwins(1, 1, true)
	sel := join(ref, neg, config("C", good, constant(Support), 50), config("D", good, constant(Support), 80))

	d := Decide(sel, rule)
	if ch := d.Choices[0]; ch.Config != "C" || ch.Outcome != OutcomePending {
		t.Fatalf("choice %+v; want C pending (fastest, no confirmation runs)", ch)
	}

	conf := join(rowsFor("R", ArmSwarm, confItems, perfect, 100),
		rowsFor("C", ArmSwarm, confItems, wrongTwins(7, 7, false), 50),
		rowsFor("D", ArmSwarm, confItems, good, 80))
	d = Decide(join(sel, conf), rule)
	if ch := d.Choices[0]; ch.Config != "D" || ch.Outcome != OutcomeAccept || strings.Join(ch.Tried, ",") != "C,D" {
		t.Fatalf("choice %+v; want D accepted after C failed confirmation", ch)
	}
	if c := resultFor(d, "C"); c.Outcome != OutcomeInconclusive {
		t.Fatalf("C %s; a failed confirmation is inconclusive", c.Outcome)
	}
}

// Within WallTie of the fastest, the configuration needing less memory
// wins; a class too small for a configuration never gets it.
func TestDecide_ChoosesPerMachineClass(t *testing.T) {
	rule := fixtureRule()
	rule.Configs = map[string]ConfigSpec{"fast": {MemoryGB: 30}, "lean": {MemoryGB: 6}}
	rule.Classes = []MachineClass{{Name: "big", MemoryGB: 64}, {Name: "small", MemoryGB: 8}, {Name: "tiny", MemoryGB: 4}}
	good := wrongTwins(1, 1, true)
	rows := join(ref, neg, config("fast", good, constant(Support), 50), config("lean", good, constant(Support), 54),
		rowsFor("R", ArmSwarm, confItems, perfect, 100),
		rowsFor("fast", ArmSwarm, confItems, good, 50),
		rowsFor("lean", ArmSwarm, confItems, good, 54))
	d := Decide(rows, rule)
	got := map[string]string{}
	for _, ch := range d.Choices {
		got[ch.Class] = ch.Outcome + " " + ch.Config
	}
	want := map[string]string{"big": "accept lean", "small": "accept lean", "tiny": "none "}
	for class, w := range want {
		if got[class] != w {
			t.Errorf("class %s: %q, want %q", class, got[class], w)
		}
	}
}

// A guard the rows cannot measure never passes: finish reasons are not
// recorded yet, so the reference's guard is unmeasured and no choice is made.
func TestDecide_UnmeasuredReferenceGuardIsInconclusive(t *testing.T) {
	rule := fixtureRule()
	r := config("R", perfect, constant(Support), 100)
	for i := range r {
		r[i].FinishLength = nil
	}
	d := Decide(join(r, neg, config("C", wrongTwins(1, 1, true), constant(Support), 50)), rule)
	if d.Verdict != "inconclusive" || !strings.Contains(strings.Join(d.Reasons, " "), GuardFinish) {
		t.Fatalf("verdict %s %v; want inconclusive naming %q", d.Verdict, d.Reasons, GuardFinish)
	}
	if len(d.Choices) != 0 {
		t.Fatalf("an inconclusive run chose %+v", d.Choices)
	}
}

// Confirmation items that repeat selection items void the run.
func TestDecide_OverlappingSplitsAreVoid(t *testing.T) {
	rule := fixtureRule()
	leak := rowsFor("R", ArmSwarm, confItems, perfect, 100)
	for i := range leak {
		leak[i].ItemHash = ref[i].ItemHash
	}
	d := Decide(join(ref, neg, leak), rule)
	if d.Verdict != "void" || len(d.Overlap) == 0 {
		t.Fatalf("verdict %s overlap %d; want void", d.Verdict, len(d.Overlap))
	}
}

// The Wilson lower bound L solves (p̂ − L)² = z²·L(1 − L)/n, below p̂.
func TestWilsonLowerSolvesTheScoreEquation(t *testing.T) {
	z := NormalQuantile(0.975)
	for _, c := range []struct{ k, n int }{{24, 24}, {20, 24}, {37, 40}, {1, 10}} {
		l := WilsonLower(c.k, c.n, z)
		p := float64(c.k) / float64(c.n)
		lhs, rhs := (p-l)*(p-l), z*z*l*(1-l)/float64(c.n)
		if l > p || math.Abs(lhs-rhs) > 1e-12 {
			t.Errorf("k=%d n=%d: L=%v, (p-L)^2=%v, z^2 L(1-L)/n=%v", c.k, c.n, l, lhs, rhs)
		}
	}
}

// NormalQuantile inverts the normal CDF, Φ(x) = (1 + erf(x/√2))/2.
func TestNormalQuantileInvertsTheCDF(t *testing.T) {
	for _, p := range []float64{0.5, 0.9, 0.95, 0.975, 0.999} {
		x := NormalQuantile(p)
		if cdf := (1 + math.Erf(x/math.Sqrt2)) / 2; math.Abs(cdf-p) > 1e-12 {
			t.Errorf("Φ(NormalQuantile(%v)) = %v", p, cdf)
		}
	}
}

// The t quantile matches the closed forms for 1 and 2 degrees of freedom,
// tan(π(p − ½)) and (2p − 1)/√(2p(1 − p)), and nears the normal quantile as
// the degrees of freedom grow.
func TestStudentTQuantile(t *testing.T) {
	for _, p := range []float64{0.6, 0.9, 0.95, 0.975, 0.999} {
		for _, c := range []struct {
			df   int
			want float64
		}{
			{1, math.Tan(math.Pi * (p - 0.5))},
			{2, (2*p - 1) / math.Sqrt(2*p*(1-p))},
		} {
			if got := studentTQuantile(p, c.df); math.Abs(got-c.want) > 1e-9*math.Max(1, math.Abs(c.want)) {
				t.Errorf("t quantile p=%v df=%d: %v, want %v", p, c.df, got, c.want)
			}
		}
		if got, z := studentTQuantile(p, 100000), NormalQuantile(p); math.Abs(got-z) > 1e-4 {
			t.Errorf("t quantile p=%v df=100000: %v, the normal quantile is %v", p, got, z)
		}
	}
}

// The t CDF equals ½ plus the integral of the t density from 0, done by
// Simpson's rule, and the quantile inverts it.
func TestStudentTCDFIntegratesTheDensity(t *testing.T) {
	for _, df := range []int{3, 4, 5, 10, 23, 30} {
		nu := float64(df)
		lg1, _ := math.Lgamma((nu + 1) / 2)
		lg2, _ := math.Lgamma(nu / 2)
		density := func(u float64) float64 {
			return math.Exp(lg1-lg2) / math.Sqrt(nu*math.Pi) * math.Pow(1+u*u/nu, -(nu+1)/2)
		}
		for _, x := range []float64{-2.5, 0.5, 1, 1.714, 3} {
			const steps = 4000
			h := x / steps
			sum := density(0) + density(x)
			for i := 1; i < steps; i++ {
				w := 2.0
				if i%2 == 1 {
					w = 4
				}
				sum += w * density(float64(i)*h)
			}
			if want, got := 0.5+sum*h/3, studentTCDF(x, df); math.Abs(got-want) > 1e-10 {
				t.Errorf("t CDF(%v; df=%d) = %v, the integral gives %v", x, df, got, want)
			}
		}
		for _, p := range []float64{0.05, 0.9, 0.95, 0.99} {
			if got := studentTCDF(studentTQuantile(p, df), df); math.Abs(got-p) > 1e-12 {
				t.Errorf("CDF(quantile(%v; df=%d)) = %v", p, df, got)
			}
		}
	}
}

func TestLoadRule(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "rule.yaml")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	r, err := LoadRule(write("reference: R\nnegative: N\ndelta: 0.1\n"))
	if err != nil {
		t.Fatal(err)
	}
	def := DefaultRule()
	if r.Delta != 0.1 || r.Alpha != def.Alpha || r.ClassFloor != def.ClassFloor || len(r.SelectionSeeds) != len(def.SelectionSeeds) {
		t.Fatalf("rule %+v: want delta overridden and every other value defaulted", r)
	}
	for _, bad := range []string{
		"negative: N\n",
		"reference: R\nnegative: R\n",
		"reference: R\nnegative: N\ndelta: 0.5\n",
		"reference: R\nnegative: N\nselection_seeds: [1]\nconfirmation_seeds: [1]\n",
	} {
		if _, err := LoadRule(write(bad)); err == nil {
			t.Errorf("rule %q was accepted", bad)
		}
	}
	if _, err := LoadRule(filepath.Join("..", "..", "fixtures", "bench", "rule.yaml")); err != nil {
		t.Fatalf("the shipped rule does not load: %v", err)
	}
}
