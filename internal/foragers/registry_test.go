package foragers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeForagerFile writes a frontmatter+body file to dir/<name>.md.
func writeForagerFile(t *testing.T, dir, name, frontmatter, body string) {
	t.Helper()
	content := "---\n" + frontmatter + "---\n" + body
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestLoad_ParsesFrontmatterAndBody(t *testing.T) {
	dir := t.TempDir()
	writeForagerFile(t, dir, "optimist",
		"name: optimist\ntitle: The Optimist\ndescription: Sees upside.\ndefault: true\ntags: [a, b]\n",
		"You are The Optimist. Persona body.\n",
	)
	writeForagerFile(t, dir, "forecaster",
		"name: forecaster\ntitle: The Forecaster\ndescription: Sees ripple.\ndefault: false\n",
		"You are The Forecaster.\n",
	)

	all, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 foragers, got %d", len(all))
	}
	// Sorted by Name → forecaster, optimist.
	if all[0].Name != "forecaster" || all[1].Name != "optimist" {
		t.Errorf("unexpected order: %s, %s", all[0].Name, all[1].Name)
	}
	opt := all[1]
	if opt.Title != "The Optimist" || opt.Description != "Sees upside." {
		t.Errorf("frontmatter mismatch: %+v", opt)
	}
	if !opt.Default {
		t.Errorf("optimist should be default")
	}
	if len(opt.Tags) != 2 || opt.Tags[0] != "a" {
		t.Errorf("tags mismatch: %+v", opt.Tags)
	}
	if !strings.Contains(opt.Body, "Persona body") {
		t.Errorf("body missing: %q", opt.Body)
	}
}

func TestLoad_SkipsReadmeAndMissingFrontmatter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"),
		[]byte("# Foragers Directory\n\nThis is human docs.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "no-frontmatter.md"),
		[]byte("just body, no frontmatter at all\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeForagerFile(t, dir, "valid",
		"name: valid\ntitle: V\ndescription: x\n",
		"body\n",
	)

	all, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(all) != 1 || all[0].Name != "valid" {
		t.Fatalf("expected 1 valid forager, got %+v", all)
	}
}

func TestLoad_MissingDirReturnsEmpty(t *testing.T) {
	all, err := Load(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing dir should not error, got: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("expected empty slice, got %+v", all)
	}
}

func TestLoad_MalformedFrontmatterSoftFails(t *testing.T) {
	dir := t.TempDir()
	// Open marker but no close marker — incomplete frontmatter.
	if err := os.WriteFile(filepath.Join(dir, "broken.md"),
		[]byte("---\nname: broken\ntitle: still inside frontmatter\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeForagerFile(t, dir, "good",
		"name: good\ntitle: G\ndescription: x\n",
		"body\n",
	)

	all, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(all) != 1 || all[0].Name != "good" {
		t.Fatalf("expected 1 good forager, got %+v", all)
	}
}

func TestDefault_FiltersDefaultTrue(t *testing.T) {
	all := []Forager{
		{Name: "a", Default: true},
		{Name: "b", Default: false},
		{Name: "c", Default: true},
	}
	out := Default(all)
	if len(out) != 2 || out[0].Name != "a" || out[1].Name != "c" {
		t.Errorf("unexpected default subset: %+v", out)
	}
}

func TestByName_CaseInsensitive(t *testing.T) {
	all := []Forager{{Name: "Skeptic"}, {Name: "optimist"}}
	w, ok := ByName(all, "SKEPTIC")
	if !ok || w.Name != "Skeptic" {
		t.Errorf("ByName SKEPTIC: ok=%v w=%+v", ok, w)
	}
	w, ok = ByName(all, "optimist")
	if !ok || w.Name != "optimist" {
		t.Errorf("ByName optimist: ok=%v w=%+v", ok, w)
	}
	if _, ok := ByName(all, "missing"); ok {
		t.Errorf("ByName missing should fail")
	}
}

func TestFilter_DefaultExpandsToDefaults(t *testing.T) {
	all := []Forager{
		{Name: "a", Default: true},
		{Name: "b", Default: false},
		{Name: "c", Default: true},
	}
	out, err := Filter(all, []string{"default"})
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 defaults, got %d", len(out))
	}
}

