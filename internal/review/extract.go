package review

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// Extract reads workflow_node_states.rationale rows whose node_name starts
// with `prefix` (typically "audit-"), extracts the trailing JSON object via
// workflow.ExtractJSONOutput, and aggregates the result.
//
// Nodes whose rationale doesn't parse as a Finding-shaped JSON object are
// returned in ParseFailures — the renderer surfaces them as ❌-rejected
// rows in the "Lens coverage" table. We deliberately reuse the extractor the
// runner reads node outputs with rather than maintaining a separate regex
// implementation.
func Extract(readDB *sql.DB, prefix string, runID int64) (*Aggregate, error) {
	if readDB == nil {
		return nil, fmt.Errorf("nil DB")
	}
	runID, found, err := runToReview(readDB, runID)
	if err != nil {
		return nil, err
	}
	if !found {
		return newAggregate(0), nil
	}
	return aggregateRun(readDB, cmp.Or(prefix, "audit-"), runID)
}

// runToReview is the run a review aggregates: runID, or the most recent run
// when it is zero; found is false when there is no run. A review is scoped
// to one run: aggregating across every run in the database would resurrect
// the previous run's findings in a second review and feed them to the fixer
// as if they were new.
func runToReview(readDB *sql.DB, runID int64) (int64, bool, error) {
	if runID != 0 {
		return runID, true, nil
	}
	err := readDB.QueryRow(`SELECT id FROM workflow_runs ORDER BY id DESC LIMIT 1`).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("latest run: %w", err)
	}
	return runID, true, nil
}

// newAggregate is run runID's aggregate before any node is read.
func newAggregate(runID int64) *Aggregate {
	return &Aggregate{
		RunID:         runID,
		ByLens:        map[string]LensReport{},
		Totals:        map[string]int{"critical": 0, "high": 0, "medium": 0, "low": 0},
		ParseFailures: []string{},
		AllFindings:   []Finding{},
	}
}

// aggregateRun aggregates the rationale of run runID's nodes whose names
// start with prefix.
func aggregateRun(readDB *sql.DB, prefix string, runID int64) (*Aggregate, error) {
	rows, err := readDB.Query(
		`SELECT node_name, COALESCE(rationale, '')
		 FROM workflow_node_states
		 WHERE run_id = ? AND node_name LIKE ? AND rationale IS NOT NULL`,
		runID, prefix+"%",
	)
	if err != nil {
		return nil, fmt.Errorf("query rationale: %w", err)
	}
	defer rows.Close()
	out := newAggregate(runID)
	if err := out.scan(rows); err != nil {
		return nil, err
	}
	sort.Strings(out.ParseFailures)
	sort.SliceStable(out.AllFindings, func(i, j int) bool {
		return severityThenFile(out.AllFindings[i], out.AllFindings[j])
	})
	return out, nil
}

// scan adds each (node, rationale) row of rows to the aggregate.
func (a *Aggregate) scan(rows *sql.Rows) error {
	for rows.Next() {
		var node, rationale string
		if err := rows.Scan(&node, &rationale); err != nil {
			return fmt.Errorf("scan: %w", err)
		}
		a.addNode(node, rationale)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate: %w", err)
	}
	return nil
}

// addNode adds one audit node's rationale: its report and its findings, or
// a parse failure.
func (a *Aggregate) addNode(node, rationale string) {
	report, ok := parseRationale(rationale)
	if !ok || report.Lens == "" {
		a.ParseFailures = append(a.ParseFailures, node)
		return
	}
	a.ByLens[node] = report
	for _, f := range report.Findings {
		f.Lens = report.Lens
		f.Node = node
		a.AllFindings = append(a.AllFindings, f)
		a.count(f.Severity)
	}
}

// count adds a finding of severity to the totals, when it is one the
// renderer recognizes.
func (a *Aggregate) count(severity string) {
	if _, known := severityOrder[severity]; known {
		a.Totals[severity]++
	}
}

// severityThenFile orders findings by severity, then by file.
func severityThenFile(x, y Finding) bool {
	if rx, ry := severityRank(x.Severity), severityRank(y.Severity); rx != ry {
		return rx < ry
	}
	return x.File < y.File
}

// parseRationale extracts the LensReport from one agent's full final text.
// Reuses workflow.ExtractJSONOutput so the extraction rules stay in lockstep
// with the dispatcher's own output-parsing path. Severities are normalised
// here, where findings are read, so the totals, the high-severity table, the
// per-lens sections and the fix workflow all see one spelling, and a lens's
// "Critical" or "HIGH" counts in each.
func parseRationale(text string) (LensReport, bool) {
	jsonMap := workflow.ExtractJSONOutput(text)
	if !isReportJSON(jsonMap) {
		return LensReport{}, false
	}
	report, err := decodeReport(jsonMap)
	if err != nil || report.Lens == "" {
		return LensReport{}, false
	}
	normalizeSeverities(report.Findings)
	return report, true
}

// normalizeSeverities normalises the severity of each finding in place.
func normalizeSeverities(findings []Finding) {
	for i := range findings {
		findings[i].Severity = normalizeSeverity(findings[i].Severity)
	}
}

// isReportJSON reports whether the extractor found a JSON object.
// workflow.ExtractJSONOutput falls back to {"final_text": …} when no JSON
// was found — that's not a real lens report, so reject it explicitly.
func isReportJSON(jsonMap map[string]any) bool {
	if jsonMap == nil {
		return false
	}
	_, isFallback := jsonMap["final_text"]
	return !isFallback || len(jsonMap) != 1
}

// decodeReport re-marshals the extracted object, then unmarshals it into
// the typed shape. Cleaner than reflecting over map[string]any field by
// field.
func decodeReport(jsonMap map[string]any) (LensReport, error) {
	raw, err := json.Marshal(jsonMap)
	if err != nil {
		return LensReport{}, err
	}
	var report LensReport
	err = json.Unmarshal(raw, &report)
	return report, err
}
