package design

import (
	"fmt"
	"math"
	"sort"
)

// The decision's metrics, their outcomes and the verdicts
// (bench-design.md § The decision rule).
const (
	metricTests        = "hidden tests"
	metricCompleteness = "plan completeness"
	metricFidelity     = "plan fidelity (reported, not in the rule)"

	Worse    = "worse"     // the sign test says the control won
	NotBelow = "not-below" // no lopsided loss, and the mean difference is at least 0
	Below    = "below"     // no lopsided loss, but the mean difference is negative

	VerdictWorthIt      = "worth-it"
	VerdictNotWorthIt   = "not-worth-it"
	VerdictNoDifference = "no-difference" // every task tied on hidden tests
	VerdictTooFew       = "too-few"
)

// DefaultAlpha is the sign test's level.
const DefaultAlpha = 0.05

// TaskPair is one task's two rows, and the differences the rule reads.
type TaskPair struct {
	Task     string
	Designer Row
	Control  Row
	// TestDiff is the designer's pass rate minus the control's.
	// CompletenessDiff is the designer's plan completeness minus the
	// control's, nil when either arm has no plan. FidelityDiff is the
	// control's deviation rate minus the designer's, nil when either arm
	// has no rate; it is reported and the rule does not read it.
	TestDiff         float64
	CompletenessDiff *float64
	FidelityDiff     *float64
}

// SignTest is one metric's test: the non-tied tasks, the designer's wins
// and losses among them, the one-sided p-value that the designer is worse,
// the mean difference over every paired task, and the outcome.
type SignTest struct {
	Metric   string  `json:"metric"`
	Paired   int     `json:"paired"`
	N        int     `json:"n"`
	Wins     int     `json:"wins"`
	Losses   int     `json:"losses"`
	P        float64 `json:"p"`
	Critical int     `json:"critical"` // the fewest losses of n that reach alpha; 0 when none can
	Mean     float64 `json:"mean_difference"`
	Outcome  string  `json:"outcome"`
	Detail   string  `json:"detail"`
}

// Decision is the rule's result.
type Decision struct {
	Alpha        float64    `json:"alpha"`
	Pairs        []TaskPair `json:"pairs"`
	Dropped      []string   `json:"dropped"` // tasks with one arm only
	Tests        SignTest   `json:"tests"`
	Completeness SignTest   `json:"completeness"`
	Fidelity     SignTest   `json:"fidelity"`
	// CostRatio is the designer arm's tokens, in and out, executor
	// included, over the control's, summed over the paired tasks; nil when
	// the control recorded none.
	CostRatio *float64 `json:"cost_ratio"`
	Verdict   string   `json:"verdict"`
	Reasons   []string `json:"reasons"`
}

// Decide applies the rule to rows: pair by task, sign-test each metric,
// and give the verdict.
func Decide(rows []Row, alpha float64) Decision {
	d := Decision{Alpha: alpha}
	cost := d.pairTasks(rows)
	d.testMetrics()
	d.CostRatio = cost.ratio()
	d.decideVerdict()
	d.addReasons(cost)
	return d
}

// tokenCost is the tokens, in and out, executors included, that each arm
// spent over the paired tasks.
type tokenCost struct{ designer, control int64 }

// ratio is the designer's tokens over the control's, nil when the control
// recorded none.
func (c tokenCost) ratio() *float64 {
	if c.control <= 0 {
		return nil
	}
	r := float64(c.designer) / float64(c.control)
	return &r
}

// pairTasks pairs the rows by task, in name order, and drops a task with
// one arm only. It returns what the paired tasks cost.
func (d *Decision) pairTasks(rows []Row) tokenCost {
	byTask := rowsByTask(rows)
	var cost tokenCost
	for _, n := range sortedTaskNames(byTask) {
		des, okD := byTask[n][ArmDesigner]
		con, okC := byTask[n][ArmControl]
		if !okD || !okC {
			d.Dropped = append(d.Dropped, n)
			continue
		}
		d.Pairs = append(d.Pairs, newTaskPair(n, des, con))
		cost.designer += des.TokensIn + des.TokensOut
		cost.control += con.TokensIn + con.TokensOut
	}
	return cost
}

// rowsByTask files the rows by task and arm.
func rowsByTask(rows []Row) map[string]map[string]Row {
	byTask := map[string]map[string]Row{}
	for _, r := range rows {
		if byTask[r.Task] == nil {
			byTask[r.Task] = map[string]Row{}
		}
		byTask[r.Task][r.Arm] = r
	}
	return byTask
}

