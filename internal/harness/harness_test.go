package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	hive "github.com/Chubby-Honey-Bee/hive"
	"github.com/Chubby-Honey-Bee/hive/internal/bench"
	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
	"github.com/Chubby-Honey-Bee/hive/internal/models"
	"github.com/Chubby-Honey-Bee/hive/internal/runner"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// The harness counts a swarm or bench case's model calls from the live
// roster, so the count follows the presets: one call per lens and one for
// Queen, the evaluator and up to DefaultFollowups follow-ups with the
// coverage pass, and per bench run on each arm and repetition.
func TestNominalCalls_FollowTheRoster(t *testing.T) {
	t.Setenv("HIVE_FORAGERS_DIR", filepath.Join("..", "..", "foragers"))
	all := liveRoster(t)
	lenses := func(preset []foragers.Forager) int {
		lens, _ := foragers.SplitByArchetype(preset)
		return len(lens)
	}
	minimal, balanced := lenses(foragers.Minimal(all)), lenses(foragers.Balanced(all))
	f1 := len(liveItems(t, []string{"F1"}, []int64{1}))
	calls := func(lo, hi int) string {
		if lo == hi {
			return fmt.Sprintf("%d model calls", lo)
		}
		return fmt.Sprintf("%d–%d model calls", lo, hi)
	}
	cases := []struct {
		c    harnessCase
		reps int
		want string
	}{
		{harnessCase{Kind: "swarm", Foragers: "minimal"}, 1, calls(minimal+1, minimal+1)},
		{harnessCase{Kind: "swarm", Foragers: "balanced", Eval: true}, 1, calls(balanced+2, balanced+2+foragers.DefaultFollowups)},
		{harnessCase{Kind: "bench", Families: []string{"F1"}, Seeds: []int64{1}, Arms: []string{bench.ArmSwarm}}, 1, calls(f1*(minimal+1), f1*(minimal+1))},
		{harnessCase{Kind: "bench", Families: []string{"F1"}, Seeds: []int64{1}}, 2, calls(2*f1*(minimal+2), 2*f1*(minimal+2))},
		{harnessCase{Kind: "template"}, 1, ""},
	}
	for _, c := range cases {
		h := &agentHarness{Options: Options{Reps: c.reps, LoadForagers: loadForagers}}
		if got := h.nominalCalls(c.c); got != c.want {
			t.Errorf("%+v, reps %d: %q, want %q", c.c, c.reps, got, c.want)
		}
	}
	h := &agentHarness{Options: Options{Reps: 1, LoadForagers: loadForagers}}
	c := harnessCase{Kind: "swarm", Foragers: "minimal", Cost: " about $0.25 "}
	if got, want := h.caseCost(c), calls(minimal+1, minimal+1)+"; about $0.25"; got != want {
		t.Errorf("cost %q, want %q", got, want)
	}
}

// Every case the harness runs by default says what it costs.
func TestShippedSuite_EveryDefaultCaseSaysItsCost(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "fixtures", "agent-harness", "suite.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var suite harnessSuite
	if err := yaml.Unmarshal(raw, &suite); err != nil {
		t.Fatal(err)
	}
	for _, c := range suite.Cases {
		if strings.TrimSpace(c.Cost) == "" {
			t.Errorf("case %s has no cost note", c.Name)
		}
	}
}

