// Package review aggregates self-review audit findings from
// workflow_node_states.rationale and renders a Markdown report.
//
// The two public entry points are:
//
//	Extract(db *sql.DB, prefix string, runID int64) (*Aggregate, error)  — extract findings
//	Render(a *Aggregate, meta Meta) (string, error)                      — render Markdown
//
// Both are pure functions of their inputs, so internal/cli's subcommands are
// thin (~30 lines each) and the test surface lives here.
package review

import "strings"

// Severities recognized by the renderer. Order matters — used for grouping
// the "top high-severity findings" table and for table-stable sort.
var severityOrder = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}

// severityRank is a severity's place in severityOrder; a severity the review
// does not know ranks after every known one.
func severityRank(severity string) int {
	if rank, known := severityOrder[severity]; known {
		return rank
	}
	return len(severityOrder)
}

// normalizeSeverity folds the case and spacing a lens may emit. Findings are
// written by agents, so "Critical", "HIGH" and " high " all turn up.
func normalizeSeverity(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// Finding is one issue surfaced by an audit-lens agent. Mirrors the shape
// the agent persona's contract emits in its JSON output.
type Finding struct {
	Lens     string `json:"lens,omitempty"`
	Node     string `json:"node,omitempty"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Issue    string `json:"issue"`
	Fix      string `json:"fix"`
}

// LensReport is one audit agent's full output (the JSON the agent returned).
type LensReport struct {
	Lens     string         `json:"lens"`
	Verdict  string         `json:"verdict,omitempty"`
	Summary  string         `json:"summary,omitempty"`
	Findings []Finding      `json:"findings"`
	Extra    map[string]any `json:"-"`
}

// Aggregate is the consolidated output of Extract: one LensReport per
// successful audit node, all flat findings, severity totals, and the list
// of nodes whose rationale couldn't be parsed.
type Aggregate struct {
	RunID         int64                 `json:"run_id"`
	ByLens        map[string]LensReport `json:"by_lens"`
	AllFindings   []Finding             `json:"all_findings"`
	Totals        map[string]int        `json:"totals"`
	ParseFailures []string              `json:"parse_failures"`
}

// Meta is the metadata banner the renderer puts at the top of REVIEW.md.
type Meta struct {
	Workflow  string
	Provider  string
	RunID     string
	CostUSD   float64
	TokensIn  int64
	TokensOut int64
}
