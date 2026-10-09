package mcp

// chb_mss_repo_audit: a deterministic walk of a repository that classifies
// each surface into the four MSS labels and flags missing functionality. It
// calls no model; it is the cheap structural first pass before the five-lens
// audit chb_self_review runs.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func mssRepoAuditSpec() map[string]any {
	return map[string]any{
		"name":        "chb_mss_repo_audit",
		"title":       "MSS repo audit",
		"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
		"description": "Walk a repository and produce an MSS-framework audit: classify each surface (governance, binary, swarm, docs, ci, runner, ...) into MSS labels (definition / guarantee / assumption / unknown) and emit a structured JSON report: per-label counts, the entries by surface, missing-functionality flags and a one-line summary (sufficiency is not assessed). Reproducible by any MCP host: takes only a repo path. Deterministic — re-running on an unchanged repo gives the same report apart from its generated_at timestamp.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo_path":      map[string]any{"type": "string", "description": "Absolute path to the repository to audit. Defaults to the MCP server's repo-root resolution."},
				"include_hidden": map[string]any{"type": "boolean", "description": "Whether to descend into dot-prefixed directories such as .github. Default false."},
				"max_depth":      map[string]any{"type": "integer", "description": "Maximum directory depth to walk. Default 4."},
			},
		},
	}
}

// handleMSSRepoAudit produces a deterministic MSS-framework audit of a
// repository by walking its filesystem surface and classifying each
// salient artifact into one of the four MSS labels:
//
//   - definition: a chosen artifact, irreducible (LICENSE, CHANGELOG,
//     governance files, the binary entrypoint, the specs under docs/specs/)
//   - guarantee:  derivable from definitions + assumptions and verified
//     by an automated check (CI workflows, regression-gate, validate)
//   - assumption: a bet that could be wrong (multi-arch publish target,
//     dimension defaults, optional foragers being optional)
//   - unknown:    an honest gap (unaudited dependencies, license of
//     transitive deps, untested surfaces)
//
// Reproducible by any MCP host. The handler uses no external state
// other than `repo_path` and emits a structured JSON report so callers
// can act on it programmatically (for example, on `missing_functionality`).
//
// What this is NOT: it doesn't replace `chb_self_review`, which
// runs an LLM-driven 5-lens audit. This is the structural-only first
// pass, which is cheap (no LLM calls), repeatable, and acts as input
// to the deeper five-lens review.
func (s *mcpServer) handleMSSRepoAudit(ctx context.Context, req rpcRequest, args map[string]any) {
	repoPath := stringArg(args, "repo_path")
	if repoPath == "" {
		repoPath = s.repoRoot()
	}
	includeHidden := boolArg(args, "include_hidden")
	maxDepth := intArgDefault(args, "max_depth", 4)

	report := buildMSSRepoAudit(repoPath, includeHidden, maxDepth)
	s.writeJSONToolResult(req.ID, report)
}

// mssRepoAuditReport is the structured JSON shape returned by
// chb_mss_repo_audit. Stable across releases; new fields may be
// added but existing ones won't be removed without a major version.
type mssRepoAuditReport struct {
	RepoPath             string                     `json:"repo_path"`
	GeneratedAt          string                     `json:"generated_at"`
	Counts               map[string]int             `json:"mss_label_counts"`
	BySurface            map[string][]mssAuditEntry `json:"by_surface"`
	MissingFunctionality []string                   `json:"missing_functionality"`
	Verdict              string                     `json:"verdict"`
}

// mssAuditEntry is one surface (file, directory, capability) classified
// against the MSS partition.
type mssAuditEntry struct {
	Path    string `json:"path"`
	Label   string `json:"mss_label"` // definition | guarantee | assumption | unknown
	Surface string `json:"surface"`   // governance | binary | docs | ci | foragers | ...
	Reason  string `json:"reason"`
}

// mssAuditRule classifies the paths it matches as one surface, under one
// MSS label, for one reason.
type mssAuditRule struct {
	match   func(rel string) bool
	surface string
	label   string
	reason  string
}

