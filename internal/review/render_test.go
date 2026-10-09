package review

import (
	"strings"
	"testing"
)

func TestRender_NilAggregate(t *testing.T) {
	_, err := Render(nil, Meta{})
	if err == nil {
		t.Fatal("expected error for nil aggregate")
	}
}

func TestRender_AllVerdicts(t *testing.T) {
	cases := []struct {
		name    string
		totals  map[string]int
		verdict string
	}{
		{"clean", map[string]int{"critical": 0, "high": 0, "medium": 0, "low": 0}, "clean"},
		{"critical", map[string]int{"critical": 1}, "critical"},
		{"high", map[string]int{"high": 1}, "needs_work"},
		{"medium", map[string]int{"medium": 1}, "minor_issues"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agg := &Aggregate{
				ByLens:      map[string]LensReport{},
				Totals:      tc.totals,
				AllFindings: []Finding{},
			}
			body, err := Render(agg, Meta{})
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if !strings.Contains(strings.ToLower(body), strings.ToLower(tc.verdict)) {
				t.Errorf("expected verdict %q in body", tc.verdict)
			}
		})
	}
}

func TestRender_WithMetaAndTokens(t *testing.T) {
	agg := &Aggregate{
		ByLens: map[string]LensReport{
			"audit-v1": {
				Lens:     "v1",
				Verdict:  "clean",
				Summary:  "all good",
				Findings: []Finding{},
			},
		},
		Totals:      map[string]int{},
		AllFindings: []Finding{},
	}
	meta := Meta{
		Workflow:  "self-review.yaml",
		Provider:  "anthropic",
		RunID:     "42",
		TokensIn:  1234,
		TokensOut: 5678,
		CostUSD:   0.42,
	}
	body, err := Render(agg, meta)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{"self-review.yaml", "anthropic", "42"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in body", want)
		}
	}
}

func TestRender_HighSeverityFindings(t *testing.T) {
	agg := &Aggregate{
		ByLens: map[string]LensReport{
			"audit-v1": {
				Lens:    "v1",
				Verdict: "issues",
				Findings: []Finding{
					{File: "x.go", Line: 1, Severity: "critical", Issue: "broken", Fix: "fix"},
					{File: "y.go", Line: 2, Severity: "high", Issue: "bad", Fix: "fix"},
					{File: "z.go", Line: 3, Severity: "medium", Issue: "meh", Fix: "fix"},
				},
			},
		},
		Totals: map[string]int{"critical": 1, "high": 1, "medium": 1},
		AllFindings: []Finding{
			{File: "x.go", Line: 1, Severity: "critical", Issue: "broken", Fix: "fix"},
			{File: "y.go", Line: 2, Severity: "high", Issue: "bad", Fix: "fix"},
			{File: "z.go", Line: 3, Severity: "medium", Issue: "meh", Fix: "fix"},
		},
	}
	body, err := Render(agg, Meta{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// Critical + high should appear in the high-findings table.
	if !strings.Contains(body, "x.go") {
		t.Error("expected critical finding (x.go) in body")
	}
	if !strings.Contains(body, "y.go") {
		t.Error("expected high finding (y.go) in body")
	}
}

func TestRender_ParseFailures(t *testing.T) {
	agg := &Aggregate{
		ByLens:        map[string]LensReport{},
		Totals:        map[string]int{},
		ParseFailures: []string{"audit-broken-1", "audit-broken-2"},
		AllFindings:   []Finding{},
	}
	body, err := Render(agg, Meta{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{"audit-broken-1", "audit-broken-2"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in body", want)
		}
	}
}
