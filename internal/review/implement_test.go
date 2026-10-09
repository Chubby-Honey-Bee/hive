package review

import (
	"os"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// fixture aggregate covering critical, high, medium, low so filtering tests
// can pull exactly what they need without rebuilding.
func sampleAggregate() *Aggregate {
	return &Aggregate{
		ByLens: map[string]LensReport{},
		AllFindings: []Finding{
			{Lens: "WASP", File: "a.go", Line: 1, Severity: "critical", Issue: "boom", Fix: "defuse"},
			{Lens: "SOLID", File: "b.go", Line: 2, Severity: "high", Issue: "god object", Fix: "split"},
			{Lens: "SOLID", File: "c.go", Line: 3, Severity: "high", Issue: "tight coupling", Fix: "interface"},
			{Lens: "go-idiom", File: "d.go", Line: 4, Severity: "medium", Issue: "string concat", Fix: "Builder"},
			{Lens: "go-idiom", File: "e.go", Line: 5, Severity: "low", Issue: "stale comment", Fix: "remove"},
			// missing-file finding should be dropped
			{Lens: "?", File: "", Line: 0, Severity: "high", Issue: "no path", Fix: "?"},
			// missing-issue finding should be dropped
			{Lens: "?", File: "f.go", Line: 6, Severity: "high", Issue: "", Fix: "?"},
		},
		Totals: map[string]int{"critical": 1, "high": 2, "medium": 1, "low": 1},
	}
}

func TestGenerate_DefaultsCriticalAndHigh(t *testing.T) {
	yaml, n, err := GenerateImplementWorkflow(sampleAggregate(), ImplementOptions{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if n != 3 {
		t.Fatalf("included count = %d, want 3 (1 critical + 2 high)", n)
	}
	for _, want := range []string{
		"name: self-implement-",
		"plan:",
		"fix-1:",
		"fix-2:",
		"fix-3:",
		"final-gate-go-test:",
		"final-gate-validate:",
		"final-gate-replay:",
		`accept:`,
		`"outputs.compile_ok == true"`,
		`"outputs.tests_pass == true"`,
		"on_reject:",
		"max_repair_iterations: 1",
		"tier: planner",    // top-tier plan node
		"tier: worker",     // cheap fan-out
		"tier: synthesist", // mid-tier repair escalation
		"verify_tests: true",
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("missing %q in YAML", want)
		}
	}
	// must not include medium/low rows
	for _, drop := range []string{"d.go", "e.go", "no path", "f.go"} {
		if strings.Contains(yaml, drop) {
			t.Errorf("YAML should not include filtered finding %q", drop)
		}
	}
}

func TestGenerate_SeverityFilter(t *testing.T) {
	yaml, n, err := GenerateImplementWorkflow(sampleAggregate(), ImplementOptions{
		Severities: []string{"medium"},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if n != 1 {
		t.Fatalf("included count = %d, want 1 (only the medium row)", n)
	}
	if !strings.Contains(yaml, "d.go") {
		t.Error("expected d.go (medium) in output")
	}
	if strings.Contains(yaml, "a.go") || strings.Contains(yaml, "b.go") {
		t.Error("expected critical/high rows excluded by severity filter")
	}
}

func TestGenerate_MaxFixesTruncates(t *testing.T) {
	yaml, n, err := GenerateImplementWorkflow(sampleAggregate(), ImplementOptions{
		Severities: []string{"critical", "high"},
		MaxFixes:   2,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if n != 2 {
		t.Fatalf("count = %d, want 2 (capped)", n)
	}
	if strings.Contains(yaml, "fix-3:") {
		t.Error("max-fixes=2 should have prevented fix-3 from being generated")
	}
}

func TestGenerate_RoundTripsThroughWorkflowValidate(t *testing.T) {
	yaml, _, err := GenerateImplementWorkflow(sampleAggregate(), ImplementOptions{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	tmp, err := os.CreateTemp("", "impl-test-*.yaml")
	if err != nil {
		t.Fatalf("tempfile: %v", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(yaml); err != nil {
		t.Fatalf("write: %v", err)
	}
	tmp.Close()

	errs, _, vErr := workflow.Validate(tmp.Name())
	if vErr != nil {
		t.Fatalf("workflow.Validate err: %v", vErr)
	}
	if len(errs) > 0 {
		t.Fatalf("workflow.Validate errors: %v\n----\n%s", errs, yaml)
	}
}

func TestGenerate_NoMatchingFindingsErrors(t *testing.T) {
	_, _, err := GenerateImplementWorkflow(&Aggregate{
		AllFindings: []Finding{
			{File: "x", Severity: "low", Issue: "y", Fix: "z"},
		},
	}, ImplementOptions{Severities: []string{"high"}})
	if err == nil {
		t.Fatal("expected error when no findings match severity filter")
	}
	if !strings.Contains(err.Error(), "no findings matched") {
		t.Errorf("error message should mention empty filter; got %v", err)
	}
}

func TestGenerate_NilAggregateErrors(t *testing.T) {
	if _, _, err := GenerateImplementWorkflow(nil, ImplementOptions{}); err == nil {
		t.Fatal("expected error for nil aggregate")
	}
}

func TestGenerate_DeterministicOrder(t *testing.T) {
	// Same input should produce same byte-equal output (modulo timestamp).
	a := sampleAggregate()
	y1, _, err := GenerateImplementWorkflow(a, ImplementOptions{Name: "fixed"})
	if err != nil {
		t.Fatalf("Generate y1: %v", err)
	}
	y2, _, err := GenerateImplementWorkflow(a, ImplementOptions{Name: "fixed"})
	if err != nil {
		t.Fatalf("Generate y2: %v", err)
	}
	// Strip the date stamp line which is the only non-deterministic field.
	canon := func(s string) string {
		var b strings.Builder
		for _, line := range strings.Split(s, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "Generated") {
				continue
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
		return b.String()
	}
	if canon(y1) != canon(y2) {
		t.Errorf("generator output differs between runs:\n--y1--\n%s\n--y2--\n%s", y1, y2)
	}
}

func TestGenerate_TrimsMultiLineIssue(t *testing.T) {
	agg := &Aggregate{
		AllFindings: []Finding{
			{
				Lens: "L", File: "x.go", Line: 1, Severity: "high",
				Issue: "first line\n\twith tab\n  and indented continuation",
				Fix:   "single line fix",
			},
		},
	}
	yaml, _, err := GenerateImplementWorkflow(agg, ImplementOptions{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Multi-line issue should collapse to one line under the issue: key
	// (otherwise the YAML block under the |-literal scalar breaks).
	if strings.Contains(yaml, "with tab\n") {
		t.Error("multi-line issue field should have been collapsed to one line")
	}
	if !strings.Contains(yaml, "first line with tab and indented continuation") {
		t.Errorf("collapsed text not present in YAML; got:\n%s", yaml)
	}
}

func TestGenerateImplementWorkflow_NilAggregateExtra(t *testing.T) {
	_, _, err := GenerateImplementWorkflow(nil, ImplementOptions{})
	if err == nil {
		t.Fatal("expected error for nil aggregate")
	}
}

func TestGenerateImplementWorkflow_NoMatchesExtra(t *testing.T) {
	agg := &Aggregate{
		AllFindings: []Finding{
			{Severity: "low", File: "x.go", Issue: "i", Fix: "f"},
		},
	}
	_, _, err := GenerateImplementWorkflow(agg, ImplementOptions{
		Severities: []string{"critical", "high"},
	})
	if err == nil {
		t.Fatal("expected error for no matching severities")
	}
	if !strings.Contains(err.Error(), "no findings matched") {
		t.Errorf("error = %v", err)
	}
}

func TestGenerateImplementWorkflow_MaxFixesTruncatesExtra(t *testing.T) {
	agg := &Aggregate{
		AllFindings: []Finding{
			{Severity: "high", File: "a.go", Line: 1, Issue: "i1", Fix: "f1"},
			{Severity: "high", File: "b.go", Line: 1, Issue: "i2", Fix: "f2"},
			{Severity: "high", File: "c.go", Line: 1, Issue: "i3", Fix: "f3"},
		},
	}
	_, count, err := GenerateImplementWorkflow(agg, ImplementOptions{
		Severities: []string{"high"},
		MaxFixes:   2,
	})
	if err != nil {
		t.Fatalf("GenerateImplementWorkflow: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d; want 2", count)
	}
}

func TestGenerateImplementWorkflow_FiltersMissingFieldsExtra(t *testing.T) {
	agg := &Aggregate{
		AllFindings: []Finding{
			{Severity: "high", File: "", Issue: "no-file"},
			{Severity: "high", File: "x.go", Issue: ""},
			{Severity: "high", File: "x.go", Issue: "valid"},
		},
	}
	_, count, err := GenerateImplementWorkflow(agg, ImplementOptions{
		Severities: []string{"high"},
	})
	if err != nil {
		t.Fatalf("GenerateImplementWorkflow: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d; want 1", count)
	}
}

func TestGenerateImplementWorkflow_FixDefaultPlaceholderExtra(t *testing.T) {
	agg := &Aggregate{
		AllFindings: []Finding{
			{Severity: "high", File: "x.go", Issue: "i", Fix: ""},
		},
	}
	yaml, _, err := GenerateImplementWorkflow(agg, ImplementOptions{
		Severities: []string{"high"},
	})
	if err != nil {
		t.Fatalf("GenerateImplementWorkflow: %v", err)
	}
	if !strings.Contains(yaml, "no specific fix provided") {
		t.Error("expected default fix placeholder")
	}
}
