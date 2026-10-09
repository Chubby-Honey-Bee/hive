package design

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

// choose is C(n, k) by Pascal's rule, an independent route to the tail.
func choose(n, k int) float64 {
	if k < 0 || k > n {
		return 0
	}
	row := []float64{1}
	for i := 1; i <= n; i++ {
		next := make([]float64, i+1)
		next[0], next[i] = 1, 1
		for j := 1; j < i; j++ {
			next[j] = row[j-1] + row[j]
		}
		row = next
	}
	return row[k]
}

func tail2(n, k int) float64 {
	s := 0.0
	for i := k; i <= n; i++ {
		s += choose(n, i)
	}
	return s / math.Pow(2, float64(n))
}

// The sign test's limits, computed from the binomial: the fewest losses of
// n that reach alpha, for every n up to 12, and the chance a designer that
// truly loses 80% of the time escapes it at n = 12.
func TestDecide_SignTestLimits(t *testing.T) {
	for n := 0; n <= 12; n++ {
		want := 0
		for k := 0; k <= n; k++ {
			if tail2(n, k) <= DefaultAlpha {
				want = k
				break
			}
		}
		if got := CriticalLosses(n, DefaultAlpha); got != want {
			t.Errorf("CriticalLosses(%d) = %d, want %d", n, got, want)
		}
		for k := 0; k <= n; k++ {
			if got, want := BinomialTail(n, k), tail2(n, k); math.Abs(got-want) > 1e-12 {
				t.Errorf("BinomialTail(%d, %d) = %v, want %v", n, k, got, want)
			}
		}
	}
	// Below five non-tied tasks the test can never reject.
	for n := 1; n < 5; n++ {
		if CriticalLosses(n, DefaultAlpha) != 0 {
			t.Errorf("n = %d can reject, but 2^-%d > alpha", n, n)
		}
	}
	if c := CriticalLosses(12, DefaultAlpha); c != 10 {
		t.Errorf("at n = 12 the critical count is %d, want 10", c)
	}
	// Power at n = 12 against a designer that loses with probability 0.8.
	escape := 0.0
	for l := 0; l < CriticalLosses(12, DefaultAlpha); l++ {
		escape += choose(12, l) * math.Pow(0.8, float64(l)) * math.Pow(0.2, float64(12-l))
	}
	if escape < 0.40 || escape > 0.48 {
		t.Errorf("a designer losing 80%% of the time escapes with probability %.3f; the spec says about 0.44", escape)
	}
}

// pair is one task's rows: pass rates, completeness and deviation rates per
// arm; the designer records 300 tokens to the control's 100.
func pair(task string, des, con float64, desComp, conComp, desDev, conDev *float64) []Row {
	return []Row{
		{Task: task, Arm: ArmDesigner, TestsPassed: int(des * 10), TestsTotal: 10, Completeness: Completeness{RefFiles: 1, Rate: desComp}, Fidelity: Fidelity{Steps: 3, Rate: desDev}, TokensIn: 200, TokensOut: 100},
		{Task: task, Arm: ArmControl, TestsPassed: int(con * 10), TestsTotal: 10, Completeness: Completeness{RefFiles: 1, Rate: conComp}, Fidelity: Fidelity{Steps: 3, Rate: conDev}, TokensIn: 70, TokensOut: 30},
	}
}

func f(x float64) *float64 { return &x }

// tokenRatio recomputes the cost ratio from rows: designer tokens over
// control tokens, in and out, over the tasks with both arms.
func tokenRatio(rows []Row) float64 {
	arms := map[string]map[string]Row{}
	for _, r := range rows {
		if arms[r.Task] == nil {
			arms[r.Task] = map[string]Row{}
		}
		arms[r.Task][r.Arm] = r
	}
	var des, con int64
	for _, a := range arms {
		d, okD := a[ArmDesigner]
		c, okC := a[ArmControl]
		if okD && okC {
			des += d.TokensIn + d.TokensOut
			con += c.TokensIn + c.TokensOut
		}
	}
	return float64(des) / float64(con)
}

