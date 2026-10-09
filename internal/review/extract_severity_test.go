package review

import (
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// A lens spells a severity as it likes: "Critical", "HIGH", " low ". Extract
// folds each to one spelling, so those findings count in the totals and the
// high-severity table, as they do in the fix workflow.
func TestExtract_SeverityCaseIsFoldedForEveryConsumer(t *testing.T) {
	store := reviewTestStore(t)
	if _, err := store.Workflows().CreateWorkflowRun("t", 1, "name: t", "{}", []db.NodeSeed{{Name: "audit-v1", Type: "agent"}}); err != nil {
		t.Fatal(err)
	}
	rationale := "```json\n" + `{"lens":"v1","verdict":"issues","findings":[
		{"file":"low.go","line":3,"severity":" low ","issue":"nit","fix":"tidy"},
		{"file":"high.go","line":2,"severity":"HIGH","issue":"bad thing","fix":"fix it"},
		{"file":"crit.go","line":1,"severity":"Critical","issue":"broken invariant","fix":"restore it"}]}` + "\n```"
	if _, err := store.WriteDB.Exec(`UPDATE workflow_node_states SET rationale = ? WHERE node_name = 'audit-v1'`, rationale); err != nil {
		t.Fatal(err)
	}

	agg, err := Extract(store.ReadConn(), "audit-", 0)
	if err != nil {
		t.Fatal(err)
	}
	for sev, want := range map[string]int{"critical": 1, "high": 1, "medium": 0, "low": 1} {
		if agg.Totals[sev] != want {
			t.Errorf("Totals[%s] = %d, want %d (totals %v)", sev, agg.Totals[sev], want, agg.Totals)
		}
	}
	for i, want := range []string{"critical", "high", "low"} {
		if got := agg.AllFindings[i].Severity; got != want {
			t.Errorf("AllFindings[%d].Severity = %q, want %q", i, got, want)
		}
	}
	for i, f := range agg.ByLens["audit-v1"].Findings {
		if _, known := severityOrder[f.Severity]; !known {
			t.Errorf("ByLens finding %d keeps severity %q", i, f.Severity)
		}
	}

	md, err := Render(agg, Meta{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "3 findings (1 critical / 1 high / 0 medium / 1 low)") {
		t.Errorf("verdict line does not count the three findings:\n%s", md)
	}
	high := between(md, "## Top high-severity findings", "## All findings by lens")
	for _, row := range []string{"| critical | [crit.go:1]", "| high | [high.go:2]"} {
		if !strings.Contains(high, row) {
			t.Errorf("high-severity table lacks %q:\n%s", row, high)
		}
	}
	if strings.Contains(high, "low.go") {
		t.Errorf("high-severity table holds the low finding:\n%s", high)
	}
}

// between is the part of s after the first from and before the next to.
func between(s, from, to string) string {
	_, rest, _ := strings.Cut(s, from)
	part, _, _ := strings.Cut(rest, to)
	return part
}

// A severity the review does not know sorts after "low", not before
// "critical", where a map miss's zero rank would put it.
func TestSeverityRank_AnUnknownSeveritySortsLast(t *testing.T) {
	findings := []Finding{
		{Severity: "severe", File: "a.go", Issue: "unknown"},
		{Severity: "low", File: "b.go", Issue: "low"},
		{Severity: "critical", File: "c.go", Issue: "critical"},
	}
	got := bySeverity(findings)
	if got[0].Severity != "critical" || got[2].Severity != "severe" {
		t.Errorf("bySeverity order %q, %q, %q; want critical first and the unknown severity last",
			got[0].Severity, got[1].Severity, got[2].Severity)
	}
	if severityThenFile(findings[0], findings[1]) {
		t.Error("severityThenFile put an unknown severity before low")
	}
}
