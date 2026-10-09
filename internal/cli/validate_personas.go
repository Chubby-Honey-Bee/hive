package cli

import (
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
	"github.com/spf13/cobra"
)

// newSwarmValidatePersonasCmd implements `chb validate-personas`.
// It enforces template-v1
// compliance on every deliberation-eligible persona file under
// foragers/: required frontmatter keys, locked body section order,
// presence of the literal JSON-only emission guard string.
//
// Exits non-zero on any violation. Intended for CI.
func newSwarmValidatePersonasCmd() *cobra.Command {
	var (
		strict bool
		dir    string
	)
	cmd := &cobra.Command{
		Use:   "validate-personas",
		Short: "Validate every forager persona against template-v1 (frontmatter keys + body section order + JSON-only guard)",
		Long: `Validates each persona in foragers/<name>.md against the template-v1 spec
(see docs/forager-template.md and docs/specs/swarm.md § "Requirement:
Template-v1 personas").

Checked invariants:

  Frontmatter — required keys (template-v1):
    axis, non_overlap_with, model_tier_floor, behavioral_floor,
    forbidden_phrases, output_schema, length_caps, abstain_triggers.

  Body — sections in locked order:
    1. Output contract  → "## § 1"
    2. Decision rubric  → "## § 2"
    3. Worked example   → "## § 3"
    4. Anti-pattern     → "## § 4"
    5. Tie-breaker      → "## § 5"
    6. Bonds in prose   → "## § 6"
    7. JSON-only guard  → "## § 7" + the literal locked guard string
    8. Lens lore        → "## § 8"

  Locked emission-guard string MUST appear verbatim in body §7. Every
  persona in scope returns one JSON object, so one guard holds for all.

  Output contract — output_schema and length_caps parse as the swarm
  generator reads them (a JSON Schema subset; max_items becomes
  maxItems). A synthesizer's schema declares a string report, the
  Markdown synthesis.

The scope is the balanced-9 plus Queen, who is held to the same contract;
her Markdown synthesis is the report field of her JSON object. All ten
must be present. Any other
forager the balanced preset selects is checked too. Every other persona —
the specialists Framer, Surveyor, Forecaster and Dreamer, and render-layer
Editor — is skipped and named in the report.

Exits non-zero on any violation. Use --strict to also fail on warnings.

Examples:
  chb validate-personas
  chb validate-personas --dir foragers
  chb validate-personas --strict      # CI gate
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runValidatePersonas(cmd.OutOrStdout(), dir, strict)
		},
	}
	cmd.Flags().BoolVar(&strict, "strict", false, "treat warnings as failures (intended for CI)")
	cmd.Flags().StringVar(&dir, "dir", "", "foragers directory (default: $HIVE_FORAGERS_DIR, else foragers/ in the working directory or beside the binary, else the copy the binary carries)")
	return cmd
}

// runValidatePersonas validates the personas under dir, or wherever the
// foragers resolve from when dir is empty, and prints the report. It fails
// on a violation, and with strict on a warning too.
func runValidatePersonas(out io.Writer, dir string, strict bool) error {
	src := foragersSource()
	if dir != "" {
		src = foragers.DirSource(dir)
	}
	report, err := validatePersonas(src)
	if err != nil {
		return err
	}
	report.print(out)
	if report.failed(strict) {
		return fmt.Errorf("validate-personas failed: %d violation(s), %d warning(s)", report.violations, report.warnings)
	}
	return nil
}

// lockedEmissionGuard is the canonical template-v1 emission guard
// string. Every retained persona body §7 contains this string
// byte-for-byte. The string is also the single source of truth for the
// `behavioral_floor.must_emit_json_only` runner contract.
const lockedEmissionGuard = "Respond with the JSON object specified in §1 Output contract and nothing else. No prose preamble. No trailing commentary. No markdown code fences unless §1 explicitly specifies them. If you would otherwise abstain, emit the abstain JSON per §1, do not emit prose."

// requiredFrontmatterKeys lists every template-v1 frontmatter key the
// validator checks for. A missing key is a violation; foragers outside the
// scope below are not checked at all.
var requiredFrontmatterKeys = []string{
	"axis",
	"non_overlap_with",
	"model_tier_floor",
	"behavioral_floor",
	"forbidden_phrases",
	"output_schema",
	"length_caps",
	"abstain_triggers",
}

// requiredBodySections lists the §N section markers in their locked
// order. The validator checks each marker appears AND that they appear
// in order.
var requiredBodySections = []string{
	"## § 1",
	"## § 2",
	"## § 3",
	"## § 4",
	"## § 5",
	"## § 6",
	"## § 7",
	"## § 8",
}

type personaIssue struct {
	forager  string
	severity string // "violation" | "warning"
	message  string
}

type validationReport struct {
	issues     []personaIssue
	violations int
	warnings   int
	checked    int
	skipped    []string
}

func (r *validationReport) add(forager, severity, msg string) {
	r.issues = append(r.issues, personaIssue{forager: forager, severity: severity, message: msg})
	if severity == "violation" {
		r.violations++
	} else {
		r.warnings++
	}
}

// print writes the report: the counts, the skipped personas and every
// issue, sorted.
func (r *validationReport) print(w interface{ Write([]byte) (int, error) }) {
	r.sortIssues()
	fmt.Fprintf(w, "Validated %d persona(s).\n", r.checked)
	if len(r.skipped) > 0 {
		fmt.Fprintf(w, "Skipped (outside the template-v1 scope): %s\n", strings.Join(r.skipped, ", "))
	}
	for _, iss := range r.issues {
		fmt.Fprintf(w, "  [%s] %s: %s\n", iss.marker(), iss.forager, iss.message)
	}
	fmt.Fprintf(w, "\n%d violation(s), %d warning(s)\n", r.violations, r.warnings)
	if r.violations == 0 {
		fmt.Fprintln(w, "All checked personas conform to template-v1.")
	}
}

// sortIssues orders the issues by forager, then by severity, keeping the
// order they were found in within each.
func (r *validationReport) sortIssues() {
	sort.SliceStable(r.issues, func(i, j int) bool {
		if r.issues[i].forager != r.issues[j].forager {
			return r.issues[i].forager < r.issues[j].forager
		}
		return r.issues[i].severity < r.issues[j].severity
	})
}

// failed reports whether the report fails the command: on a violation, and
// with strict on a warning too.
func (r *validationReport) failed(strict bool) bool {
	return r.violations > 0 || (strict && r.warnings > 0)
}

// marker is the tag the issue is printed with.
func (iss personaIssue) marker() string {
	if iss.severity == "warning" {
		return "warning"
	}
	return "VIOLATION"
}

// templateV1Scope returns the names of foragers that must be present and
// conform to template-v1 — the shipped balanced-9 plus Queen.
// validatePersonas also checks any other forager the balanced preset
// selects, since that preset is computed from coverage. Other foragers (opt-in specialists like Framer, Dreamer,
// Forecaster, Surveyor; render-layer-only Editor) are out of scope for
// the validator's hard checks. A specialist that wants to be checked
// can be added explicitly to this list.
func templateV1Scope() map[string]bool {
	return map[string]bool{
		"architect":  true,
		"skeptic":    true,
		"timekeeper": true,
		"empiricist": true,
		"scholar":    true,
		"steward":    true,
		"pragmatist": true,
		"optimist":   true,
		"historian":  true,
		"queen":      true,
	}
}

// validatePersonas validates the foragers of src, reading each file through
// its tree, so the copy the binary carries is checked the same way as a
// directory.
func validatePersonas(src foragers.Source) (*validationReport, error) {
	all, err := src.Load()
	if err != nil {
		return nil, fmt.Errorf("load foragers from %s: %w", sourceLabel(src), err)
	}
	inScope := personaScope(all)
	report := &validationReport{}
	loaded := map[string]bool{}
	for _, w := range all {
		name := strings.ToLower(w.Name)
		if !inScope[name] {
			report.skipped = append(report.skipped, w.Name)
			continue
		}
		loaded[name] = true
		report.checkForagerFile(src, w)
	}
	report.addUnloaded(inScope, loaded)
	return report, nil
}

// personaScope names the foragers held to template-v1: templateV1Scope, and
// every forager the balanced preset selects. A forager that gains a
// coverage axis joins balanced, the `chb ask` default, so it is held to
// template-v1 too.
func personaScope(all []foragers.Forager) map[string]bool {
	inScope := templateV1Scope()
	for _, w := range foragers.Balanced(all) {
		inScope[strings.ToLower(w.Name)] = true
	}
	return inScope
}

// checkForagerFile counts the forager as checked and validates its file.
// It reads the file the registry loaded, which is the one the swarm
// dispatches: an earlier file declaring the same name shadows <name>.md,
// and checking <name>.md passed a persona that never runs.
func (r *validationReport) checkForagerFile(src foragers.Source, w foragers.Forager) {
	r.checked++
	raw, err := fs.ReadFile(src.FS, w.File)
	if err != nil {
		r.add(w.Name, "violation", fmt.Sprintf("could not read %s: %v", w.File, err))
		return
	}
	// Normalize line endings before parsing: the frontmatter regex and
	// section markers are LF-oriented, so a CRLF checkout (Windows,
	// core.autocrlf=true) would otherwise fail every persona. .gitattributes
	// also forces LF on checkout; this makes the parser robust regardless.
	validatePersona(r, w, strings.ReplaceAll(string(raw), "\r\n", "\n"))
}

// addUnloaded reports each scoped persona that did not load: Load skips a
// missing file or unparseable frontmatter silently, so without this a scoped
// persona in that state would be neither checked nor listed.
func (r *validationReport) addUnloaded(inScope, loaded map[string]bool) {
	for name := range inScope {
		if !loaded[name] {
			r.add(name, "violation", "not loaded (missing file or unparseable frontmatter)")
		}
	}
}

// validatePersona runs every template-v1 check against a single forager
// persona file. Errors are appended to report; this function returns
// nothing.
func validatePersona(report *validationReport, w foragers.Forager, raw string) {
	fm := extractFrontmatter(raw)
	body := extractBody(raw)
	report.checkFrontmatterKeys(w.Name, fm)
	report.checkBodySectionOrder(w.Name, body)
	report.checkEmissionGuard(w.Name, body)
	report.checkWorkedExample(w, body)
	report.checkVerdictEnum(w.Name, fm)
	report.checkOutputContract(w)
}

// checkFrontmatterKeys is check 1, frontmatter keys: it checks the raw text
// for `^key:` (since the YAML parser already accepted any valid YAML; we
// want to verify template-v1 keys are present as top-level frontmatter
// entries).
func (r *validationReport) checkFrontmatterKeys(forager, fm string) {
	for _, key := range requiredFrontmatterKeys {
		if !hasFrontmatterKey(fm, key) {
			r.add(forager, "violation", fmt.Sprintf("missing frontmatter key: %s", key))
		}
	}
}

// checkBodySectionOrder is check 2, body section order: each "## § N"
// marker must appear, in order.
func (r *validationReport) checkBodySectionOrder(forager, body string) {
	lastIdx := -1
	for _, marker := range requiredBodySections {
		idx := strings.Index(body, marker)
		if idx == -1 {
			r.add(forager, "violation", fmt.Sprintf("missing body section: %s", marker))
			continue
		}
		if idx < lastIdx {
			r.add(forager, "violation", fmt.Sprintf("body sections out of order at %s (offset %d < previous %d)", marker, idx, lastIdx))
		}
		lastIdx = idx
	}
}

// checkEmissionGuard is check 3, the emission guard: the locked literal
// string must appear verbatim in body §7. Every persona in scope returns
// one JSON object, the Queen included (her Markdown synthesis is its
// `report` field), so one guard holds for all of them, byte-for-byte.
func (r *validationReport) checkEmissionGuard(forager, body string) {
	if !strings.Contains(sectionBody(body, "## § 7", "## § 8"), lockedEmissionGuard) {
		r.add(forager, "violation", "missing locked JSON-only emission guard string verbatim in body §7")
	}
}

// checkWorkedExample is check 4, the worked example § 3: it must contain
// at least one fenced code block (json for lens foragers;
// markdown-containing-json for synthesizers). Catches personas that copy
// the section header but forget the example.
func (r *validationReport) checkWorkedExample(w foragers.Forager, body string) {
	s3 := sectionBody(body, "## § 3", "## § 4")
	if s3 != "" && !workedExampleHasBlock(s3, w.Archetype) {
		r.add(w.Name, "warning", "§ 3 Worked example exists but contains no fenced code block")
	}
}

// workedExampleHasBlock reports whether a worked example holds a fenced
// json block. Synthesizers may use ```markdown blocks containing JSON
// tails — accept either.
func workedExampleHasBlock(s3, archetype string) bool {
	if hasFencedJSON(s3) {
		return true
	}
	return archetype == foragers.ArchetypeSynthesizer && (strings.Contains(s3, "```markdown") || strings.Contains(s3, "```"))
}

// checkVerdictEnum is check 5, the output schema verdict enum:
// output_schema.properties.verdict.enum must contain the four canonical
// verdict values. (Queen is exempt from JSON-only but still must have
// verdict enum.)
func (r *validationReport) checkVerdictEnum(forager, fm string) {
	if !hasVerdictEnum(fm) {
		r.add(forager, "warning", "output_schema lacks verdict enum [support, oppose, conditional, abstain]")
	}
}

// checkOutputContract is check 6, the output contract: the swarm generator
// sends output_schema, with length_caps' max_items compiled in, on the
// persona's node; one that does not parse refuses the swarm. A
// synthesizer's reply is one JSON object, so its Markdown needs a string
// field: report.
func (r *validationReport) checkOutputContract(w foragers.Forager) {
	if w.ContractErr != nil {
		r.add(w.Name, "violation", w.ContractErr.Error())
		return
	}
	if w.Archetype == foragers.ArchetypeSynthesizer && !declaresStringReport(w) {
		r.add(w.Name, "violation", "output_schema declares no report property of type string; a synthesizer's Markdown synthesis goes there")
	}
}

// declaresStringReport reports whether the forager's output_schema declares
// a report property of type string.
func declaresStringReport(w foragers.Forager) bool {
	s := w.OutputSchema
	return s != nil && s.Property("report") != nil && slices.Equal(s.Property("report").Types, []string{"string"})
}

var frontmatterRE = regexp.MustCompile(`(?s)^---\n(.+?)\n---\n`)

func extractFrontmatter(raw string) string {
	m := frontmatterRE.FindStringSubmatch(raw)
	if m == nil {
		return ""
	}
	return m[1]
}

func extractBody(raw string) string {
	m := frontmatterRE.FindStringIndex(raw)
	if m == nil {
		return raw
	}
	return raw[m[1]:]
}

// hasFrontmatterKey checks for `^<key>:` at the top level (zero leading
// whitespace). Nested keys (e.g., `behavioral_floor.must_emit_json_only`)
// are not counted — only the top-level parent must be present.
func hasFrontmatterKey(fm, key string) bool {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `:`)
	return re.MatchString(fm)
}

// sectionBody returns the substring of body between the start marker
// and the next-section marker (or end-of-body if next is empty).
func sectionBody(body, start, next string) string {
	i := strings.Index(body, start)
	if i == -1 {
		return ""
	}
	rest := body[i+len(start):]
	if next == "" {
		return rest
	}
	j := strings.Index(rest, next)
	if j == -1 {
		return rest
	}
	return rest[:j]
}

var fencedJSONRE = regexp.MustCompile("(?s)```json\\s*\\n.+?\\n\\s*```")

func hasFencedJSON(s string) bool {
	return fencedJSONRE.MatchString(s)
}

// hasVerdictEnum looks for the four canonical verdict values as enum
// members anywhere in the output_schema block.
func hasVerdictEnum(fm string) bool {
	block, ok := outputSchemaBlock(fm)
	return ok && containsAllVerdicts(block)
}

var (
	outputSchemaStartRE = regexp.MustCompile(`(?m)^output_schema:`)
	frontmatterTopKeyRE = regexp.MustCompile(`(?m)^[a-z_]+:`)
)

// outputSchemaBlock finds the output_schema block — naive: from
// `^output_schema:` until the next top-level key (any `^[a-z_]+:`).
func outputSchemaBlock(fm string) (string, bool) {
	si := outputSchemaStartRE.FindStringIndex(fm)
	if si == nil {
		return "", false
	}
	rest := fm[si[1]:]
	if end := frontmatterTopKeyRE.FindStringIndex(rest); end != nil {
		return rest[:end[0]], true
	}
	return rest, true
}

// containsAllVerdicts reports whether block names all four canonical
// verdict values.
func containsAllVerdicts(block string) bool {
	for _, v := range []string{"support", "oppose", "conditional", "abstain"} {
		if !strings.Contains(block, v) {
			return false
		}
	}
	return true
}