func TestDecide_Scenarios(t *testing.T) {
	var lost []Row
	for i := 0; i < 12; i++ {
		lost = append(lost, pair(fmt.Sprintf("t%02d", i), 0.5, 1.0, f(1), f(1), f(0), f(0))...)
	}
	d := Decide(lost, DefaultAlpha)
	if d.Tests.Outcome != Worse || math.Abs(d.Tests.P-math.Pow(2, -12)) > 1e-12 || d.Tests.Losses != 12 || d.Verdict != VerdictNotWorthIt {
		t.Errorf("losing every task: %+v verdict %s", d.Tests, d.Verdict)
	}
	if d.Completeness.Outcome != NotBelow || d.Completeness.N != 0 {
		t.Errorf("tied completeness: %+v", d.Completeness)
	}
	if d.CostRatio == nil || *d.CostRatio != tokenRatio(lost) || !strings.Contains(strings.Join(d.Reasons, "\n"), fmt.Sprintf("= %.2f×", tokenRatio(lost))) {
		t.Errorf("cost ratio %v reasons %v; want %v in the reasons", d.CostRatio, d.Reasons, tokenRatio(lost))
	}

	// Every task tied on tests: no difference, whatever completeness says,
	// and the cost of the tie is reported.
	var equal []Row
	for i := 0; i < 12; i++ {
		equal = append(equal, pair(fmt.Sprintf("t%02d", i), 1.0, 1.0, f(1), f(0.5), f(0), f(0))...)
	}
	d = Decide(equal, DefaultAlpha)
	if d.Verdict != VerdictNoDifference || d.Tests.N != 0 || d.Completeness.Wins != 12 || !strings.Contains(d.Tests.Detail, "tied") || d.CostRatio == nil || *d.CostRatio != tokenRatio(equal) {
		t.Errorf("equal arms: verdict %s tests %+v completeness %+v ratio %v", d.Verdict, d.Tests, d.Completeness, d.CostRatio)
	}

	// One task won outright, the rest tied, completeness equal: worth it.
	var oneWin []Row
	for i := 0; i < 12; i++ {
		con := 1.0
		if i == 0 {
			con = 0.8
		}
		oneWin = append(oneWin, pair(fmt.Sprintf("t%02d", i), 1.0, con, f(1), f(1), f(0), f(0))...)
	}
	d = Decide(oneWin, DefaultAlpha)
	if d.Verdict != VerdictWorthIt || d.Tests.Wins != 1 || d.Tests.N != 1 || d.Tests.Outcome != NotBelow || d.Completeness.Outcome != NotBelow {
		t.Errorf("one win: verdict %s tests %+v completeness %+v", d.Verdict, d.Tests, d.Completeness)
	}

	// The same win, but the designer's plans miss the reference file on two
	// tasks the control's name: completeness below, not worth it.
	var misses []Row
	for i := 0; i < 12; i++ {
		con := 1.0
		if i == 0 {
			con = 0.8
		}
		desComp := 1.0
		if i >= 10 {
			desComp = 0
		}
		misses = append(misses, pair(fmt.Sprintf("t%02d", i), 1.0, con, f(desComp), f(1), f(0), f(0))...)
	}
	d = Decide(misses, DefaultAlpha)
	if d.Verdict != VerdictNotWorthIt || d.Tests.Outcome != NotBelow || d.Completeness.Outcome != Below || d.Completeness.Losses != 2 {
		t.Errorf("missed files: verdict %s tests %+v completeness %+v", d.Verdict, d.Tests, d.Completeness)
	}

	// Nine of twelve lost and three won: n is 12, the test cannot reject
	// (critical is 10), the mean is negative, so the metric is below and
	// the verdict not worth it.
	var nine []Row
	for i := 0; i < 12; i++ {
		des, con := 0.8, 1.0
		if i >= 9 {
			des, con = 1.0, 0.9
		}
		nine = append(nine, pair(fmt.Sprintf("t%02d", i), des, con, f(1), f(1), f(0), f(0))...)
	}
	d = Decide(nine, DefaultAlpha)
	if d.Tests.Outcome != Below || d.Tests.N != 12 || d.Tests.Losses != 9 || math.Abs(d.Tests.P-tail2(12, 9)) > 1e-12 || d.Tests.P <= DefaultAlpha || d.Verdict != VerdictNotWorthIt {
		t.Errorf("nine lost: %+v verdict %s", d.Tests, d.Verdict)
	}
	// The same nine losses with the other three tied: n is 9, and nine of
	// nine is worse.
	var tied []Row
	for i := 0; i < 12; i++ {
		des := 0.8
		if i >= 9 {
			des = 1.0
		}
		tied = append(tied, pair(fmt.Sprintf("t%02d", i), des, 1.0, f(1), f(1), f(0), f(0))...)
	}
	if d = Decide(tied, DefaultAlpha); d.Tests.Outcome != Worse || d.Tests.N != 9 {
		t.Errorf("nine lost, three tied: %+v", d.Tests)
	}

	// The designer wins tests and names the files but records more
	// deviations: fidelity is reported worse, and the verdict ignores it.
	var sloppy []Row
	for i := 0; i < 12; i++ {
		sloppy = append(sloppy, pair(fmt.Sprintf("t%02d", i), 1.0, 0.9, f(1), f(1), f(1), f(0))...)
	}
	d = Decide(sloppy, DefaultAlpha)
	if d.Tests.Outcome != NotBelow || d.Fidelity.Outcome != Worse || d.Verdict != VerdictWorthIt || !strings.Contains(d.Fidelity.Metric, "not in the rule") {
		t.Errorf("sloppy designer: tests %s fidelity %s verdict %s", d.Tests.Outcome, d.Fidelity.Outcome, d.Verdict)
	}

	// One arm missing on a task drops it; a lone pair is too few.
	one := append(pair("a", 1, 1, f(1), f(1), f(0), f(0)), Row{Task: "b", Arm: ArmDesigner, TestsPassed: 1, TestsTotal: 1})
	d = Decide(one, DefaultAlpha)
	if d.Verdict != VerdictTooFew || len(d.Dropped) != 1 || d.Dropped[0] != "b" {
		t.Errorf("one pair: verdict %s dropped %v", d.Verdict, d.Dropped)
	}

	// An arm with no plan has neither completeness nor a deviation rate:
	// that task is dropped from those metrics only.
	noPlan := append(pair("a", 1, 0.5, nil, f(1), nil, f(0)), pair("b", 1, 0.5, f(1), f(1), f(0), f(0))...)
	d = Decide(noPlan, DefaultAlpha)
	if d.Tests.Paired != 2 || d.Completeness.Paired != 1 || d.Fidelity.Paired != 1 || !strings.Contains(d.Completeness.Detail, "1 task(s) dropped") {
		t.Errorf("no plan: tests paired %d completeness paired %d fidelity paired %d detail %q", d.Tests.Paired, d.Completeness.Paired, d.Fidelity.Paired, d.Completeness.Detail)
	}

	// No control tokens: no ratio, and the reasons say so.
	free := pair("a", 1, 0.5, f(1), f(1), f(0), f(0))
	free = append(free, pair("b", 1, 0.5, f(1), f(1), f(0), f(0))...)
	for i := range free {
		free[i].TokensIn, free[i].TokensOut = 0, 0
	}
	if d = Decide(free, DefaultAlpha); d.CostRatio != nil || !strings.Contains(strings.Join(d.Reasons, "\n"), "no ratio") {
		t.Errorf("no tokens: ratio %v reasons %v", d.CostRatio, d.Reasons)
	}
}

