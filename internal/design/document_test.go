package design

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var fixedPlan = []Step{
	{Step: 1, Action: "Add the type", Files: []string{"kv.go"}},
	{Step: 2, Action: "Implement Parse", Files: []string{"kv.go"}},
	{Step: 3, Action: "Add a helper package", Files: []string{"internal/"}},
	{Step: 4, Action: "Run go vet", Files: nil},
}

// Fidelity is read from a fixed report: the steps are the plan's, the
// deviations and additions the report's, the rate their sum over the
// steps, and a plan with no step has no rate.
func TestPlanFidelity_FromAFixedReport(t *testing.T) {
	report := Report{
		StepsDone:  []int{1, 2, 4},
		Deviations: []Deviation{{Step: 3, Why: "no helper was needed"}, {Step: 9, Why: "the plan had no step 9"}},
		Additions:  []string{"added a doc comment"},
		Summary:    "done",
	}
	f := PlanFidelity(fixedPlan, report)
	wantRate := float64(len(report.Deviations)+len(report.Additions)) / float64(len(fixedPlan))
	if f.Steps != 4 || f.Deviations != 2 || f.Additions != 1 || f.Rate == nil || *f.Rate != wantRate {
		t.Errorf("fidelity %+v (rate %v), want steps 4, deviations 2, additions 1, rate %v", f, f.Rate, wantRate)
	}
	if f := PlanFidelity(nil, report); f.Rate != nil || f.Steps != 0 {
		t.Errorf("no plan: %+v; want no rate", f)
	}
	if f := PlanFidelity(fixedPlan, Report{}); f.Rate == nil || *f.Rate != 0 {
		t.Errorf("clean report: rate %v, want 0", f.Rate)
	}
}

func TestFileCoverage(t *testing.T) {
	changed := []string{"kv.go", "internal/helper/h.go", "README.md"}
	c := FileCoverage(fixedPlan, changed)
	if c.Changed != 3 || c.Named != 2 || c.Rate == nil || *c.Rate != 2.0/3 {
		t.Errorf("coverage %+v (rate %v); want 2 of 3 named", c, c.Rate)
	}
	if c := FileCoverage(fixedPlan, nil); c.Rate != nil || c.Changed != 0 {
		t.Errorf("nothing changed: %+v; want no rate", c)
	}
	if c := FileCoverage([]Step{{Files: []string{"./kv.go"}}}, []string{"kv.go"}); c.Named != 1 {
		t.Errorf("./kv.go should match kv.go: %+v", c)
	}
}

// Plan completeness is the share of the reference's changed files the plan
// names: from a shipped fixture, whose changed files are recomputed here by
// a byte comparison of reference/ against tree/, and from a two-file
// fixture where a plan naming one of them scores a half. The denominator is
// the reference's files, never the plan's own.
func TestPlanCompleteness_FromAReferenceDiff(t *testing.T) {
	task, err := LoadTask(filepath.Join(fixturesRoot, "cli-wc"))
	if err != nil {
		t.Fatal(err)
	}
	refFiles, err := TreeFiles(task.Reference())
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, f := range refFiles {
		ref, _ := os.ReadFile(filepath.Join(task.Reference(), filepath.FromSlash(f)))
		cur, err := os.ReadFile(filepath.Join(task.Tree(), filepath.FromSlash(f)))
		if err != nil || !bytes.Equal(ref, cur) {
			want = append(want, f)
		}
	}
	changed, err := ReferenceChanges(task)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 || strings.Join(changed, ",") != strings.Join(want, ",") {
		t.Fatalf("ReferenceChanges = %v, want %v", changed, want)
	}
	names := func(plan []Step) Completeness { return PlanCompleteness(plan, changed) }
	if c := names([]Step{{Step: 1, Files: changed}}); c.RefFiles != len(changed) || c.RefNamed != len(changed) || c.Rate == nil || *c.Rate != 1 {
		t.Errorf("a plan naming every changed file: %+v", c)
	}
	if c := names([]Step{{Step: 1, Files: []string{"main.go"}}}); c.RefNamed != 0 || c.Rate == nil || *c.Rate != 0 {
		t.Errorf("a plan naming an unchanged file: %+v", c)
	}
	if c := names(nil); c.Rate != nil || c.RefFiles != len(changed) {
		t.Errorf("no plan: %+v", c)
	}

	root := t.TempDir()
	writeTask(t, root, "two", "", "TestHiddenA",
		"tree/a.go", "package x\n\nfunc A() {}\n", "tree/b.go", "package x\n\nfunc B() {}\n", "tree/c.go", "package x\n\nfunc C() {}\n",
		"reference/a.go", "package x\n\nfunc A() int { return 1 }\n", "reference/b.go", "package x\n\nfunc B() int { return 2 }\n", "reference/c.go", "package x\n\nfunc C() {}\n",
		"reference/x.go", "")
	two, err := LoadTask(filepath.Join(root, "two"))
	if err != nil {
		t.Fatal(err)
	}
	changed, err = ReferenceChanges(two)
	if err != nil || strings.Join(changed, ",") != "a.go,b.go" {
		t.Fatalf("two-file reference: %v, %v; want a.go,b.go (c.go is unchanged)", changed, err)
	}
	if c := PlanCompleteness([]Step{{Step: 1, Files: []string{"a.go"}}, {Step: 2, Files: []string{"c.go"}}}, changed); c.RefFiles != 2 || c.RefNamed != 1 || c.Rate == nil || *c.Rate != 0.5 {
		t.Errorf("a plan naming one of two changed files: %+v", c)
	}
	if c := PlanCompleteness([]Step{{Step: 1, Files: []string{"./"}}}, changed); c.RefNamed != 0 {
		t.Errorf("a plan naming the root names nothing: %+v", c)
	}
	// Rows built from the two plans decide completeness for the designer.
	rows := []Row{
		{Task: "two", Arm: ArmDesigner, TestsPassed: 1, TestsTotal: 1, Completeness: PlanCompleteness([]Step{{Files: changed}}, changed)},
		{Task: "two", Arm: ArmControl, TestsPassed: 1, TestsTotal: 1, Completeness: PlanCompleteness([]Step{{Files: []string{"a.go"}}}, changed)},
		{Task: "one", Arm: ArmDesigner, TestsPassed: 1, TestsTotal: 1, Completeness: PlanCompleteness([]Step{{Files: changed}}, changed)},
		{Task: "one", Arm: ArmControl, TestsPassed: 0, TestsTotal: 1, Completeness: PlanCompleteness([]Step{{Files: changed}}, changed)},
	}
	d := Decide(rows, DefaultAlpha)
	if d.Completeness.Wins != 1 || d.Completeness.N != 1 || d.Completeness.Mean != 0.25 || d.Verdict != VerdictWorthIt {
		t.Errorf("completeness %+v verdict %s", d.Completeness, d.Verdict)
	}
}

