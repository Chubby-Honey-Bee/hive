package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
)

// copyForagers copies the shipped foragers/ into a temp dir the test can
// break without touching the repo.
func copyForagers(t *testing.T) string {
	t.Helper()
	src := "../../foragers"
	dst := t.TempDir()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

// violationsFor returns the violation messages the report holds for one forager.
func violationsFor(r *validationReport, forager string) []string {
	var out []string
	for _, iss := range r.issues {
		if iss.forager == forager && iss.severity == "violation" {
			out = append(out, iss.message)
		}
	}
	return out
}

// A scoped persona that Load drops — deleted, or with frontmatter that does
// not parse — is a violation, not a silent pass.
func TestValidatePersonas_UnloadedScopedPersonaIsAViolation(t *testing.T) {
	dir := copyForagers(t)
	if err := os.Remove(filepath.Join(dir, "queen.md")); err != nil {
		t.Fatal(err)
	}
	hist := filepath.Join(dir, "historian.md")
	raw, err := os.ReadFile(hist)
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(raw), "---\n", "---\nbad: [unclosed\n", 1)
	if err := os.WriteFile(hist, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	dropped := []string{"queen", "historian"}

	report, err := validatePersonasDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if report.violations != len(dropped) {
		t.Errorf("violations = %d, want %d (one per dropped persona): %+v", report.violations, len(dropped), report.issues)
	}
	for _, name := range dropped {
		if v := violationsFor(report, name); len(v) != 1 || !strings.Contains(v[0], "not loaded") {
			t.Errorf("%s: violations %v, want one 'not loaded'", name, v)
		}
	}
}

// The validator checks the file the registry loaded. An earlier file that
// declares `name: skeptic` shadows skeptic.md, and it is the one dispatched.
func TestValidatePersonas_ChecksTheShadowingFile(t *testing.T) {
	dir := copyForagers(t)
	shadow := "---\nname: skeptic\n---\nSHADOW skeptic, free-form, no template-v1 structure.\n"
	if err := os.WriteFile(filepath.Join(dir, "a-skeptic.md"), []byte(shadow), 0o644); err != nil {
		t.Fatal(err)
	}
	// The shadow has none of the keys, none of the sections, and no guard.
	want := len(requiredFrontmatterKeys) + len(requiredBodySections) + 1

	report, err := validatePersonasDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := violationsFor(report, "skeptic"); len(got) != want {
		t.Errorf("skeptic violations = %d, want %d: %v", len(got), want, got)
	}
	if report.violations != want {
		t.Errorf("total violations = %d, want %d", report.violations, want)
	}
}

// balanced is computed from coverage. A forager that declares an axis joins
// it, and the `chb ask` default, so validate-personas checks it too.
func TestValidatePersonas_ChecksEveryBalancedForager(t *testing.T) {
	dir := copyForagers(t)
	extra := "---\nname: economist\ncoverage:\n  wasp: I\n---\nFree-form economist, no template-v1 structure.\n"
	if err := os.WriteFile(filepath.Join(dir, "economist.md"), []byte(extra), 0o644); err != nil {
		t.Fatal(err)
	}
	all, err := foragers.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := foragers.ByName(foragers.Balanced(all), "economist"); !ok {
		t.Fatal("test setup: economist should be in the balanced preset")
	}
	want := len(requiredFrontmatterKeys) + len(requiredBodySections) + 1

	report, err := validatePersonasDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := violationsFor(report, "economist"); len(got) != want {
		t.Errorf("economist violations = %d, want %d: %v", len(got), want, got)
	}
	for _, name := range report.skipped {
		if name == "economist" {
			t.Errorf("economist was skipped; balanced selects it")
		}
	}
}

// The guard must sit in § 7. A drifted § 7 fails even when the verbatim
// string is quoted elsewhere in the body.
func TestValidatePersonas_GuardMustBeInSection7(t *testing.T) {
	dir := copyForagers(t)
	path := filepath.Join(dir, "skeptic.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	drifted := strings.Replace(lockedEmissionGuard, "No trailing commentary.", "Trailing commentary is fine.", 1)
	body := strings.Replace(string(raw), lockedEmissionGuard, drifted, 1) + "\n" + lockedEmissionGuard + "\n"
	if strings.Count(body, lockedEmissionGuard) != 1 || !strings.Contains(body, drifted) {
		t.Fatal("test setup: expected one drifted guard in § 7 and one verbatim copy after it")
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := validatePersonasDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := violationsFor(report, "skeptic"); len(got) != 1 || !strings.Contains(got[0], "§7") {
		t.Errorf("skeptic violations = %v, want exactly the § 7 guard", got)
	}
}
