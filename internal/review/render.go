package review

import (
	"bytes"
	_ "embed"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"
)

//go:embed review.md.tmpl
var reviewTemplate string

// renderData is the template-context shape; kept private so the template
// stays a single concern (presentation).
type renderData struct {
	Date          string
	Meta          Meta
	HasMeta       bool
	HasTokens     bool
	TotalTokens   int64
	Verdict       string
	TotalFindings int
	Totals        map[string]int
	LensRows      []lensRow
	ParseFailures []string
	HighFindings  []Finding
	LensSections  []lensSection
}

type lensRow struct {
	Node    string
	Lens    string
	Verdict string
	Count   int
}

type lensSection struct {
	Node     string
	Lens     string
	Verdict  string
	Summary  string
	Findings []Finding
}

// Render produces the Markdown report for a given aggregate + metadata.
func Render(a *Aggregate, meta Meta) (string, error) {
	if a == nil {
		return "", fmt.Errorf("nil aggregate")
	}
	// Lens coverage rows and per-lens sections, sorted by node name for
	// stable output.
	nodes := slices.Sorted(maps.Keys(a.ByLens))
	return renderMarkdown(renderData{
		Date:          time.Now().UTC().Format("2006-01-02"),
		Meta:          meta,
		HasMeta:       meta.hasBanner(),
		HasTokens:     meta.hasUsage(),
		TotalTokens:   meta.TokensIn + meta.TokensOut,
		Verdict:       verdictOf(a.Totals),
		TotalFindings: a.Totals["critical"] + a.Totals["high"] + a.Totals["medium"] + a.Totals["low"],
		Totals:        a.Totals,
		LensRows:      lensRows(a.ByLens, nodes),
		ParseFailures: a.ParseFailures,
		HighFindings:  highFindings(a.AllFindings),
		LensSections:  lensSections(a.ByLens, nodes),
	})
}

// hasBanner reports whether the banner names a workflow, provider or run.
func (m Meta) hasBanner() bool {
	return m.Workflow != "" || m.Provider != "" || m.RunID != ""
}

// hasUsage reports whether the banner has tokens or a cost to show.
func (m Meta) hasUsage() bool {
	return m.TokensIn > 0 || m.TokensOut > 0 || m.CostUSD > 0
}

// verdictOf is the report's verdict, from the most severe total that is
// not zero.
func verdictOf(totals map[string]int) string {
	switch {
	case totals["critical"] > 0:
		return "**critical** — broken invariant somewhere"
	case totals["high"] > 0:
		return "**needs_work** — high-severity findings present"
	case totals["medium"] > 0:
		return "minor_issues"
	}
	return "clean"
}

// lensRows is the lens coverage table: one row per node, in nodes' order.
func lensRows(byLens map[string]LensReport, nodes []string) []lensRow {
	rows := make([]lensRow, 0, len(nodes))
	for _, n := range nodes {
		r := byLens[n]
		rows = append(rows, lensRow{
			Node:    n,
			Lens:    r.Lens,
			Verdict: r.Verdict,
			Count:   len(r.Findings),
		})
	}
	return rows
}

// highFindings is the top high-severity findings: severity in {critical,
// high}, in order.
func highFindings(all []Finding) []Finding {
	var high []Finding
	for _, f := range all {
		if f.Severity == "critical" || f.Severity == "high" {
			high = append(high, f)
		}
	}
	return high
}

// lensSections is one section per node, in nodes' order — the order of the
// coverage rows.
func lensSections(byLens map[string]LensReport, nodes []string) []lensSection {
	sections := make([]lensSection, 0, len(nodes))
	for _, n := range nodes {
		r := byLens[n]
		sections = append(sections, lensSection{
			Node:     n,
			Lens:     r.Lens,
			Verdict:  r.Verdict,
			Summary:  r.Summary,
			Findings: bySeverity(r.Findings),
		})
	}
	return sections
}

// bySeverity is a copy of findings sorted by severity for readability,
// keeping their order within a severity.
func bySeverity(findings []Finding) []Finding {
	out := append([]Finding(nil), findings...)
	sort.SliceStable(out, func(i, j int) bool {
		return severityRank(out[i].Severity) < severityRank(out[j].Severity)
	})
	return out
}

// renderMarkdown executes the report template on data.
func renderMarkdown(data renderData) (string, error) {
	t, err := template.New("review").Funcs(template.FuncMap{
		"loc":       formatLocation,
		"thousands": thousands,
	}).Delims("<<", ">>").Parse(reviewTemplate)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}

// formatLocation renders a finding's file:line as a clickable Markdown link
// when line > 0, falling back to plain link when line is missing.
func formatLocation(f Finding) string {
	if f.File == "" {
		return ""
	}
	if f.Line > 0 {
		return fmt.Sprintf("[%s:%d](%s#L%d)", f.File, f.Line, f.File, f.Line)
	}
	return fmt.Sprintf("[%s](%s)", f.File, f.File)
}

// thousands formats an integer with comma separators (1234567 → "1,234,567").
// Avoids pulling in golang.org/x/text/message just for this.
func thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	sign, digits := "", s
	if s[0] == '-' {
		sign, digits = "-", s[1:]
	}
	return sign + groupDigits(digits)
}

// groupDigits puts a comma between each group of three digits, counting from
// the right.
func groupDigits(s string) string {
	var out strings.Builder
	for i, ch := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out.WriteByte(',')
		}
		out.WriteRune(ch)
	}
	return out.String()
}
