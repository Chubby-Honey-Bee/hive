package review

// Self-implement-loop generator:
//
// Turns a filtered Aggregate of self-review Findings into a workflow YAML
// that an autonomous agent-run can dispatch end-to-end. One agent node per
// finding, each gated on compile_ok + tests_pass, with a final gate that
// re-runs the full regression. Pure logic — no DB, no IO beyond reading
// the embedded template — so it's easy to unit-test.

import (
	"bytes"
	_ "embed"
	"fmt"
	"sort"
	"strings"
	"text/template"
	"time"
)

//go:embed implement.go.tmpl
var implementTemplate string

// ImplementOptions controls the generated workflow's shape. All fields
// have sensible defaults applied via WithDefaults().
type ImplementOptions struct {
	// Severity filter — only findings whose severity matches one of these
	// values are included. Default: ["critical", "high"].
	Severities []string

	// MaxFixes caps the number of fixes generated; 0 means no cap, as both
	// CLI flags document, and WithDefaults keeps it. The flags default to 5,
	// so a run does not try to fix everything in one shot.
	MaxFixes int

	// Model used for the initial fix attempt on every fix node. Empty: the
	// node names `tier: worker` and the budget mode picks the model.
	Model string

	// RepairModel used by on_reject:. Empty: the block names
	// `tier: synthesist` and the budget mode picks the model.
	RepairModel string

	// Name embedded into the workflow YAML. Default: "self-implement-<date>".
	Name string
}

// WithDefaults returns a copy of opts with empty fields filled in.
func (o ImplementOptions) WithDefaults() ImplementOptions {
	out := o
	if len(out.Severities) == 0 {
		out.Severities = []string{"critical", "high"}
	}
	if out.Name == "" {
		out.Name = "self-implement-" + time.Now().UTC().Format("2006-01-02")
	}
	return out
}

// GenerateImplementWorkflow renders a workflow YAML from the given findings
// and options. Returns the YAML string and the count of fixes included.
//
// Filtering rules:
//  1. Drop findings whose severity is not in opts.Severities.
//  2. Drop findings missing both file and issue (synthesizer artifacts).
//  3. Sort by severity ascending (critical → high → medium → low) then
//     by file path so output is deterministic across runs.
//  4. Truncate to opts.MaxFixes.
func GenerateImplementWorkflow(agg *Aggregate, opts ImplementOptions) (string, int, error) {
	if agg == nil {
		return "", 0, fmt.Errorf("nil aggregate")
	}
	opts = opts.WithDefaults()
	candidates := fixCandidates(agg.AllFindings, opts)
	if len(candidates) == 0 {
		return "", 0, fmt.Errorf("no findings matched severity filter %v after reading %d total findings", opts.Severities, len(agg.AllFindings))
	}
	fixes := fixNodes(candidates)
	out, err := renderWorkflow(implementData{
		Name:        opts.Name,
		Date:        time.Now().UTC().Format("2006-01-02 15:04:05Z"),
		Model:       opts.Model,
		RepairModel: opts.RepairModel,
		Fixes:       fixes,
	})
	if err != nil {
		return "", 0, err
	}
	return out, len(fixes), nil
}

// fixCandidates applies filtering rules 1-4 to all.
func fixCandidates(all []Finding, opts ImplementOptions) []Finding {
	// stringSet lowercases the requested severities and normalizeSeverity
	// the finding's, so a lens's "Critical" or "HIGH" matches; the input may
	// be a hand-written findings.json that Extract never saw.
	wanted := stringSet(opts.Severities)
	candidates := make([]Finding, 0, len(all))
	for _, f := range all {
		f.Severity = normalizeSeverity(f.Severity)
		if isFixCandidate(f, wanted) {
			candidates = append(candidates, f)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return fixOrder(candidates[i], candidates[j]) })
	return firstN(candidates, opts.MaxFixes)
}

// isFixCandidate reports whether f is of a wanted severity and names both a
// file and an issue; a finding missing either is dropped as a synthesizer
// artifact.
func isFixCandidate(f Finding, wanted map[string]bool) bool {
	return wanted[f.Severity] && f.File != "" && f.Issue != ""
}

// fixOrder orders findings by severity ascending, then file, then line.
func fixOrder(x, y Finding) bool {
	if sx, sy := severityRank(x.Severity), severityRank(y.Severity); sx != sy {
		return sx < sy
	}
	if x.File != y.File {
		return x.File < y.File
	}
	return x.Line < y.Line
}

// firstN is the first n findings, or all of them when n is 0 or more than
// there are.
func firstN(findings []Finding, n int) []Finding {
	if n > 0 && len(findings) > n {
		return findings[:n]
	}
	return findings
}

// fixNode is one fix node of the generated workflow.
type fixNode struct {
	NodeIndex int
	Lens      string
	Severity  string
	File      string
	Line      int
	Issue     string
	Fix       string
}

// implementData is the template context of the generated workflow.
type implementData struct {
	Name        string
	Date        string
	Model       string
	RepairModel string
	Fixes       []fixNode
}

// fixNodes is one fix node per candidate, numbered from 1.
func fixNodes(candidates []Finding) []fixNode {
	fixes := make([]fixNode, 0, len(candidates))
	for i, f := range candidates {
		fixes = append(fixes, fixNode{
			NodeIndex: i + 1,
			Lens:      orFallback(f.Lens, "(unknown)"),
			Severity:  f.Severity,
			File:      f.File,
			Line:      f.Line,
			Issue:     trimToOneLine(f.Issue),
			Fix:       trimToOneLine(orFallback(f.Fix, "(no specific fix provided — apply minimal change implied by issue)")),
		})
	}
	return fixes
}

// renderWorkflow executes the workflow template on data.
func renderWorkflow(data implementData) (string, error) {
	t, err := template.New("implement").Delims("<<", ">>").Parse(implementTemplate)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}

func stringSet(in []string) map[string]bool {
	out := make(map[string]bool, len(in))
	for _, s := range in {
		out[strings.ToLower(strings.TrimSpace(s))] = true
	}
	return out
}

func orFallback(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// trimToOneLine collapses internal whitespace runs into single spaces and
// strips the leading + trailing whitespace, so the YAML scalar stays on
// one logical line. Multi-line issue/fix text would otherwise inject
// `\n` characters that break the YAML block.
func trimToOneLine(s string) string {
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}