// The harness prints each case's cost on the line that skips it, or right
// after the line that starts it, and the report's case table carries it.
// A slow case and a template case on another provider are skipped; a
// swarm case with a grade starts and is refused at once.
func TestHarness_PrintsEachCaseCost(t *testing.T) {
	t.Setenv("HIVE_FORAGERS_DIR", filepath.Join("..", "..", "foragers"))
	dir := t.TempDir()
	cases := []harnessCase{
		{Name: "slow-swarm", Kind: "swarm", Foragers: "minimal", Slow: true, Cost: "about $0.25"},
		{Name: "a-template", Kind: "template", Cost: "one claude -p call"},
		{Name: "graded-swarm", Kind: "swarm", Foragers: "minimal", Grade: "sqlite-default-page-size", Cost: "refused before it runs"},
	}
	raw, err := yaml.Marshal(harnessSuite{Cases: cases})
	if err != nil {
		t.Fatal(err)
	}
	suite := filepath.Join(dir, "suite.yaml")
	if err := os.WriteFile(suite, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	h := &agentHarness{Options: Options{Self: filepath.Join(dir, "no-chb"), Stdout: out, Suite: suite, Workspace: filepath.Join(dir, "ws"),
		Provider: "openai", Reps: 1, KeepGoing: true, LoadForagers: loadForagers}}
	if err := h.run(context.Background()); err == nil {
		t.Fatal("the graded swarm case passed; want it refused")
	}
	printed, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	report, err := os.ReadFile(filepath.Join(h.Workspace, "REPORT.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(printed), "\n")
	for _, c := range cases {
		cost := h.caseCost(c)
		if !strings.Contains(cost, c.Cost) {
			t.Fatalf("%s: cost %q lacks its note %q", c.Name, cost, c.Cost)
		}
		shown := false
		for i, l := range lines {
			switch {
			case strings.HasPrefix(l, "  - "+c.Name+" ") && strings.Contains(l, "skipped"):
				shown = strings.HasSuffix(l, detailSuffix(cost))
			case strings.HasPrefix(l, "── "+c.Name+" ("):
				shown = i+1 < len(lines) && lines[i+1] == "  cost: "+cost
			}
			if shown {
				break
			}
		}
		if !shown {
			t.Errorf("%s: no printed line gives its cost %q:\n%s", c.Name, cost, printed)
		}
		row := ""
		for _, l := range strings.Split(string(report), "\n") {
			if strings.HasPrefix(l, "| "+c.Name+" |") {
				row = l
			}
		}
		if !strings.Contains(row, "| "+cost+" |") {
			t.Errorf("%s: report row %q lacks the cost %q", c.Name, row, cost)
		}
	}
}

// The model flags are checked before any case runs: an empty entry in the
// --lens-model list, or a reasoning level outside models.ReasoningLevels,
// is refused, and nothing is read or written. A good list is split and
// trimmed.
func TestHarness_ChecksTheModelFlagsFirst(t *testing.T) {
	cases := []struct {
		lens, lensReasoning, queenReasoning, flag string
	}{
		{"qwen3.5:4b,,ministral-3:8b", "", "", "--lens-model"},
		{",ministral-3:8b", "", "", "--lens-model"},
		{"qwen3.5:4b, ", "", "", "--lens-model"},
		{"qwen3.5:4b", "think", "", "--lens-reasoning"},
		{"", "", "max", "--queen-reasoning"},
	}
	for _, c := range cases {
		ws := filepath.Join(t.TempDir(), "ws")
		h := &agentHarness{Options: Options{Suite: filepath.Join(t.TempDir(), "no-suite.yaml"), Workspace: ws, Provider: "openai", Reps: 1,
			LensModel: c.lens, LensReasoning: c.lensReasoning, QueenReasoning: c.queenReasoning}}
		err := h.run(context.Background())
		if err == nil || !strings.HasPrefix(err.Error(), c.flag) {
			t.Errorf("%+v: error %v; want it refused on %s before the suite is read", c, err, c.flag)
		}
		if _, statErr := os.Stat(ws); statErr == nil {
			t.Errorf("%+v: the workspace was created", c)
		}
	}
	list := " qwen3.5:4b , ministral-3:8b"
	var want []string
	for _, m := range strings.Split(list, ",") {
		want = append(want, strings.TrimSpace(m))
	}
	h := &agentHarness{Options: Options{LensModel: list, LensReasoning: "none", QueenReasoning: "none"}}
	if err := h.checkModelFlags(); err != nil || !slices.Equal(h.lensModels, want) {
		t.Errorf("lens models %q (%v), want %q", h.lensModels, err, want)
	}
}

// A swarm case with a grade is refused before it runs: one verdict is
// passed by a model that always gives it.
func TestSwarmCase_RefusesAGrade(t *testing.T) {
	h := &agentHarness{Options: Options{Workspace: t.TempDir(), Provider: "openai", Self: filepath.Join(t.TempDir(), "no-chb")}}
	res := h.runCase(context.Background(), harnessCase{Name: "graded", Kind: "swarm", Foragers: "minimal", Grade: "sqlite-default-page-size"})
	if res.OK || len(res.Checks) != 1 || res.Checks[0].Name != "grade" {
		t.Fatalf("result %+v; want the case refused on its grade before anything runs", res)
	}
}

func adherenceCase(checks ...harnessCheck) harnessResult {
	return harnessResult{Name: "c", OK: true, Checks: checks}
}

// The rate counts adherence checks only, and counts the ones that held.
func TestAdherenceRate_CountsOnlyAdherenceChecks(t *testing.T) {
	res := []harnessResult{adherenceCase(
		harnessCheck{Name: "artifact sha256 verifies", OK: true},
		harnessCheck{Name: "skeptic: no forbidden phrases (adherence)", OK: false, Warn: true, Detail: "obviously"},
		harnessCheck{Name: "skeptic: length caps respected (adherence)", OK: true},
		harnessCheck{Name: "personas emitted bare JSON (adherence)", OK: true},
	)}
	clean, total := adherenceRate(res)
	if clean != 2 || total != 3 {
		t.Fatalf("adherenceRate = %d/%d, want 2/3 (the contract check is not adherence)", clean, total)
	}
}

// With no adherence checks the report prints no rate.
func TestAdherenceRate_EmptyWhenNoneRan(t *testing.T) {
	if clean, total := adherenceRate([]harnessResult{adherenceCase(harnessCheck{Name: "run completed", OK: true})}); clean != 0 || total != 0 {
		t.Fatalf("adherenceRate = %d/%d, want 0/0", clean, total)
	}
}

// The harness passes the two flags to every chb ask it runs, names them in
// the configuration, and counts the direct voice's call.
func TestHarness_SwarmFlagsPassThrough(t *testing.T) {
	t.Setenv("HIVE_FORAGERS_DIR", filepath.Join("..", "..", "foragers"))
	it := liveItems(t, []string{"F1"}, []int64{1})[0]
	off := &agentHarness{Options: Options{Provider: "openai", LensModel: "m", QueenModel: "q", BudgetMode: "cheap", LoadForagers: loadForagers}}
	on := &agentHarness{Options: Options{Provider: "openai", LensModel: "m", QueenModel: "q", BudgetMode: "cheap", DirectVoice: true, ContextSplit: true, LoadForagers: loadForagers}}
	has := func(args []string, flag string) bool {
		for _, a := range args {
			if a == flag {
				return true
			}
		}
		return false
	}
	for _, flag := range []string{"--direct-voice", "--context-split"} {
		if has(off.askArgs(it, 1, t.TempDir(), "minimal", false, "", nil), flag) {
			t.Errorf("askArgs carries %s with the flag off", flag)
		}
		if !has(on.askArgs(it, 1, t.TempDir(), "minimal", false, "", nil), flag) {
			t.Errorf("askArgs lacks %s with the flag on", flag)
		}
	}
	if got, want := off.configName(), "openai/m/q/cheap"; got != want {
		t.Errorf("config %q, want %q", got, want)
	}
	if got, want := on.configName(), "openai/m/q/cheap/direct-voice/context-split"; got != want {
		t.Errorf("config %q, want %q", got, want)
	}
	lens, _ := foragers.SplitByArchetype(foragers.Minimal(liveRoster(t)))
	c := harnessCase{Kind: "bench", Families: []string{"F1"}, Seeds: []int64{1}, Arms: []string{bench.ArmSwarm}}
	off.Reps, on.Reps = 1, 1
	items := len(liveItems(t, []string{"F1"}, []int64{1}))
	if got, want := off.nominalCalls(c), fmt.Sprintf("%d model calls", items*(len(lens)+1)); got != want {
		t.Errorf("calls off %q, want %q", got, want)
	}
	if got, want := on.nominalCalls(c), fmt.Sprintf("%d model calls", items*(len(lens)+2)); got != want {
		t.Errorf("calls with the direct voice %q, want %q", got, want)
	}
}

// The harness passes --persona-profile and --persona-sections to every chb
// ask it runs, names the configuration by them when either is given, and
// refuses a value ask would refuse before any case runs.
func TestAgentHarness_PersonaProfile(t *testing.T) {
	base := []string{"openai", "m", "tier", "cheap"}
	cases := []struct {
		profile, sections string
		wantArgs          []string
		wantConfig        string
	}{
		{"", "", nil, strings.Join(base, "/")},
		{"lean", "", []string{"--persona-profile", "lean"}, strings.Join(append(base, "lean"), "/")},
		{"lean", "1,2,3,4,5,7", []string{"--persona-profile", "lean", "--persona-sections", "1,2,3,4,5,7"}, strings.Join(append(base, "lean§1,2,3,4,5,7"), "/")},
		{"", "1,2", []string{"--persona-sections", "1,2"}, strings.Join(append(base, "full§1,2"), "/")},
	}
	for _, c := range cases {
		h := &agentHarness{Options: Options{Provider: "openai", LensModel: "m", BudgetMode: "cheap", PersonaProfile: c.profile, PersonaSections: c.sections}}
		if got := h.personaArgs(); !slices.Equal(got, c.wantArgs) {
			t.Errorf("%q/%q: args %q, want %q", c.profile, c.sections, got, c.wantArgs)
		}
		if got := h.configName(); got != c.wantConfig {
			t.Errorf("%q/%q: config %q, want %q", c.profile, c.sections, got, c.wantConfig)
		}
	}
	// With reasoning levels set too, the name gives them first, then the
	// persona, and a mixed lens list reaches ask beside the persona flags.
	mixed := &agentHarness{Options: Options{Provider: "openai", LensModel: "a,b", BudgetMode: "cheap", LensReasoning: "none", PersonaProfile: "lean"}, lensModels: []string{"a", "b"}}
	if got, want := mixed.configName(), "openai/a,b/tier/cheap/reasoning none,default/lean"; got != want {
		t.Errorf("config with reasoning and persona %q, want %q", got, want)
	}
	if got, want := append(mixed.modelArgs(mixed.lensModels), mixed.personaArgs()...), []string{"--model", "a,b", "--forager-reasoning", "none", "--persona-profile", "lean"}; !slices.Equal(got, want) {
		t.Errorf("ask args %q, want %q", got, want)
	}
	for _, h := range []*agentHarness{
		{Options: Options{Provider: "openai", Reps: 1, PersonaProfile: "tiny", Suite: "no-such-suite.yaml"}},
		{Options: Options{Provider: "openai", Reps: 1, PersonaSections: "x", Suite: "no-such-suite.yaml"}},
	} {
		if err := h.run(t.Context()); err == nil || !strings.Contains(err.Error(), "--persona") {
			t.Errorf("run with profile %q, sections %q: err %v, want the flag refused", h.PersonaProfile, h.PersonaSections, err)
		}
	}
}

// The harness under a routing profile passes the persona flags to every
// chb ask it runs, names the configuration by both profiles, so a lean arm
// and a full arm under one routing profile stay apart in chb bench decide,
// and its report names the routing profile's models and the persona
// profile.
func TestHarness_RoutingProfileWithPersonaProfile(t *testing.T) {
	const profile = "local-small"
	p, err := runner.ResolveProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HIVE_PROFILE", "")
	cases := []struct {
		persona, sections string
		wantConfig        string
		wantArgs          []string
		wantLabel         string
	}{
		{"", "", "profile " + profile, nil, "`full`"},
		{"lean", "", "profile " + profile + "/lean", []string{"--persona-profile", "lean"}, "`lean`"},
		{"", "1,2", "profile " + profile + "/full§1,2", []string{"--persona-sections", "1,2"}, "`full`, sections 1,2"},
	}
	for _, c := range cases {
		t.Run(c.wantConfig, func(t *testing.T) {
			h := &agentHarness{Options: Options{Profile: profile, Provider: string(runner.BackendClaudeCLI), PersonaProfile: c.persona, PersonaSections: c.sections}}
			if err := h.useProfile(); err != nil {
				t.Fatal(err)
			}
			if got := h.configName(); got != c.wantConfig {
				t.Errorf("config %q, want %q", got, c.wantConfig)
			}
			if got := h.personaArgs(); !slices.Equal(got, c.wantArgs) {
				t.Errorf("ask args %q, want %q", got, c.wantArgs)
			}
			h.Workspace = t.TempDir()
			if err := h.writeReport(nil, "sha"); err != nil {
				t.Fatal(err)
			}
			report, err := os.ReadFile(filepath.Join(h.Workspace, "REPORT.md"))
			if err != nil {
				t.Fatal(err)
			}
			lens, queen := p.Routes["lens"], p.Routes["queen"]
			for _, want := range []string{
				fmt.Sprintf("routing profile `%s`", profile),
				fmt.Sprintf("lens model `%s`; queen model `%s`", lens.Model, queen.Model),
				"persona profile " + c.wantLabel,
				"config: `" + c.wantConfig + "`",
			} {
				if !strings.Contains(string(report), want) {
					t.Errorf("the report lacks %q:\n%s", want, report)
				}
			}
		})
	}
}

// The harness takes the profile's provider unless --provider is given,
// refuses the flags the profile replaces, names its configuration after the
// profile and exports it to every case.
func TestHarnessProfile(t *testing.T) {
	const profile = "local-small"
	p, err := runner.ResolveProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HIVE_PROFILE", "")
	h := &agentHarness{Options: Options{Profile: profile, Provider: string(runner.BackendClaudeCLI)}}
	if err := h.useProfile(); err != nil {
		t.Fatal(err)
	}
	if h.Provider != string(p.Provider) || h.profileLens.Model != p.Routes["lens"].Model || h.profileQueen.Model != p.Routes["queen"].Model {
		t.Errorf("harness %+v, want provider %s, lens %s, queen %s", h, p.Provider, p.Routes["lens"].Model, p.Routes["queen"].Model)
	}
	if h.configName() != "profile "+profile {
		t.Errorf("config %q", h.configName())
	}
	if env := h.caseEnv("db"); !slices.Contains(env, "HIVE_PROFILE="+profile) {
		t.Errorf("case env lacks HIVE_PROFILE=%s", profile)
	}
	// The report names the profile's lens and Queen models and reasoning,
	// not the flags it replaces.
	h.Workspace = t.TempDir()
	if err := h.writeReport(nil, "sha"); err != nil {
		t.Fatal(err)
	}
	report, err := os.ReadFile(filepath.Join(h.Workspace, "REPORT.md"))
	if err != nil {
		t.Fatal(err)
	}
	lens, queen := p.Routes["lens"], p.Routes["queen"]
	if want := fmt.Sprintf("lens model `%s`; queen model `%s`; lens reasoning `%s`; queen reasoning `%s`", lens.Model, queen.Model, lens.Reasoning, queen.Reasoning); !strings.Contains(string(report), want) {
		t.Errorf("the report lacks %q:\n%s", want, report)
	}
	var js map[string]any
	raw, err := os.ReadFile(filepath.Join(h.Workspace, "report.json"))
	if err != nil || json.Unmarshal(raw, &js) != nil || js["lens_model"] != lens.Model || js["queen_model"] != queen.Model {
		t.Errorf("report.json names lens %v and queen %v, want %s and %s (%v)", js["lens_model"], js["queen_model"], lens.Model, queen.Model, err)
	}
	kept := &agentHarness{Options: Options{Profile: profile, Provider: string(runner.BackendOpenAI), ProviderSet: true}}
	if err := kept.useProfile(); err != nil || kept.Provider != string(runner.BackendOpenAI) {
		t.Errorf("an explicit --provider became %q (%v)", kept.Provider, err)
	}
	for _, h := range []*agentHarness{{Options: Options{Profile: profile, LensModel: "m"}}, {Options: Options{Profile: profile, QueenModel: "m"}}, {Options: Options{Profile: profile, LensReasoning: "none"}}, {Options: Options{Profile: profile, QueenReasoning: "none"}}} {
		if err := h.useProfile(); err == nil || !strings.Contains(err.Error(), "cannot be combined with routing profile") {
			t.Errorf("%+v: err = %v", h, err)
		}
	}
}

// Under each shipped routing profile the proof case is not refused at
// preflight for its routing: the proof workflow chb proof runs, routed by
// the profile with the provider the harness passes it, sends every call, its
// repair's included, to a model the profile routes on provider local, and
// none off this machine.
func TestProofCase_RoutesUnderEachShippedProfile(t *testing.T) {
	t.Setenv("HIVE_PROFILE", "")
	t.Setenv("HIVE_LOCAL_BASE_URL", "")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	proof, err := fs.ReadFile(hive.Workflows, "proof.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for name := range models.Load().Profiles {
		t.Run(name, func(t *testing.T) {
			h := &agentHarness{Options: Options{Profile: name, Provider: string(runner.BackendClaudeCLI)}}
			if err := h.useProfile(); err != nil {
				t.Fatal(err)
			}
			p, err := runner.ResolveProfile(name)
			if err != nil {
				t.Fatal(err)
			}
			routed, err := runner.ApplyProfile(string(proof), p)
			if err != nil {
				t.Fatal(err)
			}
			defn, err := workflow.LoadYAMLString(routed)
			if err != nil {
				t.Fatal(err)
			}
			routes := map[string]bool{}
			for _, r := range p.Routes {
				routes[r.Model] = true
			}
			lines := runner.Routing(runner.Config{Provider: h.Provider}, defn)
			for _, l := range lines {
				if l.Provider != runner.BackendLocal || !routes[l.Model] {
					t.Errorf("%s: want a model %s routes, on provider local", l, name)
				}
			}
			if err := runner.PreflightLocality(p, lines); err != nil {
				t.Error(err)
			}
		})
	}
}

// A check's detail and an output's tail are cut by byte length on a rune
// boundary: a cut that falls inside a multi-byte character leaves valid
// UTF-8, so the console and REPORT.md show no replacement character.
func TestDetailAndTailCutOnARuneBoundary(t *testing.T) {
	detail := strings.Repeat("a", 159) + "—" + strings.Repeat("b", 50)
	if got := detailSuffix(detail); !utf8.ValidString(got) || len(got) > len(" — ")+160+len("…") {
		t.Errorf("detailSuffix = %q, want valid UTF-8 of at most 160 bytes and an ellipsis", got)
	}
	out := strings.Repeat("c", 50) + "—" + strings.Repeat("d", 398)
	if got := tail(out); !utf8.ValidString(got) || !strings.HasSuffix(got, strings.Repeat("d", 398)) {
		t.Errorf("tail = %q, want valid UTF-8 that ends with the output's end", got)
	}
}
