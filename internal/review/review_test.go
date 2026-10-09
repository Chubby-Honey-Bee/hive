package review

import (
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// TestExtract_FromFencedJSONRationale seeds the schema with two
// audit-* nodes whose rationale contains a fenced JSON block, then
// asserts Extract pulls the LensReports correctly + counts severities.
func TestExtract_FromFencedJSONRationale(t *testing.T) {
	tmp := t.TempDir() + "/hive.db"
	store, err := db.NewStore(tmp)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Seed a workflow_runs row + two audit-* nodes with rationale.
	if _, err := store.WriteDB.Exec(
		`INSERT INTO workflow_runs (workflow_name, definition_yaml, inputs_json)
		 VALUES (?, '', '{}')`, "test-review",
	); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	rationaleSOLID := "Based on my SOLID audit, here are the findings:\n\n```json\n" + `{
  "lens": "SOLID",
  "verdict": "needs_work",
  "summary": "Two god objects flagged.",
  "findings": [
    {"file":"a.go","line":10,"severity":"high","issue":"god object","fix":"split"},
    {"file":"b.go","line":20,"severity":"medium","issue":"mixed concerns","fix":"extract"}
  ]
}` + "\n```\n"

	rationaleCyclo := `{
  "lens": "cyclomatic",
  "verdict": "needs_work",
  "summary": "One hot function.",
  "findings": [
    {"file":"x.go","line":5,"severity":"high","issue":"complexity 200","fix":"extract"}
  ]
}`

	rationaleNoJSON := "I couldn't find any issues but here's a long prose summary."

	for i, r := range [][]string{
		{"audit-solid", rationaleSOLID},
		{"audit-cyclomatic", rationaleCyclo},
		{"audit-mss", rationaleNoJSON}, // should land in ParseFailures
	} {
		if _, err := store.WriteDB.Exec(
			`INSERT INTO workflow_node_states (run_id, node_name, node_type, status, rationale)
			 VALUES (?, ?, 'agent', 'completed', ?)`,
			1, r[0], r[1],
		); err != nil {
			t.Fatalf("seed node %d: %v", i, err)
		}
	}

	agg, err := Extract(store.ReadDB, "audit-", 0)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if len(agg.ByLens) != 2 {
		t.Errorf("ByLens count = %d, want 2", len(agg.ByLens))
	}
	if got := agg.Totals["high"]; got != 2 {
		t.Errorf("totals.high = %d, want 2", got)
	}
	if got := agg.Totals["medium"]; got != 1 {
		t.Errorf("totals.medium = %d, want 1", got)
	}
	if len(agg.ParseFailures) != 1 || agg.ParseFailures[0] != "audit-mss" {
		t.Errorf("ParseFailures = %v, want [audit-mss]", agg.ParseFailures)
	}
	if len(agg.AllFindings) != 3 {
		t.Errorf("AllFindings = %d, want 3", len(agg.AllFindings))
	}
	// AllFindings should be severity-sorted: highs first
	if agg.AllFindings[0].Severity != "high" {
		t.Errorf("AllFindings[0].Severity = %q, want high", agg.AllFindings[0].Severity)
	}
}

// TestRender_StructureAndContent exercises the full template render
// against a known aggregate and asserts the report contains the right
// structural anchors + every finding's location.
func TestRender_StructureAndContent(t *testing.T) {
	agg := &Aggregate{
		ByLens: map[string]LensReport{
			"audit-solid": {
				Lens:    "SOLID",
				Verdict: "needs_work",
				Summary: "Two god objects flagged.",
				Findings: []Finding{
					{File: "a.go", Line: 10, Severity: "high", Issue: "god object", Fix: "split"},
					{File: "b.go", Line: 20, Severity: "medium", Issue: "mixed", Fix: "extract"},
				},
			},
			"audit-cyclomatic": {
				Lens:    "cyclomatic",
				Verdict: "needs_work",
				Summary: "One hot function.",
				Findings: []Finding{
					{File: "x.go", Line: 5, Severity: "high", Issue: "200 complexity", Fix: "extract"},
				},
			},
		},
		AllFindings: []Finding{
			{Lens: "SOLID", File: "a.go", Line: 10, Severity: "high", Issue: "god object", Fix: "split"},
			{Lens: "cyclomatic", File: "x.go", Line: 5, Severity: "high", Issue: "200 complexity", Fix: "extract"},
			{Lens: "SOLID", File: "b.go", Line: 20, Severity: "medium", Issue: "mixed", Fix: "extract"},
		},
		Totals:        map[string]int{"critical": 0, "high": 2, "medium": 1, "low": 0},
		ParseFailures: []string{"audit-mss"},
	}
	meta := Meta{
		Workflow:  "workflows/self-review.yaml",
		Provider:  "claude-cli",
		RunID:     "42",
		CostUSD:   0.1234,
		TokensIn:  1000,
		TokensOut: 50000,
	}
	md, err := Render(agg, meta)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	wants := []string{
		"# HIVE Review Report",
		"**Workflow:** `workflows/self-review.yaml`",
		"(run id 42)",
		"**Provider:** claude-cli",
		"$0.1234",
		"51,000 tokens", // thousands formatter
		"in 1,000 / out 50,000",
		"**needs_work**",
		"3 findings (0 critical / 2 high / 1 medium / 0 low)",
		"## Lens coverage",
		"| SOLID | ✅ needs_work | 2 |",
		"| cyclomatic | ✅ needs_work | 1 |",
		"| audit-mss | ❌ rejected",
		"## Top high-severity findings",
		"[a.go:10](a.go#L10)",
		"[x.go:5](x.go#L5)",
		"### SOLID  *(verdict: needs_work)*",
		"_Two god objects flagged._",
		"### cyclomatic  *(verdict: needs_work)*",
		"## Rejected audits",
		"`audit-mss`",
		// The section says what the report was rendered from and what it
		// cannot see.
		"How this report was produced",
		"workflow_node_states.rationale",
	}
	for _, w := range wants {
		if !strings.Contains(md, w) {
			t.Errorf("Render output missing %q", w)
		}
	}
}

// TestRender_VerdictTransitions checks the four verdict thresholds.
func TestRender_VerdictTransitions(t *testing.T) {
	cases := []struct {
		name    string
		totals  map[string]int
		wantSub string
	}{
		{"clean", map[string]int{"critical": 0, "high": 0, "medium": 0, "low": 0}, "clean"},
		{"low only", map[string]int{"critical": 0, "high": 0, "medium": 0, "low": 3}, "clean"},
		{"medium only", map[string]int{"critical": 0, "high": 0, "medium": 1, "low": 0}, "minor_issues"},
		{"high present", map[string]int{"critical": 0, "high": 1, "medium": 0, "low": 0}, "**needs_work**"},
		{"critical present", map[string]int{"critical": 1, "high": 0, "medium": 0, "low": 0}, "**critical**"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			md, err := Render(&Aggregate{
				ByLens:        map[string]LensReport{},
				AllFindings:   nil,
				Totals:        c.totals,
				ParseFailures: nil,
			}, Meta{})
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if !strings.Contains(md, "**Verdict:** "+c.wantSub) {
				t.Errorf("verdict line missing %q in:\n%s", c.wantSub, md[:200])
			}
		})
	}
}