// sortedTaskNames is the tasks' names in order.
func sortedTaskNames(byTask map[string]map[string]Row) []string {
	names := make([]string, 0, len(byTask))
	for n := range byTask {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// newTaskPair is one task's pair and its differences.
func newTaskPair(task string, des, con Row) TaskPair {
	return TaskPair{Task: task, Designer: des, Control: con, TestDiff: armRate(des) - armRate(con),
		CompletenessDiff: rateDiff(des.Completeness.Rate, con.Completeness.Rate),
		FidelityDiff:     rateDiff(con.Fidelity.Rate, des.Fidelity.Rate)}
}

// rateDiff is *a − *b, nil when either is nil.
func rateDiff(a, b *float64) *float64 {
	if a == nil || b == nil {
		return nil
	}
	diff := *a - *b
	return &diff
}

// testMetrics sign-tests each metric over the paired tasks, noting the
// tasks a metric dropped.
func (d *Decision) testMetrics() {
	tests, completeness, fidelity := metricDiffs(d.Pairs)
	d.Tests = signTest(metricTests, tests, d.Alpha)
	d.Completeness = signTest(metricCompleteness, completeness, d.Alpha)
	d.Fidelity = signTest(metricFidelity, fidelity, d.Alpha)
	d.Completeness.Detail += droppedNote(len(d.Pairs)-len(completeness), "an arm had no plan")
	d.Fidelity.Detail += droppedNote(len(d.Pairs)-len(fidelity), "an arm had no deviation rate")
}

// metricDiffs is each metric's differences over the pairs that have one.
func metricDiffs(pairs []TaskPair) (tests, completeness, fidelity []float64) {
	for _, p := range pairs {
		tests = append(tests, p.TestDiff)
		if p.CompletenessDiff != nil {
			completeness = append(completeness, *p.CompletenessDiff)
		}
		if p.FidelityDiff != nil {
			fidelity = append(fidelity, *p.FidelityDiff)
		}
	}
	return tests, completeness, fidelity
}

// droppedNote says how many tasks a metric dropped and why, "" for none.
func droppedNote(dropped int, why string) string {
	if dropped <= 0 {
		return ""
	}
	return fmt.Sprintf("; %d task(s) dropped where %s", dropped, why)
}

// decideVerdict gives the verdict, with its reason when there are too few
// tasks or every task tied.
func (d *Decision) decideVerdict() {
	switch {
	case len(d.Pairs) < 2:
		d.Verdict = VerdictTooFew
		d.Reasons = append(d.Reasons, fmt.Sprintf("%d task(s) have both arms; the rule needs two", len(d.Pairs)))
	case d.Tests.N == 0:
		d.Verdict = VerdictNoDifference
		d.Reasons = append(d.Reasons, fmt.Sprintf("every one of %d tasks tied on hidden tests", d.Tests.Paired))
	case d.worthIt():
		d.Verdict = VerdictWorthIt
	default:
		d.Verdict = VerdictNotWorthIt
	}
}

// worthIt: the designer is not below the control on hidden tests, won at
// least one task, and is not below it on plan completeness.
func (d *Decision) worthIt() bool {
	return d.Tests.Outcome == NotBelow && d.Tests.Wins >= 1 && d.Completeness.Outcome == NotBelow
}

// addReasons states each metric's outcome, the token cost, and the tasks
// dropped for having one arm only.
func (d *Decision) addReasons(cost tokenCost) {
	for _, t := range []SignTest{d.Tests, d.Completeness, d.Fidelity} {
		d.Reasons = append(d.Reasons, fmt.Sprintf("%s: %s (%s)", t.Metric, t.Outcome, t.Detail))
	}
	d.Reasons = append(d.Reasons, costLine(d.CostRatio, cost.designer, cost.control))
	if len(d.Dropped) > 0 {
		d.Reasons = append(d.Reasons, fmt.Sprintf("%d task(s) with one arm only were dropped", len(d.Dropped)))
	}
}

// costLine says what the designer arm cost against the control, in tokens,
// executors included.
func costLine(ratio *float64, des, con int64) string {
	if ratio == nil {
		return fmt.Sprintf("token cost: designer %d, control %d; no ratio, the control recorded no tokens", des, con)
	}
	return fmt.Sprintf("token cost: designer %d ÷ control %d = %.2f× (both arms with their executor)", des, con, *ratio)
}

// armRate is an arm's pass rate: 0 for an arm that built nothing.
func armRate(r Row) float64 {
	if r.TestsTotal == 0 {
		return 0
	}
	return float64(r.TestsPassed) / float64(r.TestsTotal)
}

// signTest tests one metric's differences, designer minus control.
func signTest(metric string, diffs []float64, alpha float64) SignTest {
	t := SignTest{Metric: metric, Paired: len(diffs)}
	sum := t.count(diffs)
	t.N = t.Wins + t.Losses
	if t.Paired > 0 {
		t.Mean = sum / float64(t.Paired)
	}
	t.P = BinomialTail(t.N, t.Losses)
	t.Critical = CriticalLosses(t.N, alpha)
	t.Outcome = t.outcome(alpha)
	t.Detail = t.detail()
	return t
}

// count tallies the designer's wins and losses, and returns the
// differences' sum.
func (t *SignTest) count(diffs []float64) float64 {
	sum := 0.0
	for _, x := range diffs {
		sum += x
		switch {
		case x > 0:
			t.Wins++
		case x < 0:
			t.Losses++
		}
	}
	return sum
}

// outcome is worse when the test reaches alpha, else not-below or below by
// the sign of the mean difference.
func (t *SignTest) outcome(alpha float64) string {
	switch {
	case t.N > 0 && t.P <= alpha:
		return Worse
	case t.Mean >= 0:
		return NotBelow
	}
	return Below
}

// detail says what the test counted and what it can conclude.
func (t *SignTest) detail() string {
	switch {
	case t.Paired == 0:
		return "no task paired"
	case t.N == 0:
		return fmt.Sprintf("every one of %d tasks tied; mean difference %+.3f", t.Paired, t.Mean)
	}
	return fmt.Sprintf("designer won %d and lost %d of %d non-tied tasks (%d tied), p = %.3f that it is worse", t.Wins, t.Losses, t.N, t.Paired-t.N, t.P) + t.reach()
}

// reach says at how many losses the test calls the designer worse, or that
// it never can.
func (t *SignTest) reach() string {
	if t.Critical > 0 {
		return fmt.Sprintf(", worse at %d or more losses; mean difference %+.3f", t.Critical, t.Mean)
	}
	return fmt.Sprintf("; %d non-tied tasks can never reach alpha; mean difference %+.3f", t.N, t.Mean)
}

// BinomialTail is P(Bin(n, ½) ≥ k), 1 when n is 0.
func BinomialTail(n, k int) float64 {
	if n <= 0 {
		return 1
	}
	if k <= 0 {
		return 1
	}
	sum := 0.0
	for i := k; i <= n; i++ {
		sum += math.Exp(lchoose(n, i))
	}
	return sum / math.Pow(2, float64(n))
}

// lchoose is ln C(n, k).
func lchoose(n, k int) float64 {
	a, _ := math.Lgamma(float64(n + 1))
	b, _ := math.Lgamma(float64(k + 1))
	c, _ := math.Lgamma(float64(n - k + 1))
	return a - b - c
}

// CriticalLosses is the fewest losses of n at which the one-sided sign test
// reaches alpha, 0 when no count of n does.
func CriticalLosses(n int, alpha float64) int {
	for k := 0; k <= n; k++ {
		if BinomialTail(n, k) <= alpha {
			return k
		}
	}
	return 0
}

// ArmMeans are one arm's means over its rows.
type ArmMeans struct {
	Arm                 string   `json:"arm"`
	Rows                int      `json:"rows"`
	Completed           int      `json:"completed"`
	PassRate            float64  `json:"pass_rate"`
	PlanCompleteness    *float64 `json:"plan_completeness"`
	DeviationRate       *float64 `json:"deviation_rate"`
	FileCoverage        *float64 `json:"file_coverage"`
	AuditPassed         int      `json:"audit_passed"`
	GuaranteesHonest    float64  `json:"guarantees_with_premises_rate"`
	WallSeconds         float64  `json:"wall_seconds"`
	TokensIn, TokensOut int64
}

// Summarize gives each arm's means, designer first.
func Summarize(rows []Row) []ArmMeans {
	by := map[string][]Row{}
	for _, r := range rows {
		by[r.Arm] = append(by[r.Arm], r)
	}
	var out []ArmMeans
	for _, arm := range []string{ArmDesigner, ArmControl} {
		if rs := by[arm]; len(rs) > 0 {
			out = append(out, armMeans(arm, rs))
		}
	}
	return out
}

// armMeans is one arm's means over its rows.
func armMeans(arm string, rs []Row) ArmMeans {
	m := ArmMeans{Arm: arm, Rows: len(rs)}
	var comp, dev, cov []float64
	g, gp := 0, 0
	for _, r := range rs {
		m.add(r)
		comp, dev, cov = appendRate(comp, r.Completeness.Rate), appendRate(dev, r.Fidelity.Rate), appendRate(cov, r.Coverage.Rate)
		g += r.Guarantees
		gp += r.GuaranteesWithPremises
	}
	n := float64(len(rs))
	m.PassRate /= n
	m.WallSeconds /= n
	m.PlanCompleteness = mean(comp)
	m.DeviationRate = mean(dev)
	m.FileCoverage = mean(cov)
	if g > 0 {
		m.GuaranteesHonest = float64(gp) / float64(g)
	}
	return m
}

// add counts one row into the arm's totals.
func (m *ArmMeans) add(r Row) {
	if r.Completed {
		m.Completed++
	}
	m.PassRate += armRate(r)
	if r.AuditPass {
		m.AuditPassed++
	}
	m.WallSeconds += r.WallSeconds
	m.TokensIn += r.TokensIn
	m.TokensOut += r.TokensOut
}

// appendRate appends *rate to xs, when there is one.
func appendRate(xs []float64, rate *float64) []float64 {
	if rate == nil {
		return xs
	}
	return append(xs, *rate)
}

// mean is the mean of xs, nil when xs is empty.
func mean(xs []float64) *float64 {
	if len(xs) == 0 {
		return nil
	}
	s := 0.0
	for _, x := range xs {
		s += x
	}
	s /= float64(len(xs))
	return &s
}