func TestFilter_AllExpandsToEveryone(t *testing.T) {
	all := []Forager{
		{Name: "a", Default: true},
		{Name: "b", Default: false},
		{Name: "c", Default: true},
	}
	out, err := Filter(all, []string{"all"})
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 foragers, got %d", len(out))
	}
}

func TestFilter_DefaultPlusCustom(t *testing.T) {
	all := []Forager{
		{Name: "optimist", Default: true},
		{Name: "skeptic", Default: true},
		{Name: "contrarian", Default: false},
	}
	out, err := Filter(all, []string{"default", "contrarian"})
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 (2 defaults + contrarian), got %d", len(out))
	}
}

func TestFilter_DedupesRepeats(t *testing.T) {
	all := []Forager{{Name: "optimist", Default: true}}
	out, err := Filter(all, []string{"default", "optimist", "optimist"})
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected dedup to 1, got %d", len(out))
	}
}

func TestFilter_UnknownNameReportsAvailable(t *testing.T) {
	all := []Forager{{Name: "optimist"}, {Name: "skeptic"}}
	_, err := Filter(all, []string{"optimist", "forager-of-oz"})
	if err == nil {
		t.Fatalf("expected error for unknown forager")
	}
	msg := err.Error()
	if !strings.Contains(msg, "forager-of-oz") {
		t.Errorf("error should name the unknown forager, got: %s", msg)
	}
	if !strings.Contains(msg, "optimist") || !strings.Contains(msg, "skeptic") {
		t.Errorf("error should list available foragers, got: %s", msg)
	}
}

func TestFilter_EmptyListReturnsDefaults(t *testing.T) {
	all := []Forager{
		{Name: "a", Default: true},
		{Name: "b", Default: false},
	}
	out, err := Filter(all, nil)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(out) != 1 || out[0].Name != "a" {
		t.Errorf("empty list should return defaults, got %+v", out)
	}
}