// mssAuditRules are the surface classification rules; the first that
// matches a path classifies it. Each rule maps a path-relative pattern to
// (surface, label, reason). The patterns below encode the MSS framework's
// own audit of the HIVE repo; they're general enough to apply to any Go
// project that follows the same conventions.
var mssAuditRules = []mssAuditRule{
	// ── governance: the OSS contract ────────────────────────────
	{func(r string) bool { return r == "LICENSE" }, "governance", "definition", "Chosen license — defines the legal contract."},
	{func(r string) bool { return r == "CONTRIBUTING.md" }, "governance", "definition", "Chosen contribution workflow."},
	{func(r string) bool { return r == "SECURITY.md" }, "governance", "definition", "Chosen disclosure policy."},
	{func(r string) bool { return r == "CHANGELOG.md" }, "governance", "guarantee", "Derived from git log + version tags."},
	{func(r string) bool { return r == "README.md" }, "governance", "definition", "Chosen public-facing pitch."},
	// First match wins, so the specific CI rule has to precede the general
	// .github/ rule — placed after it, the CI guarantee could never match.
	{func(r string) bool { return strings.HasPrefix(r, ".github/workflows/") && strings.HasSuffix(r, ".yml") }, "ci", "guarantee", "Derived from spec — each workflow runs the jobs it declares on the triggers it declares."},
	{func(r string) bool { return strings.HasPrefix(r, ".github/") }, "governance", "definition", "Chosen GitHub-specific surfaces (issue/PR templates, workflows)."},
	// ── docker / packaging ──────────────────────────────────────
	{func(r string) bool { return r == "Dockerfile" }, "docker", "definition", "Chosen container build."},
	{func(r string) bool { return r == ".dockerignore" }, "docker", "guarantee", "Derived from build-context-leanness goal."},
	{func(r string) bool { return r == "docker-compose.yml" }, "docker", "definition", "Chosen local-dev surface."},
	{func(r string) bool { return r == ".goreleaser.yaml" }, "docker", "definition", "Chosen release-publishing config."},
	// ── binaries ────────────────────────────────────────────────
	{func(r string) bool { return strings.HasPrefix(r, "internal/cli/") }, "binary", "definition", "Chosen CLI entrypoint."},
	{func(r string) bool { return strings.HasPrefix(r, "internal/mcp/") }, "binary", "definition", "Chosen MCP entrypoint."},
	// ── swarm / foragers ──────────────────────────────────────
	{func(r string) bool {
		return strings.HasPrefix(r, "foragers/") && strings.HasSuffix(r, ".md") && !strings.HasSuffix(r, "README.md")
	}, "swarm", "definition", "One analytical lens — chosen MSS partition over perspective-space."},
	{func(r string) bool { return r == "foragers/palette.json" }, "swarm", "guarantee", "Derived from forager frontmatter via `chb palette`."},
	// ── workflows / agents (the dispatch surface) ──────────────
	{func(r string) bool { return strings.HasPrefix(r, "workflows/") && strings.HasSuffix(r, ".yaml") }, "workflow", "definition", "Chosen reusable workflow."},
	{func(r string) bool { return strings.HasPrefix(r, "agents/") && strings.HasSuffix(r, ".md") }, "agent", "definition", "Chosen specialist persona."},
	// ── ci / regression-gate ───────────────────────────────────
	// ── specs (the contract) ───────────────────────────────────
	{func(r string) bool { return strings.HasPrefix(r, "docs/specs/") && strings.HasSuffix(r, ".md") }, "spec", "definition", "Chosen formal spec."},
	// ── docs ───────────────────────────────────────────────────
	{func(r string) bool { return strings.HasPrefix(r, "docs/") && strings.HasSuffix(r, ".md") }, "docs", "definition", "Chosen reference doc — extracted from README."},
	{func(r string) bool { return r == "docs/assumptions.md" }, "docs", "assumption", "Catalog of bets that could be wrong; the assumption-itself document."},
	// ── lean4 (formal verification) ────────────────────────────
	{func(r string) bool { return strings.HasPrefix(r, "lean4/") }, "lean4", "guarantee", "Mechanical proof — derived from definitions + assumptions."},
	// ── fixtures / tests ──────────────────────────────────────
	{func(r string) bool { return r == "fixtures/cli-behavior.jsonl" }, "ci", "guarantee", "Byte-equal replay contract — derived from current binary output."},
	{func(r string) bool { return strings.HasSuffix(r, "_test.go") }, "ci", "guarantee", "Test — derived from spec via assertions."},
	// ── scripts ─────────────────────────────────────────────────
	{func(r string) bool { return strings.HasPrefix(r, "scripts/") }, "scripts", "definition", "Chosen developer-flow shim."},
}

// buildMSSRepoAudit is the deterministic walker. Pure function for
// testability — no I/O beyond os.Stat / os.ReadDir on repoPath.
func buildMSSRepoAudit(repoPath string, includeHidden bool, maxDepth int) *mssRepoAuditReport {
	report := &mssRepoAuditReport{
		RepoPath:    repoPath,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Counts:      map[string]int{"definition": 0, "guarantee": 0, "assumption": 0, "unknown": 0},
		BySurface:   make(map[string][]mssAuditEntry),
	}
	walk(repoPath, "", maxDepth, includeHidden, func(rel string, _ os.FileInfo) {
		report.classify(rel)
	})

	// Missing-functionality flags: capabilities the MSS framework
	// implies but that aren't surfaced in the repo. Each one is a
	// concrete observation about the audit gap.
	report.MissingFunctionality = detectMissingFunctionality(repoPath)

	report.Verdict = labelSummary(report)
	return report
}