// TestParseRationale exercises every branch of parseRationale directly.
func TestParseRationale(t *testing.T) {
	validJSON := `{"lens":"SOLID","verdict":"needs_work","summary":"ok","findings":[]}`
	fencedJSON := "```json\n" + validJSON + "\n```"
	prosText := "No issues found today — everything looks great."
	emptyLens := `{"verdict":"needs_work","summary":"no lens field"}`
	typeMismatch := `{"lens":[1,2,3],"verdict":"x"}`

	cases := []struct {
		name    string
		input   string
		wantOK  bool
		wantLen string // expected Lens value on success
	}{
		{
			name:    "happy path raw JSON",
			input:   validJSON,
			wantOK:  true,
			wantLen: "SOLID",
		},
		{
			name:    "happy path fenced JSON",
			input:   fencedJSON,
			wantOK:  true,
			wantLen: "SOLID",
		},
		{
			name:   "prose text — final_text fallback",
			input:  prosText,
			wantOK: false,
		},
		{
			name:   "empty string",
			input:  "",
			wantOK: false,
		},
		{
			name:   "valid JSON but missing lens field",
			input:  emptyLens,
			wantOK: false,
		},
		{
			name:   "type mismatch causes unmarshal error",
			input:  typeMismatch,
			wantOK: false,
		},
		{
			name:   "JSON with final_text key only",
			input:  `{"final_text":"some text"}`,
			wantOK: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report, ok := parseRationale(c.input)
			if ok != c.wantOK {
				t.Fatalf("parseRationale ok = %v, want %v", ok, c.wantOK)
			}
			if c.wantOK && report.Lens != c.wantLen {
				t.Errorf("report.Lens = %q, want %q", report.Lens, c.wantLen)
			}
		})
	}
}

func TestThousandsFormatter(t *testing.T) {
	cases := map[int64]string{
		0:       "0",
		42:      "42",
		1000:    "1,000",
		1234:    "1,234",
		1234567: "1,234,567",
		-12345:  "-12,345",
	}
	for in, want := range cases {
		if got := thousands(in); got != want {
			t.Errorf("thousands(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestExtract_PreservesSeverityOrder verifies the all_findings slice
// returned by Extract sorts highs before mediums (the renderer relies
// on this for the "top high-severity" table).
func TestExtract_PreservesSeverityOrder(t *testing.T) {
	tmp := t.TempDir() + "/hive.db"
	s, err := db.NewStore(tmp)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := s.WriteDB.Exec(
		`INSERT INTO workflow_runs (workflow_name, definition_yaml, inputs_json) VALUES (?, '', '{}')`,
		"t",
	); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if _, err := s.WriteDB.Exec(
		`INSERT INTO workflow_node_states (run_id, node_name, node_type, status, rationale)
		 VALUES (1, 'audit-mixed', 'agent', 'completed', ?)`,
		`{"lens":"mixed","findings":[
		   {"file":"a","severity":"low","issue":"l","fix":""},
		   {"file":"b","severity":"high","issue":"h","fix":""},
		   {"file":"c","severity":"medium","issue":"m","fix":""}
		 ]}`,
	); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	agg, err := Extract(s.ReadDB, "audit-", 0)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	want := []string{"high", "medium", "low"}
	if len(agg.AllFindings) != len(want) {
		t.Fatalf("AllFindings = %d, want %d", len(agg.AllFindings), len(want))
	}
	for i, w := range want {
		if got := agg.AllFindings[i].Severity; got != w {
			t.Errorf("AllFindings[%d].Severity = %q, want %q", i, got, w)
		}
	}
}
