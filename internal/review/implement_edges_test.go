package review

import (
	"strings"
	"testing"
)

// Fix nodes run in a chain — plan, fix-1, fix-2, …, the final gate — so no
// two agents edit the one shared working tree at once.
func TestGenerateImplementWorkflow_FixNodesAreSerial(t *testing.T) {
	agg := &Aggregate{}
	for i := 0; i < 3; i++ {
		agg.AllFindings = append(agg.AllFindings, Finding{
			Lens: "l", Severity: "critical", File: "a.go", Line: i + 1,
			Issue: "i", Fix: "f",
		})
	}
	yaml, n, err := GenerateImplementWorkflow(agg, ImplementOptions{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("generated %d fix nodes, want 3", n)
	}
	for _, want := range []string{
		"{from: plan, to: fix-1}",
		"{from: fix-1, to: fix-2}",
		"{from: fix-2, to: fix-3}",
		"{from: fix-3, to: final-gate-go-test}",
		"{from: final-gate-go-test, to: final-gate-validate}",
		"{from: final-gate-validate, to: final-gate-replay}",
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("missing edge %q\n---\n%s", want, edgesOf(yaml))
		}
	}
	if strings.Contains(yaml, "{from: plan, to: fix-2}") {
		t.Errorf("fix-2 still dispatches straight off plan:\n%s", edgesOf(yaml))
	}
}

func edgesOf(y string) string {
	i := strings.Index(y, "edges:")
	if i < 0 {
		return y
	}
	return y[i:]
}

// Severity matching folds case and surrounding space, so "Critical", "HIGH"
// and " high " each get a fix node and the low finding is filtered out.
func TestGenerateImplementWorkflow_SeverityMatchIsCaseInsensitive(t *testing.T) {
	agg := &Aggregate{AllFindings: []Finding{
		{Lens: "l", Severity: "Critical", File: "a.go", Line: 1, Issue: "i", Fix: "f"},
		{Lens: "l", Severity: "HIGH", File: "b.go", Line: 2, Issue: "i", Fix: "f"},
		{Lens: "l", Severity: " high ", File: "c.go", Line: 3, Issue: "i", Fix: "f"},
		{Lens: "l", Severity: "low", File: "d.go", Line: 4, Issue: "i", Fix: "f"},
	}}
	_, n, err := GenerateImplementWorkflow(agg, ImplementOptions{MaxFixes: 5}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("generated %d fix nodes; want 3 (the low one is filtered, the other three differ only in case)", n)
	}
}

// MaxFixes 0 means no cap, as both CLI flags document, and WithDefaults
// keeps it.
func TestImplementOptions_MaxFixesZeroMeansNoCap(t *testing.T) {
	agg := &Aggregate{}
	for i := 0; i < 9; i++ {
		agg.AllFindings = append(agg.AllFindings, Finding{
			Lens: "l", Severity: "critical", File: "a.go", Line: i + 1, Issue: "i", Fix: "f",
		})
	}
	_, n, err := GenerateImplementWorkflow(agg, ImplementOptions{MaxFixes: 0}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if n != 9 {
		t.Errorf("--max-fixes 0 produced %d fix nodes; want all 9", n)
	}
	_, n, err = GenerateImplementWorkflow(agg, ImplementOptions{MaxFixes: 4}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("--max-fixes 4 produced %d fix nodes; want 4", n)
	}
}