func TestNormalizeArchetype_DefaultsToLens(t *testing.T) {
	w := Forager{Name: "x"}
	if err := w.NormalizeArchetype(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if w.Archetype != ArchetypeLens {
		t.Fatalf("expected default lens, got %q", w.Archetype)
	}
	if !w.IsLens() {
		t.Fatalf("IsLens should be true after default normalize")
	}
}

func TestNormalizeArchetype_RejectsUnknown(t *testing.T) {
	w := Forager{Name: "x", Archetype: "wraith"}
	if err := w.NormalizeArchetype(); err == nil {
		t.Fatalf("expected unknown archetype to error")
	}
}

func TestNormalizeArchetype_DependsOnExpandsToCitesBonds(t *testing.T) {
	w := Forager{Name: "x", DependsOn: []string{"a", "b"}}
	if err := w.NormalizeArchetype(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(w.Bonds) != 2 {
		t.Fatalf("expected 2 cites bonds, got %d", len(w.Bonds))
	}
	for _, b := range w.Bonds {
		if b.Kind != BondCites {
			t.Errorf("expected cites kind, got %q", b.Kind)
		}
	}
	if len(w.DependsOn) != 0 {
		t.Errorf("DependsOn should be cleared after merge, got %v", w.DependsOn)
	}
}

func TestNormalizeArchetype_RejectsBadBondKind(t *testing.T) {
	w := Forager{Name: "x", Bonds: []Bond{{To: "a", Kind: "vibes"}}}
	if err := w.NormalizeArchetype(); err == nil {
		t.Fatalf("expected unknown bond kind to error")
	}
}

func TestValidateSwarm_AcceptsAcyclicGraph(t *testing.T) {
	swarm := []Forager{
		{Name: "a"},
		{Name: "b", Bonds: []Bond{{To: "a", Kind: BondCites, Weight: 1}}},
		{Name: "c", Bonds: []Bond{{To: "b", Kind: BondCites, Weight: 1}}},
	}
	if err := ValidateSwarm(swarm); err != nil {
		t.Fatalf("expected acyclic graph to validate, got: %v", err)
	}
}

func TestValidateSwarm_DetectsCycle(t *testing.T) {
	swarm := []Forager{
		{Name: "a", Bonds: []Bond{{To: "b", Kind: BondCites, Weight: 1}}},
		{Name: "b", Bonds: []Bond{{To: "c", Kind: BondCites, Weight: 1}}},
		{Name: "c", Bonds: []Bond{{To: "a", Kind: BondCites, Weight: 1}}},
	}
	if err := ValidateSwarm(swarm); err == nil {
		t.Fatalf("expected cycle to be rejected")
	}
}

func TestValidateSwarm_ResonatesNotInCycle(t *testing.T) {
	swarm := []Forager{
		{Name: "a", Bonds: []Bond{{To: "b", Kind: BondResonates, Weight: 1}}},
		{Name: "b", Bonds: []Bond{{To: "a", Kind: BondResonates, Weight: 1}}},
	}
	if err := ValidateSwarm(swarm); err != nil {
		t.Fatalf("resonates pair should not be a cycle: %v", err)
	}
}

// A lens may not cite a dreamer: dreamers run after queen, so a lens cannot
// depend on a dreamer's verdict at dispatch time, and the generated
// workflow's edge would reference an unknown source, forager-<dreamer-name>.
func TestValidateSwarm_RejectsLensCitesDreamer(t *testing.T) {
	swarm := []Forager{
		{Name: "lens-a", Archetype: ArchetypeLens,
			Bonds: []Bond{{To: "dreamer-a", Kind: BondCites, Weight: 1}}},
		{Name: "dreamer-a", Archetype: ArchetypeDreamer},
	}
	if err := ValidateSwarm(swarm); err == nil {
		t.Fatalf("expected lens cites dreamer to be rejected")
	} else if !strings.Contains(err.Error(), "dreamer") {
		t.Fatalf("error should mention dreamer ordering: %v", err)
	}
}

func TestValidateSwarm_RejectsLensContradictsDreamer(t *testing.T) {
	swarm := []Forager{
		{Name: "scholar", Archetype: ArchetypeLens,
			Bonds: []Bond{{To: "dreamer", Kind: BondContradicts, Weight: 1}}},
		{Name: "dreamer", Archetype: ArchetypeDreamer},
	}
	if err := ValidateSwarm(swarm); err == nil {
		t.Fatalf("expected lens contradicts dreamer to be rejected")
	}
}

// TestMinimal_ReturnsShippedAxisOwners verifies the `minimal` preset
// resolves to exactly the foragers whose frontmatter declares any
// WASP/CDE/MSS axis. Today that's the 7 axis-owners.
func TestMinimal_ReturnsShippedAxisOwners(t *testing.T) {
	all := []Forager{
		{Name: "architect", Coverage: Coverage{Wasp: "k"}},
		{Name: "skeptic", Coverage: Coverage{Wasp: "E", Mss: "unk"}},
		{Name: "editor", Coverage: Coverage{Wasp: "I"}},
		{Name: "timekeeper", Coverage: Coverage{Wasp: "T"}},
		{Name: "empiricist", Coverage: Coverage{Wasp: "F", Cde: "detect", Mss: "def"}},
		{Name: "scholar", Coverage: Coverage{Cde: "decompose", Mss: "gua"}},
		{Name: "steward", Coverage: Coverage{Cde: "execute", Mss: "asm"}},
		// specialists without coverage — must NOT be in minimal
		{Name: "optimist"},
		{Name: "pragmatist"},
		{Name: "historian"},
		{Name: "forecaster"},
		{Name: "surveyor"},
		{Name: "framer"},
		{Name: "dreamer", Archetype: ArchetypeDreamer},
	}
	got := Minimal(all)
	if len(got) != 7 {
		t.Fatalf("expected 7 axis-owner foragers in minimal set, got %d: %v", len(got), names(got))
	}
	expected := map[string]bool{
		"architect": true, "skeptic": true, "editor": true, "timekeeper": true,
		"empiricist": true, "scholar": true, "steward": true,
	}
	for _, w := range got {
		if !expected[w.Name] {
			t.Errorf("unexpected forager in minimal set: %q", w.Name)
		}
	}
}

// TestMinimal_FrameworkComplete verifies Minimal()'s selection logic on a
// synthetic roster that covers every WASP axis (k, E, I, T, F), 3 of 4 CDE
// phases and all 4 MSS labels. It is not a statement about the shipped
// roster, whose Editor is render-layer and leaves WASP-I unowned — see
// TestMinimal_ShippedRosterCoverage.
func TestMinimal_FrameworkComplete(t *testing.T) {
	all := []Forager{
		{Name: "architect", Coverage: Coverage{Wasp: "k"}},
		{Name: "skeptic", Coverage: Coverage{Wasp: "E", Mss: "unk"}},
		{Name: "editor", Coverage: Coverage{Wasp: "I"}},
		{Name: "timekeeper", Coverage: Coverage{Wasp: "T"}},
		{Name: "empiricist", Coverage: Coverage{Wasp: "F", Cde: "detect", Mss: "def"}},
		{Name: "scholar", Coverage: Coverage{Cde: "decompose", Mss: "gua"}},
		{Name: "steward", Coverage: Coverage{Cde: "execute", Mss: "asm"}},
	}
	minimal := Minimal(all)

	wasp := make(map[string]bool)
	cde := make(map[string]bool)
	mss := make(map[string]bool)
	for _, w := range minimal {
		if w.Coverage.Wasp != "" {
			wasp[w.Coverage.Wasp] = true
		}
		if w.Coverage.Cde != "" {
			cde[w.Coverage.Cde] = true
		}
		if w.Coverage.Mss != "" {
			mss[w.Coverage.Mss] = true
		}
	}
	for _, axis := range []string{"k", "E", "I", "T", "F"} {
		if !wasp[axis] {
			t.Errorf("WASP axis %q is uncovered by the minimal preset", axis)
		}
	}
	for _, phase := range []string{"detect", "decompose", "execute"} {
		if !cde[phase] {
			t.Errorf("CDE phase %q is uncovered by the minimal preset", phase)
		}
	}
	for _, label := range []string{"def", "gua", "asm", "unk"} {
		if !mss[label] {
			t.Errorf("MSS label %q is uncovered by the minimal preset", label)
		}
	}
}

// TestFilter_MinimalPseudoName verifies the `minimal` pseudo-name
// in Filter() resolves to the same foragers as Minimal().
func TestFilter_MinimalPseudoName(t *testing.T) {
	all := []Forager{
		{Name: "architect", Coverage: Coverage{Wasp: "k"}},
		{Name: "skeptic", Coverage: Coverage{Wasp: "E"}},
		{Name: "optimist"}, // no coverage
		{Name: "scholar", Coverage: Coverage{Mss: "gua"}},
	}
	got, err := Filter(all, []string{"minimal"})
	if err != nil {
		t.Fatalf("Filter(minimal): %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 foragers (the 3 with coverage), got %d: %v", len(got), names(got))
	}
	for _, w := range got {
		if w.Coverage == (Coverage{}) {
			t.Errorf("forager %q has no coverage but was selected by `minimal`", w.Name)
		}
	}
}

// TestFilter_MinimalPlusSpecialist verifies that `minimal` composes
// with named specialists exactly like `default` does.
func TestFilter_MinimalPlusSpecialist(t *testing.T) {
	all := []Forager{
		{Name: "architect", Coverage: Coverage{Wasp: "k"}},
		{Name: "skeptic", Coverage: Coverage{Wasp: "E"}},
		{Name: "forecaster"},
	}
	got, err := Filter(all, []string{"minimal", "forecaster"})
	if err != nil {
		t.Fatalf("Filter(minimal, forecaster): %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 foragers (2 minimal + forecaster), got %d: %v", len(got), names(got))
	}
}

// The shipped foragers/ directory has exactly 7 axis-owner foragers in the
// minimal preset, so a change that removes coverage from a default-axis
// forager fails here.
//
// Resolves the foragers dir via the same fallback chain the CLI uses:
// $HIVE_FORAGERS_DIR, then repo-relative ../../foragers.
func TestShippedRoster_MinimalIsSeven(t *testing.T) {
	dir := os.Getenv("HIVE_FORAGERS_DIR")
	if dir == "" {
		dir = filepath.Join("..", "..", "foragers")
	}
	all, err := Load(dir)
	if err != nil {
		t.Skipf("can't load foragers from %q: %v", dir, err)
	}
	if len(all) == 0 {
		t.Skipf("no foragers loaded from %q (probably running outside the hive tree)", dir)
	}
	got := Minimal(all)
	if len(got) != 7 {
		t.Errorf("shipped roster minimal should be 7, got %d: %v", len(got), names(got))
	}
}

func names(ws []Forager) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.Name
	}
	return out
}

