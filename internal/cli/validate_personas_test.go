package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
)

// validatePersonasDir validates the foragers under dir.
func validatePersonasDir(dir string) (*validationReport, error) {
	return validatePersonas(foragers.DirSource(dir))
}

// TestValidatePersonasDir_RealRepoPasses runs the validator against
// the real foragers/ directory and asserts zero violations across the
// template-v1 scope (the balanced-9 deliberation preset + Queen).
//
// This is the CI gate for template-v1 compliance — if a future commit
// edits a persona in a way that breaks the locked structure, this test
// catches it.
func TestValidatePersonasDir_RealRepoPasses(t *testing.T) {
	report, err := validatePersonasDir("../../foragers")
	if err != nil {
		t.Fatalf("validatePersonasDir: %v", err)
	}
	if report.violations > 0 {
		var msgs []string
		for _, iss := range report.issues {
			if iss.severity == "violation" {
				msgs = append(msgs, iss.forager+": "+iss.message)
			}
		}
		t.Fatalf("expected zero template-v1 violations, got %d:\n  %s", report.violations, strings.Join(msgs, "\n  "))
	}
	if report.checked < 10 {
		t.Fatalf("expected to check 10 personas (balanced-9 + queen), got %d", report.checked)
	}
}

// TestLockedEmissionGuard_MatchesSpec asserts the locked guard string
// matches the canonical string from docs/specs/swarm.md §
// "Persona body order" (template-v1). If the spec drifts, this test
// fails — and the spec or the validator must be brought back into sync
// in the same PR.
func TestLockedEmissionGuard_MatchesSpec(t *testing.T) {
	// The canonical literal — kept here verbatim so a single point of
	// truth fails loudly if either source drifts.
	const canonical = "Respond with the JSON object specified in §1 Output contract and nothing else. No prose preamble. No trailing commentary. No markdown code fences unless §1 explicitly specifies them. If you would otherwise abstain, emit the abstain JSON per §1, do not emit prose."
	if lockedEmissionGuard != canonical {
		t.Errorf("lockedEmissionGuard drifted from canonical spec literal")
	}

	// Read the spec, rather than only comparing two copies of the same
	// literal in this file: the doc comment promises this fails when the
	// spec drifts, and without this the spec is never opened at all.
	spec, err := os.ReadFile("../../docs/specs/swarm.md")
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	if !strings.Contains(string(spec), canonical) {
		t.Errorf("the canonical literal is absent from docs/specs/swarm.md — "+
			"the spec and the validator have drifted apart:\n  %s", canonical)
	}

}

// tailGuard is a §7 guard for a Markdown synthesis with a JSON tail, which
// no persona may carry: every persona returns one JSON object.
const tailGuard = "Respond with the JSON object specified in §1 Output contract as the final content of your output, on its own line after the Markdown synthesis. No prose preamble before the JSON, no trailing commentary after the JSON. No markdown code fences around the JSON itself. If you would otherwise abstain, emit the abstain JSON per §1; do not abstain via prose."

// rewritePersona replaces old with new in dir/<name>.md, failing when old is
// not there.
func rewritePersona(t *testing.T, dir, name, old, new string) {
	t.Helper()
	path := filepath.Join(dir, name+".md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), old) {
		t.Fatalf("%s.md holds no %q", name, old)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(raw), old, new, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Queen returns one JSON object like every lens, so she is held to the
// JSON-only guard: a tail-after-Markdown guard in her body is a violation.
func TestValidatePersonas_SynthesizerHoldsTheJSONOnlyGuard(t *testing.T) {
	dir := copyForagers(t)
	rewritePersona(t, dir, "queen", lockedEmissionGuard, tailGuard)
	report, err := validatePersonasDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	v := violationsFor(report, "queen")
	if len(v) != 1 || !strings.Contains(v[0], "JSON-only emission guard") {
		t.Errorf("queen violations %v, want one naming the JSON-only guard", v)
	}
}

// An output contract the generator cannot read is a violation naming the
// key, and so is a synthesizer schema with nowhere to put the Markdown.
func TestValidatePersonas_OutputContractMustParse(t *testing.T) {
	cases := []struct {
		persona, old, new, want string
	}{
		{"architect", "key_points: {max_items: 7", "key_points: {max_items: seven", "length_caps.key_points.max_items"},
		{"skeptic", "recommendation: {max_chars: 320}", "recommendation: {max_char: 320}", "length_caps.recommendation.max_char"},
		{"historian", "recommendation: {type: string}", "recommendation: {type: string, pattern: x}", "output_schema: properties.recommendation.pattern"},
		{"queen", "report: {type: string}", "summary: {type: string}", "report"},
	}
	for _, c := range cases {
		t.Run(c.persona, func(t *testing.T) {
			dir := copyForagers(t)
			rewritePersona(t, dir, c.persona, c.old, c.new)
			if c.persona == "queen" {
				rewritePersona(t, dir, c.persona, "required: [report, ", "required: [summary, ")
			}
			report, err := validatePersonasDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			v := violationsFor(report, c.persona)
			if len(v) != 1 || !strings.Contains(v[0], c.want) {
				t.Errorf("%s violations %v, want one naming %s", c.persona, v, c.want)
			}
			if report.violations != len(v) {
				t.Errorf("violations elsewhere: %+v", report.issues)
			}
		})
	}
}

// The help cites the swarm spec's section by a heading the spec has.
func TestValidatePersonasHelp_CitesASpecHeading(t *testing.T) {
	spec, err := os.ReadFile("../../docs/specs/swarm.md")
	if err != nil {
		t.Fatal(err)
	}
	headings := map[string]bool{}
	for _, line := range strings.Split(string(spec), "\n") {
		if h, ok := strings.CutPrefix(line, "### "); ok {
			headings[strings.TrimSpace(h)] = true
		}
	}
	long := newSwarmValidatePersonasCmd().Long
	// A citation may join several sections: § "A" + "B".
	var cites []string
	quoted := regexp.MustCompile(`"([^"]+)"`)
	for _, c := range regexp.MustCompile(`docs/specs/swarm\.md\s+§\s+("[^"]+"(?:\s*\+\s*"[^"]+")*)`).FindAllStringSubmatch(long, -1) {
		for _, q := range quoted.FindAllStringSubmatch(c[1], -1) {
			cites = append(cites, strings.Join(strings.Fields(q[1]), " "))
		}
	}
	if len(cites) == 0 {
		t.Fatalf("the help cites no swarm.md section:\n%s", long)
	}
	for _, name := range cites {
		if !headings[name] {
			t.Errorf("the help cites swarm.md § %q, which is not a heading there", name)
		}
	}
}