func TestCheckLabels_AuditsTheClaims(t *testing.T) {
	honest := []Claim{
		{Label: "definition", Claim: "A slug is lowercase ASCII."},
		{Label: "assumption", Claim: "Inputs are short."},
		{Label: "guarantee", Claim: "Output is at most 32 bytes.", RestsOn: []int{0, 1}},
		{Label: "unknown", Claim: "Whether callers pass Unicode."},
	}
	cases := []struct {
		name        string
		claims      []Claim
		labelled, g int
		gp          int
		pass        bool
		detail      string
	}{
		{"an honest set", honest, 4, 1, 1, true, "audit PASS"},
		{"a guarantee with no premise", []Claim{{Label: "guarantee", Claim: "It works."}}, 1, 1, 0, false, "refused"},
		{"a guarantee on an unknown", []Claim{{Label: "unknown", Claim: "?"}, {Label: "guarantee", Claim: "Sure.", RestsOn: []int{0}}}, 2, 1, 1, false, "refused"},
		{"a guarantee on a later claim", []Claim{{Label: "guarantee", Claim: "Sure.", RestsOn: []int{1}}, {Label: "definition", Claim: "D."}}, 2, 1, 0, false, "not an earlier claim"},
		{"a label outside the partition", []Claim{{Label: "fact", Claim: "X."}}, 0, 0, 0, false, "refused"},
		{"no claims", nil, 0, 0, 0, false, "no claim"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := CheckLabels(c.claims, filepath.Join(t.TempDir(), "audit.db"))
			if l.Claims != len(c.claims) || l.Labelled != c.labelled || l.Guarantees != c.g || l.GuaranteesWithPremises != c.gp || l.AuditPass != c.pass || !strings.Contains(l.AuditDetail, c.detail) {
				t.Errorf("labels %+v; want labelled %d, guarantees %d, with premises %d, pass %v, detail %q", l, c.labelled, c.g, c.gp, c.pass, c.detail)
			}
		})
	}
}

func TestParseDocument(t *testing.T) {
	outputs := map[string]any{
		"design": "Use a mutex.",
		"claims": []any{map[string]any{"label": "definition", "claim": "x", "rests_on": []any{}}},
		"plan":   []any{map[string]any{"step": float64(1), "action": "edit", "files": []any{"a.go"}}},
	}
	d, err := ParseDocument(outputs)
	if err != nil || d.Design != "Use a mutex." || len(d.Claims) != 1 || len(d.Plan) != 1 || d.Plan[0].Step != 1 || d.Plan[0].Files[0] != "a.go" {
		t.Errorf("ParseDocument = %+v, %v", d, err)
	}
	delete(outputs, "plan")
	if _, err := ParseDocument(outputs); err == nil {
		t.Error("a document without a plan parsed")
	}
	r, err := ParseReport(map[string]any{"steps_done": []any{float64(1)}, "deviations": []any{map[string]any{"step": float64(2), "why": "w"}}, "additions": []any{"a"}, "summary": "s"})
	if err != nil || len(r.StepsDone) != 1 || len(r.Deviations) != 1 || r.Deviations[0].Step != 2 || len(r.Additions) != 1 {
		t.Errorf("ParseReport = %+v, %v", r, err)
	}
}