// A dreamer's verdict is always abstain, which the ∇ sensor never counts,
// so a lens resonating with a dreamer holds a pair that can never fire,
// while the generated prompt tells the lens it can converge. ValidateSwarm
// refuses the bond instead of generating that promise.
func TestValidateSwarm_RefusesLensResonatesDreamer(t *testing.T) {
	lens, dreamer := "lens-a", "dreamer-a"
	swarm := []Forager{
		{Name: lens, Archetype: ArchetypeLens,
			Bonds: []Bond{{To: dreamer, Kind: BondResonates, Weight: 1}}},
		{Name: dreamer, Archetype: ArchetypeDreamer},
	}
	want := `forager "` + lens + `" has ` + BondResonates + ` bond to dreamer "` + dreamer + `"`
	if err := ValidateSwarm(swarm); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("ValidateSwarm = %v, want the resonates bond onto the dreamer refused (%q)", err, want)
	}
}

func TestSplitByArchetype_PartitionsLensAndDreamer(t *testing.T) {
	swarm := []Forager{
		{Name: "optimist", Archetype: ArchetypeLens},
		{Name: "dreamer", Archetype: ArchetypeDreamer},
		{Name: "skeptic"},
	}
	lens, dreamers := SplitByArchetype(swarm)
	if len(lens) != 2 {
		t.Fatalf("expected 2 lens, got %d", len(lens))
	}
	if len(dreamers) != 1 {
		t.Fatalf("expected 1 dreamer, got %d", len(dreamers))
	}
}

// Naming queen in --foragers put a second Queen in the swarm.
func TestValidateSwarm_RefusesASynthesizerLens(t *testing.T) {
	swarm := []Forager{
		{Name: "optimist", Archetype: ArchetypeLens},
		{Name: "queen", Archetype: ArchetypeSynthesizer},
	}
	if err := ValidateSwarm(swarm); err == nil || !strings.Contains(err.Error(), `"queen" is a synthesizer`) {
		t.Fatalf("ValidateSwarm = %v, want the synthesizer refused", err)
	}
}

// Two files declaring one name: the first by file name wins everywhere.
func TestLoad_FirstFileWinsADuplicateName(t *testing.T) {
	dir := t.TempDir()
	for file, desc := range map[string]string{"a-optimist.md": "first", "b-optimist.md": "second"} {
		body := "---\nname: optimist\ndescription: " + desc + "\n---\nbody\n"
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	all, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Description != "first" {
		t.Fatalf("Load = %+v, want only the first file's optimist", all)
	}
}