// classify files a walked path under the first rule that matches it. An
// unmatched path is an honest unknown: it has not been classified, which is
// itself an MSS observation, counted so the report never claims everything
// was classified.
func (r *mssRepoAuditReport) classify(rel string) {
	for _, rule := range mssAuditRules {
		if rule.match(rel) {
			r.add(mssAuditEntry{Path: rel, Label: rule.label, Surface: rule.surface, Reason: rule.reason})
			return
		}
	}
	r.add(mssAuditEntry{Path: rel, Label: "unknown", Surface: "unclassified",
		Reason: "No rule classifies this path."})
}

// add files an entry under its surface and counts its label.
func (r *mssRepoAuditReport) add(e mssAuditEntry) {
	r.BySurface[e.Surface] = append(r.BySurface[e.Surface], e)
	r.Counts[e.Label]++
}

// labelSummary is the one line the walk can defend: the label counts. Whether
// every guarantee has a backing definition and assumption is not assessed by
// a path walk, so the summary says so.
func labelSummary(report *mssRepoAuditReport) string {
	return fmt.Sprintf("MSS labels: %d definitions, %d guarantees, %d assumptions, %d unknowns. Sufficiency not assessed.",
		report.Counts["definition"], report.Counts["guarantee"], report.Counts["assumption"], report.Counts["unknown"])
}

// walk is a small bounded directory walker. Skips hidden directories
// when includeHidden is false, caps recursion at maxDepth so a runaway
// symlink or a deeply-nested vendored tree doesn't lock up the audit.
func walk(root, rel string, depth int, includeHidden bool, visit func(rel string, info os.FileInfo)) {
	w := repoWalker{root: root, includeHidden: includeHidden, visit: visit}
	w.dir(rel, depth)
}

// repoWalker is one bounded walk of a repository.
type repoWalker struct {
	root          string
	includeHidden bool
	visit         func(rel string, info os.FileInfo)
}

// dir visits the entries of the directory rel, and descends depth levels
// below it.
func (w repoWalker) dir(rel string, depth int) {
	if depth < 0 {
		return
	}
	entries, err := os.ReadDir(filepath.Join(w.root, rel))
	if err != nil {
		return
	}
	for _, e := range entries {
		w.entry(rel, e, depth)
	}
}

// entry visits one entry of the directory rel, and walks into it when it
// is a directory.
func (w repoWalker) entry(rel string, e os.DirEntry, depth int) {
	if w.skips(rel, e.Name()) {
		return
	}
	info, err := e.Info()
	if err != nil {
		return
	}
	childRel := filepath.Join(rel, e.Name())
	w.visit(childRel, info)
	if e.IsDir() {
		w.dir(childRel, depth-1)
	}
}

// walkedDotNames are the dot-prefixed names the walk visits even when it
// skips hidden ones.
var walkedDotNames = map[string]bool{".github": true, ".dockerignore": true, ".gitignore": true, ".goreleaser.yaml": true}

// unwalkedRootNames are the root directories the walk skips: the
// workspaces dir — per-project research data, not the repo's own
// surface — and dependency trees.
var unwalkedRootNames = map[string]bool{"workspace": true, "node_modules": true, "vendor": true}

// skips reports whether the walk passes over the entry name of the
// directory rel: a hidden one (hides), or one of the root directories that
// are not the repo's surface.
func (w repoWalker) skips(rel, name string) bool {
	return w.hides(name) || rel == "" && unwalkedRootNames[name]
}

// hides reports whether the walk skips name as hidden: dot-prefixed, and
// not one of walkedDotNames, when hidden entries are not walked.
func (w repoWalker) hides(name string) bool {
	return !w.includeHidden && strings.HasPrefix(name, ".") && !walkedDotNames[name]
}

// detectMissingFunctionality probes for capabilities the MSS
// framework implies should exist but don't appear at the expected
// path. Returned as plain strings so the caller's LLM can act on them.
func detectMissingFunctionality(repoPath string) []string {
	var missing []string
	probe := func(rel, why string) {
		if _, err := os.Stat(filepath.Join(repoPath, rel)); err != nil {
			missing = append(missing, fmt.Sprintf("%s — %s", rel, why))
		}
	}

	// Standard OSS surfaces — flag what's not there.
	probe("CHANGELOG.md", "Without it, the release history requires reading git log; users can't tell what changed at a glance.")
	probe("docs/assumptions.md", "MSS framework demands every bet be labeled; without this doc, assumptions live in code comments where they decay.")
	probe(".github/workflows/ci.yml", "Locks the local quality gates into CI; without it, drift ships silently.")

	return missing
}