func TestSummarize(t *testing.T) {
	rows := append(pair("a", 1.0, 0.5, f(1), f(0), f(0.5), f(0)), pair("b", 0.5, 0.5, f(0.5), nil, nil, f(1))...)
	rows[0].Completed, rows[0].AuditPass, rows[0].Guarantees, rows[0].GuaranteesWithPremises = true, true, 2, 1
	rows[0].WallSeconds, rows[2].WallSeconds = 10, 30
	means := Summarize(rows)
	if len(means) != 2 || means[0].Arm != ArmDesigner || means[1].Arm != ArmControl {
		t.Fatalf("arms %+v", means)
	}
	des, con := means[0], means[1]
	if des.PassRate != 0.75 || des.Completed != 1 || des.AuditPassed != 1 || des.GuaranteesHonest != 0.5 || des.WallSeconds != 20 || des.DeviationRate == nil || *des.DeviationRate != 0.5 || des.PlanCompleteness == nil || *des.PlanCompleteness != 0.75 || des.TokensIn != 400 {
		t.Errorf("designer %+v", des)
	}
	if con.PassRate != 0.5 || con.DeviationRate == nil || *con.DeviationRate != 0.5 || con.FileCoverage != nil || con.PlanCompleteness == nil || *con.PlanCompleteness != 0 {
		t.Errorf("control %+v", con)
	}
}
