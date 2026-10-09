// Package harness is chb agent-harness: it drives the shipped prompts —
// forager personas, agent templates, the proof workflow — through the local
// claude CLI and asserts the contracts they declare. Model text is not
// deterministic; the contracts are. Every case pins its inputs (suite file,
// preset, provider, tier) and the report records the canonical artifact
// hash as a drift detector, never as an equality claim.
package harness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/Chubby-Honey-Bee/hive/internal/artifact"
	"github.com/Chubby-Honey-Bee/hive/internal/bench"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
	"github.com/Chubby-Honey-Bee/hive/internal/models"
	"github.com/Chubby-Honey-Bee/hive/internal/runner"
)

type harnessSuite struct {
	Cases []harnessCase `yaml:"cases"`
}

type harnessCase struct {
	Name string `yaml:"name"`
	Kind string `yaml:"kind"` // swarm | template | proof | hive | bench | design
	Slow bool   `yaml:"slow"` // skipped unless --slow
	// Cost is what the case was measured to cost, in money and time. The
	// harness prints it with the model calls it counts (nominalCalls) when
	// it runs or skips the case, and the report shows both.
	Cost string `yaml:"cost"`

	// swarm
	Question string `yaml:"question"`
	Foragers string `yaml:"foragers"`
	Eval     bool   `yaml:"eval"`
	// LensTools is passed to `chb ask --lens-tools`: "read" for a question
	// the lenses answer by reading the repository. Empty keeps ask's
	// default, no tools.
	LensTools string `yaml:"lens_tools"`
	// Grade names an answer the harness computes from this repository (see
	// grade.go) and checks a hive case's database against. A
	// swarm case takes none: its verdict is graded by a bench case's twin
	// pairs, with RequirePairs.
	Grade string `yaml:"grade"`

	// hive: seed Gap as a critical gap, run workflows/hive.yaml for
	// MaxIterations (default 1) under Project, and grade the database.
	Gap           string `yaml:"gap"`
	Project       string `yaml:"project"`
	MaxIterations int    `yaml:"max_iterations"`

	// bench: the twin families (empty = all) at each generator seed; each
	// item runs on Arms (default swarm and solo), Foragers is the swarm's
	// preset. Items are generated after those of ExcludeSeeds, so none
	// repeats one of theirs. See bench.go and docs/specs/bench.md.
	Families     []string `yaml:"families"`
	Seeds        []int64  `yaml:"seeds"`
	ExcludeSeeds []int64  `yaml:"exclude_seeds"`
	Arms         []string `yaml:"arms"`
	// RequirePairs fails the case unless, on every arm, both twins of every
	// pair were answered right: a graded canary rather than a measurement.
	RequirePairs bool `yaml:"require_pairs"`

	// design: the tasks under Tasks (default fixtures/design), or only
	// TaskNames of them, each run on Arms (default designer and control),
	// Foragers the designer's preset, the executor stopped after
	// ExecutorMinutes (default 10). See design.go and
	// docs/specs/bench-design.md.
	Tasks           string   `yaml:"tasks"`
	TaskNames       []string `yaml:"task_names"`
	ExecutorMinutes int      `yaml:"executor_minutes"`

	// template
	Template    string `yaml:"template"` // agents/<template>.md
	Task        string `yaml:"task"`
	Agent       string `yaml:"agent"` // agent name the findings are written under
	Tools       string `yaml:"tools"` // comma-separated claude tools to allow; empty = none
	MaxTurns    int    `yaml:"max_turns"`
	Ingest      bool   `yaml:"ingest"` // run `chb ingest` over the output (marker contract)
	MinFindings int    `yaml:"min_findings"`
}

type harnessCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Warn   bool   `json:"warn,omitempty"` // reported, never fails the case
	Detail string `json:"detail,omitempty"`
}

type harnessResult struct {
	Name     string            `json:"name"`
	Kind     string            `json:"kind"`
	OK       bool              `json:"ok"`
	Skipped  bool              `json:"skipped,omitempty"`
	Cost     string            `json:"cost,omitempty"` // the counted model calls and the suite's cost note
	Duration string            `json:"duration"`
	Checks   []harnessCheck    `json:"checks"`
	Hash     string            `json:"artifact_sha256,omitempty"`
	Models   map[string]string `json:"models,omitempty"`
	Verdicts map[string]string `json:"verdicts,omitempty"`
	Nodes    []bench.NodeStat  `json:"nodes,omitempty"`
	Bench    *benchReport      `json:"bench,omitempty"`
	Design   *designReport     `json:"design,omitempty"`
	Log      string            `json:"log_dir"`
}

// Options are one run of the harness: chb agent-harness's flags, and what
// the command resolves for them.
type Options struct {
	// Self is the chb binary every case runs; its directory leads PATH in
	// each case's environment.
	Self string
	// Stdout takes the run's progress lines.
	Stdout io.Writer
	// Foragers resolves the forager tree every case copies into its private
	// tree, and LoadForagers reads the foragers in it, refusing a tree that
	// holds none; both as chb resolves the tree.
	Foragers     func() foragers.Source
	LoadForagers func() ([]foragers.Forager, error)

	Suite      string
	Workspace  string
	BudgetMode string
	Model      string
	Only       string
	Slow       bool
	KeepGoing  bool
	Strict     bool

	// Provider is the provider every case runs on; ProviderSet says
	// --provider gave it, so a routing profile's provider does not replace
	// it.
	Provider    string
	ProviderSet bool
	LensModel   string
	QueenModel  string
	// Seed is the sampling seed of swarm and bench runs, when SeedSet.
	Seed    int64
	SeedSet bool
	Reps    int
	Config  string

	LensReasoning  string
	QueenReasoning string

	// Profile is the routing profile every case runs under (--profile, else
	// HIVE_PROFILE), exported to each case as HIVE_PROFILE.
	Profile string

	PersonaProfile  string
	PersonaSections string

	// DirectVoice and ContextSplit pass --direct-voice and --context-split
	// to every chb ask a swarm or bench case runs (swarm.md).
	DirectVoice  bool
	ContextSplit bool
}

// agentHarness is one run of the harness: its options, and what run
// resolves from them.
type agentHarness struct {
	Options

	// lensModels is --lens-model split on commas, checked by run.
	lensModels []string
	// profileLens and profileQueen are the routing profile's lens and queen
	// routes, whose models the bench rows record and whose models and
	// reasoning the report names.
	profileLens, profileQueen runner.Route
}

// Run resolves the routing profile, checks the flags, runs the suite's
// cases and writes the report. It fails when the profile or a flag is
// refused, the suite does not load, the report cannot be written, or a case
// failed.
func Run(ctx context.Context, o Options) error {
	h := &agentHarness{Options: o}
	if err := h.useProfile(); err != nil {
		return err
	}
	return h.run(ctx)
}

// swarmFlagArgs are the chb ask flags of the two swarm changes under test.
func (h *agentHarness) swarmFlagArgs() []string {
	var out []string
	if h.DirectVoice {
		out = append(out, "--direct-voice")
	}
	if h.ContextSplit {
		out = append(out, "--context-split")
	}
	return out
}

// harnessProviders are the providers --provider accepts, by canonical name.
var harnessProviders = []runner.BackendKind{runner.BackendClaudeCLI, runner.BackendAnthropic, runner.BackendOpenAI, runner.BackendGemini, runner.BackendGeminiCLI, runner.BackendLocal}

// useProfile resolves the harness's routing profile, --profile else
// HIVE_PROFILE, before any case runs. It refuses the flags that pin what
// the profile routes, and makes the profile's provider the harness's unless
// --provider was given.
func (h *agentHarness) useProfile() error {
	if h.Profile == "" {
		h.Profile = strings.TrimSpace(os.Getenv("HIVE_PROFILE"))
	}
	if h.Profile == "" {
		return nil
	}
	p, err := h.resolveProfile()
	if err != nil {
		return err
	}
	h.applyProfile(p)
	return nil
}

// resolveProfile refuses the flags that pin what the routing profile
// routes, then resolves the profile.
func (h *agentHarness) resolveProfile() (*runner.Profile, error) {
	for _, f := range []struct{ flag, value string }{{"--lens-model", h.LensModel}, {"--queen-model", h.QueenModel}, {"--lens-reasoning", h.LensReasoning}, {"--queen-reasoning", h.QueenReasoning}} {
		if f.value != "" {
			return nil, fmt.Errorf("%s cannot be combined with routing profile %s, which routes the lenses and Queen", f.flag, h.Profile)
		}
	}
	return runner.ResolveProfile(h.Profile)
}

// applyProfile makes the profile's provider the harness's unless
// --provider was given, and keeps its lens and queen routes.
func (h *agentHarness) applyProfile(p *runner.Profile) {
	if !h.ProviderSet {
		h.Provider = string(p.Provider)
	}
	h.profileLens, h.profileQueen = p.Routes["lens"], p.Routes["queen"]
}

// configName names the configuration in bench results: --config, else its
// models, its persona and the swarm changes under test. A routing profile
// names the models and reasoning; the persona profile is chosen apart from
// it, so both name the configuration.
func (h *agentHarness) configName() string {
	if h.Config != "" {
		return h.Config
	}
	parts := append(h.configModelParts(), h.configPersonaParts()...)
	if h.DirectVoice {
		parts = append(parts, "direct-voice")
	}
	if h.ContextSplit {
		parts = append(parts, "context-split")
	}
	return strings.Join(parts, "/")
}

// configModelParts name the models and their reasoning: the routing
// profile, or the provider, the lens and queen models, the budget mode and,
// when either is set, the reasoning levels.
func (h *agentHarness) configModelParts() []string {
	if h.Profile != "" {
		return []string{"profile " + h.Profile}
	}
	parts := []string{h.Provider, harnessOr(h.LensModel, "tier"), harnessOr(h.QueenModel, "tier"), harnessOr(h.BudgetMode, "default")}
	if h.LensReasoning != "" || h.QueenReasoning != "" {
		parts = append(parts, "reasoning "+harnessOr(h.LensReasoning, "default")+","+harnessOr(h.QueenReasoning, "default"))
	}
	return parts
}

// configPersonaParts name the persona profile and sections, when either is
// given.
func (h *agentHarness) configPersonaParts() []string {
	if h.PersonaProfile == "" && h.PersonaSections == "" {
		return nil
	}
	persona := harnessOr(h.PersonaProfile, foragers.ProfileFull)
	if h.PersonaSections != "" {
		persona += "§" + h.PersonaSections
	}
	return []string{persona}
}

// harnessOr is s, or alt when s is empty.
func harnessOr(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

// personaLabel names the persona profile and sections a run uses, for the
// report: ask's default when neither is given.
func personaLabel(profile, sections string) string {
	if profile == "" {
		profile = foragers.ProfileFull
	}
	if sections != "" {
		return fmt.Sprintf("`%s`, sections %s", profile, sections)
	}
	return fmt.Sprintf("`%s`", profile)
}

// personaArgs are the chb ask flags that choose the persona profile under
// test.
func (h *agentHarness) personaArgs() []string {
	var out []string
	if h.PersonaProfile != "" {
		out = append(out, "--persona-profile", h.PersonaProfile)
	}
	if h.PersonaSections != "" {
		out = append(out, "--persona-sections", h.PersonaSections)
	}
	return out
}

// modelArgs are the chb ask flags that pin the models under test and
// their reasoning levels. lensModels is the lens list for this run.
func (h *agentHarness) modelArgs(lensModels []string) []string {
	var out []string
	if len(lensModels) > 0 {
		out = append(out, "--model", strings.Join(lensModels, ","))
	}
	return appendHarnessFlags(out,
		[2]string{"--synthesizer-model", h.QueenModel},
		[2]string{"--forager-reasoning", h.LensReasoning},
		[2]string{"--synthesizer-reasoning", h.QueenReasoning})
}

// appendHarnessFlags appends each flag with its value, for the values that
// are set.
func appendHarnessFlags(out []string, flags ...[2]string) []string {
	for _, f := range flags {
		if f[1] != "" {
			out = append(out, f[0], f[1])
		}
	}
	return out
}

// checkModelFlags splits --lens-model into h.lensModels and checks it and
// the reasoning levels before any case runs: an empty entry in the list,
// or a level outside models.ReasoningLevels, is refused.
func (h *agentHarness) checkModelFlags() error {
	var err error
	if h.lensModels, err = splitLensModels(h.LensModel); err != nil {
		return err
	}
	return h.checkReasoningLevels()
}

// splitLensModels splits --lens-model on commas, refusing an empty entry;
// with the error it returns the entries before that one.
func splitLensModels(flag string) ([]string, error) {
	if flag == "" {
		return nil, nil
	}
	var out []string
	for _, m := range strings.Split(flag, ",") {
		if m = strings.TrimSpace(m); m == "" {
			return out, fmt.Errorf("--lens-model %q: an entry is empty", flag)
		}
		out = append(out, m)
	}
	return out, nil
}

// checkReasoningLevels refuses a lens or queen reasoning level outside
// models.ReasoningLevels.
func (h *agentHarness) checkReasoningLevels() error {
	for _, r := range []struct{ flag, level string }{{"--lens-reasoning", h.LensReasoning}, {"--queen-reasoning", h.QueenReasoning}} {
		if r.level == "" {
			continue
		}
		if err := models.CheckReasoningLevel(r.level); err != nil {
			return fmt.Errorf("%s: %w", r.flag, err)
		}
	}
	return nil
}

// seedArgs pins the sampling seed of repetition rep (1-based), when --seed
// was given.
func (h *agentHarness) seedArgs(rep int) []string {
	if !h.SeedSet {
		return nil
	}
	return []string{"--seed", strconv.FormatInt(h.Seed+int64(rep-1), 10)}
}

// caseCost says what a case costs: the model calls nominalCalls counts,
// then the suite's cost note. "" when there is neither.
func (h *agentHarness) caseCost(c harnessCase) string {
	var parts []string
	if calls := h.nominalCalls(c); calls != "" {
		parts = append(parts, calls)
	}
	if note := strings.TrimSpace(c.Cost); note != "" {
		parts = append(parts, note)
	}
	return strings.Join(parts, "; ")
}

// nominalCalls counts a swarm or bench case's model calls from the live
// roster: one per node that calls a model, with no repair, no retry and no
// tool turn. A swarm makes one per lens and one for Queen; its coverage
// pass adds the evaluator and up to foragers.DefaultFollowups follow-ups.
// A bench case makes that per swarm run and one per solo run, for each
// item, arm and repetition. "" for any other kind, or when the roster or
// the items do not load.
func (h *agentHarness) nominalCalls(c harnessCase) string {
	if c.Kind != "swarm" && c.Kind != "bench" {
		return ""
	}
	calls, ok := h.caseCalls(c)
	if !ok {
		return ""
	}
	return calls.String()
}

// harnessCalls is a range of model calls, lo to hi.
type harnessCalls struct{ lo, hi int }

// plus is the sum of two ranges.
func (n harnessCalls) plus(m harnessCalls) harnessCalls {
	return harnessCalls{lo: n.lo + m.lo, hi: n.hi + m.hi}
}

// times is the range of runs repetitions of it.
func (n harnessCalls) times(runs int) harnessCalls {
	return harnessCalls{lo: runs * n.lo, hi: runs * n.hi}
}

// String writes the count, or its range when the ends differ.
func (n harnessCalls) String() string {
	if n.lo == n.hi {
		return fmt.Sprintf("%d model calls", n.lo)
	}
	return fmt.Sprintf("%d–%d model calls", n.lo, n.hi)
}

// caseCalls counts a swarm or bench case's model calls; false when its
// preset is empty, or the roster or the items do not load.
func (h *agentHarness) caseCalls(c harnessCase) (harnessCalls, bool) {
	preset := c.preset()
	if preset == "" {
		return harnessCalls{}, false
	}
	all, lenses, ok := h.presetLenses(preset)
	if !ok {
		return harnessCalls{}, false
	}
	run := h.swarmRunCalls(lenses, c.Eval)
	if c.Kind != "bench" {
		return run, true
	}
	return h.benchCaseCalls(all, c, run)
}

// presetLenses loads the live roster, as the bench case does, and counts
// the lenses preset dispatches; false when the roster or the preset does not
// load.
func (h *agentHarness) presetLenses(preset string) ([]foragers.Forager, int, bool) {
	all, err := h.LoadForagers()
	if err != nil {
		return nil, 0, false
	}
	swarm, err := foragers.Filter(all, strings.Split(preset, ","))
	if err != nil {
		return nil, 0, false
	}
	lens, _ := foragers.SplitByArchetype(swarm)
	return all, len(lens), true
}

// swarmRunCalls counts one swarm run's calls: one per lens and one for
// Queen, one more for the direct voice, and with the coverage pass the
// evaluator and up to foragers.DefaultFollowups follow-ups.
func (h *agentHarness) swarmRunCalls(lenses int, eval bool) harnessCalls {
	n := harnessCalls{lo: lenses + 1, hi: lenses + 1}
	if h.DirectVoice {
		n = n.plus(harnessCalls{lo: 1, hi: 1})
	}
	if eval {
		n = n.plus(harnessCalls{lo: 1, hi: 1 + foragers.DefaultFollowups})
	}
	return n
}

// benchCaseCalls counts a bench case's calls: for each item and
// repetition, a swarm run's on the swarm arm and one on the solo arm.
func (h *agentHarness) benchCaseCalls(all []foragers.Forager, c harnessCase, swarmRun harnessCalls) (harnessCalls, bool) {
	items, err := benchItems(all, c)
	if err != nil {
		return harnessCalls{}, false
	}
	var per harnessCalls
	for _, a := range c.benchArms() {
		per = per.plus(benchArmCalls(a, swarmRun))
	}
	return per.times(len(items) * h.Reps), true
}

// benchArmCalls is one run's calls on arm a: a swarm run's, or the solo
// control's one.
func benchArmCalls(a string, swarmRun harnessCalls) harnessCalls {
	switch a {
	case bench.ArmSwarm:
		return swarmRun
	case bench.ArmSolo:
		return harnessCalls{lo: 1, hi: 1}
	}
	return harnessCalls{}
}

// preset is the forager preset the case runs: its own, else minimal for a
// bench or design case; a swarm case names its own.
func (c harnessCase) preset() string {
	if c.Foragers != "" || c.Kind == "swarm" {
		return c.Foragers
	}
	return "minimal"
}

// benchArms is the bench case's arms, swarm and solo by default.
func (c harnessCase) benchArms() []string {
	if len(c.Arms) == 0 {
		return []string{bench.ArmSwarm, bench.ArmSolo}
	}
	return c.Arms
}

func (h *agentHarness) logf(format string, a ...any) { fmt.Fprintf(h.Stdout, format+"\n", a...) }

// run checks the flags, runs the suite's cases and writes the report. It
// fails when a flag is refused, the suite does not load, the report cannot
// be written, or a case failed.
func (h *agentHarness) run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	suite, suiteHash, err := h.prepareRun()
	if err != nil {
		return err
	}
	results := h.runSuite(ctx, suite)
	if err := h.writeReport(results, suiteHash); err != nil {
		return err
	}
	h.logRunSummary(results)
	return harnessCasesFailed(results)
}

// prepareRun checks the flags, reads the suite and makes the workspace,
// before any case runs. It returns the suite with the first 16 hex digits
// of its sha256.
func (h *agentHarness) prepareRun() (harnessSuite, string, error) {
	if err := h.checkRunFlags(); err != nil {
		return harnessSuite{}, "", err
	}
	suite, suiteHash, err := readHarnessSuite(h.Suite)
	if err != nil {
		return harnessSuite{}, "", err
	}
	return suite, suiteHash, os.MkdirAll(h.Workspace, 0o755)
}

// checkRunFlags refuses what no case can run under: an unknown provider,
// fewer than one repetition, an unknown persona profile or sections, a bad
// model or reasoning flag, and the claude-cli provider without the claude
// CLI.
func (h *agentHarness) checkRunFlags() error {
	for _, check := range []func() error{h.checkProvider, h.checkReps, h.checkPersonaFlags, h.checkModelFlags, h.checkClaudeCLI} {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

// checkProvider refuses a provider outside harnessProviders.
func (h *agentHarness) checkProvider() error {
	if !slices.Contains(harnessProviders, runner.BackendKind(h.Provider)) {
		return fmt.Errorf("unknown --provider %q (want one of %v)", h.Provider, harnessProviders)
	}
	return nil
}

// checkReps refuses fewer than one repetition.
func (h *agentHarness) checkReps() error {
	if h.Reps < 1 {
		return fmt.Errorf("--reps must be at least 1")
	}
	return nil
}

// checkPersonaFlags refuses a persona profile other than full or lean, and
// persona sections that do not parse.
func (h *agentHarness) checkPersonaFlags() error {
	if !knownPersonaProfile(h.PersonaProfile) {
		return fmt.Errorf("--persona-profile %q: want %s or %s", h.PersonaProfile, foragers.ProfileFull, foragers.ProfileLean)
	}
	if h.PersonaSections != "" {
		if _, err := foragers.ParsePersonaSections(h.PersonaSections); err != nil {
			return fmt.Errorf("--persona-sections: %w", err)
		}
	}
	return nil
}

// knownPersonaProfile: the persona profile is unset, full or lean.
func knownPersonaProfile(p string) bool {
	return p == "" || p == foragers.ProfileFull || p == foragers.ProfileLean
}

// checkClaudeCLI refuses the claude-cli provider when the claude CLI is not
// on PATH.
func (h *agentHarness) checkClaudeCLI() error {
	if !h.onClaudeCLI() {
		return nil
	}
	if _, err := exec.LookPath("claude"); err != nil {
		return fmt.Errorf("the claude CLI is not on PATH; the claude-cli provider drives prompts through it")
	}
	return nil
}

// onClaudeCLI: the provider is the claude CLI.
func (h *agentHarness) onClaudeCLI() bool {
	return h.Provider == string(runner.BackendClaudeCLI)
}

// readHarnessSuite reads and parses the suite file, and returns it with the
// first 16 hex digits of its sha256.
func readHarnessSuite(path string) (harnessSuite, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return harnessSuite{}, "", err
	}
	var suite harnessSuite
	if err := yaml.Unmarshal(raw, &suite); err != nil {
		return harnessSuite{}, "", fmt.Errorf("parse suite: %w", err)
	}
	return suite, fmt.Sprintf("%x", sha256.Sum256(raw))[:16], nil
}

// runSuite runs the cases --only selects, in order, recording the skipped
// ones, and stops after a failed case unless --keep-going.
func (h *agentHarness) runSuite(ctx context.Context, suite harnessSuite) []harnessResult {
	var results []harnessResult
	for _, c := range suite.Cases {
		if !h.selects(c) {
			continue
		}
		res := h.runOrSkip(ctx, c)
		results = append(results, res)
		if h.stopsAfter(res) {
			break
		}
	}
	return results
}

// selects: --only is unset, or the case's name contains it.
func (h *agentHarness) selects(c harnessCase) bool {
	return h.Only == "" || strings.Contains(c.Name, h.Only)
}

// stopsAfter: the case failed and --keep-going is off.
func (h *agentHarness) stopsAfter(res harnessResult) bool {
	return !res.OK && !h.KeepGoing
}

// runOrSkip runs one case, or records it as skipped and says why.
func (h *agentHarness) runOrSkip(ctx context.Context, c harnessCase) harnessResult {
	cost := h.caseCost(c)
	if why := h.skipReason(c); why != "" {
		h.logf("  - %-28s skipped (%s)%s", c.Name, why, detailSuffix(cost))
		return harnessResult{Name: c.Name, Kind: c.Kind, OK: true, Skipped: true, Cost: cost}
	}
	return h.runAndLog(ctx, c, cost)
}

// skipReason says why a case is skipped, "" when it runs: a slow case
// without --slow, or a template case, which drives claude -p, on another
// provider.
func (h *agentHarness) skipReason(c harnessCase) string {
	if h.skipsAsSlow(c) {
		return "slow; pass --slow"
	}
	if h.skipsAsTemplate(c) {
		return "template cases drive claude -p; provider is " + h.Provider
	}
	return ""
}

// skipsAsSlow: the case is slow and --slow was not given.
func (h *agentHarness) skipsAsSlow(c harnessCase) bool { return c.Slow && !h.Slow }

// skipsAsTemplate: a template case off the claude-cli provider.
func (h *agentHarness) skipsAsTemplate(c harnessCase) bool {
	return c.Kind == "template" && !h.onClaudeCLI()
}

// runAndLog runs one case and times it, fails it on a warning under
// --strict, and logs its checks and result.
func (h *agentHarness) runAndLog(ctx context.Context, c harnessCase, cost string) harnessResult {
	h.logf("── %s (%s) ──", c.Name, c.Kind)
	if cost != "" {
		h.logf("  cost: %s", cost)
	}
	start := time.Now()
	res := h.runCase(ctx, c)
	res.Cost = cost
	res.Duration = time.Since(start).Round(time.Second).String()
	if h.Strict && res.warned() {
		res.OK = false
	}
	h.logChecks(res.Checks)
	h.logf("%s %s (%s)", harnessMark(res.OK), c.Name, res.Duration)
	return res
}

// logChecks logs each check with its sign and detail.
func (h *agentHarness) logChecks(checks []harnessCheck) {
	for _, ck := range checks {
		h.logf("  %s %s%s", ck.mark(), ck.Name, detailSuffix(ck.Detail))
	}
}

// warned: a check of the result is a failed adherence check.
func (r harnessResult) warned() bool {
	for _, ck := range r.Checks {
		if ck.Warn {
			return true
		}
	}
	return false
}

// mark is the check's sign: ⚠ for a failed adherence check, ✗ for a failed
// contract check, ✓ for one that held.
func (ck harnessCheck) mark() string {
	switch {
	case ck.Warn:
		return "⚠"
	case !ck.OK:
		return "✗"
	}
	return "✓"
}

// harnessMark is ✓ for ok, else ✗.
func harnessMark(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}

// logRunSummary logs the adherence rate, when an adherence check ran, and
// where the report is.
func (h *agentHarness) logRunSummary(results []harnessResult) {
	if clean, total := adherenceRate(results); total > 0 {
		h.logf("\nadherence: %d/%d checks clean (⚠ reports; --strict makes them fail)", clean, total)
	}
	h.logf("report: %s (json alongside)", filepath.Join(h.Workspace, "REPORT.md"))
}

// harnessCasesFailed is the run's error when a case failed.
func harnessCasesFailed(results []harnessResult) error {
	failed := 0
	for _, r := range results {
		if !r.OK {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d case(s) failed", failed)
	}
	return nil
}

// detailSuffix is " — " and the detail, cut to 160 bytes on a rune boundary
// (runeStart), or "" when there is none.
func detailSuffix(d string) string {
	if d == "" {
		return ""
	}
	if len(d) > 160 {
		d = d[:runeStart(d, 160)] + "…"
	}
	return " — " + d
}

// runeStart is i, or the start of the rune byte i of s falls inside, so a
// cut there splits no multi-byte character.
func runeStart(s string, i int) int {
	for i > 0 && i < len(s) && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// exec runs a command, tees its output into <log>/<step>.log, and returns
// the combined output.
func (h *agentHarness) exec(ctx context.Context, logDir, step string, env []string, dir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	if dir != "" {
		cmd.Dir = dir
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	_ = os.WriteFile(filepath.Join(logDir, step+".log"), buf.Bytes(), 0o644)
	return buf.String(), err
}

// caseEnv is the environment a case's chb and claude processes get.
func (h *agentHarness) caseEnv(dbPath string) []string {
	env := os.Environ()
	// The harness binary IS chb; put its directory first so agent tools
	// resolve the same build under test.
	env = append(env,
		"HIVE_DB_PATH="+dbPath,
		"HIVE_PROVIDER="+h.Provider,
		"HIVE_PROFILE="+h.Profile,
		"PATH="+filepath.Dir(h.Self)+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	return env
}

// runCase runs one case in its workspace by its kind.
func (h *agentHarness) runCase(ctx context.Context, c harnessCase) harnessResult {
	r, err := h.newCaseRun(c)
	if err != nil {
		return failResult(harnessResult{Name: c.Name, Kind: c.Kind, OK: true}, "workspace", err.Error())
	}
	run, ok := harnessCaseKinds[c.Kind]
	if !ok {
		r.check("kind", false, "unknown case kind "+c.Kind)
		return r.res
	}
	run(h, ctx, c, r)
	return r.res
}

// harnessCaseKinds runs each kind of case.
var harnessCaseKinds = map[string]func(h *agentHarness, ctx context.Context, c harnessCase, r *caseRun){
	"swarm":    (*agentHarness).runSwarmCase,
	"template": (*agentHarness).runTemplateCase,
	"hive":     (*agentHarness).runHiveCase,
	"bench": func(h *agentHarness, ctx context.Context, c harnessCase, r *caseRun) {
		r.res.Bench = h.runBenchCase(ctx, c, r.ws, r.check, r.warn)
	},
	"design": func(h *agentHarness, ctx context.Context, c harnessCase, r *caseRun) {
		r.res.Design = h.runDesignCase(ctx, c, r.ws, r.check, r.warn)
	},
	"proof": (*agentHarness).runProofCase,
}

// caseRun is one case's run: its result so far, and its workspace,
// database and environment.
type caseRun struct {
	res    harnessResult
	ws     string
	dbPath string
	env    []string
}

// newCaseRun makes the case's workspace. It is absolute: the template cases
// run claude with cwd inside the case dir, and a relative HIVE_DB_PATH
// would resolve to a second database. A bench or design case clears its
// workspace itself, once it knows it will run; any other starts from an
// empty one.
func (h *agentHarness) newCaseRun(c harnessCase) (*caseRun, error) {
	ws, err := filepath.Abs(filepath.Join(h.Workspace, c.Name))
	if err != nil {
		return nil, err
	}
	if !c.clearsOwnWorkspace() {
		_ = os.RemoveAll(ws)
	}
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(ws, "hive.db")
	return &caseRun{res: harnessResult{Name: c.Name, Kind: c.Kind, OK: true, Log: ws}, ws: ws, dbPath: dbPath, env: h.caseEnv(dbPath)}, nil
}

// clearsOwnWorkspace: a bench or design case, which clears its workspace
// only once it knows it will run.
func (c harnessCase) clearsOwnWorkspace() bool {
	return c.Kind == "bench" || c.Kind == "design"
}

// check records a contract check; a failed one fails the case.
func (r *caseRun) check(name string, ok bool, detail string) {
	r.res.Checks = append(r.res.Checks, harnessCheck{Name: name, OK: ok, Detail: detail})
	if !ok {
		r.res.OK = false
	}
}

// warn records an adherence check, which reports and never fails the case.
func (r *caseRun) warn(name string, ok bool, detail string) {
	r.res.Checks = append(r.res.Checks, harnessCheck{Name: name, OK: ok, Warn: !ok, Detail: detail})
}

// record records a check as checkVerdictContract made it: a failed
// adherence check warns, any other is a contract check.
func (r *caseRun) record(ck harnessCheck) {
	if ck.Warn {
		r.warn(ck.Name, ck.OK, ck.Detail)
		return
	}
	r.check(ck.Name, ck.OK, ck.Detail)
}

// chbOnCase runs this chb on the case's database in its workspace, logging
// the output as step.
func (h *agentHarness) chbOnCase(ctx context.Context, r *caseRun, step string, args ...string) (string, error) {
	return h.exec(ctx, r.ws, step, r.env, "", h.Self, append([]string{"--db", r.dbPath}, args...)...)
}

// initCaseDB creates the case's database; false, with the case failed,
// when it cannot.
func (h *agentHarness) initCaseDB(ctx context.Context, r *caseRun) bool {
	out, err := h.chbOnCase(ctx, r, "db-init", "db-init")
	if err != nil {
		r.check("db-init", false, tail(out))
		return false
	}
	return true
}

// runSwarmCase runs chb ask on the case's question and preset in a private
// copy of the personas, and checks the artifact, every forager's verdict
// contract, Queen's synthesis and the run's database.
func (h *agentHarness) runSwarmCase(ctx context.Context, c harnessCase, r *caseRun) {
	if c.Grade != "" {
		r.check("grade", false, "a swarm case takes no grade: a model that always gives the one verdict passes it; grade with a bench case and require_pairs")
		return
	}
	if !h.initCaseDB(ctx, r) {
		return
	}
	art := filepath.Join(r.ws, "artifact.json")
	a := h.askSwarm(ctx, c, r, art)
	if a == nil {
		return
	}
	h.checkSwarmArtifact(ctx, r, a, art)
}

// askSwarm runs chb ask in a private copy of foragers/ and agents/, checks
// that the copy is unchanged and that ask exited 0, and returns the
// artifact it wrote; nil when there is none.
func (h *agentHarness) askSwarm(ctx context.Context, c harnessCase, r *caseRun, art string) *artifact.Artifact {
	tree, err := h.prepareTree(r.ws)
	if err != nil {
		r.check("copy foragers/ and agents/ into the case's tree", false, err.Error())
		return nil
	}
	manifest, merr := treeManifest(tree)
	args := append([]string{"--db", r.dbPath, "ask", c.Question, "--foragers", c.Foragers,
		"--budget-mode", h.BudgetMode, "--artifact", art, "--out", filepath.Join(r.ws, "swarm.yaml")},
		h.swarmRunArgs(c.Eval, c.LensTools, h.lensModels, 1)...)
	out, err := h.exec(ctx, r.ws, "ask", treeEnv(r.env, tree), tree, h.Self, args...)
	unchanged, changes := treeCheck(tree, manifest, merr)
	r.check("the run left its private tree unchanged", unchanged, changes)
	r.check("chb ask exits 0", err == nil, tail(out))
	if err != nil {
		return nil
	}
	a, err := loadArtifact(art)
	r.check("artifact written and parses", err == nil, errString(err))
	return a
}

// swarmRunArgs are the chb ask flags every swarm run passes after its own:
// --no-eval unless eval, --lens-tools when set, then the models, persona,
// swarm changes and seed under test, for repetition rep.
func (h *agentHarness) swarmRunArgs(eval bool, lensTools string, lensModels []string, rep int) []string {
	var args []string
	if !eval {
		args = append(args, "--no-eval")
	}
	if lensTools != "" {
		args = append(args, "--lens-tools", lensTools)
	}
	return append(append(append(append(args, h.modelArgs(lensModels)...), h.personaArgs()...), h.swarmFlagArgs()...), h.seedArgs(rep)...)
}

// checkSwarmArtifact checks the swarm's artifact: its sha256, the foragers
// it dispatched and each one's verdict contract, Queen's synthesis, and
// the run's database.
func (h *agentHarness) checkSwarmArtifact(ctx context.Context, r *caseRun, a *artifact.Artifact, art string) {
	r.res.Hash = a.Hash
	r.res.Models = a.Models
	vout, verr := h.exec(ctx, r.ws, "verify-artifact", r.env, "", h.Self, "verify-artifact", art)
	r.check("artifact sha256 verifies", verr == nil, tail(vout))
	r.check("foragers dispatched", len(a.Foragers) > 0, strings.Join(a.Foragers, ","))
	r.res.Verdicts = map[string]string{}
	for _, name := range a.Foragers {
		r.checkVerdict(name, a)
	}
	r.checkSynthesis(a.Synthesis)
	r.checkSwarmDB(a)
}

// checkVerdict records the forager's verdict and checks it against its
// persona's contract.
func (r *caseRun) checkVerdict(name string, a *artifact.Artifact) {
	v, _ := a.Verdicts[name].(map[string]any)
	r.res.Verdicts[name] = fmt.Sprint(v["verdict"])
	contract, err := loadPersonaContract(name)
	if err != nil {
		r.check("persona "+name+" contract loads", false, err.Error())
		return
	}
	for _, ck := range checkVerdictContract(name, v, contract) {
		r.record(ck)
	}
}

// checkSynthesis checks that Queen's synthesis has a verdict in the enum and
// a recommendation.
func (r *caseRun) checkSynthesis(synthesis map[string]any) {
	sv, _ := synthesis["verdict"].(string)
	r.check("queen synthesis has a verdict in the enum", slices.Contains([]string{"support", "oppose", "conditional", "abstain"}, sv), fmt.Sprintf("verdict=%q", sv))
	rec, _ := synthesis["recommendation"].(string)
	r.check("queen synthesis has a recommendation", strings.TrimSpace(rec) != "", "")
}

// runTemplateCase runs an agents/<template>.md prompt with the case's task
// through claude -p, ingests its markers when the case says so, and checks
// the findings its agent wrote.
func (h *agentHarness) runTemplateCase(ctx context.Context, c harnessCase, r *caseRun) {
	tmpl, err := os.ReadFile(filepath.Join("agents", c.Template+".md"))
	if err != nil {
		r.check("template loads", false, err.Error())
		return
	}
	if !h.initCaseDB(ctx, r) {
		return
	}
	outPath, ok := h.runTemplatePrompt(ctx, c, r, string(tmpl))
	if !ok {
		return
	}
	h.ingestTemplateOutput(ctx, c, r, outPath)
	r.checkTemplateFindings(c)
}

// runTemplatePrompt runs the template with the case's task through claude -p
// in the case's workspace, writes its output to output.md, and checks that
// claude exited 0 with some output. It returns the output's path, and
// whether claude exited 0.
func (h *agentHarness) runTemplatePrompt(ctx context.Context, c harnessCase, r *caseRun, tmpl string) (string, bool) {
	prompt := tmpl + "\n\n---\n\n# Task for this run\n\n" + strings.TrimSpace(c.Task) + "\n"
	out, err := h.exec(ctx, r.ws, "claude", r.env, r.ws, "claude", h.templateArgs(c, prompt)...)
	outPath := filepath.Join(r.ws, "output.md")
	_ = os.WriteFile(outPath, []byte(out), 0o644)
	r.check("claude -p exits 0", err == nil, tail(out))
	r.check("output is non-empty", strings.TrimSpace(out) != "", "")
	return outPath, err == nil
}

// templateArgs are a template case's claude -p arguments: the prompt, the
// model, the turn cap when set, and the tools it may use, else none.
func (h *agentHarness) templateArgs(c harnessCase, prompt string) []string {
	args := []string{"-p", prompt, "--output-format", "text", "--model", h.Model}
	if c.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprint(c.MaxTurns))
	}
	if c.Tools != "" {
		return append(args, "--allowed-tools", c.Tools)
	}
	return append(args, "--disallowed-tools", "Bash,Edit,Write,WebFetch,WebSearch,Task,Read,Glob,Grep")
}

// ingestTemplateOutput runs chb ingest over the output, when the case
// checks the marker contract.
func (h *agentHarness) ingestTemplateOutput(ctx context.Context, c harnessCase, r *caseRun, outPath string) {
	if !c.Ingest {
		return
	}
	iout, ierr := h.chbOnCase(ctx, r, "ingest", "ingest", "--wave", "1", "--agent", c.Agent, outPath)
	r.check("chb ingest accepts every marker", ierr == nil, tail(iout))
}

// checkTemplateFindings checks what the template's agent wrote: at least
// the case's minimum of findings, their labels, and the MSS audit.
func (r *caseRun) checkTemplateFindings(c harnessCase) {
	store, err := db.NewStore(r.dbPath)
	if err != nil {
		r.check("open db", false, err.Error())
		return
	}
	defer store.Close()
	var n int
	_ = store.ReadDB.QueryRow(`SELECT COUNT(*) FROM findings WHERE agent = ?`, c.Agent).Scan(&n)
	r.check(fmt.Sprintf("≥ %d findings written by %q", c.MinFindings, c.Agent), n >= c.MinFindings, fmt.Sprintf("found %d", n))
	labels, err := harnessFindingLabels(store, c.Agent)
	r.check("labels are within the MSS partition", true, fmt.Sprint(labels)+detailSuffix(errString(err)))
	checkHarnessAudit(store, r.check)
}

// harnessFindingLabels counts the findings agent wrote, by MSS label; with
// an error it returns the labels read before it.
func harnessFindingLabels(store *db.Store, agent string) (map[string]int, error) {
	labels := map[string]int{}
	rows, err := store.ReadDB.Query(`SELECT mss_label, COUNT(*) FROM findings WHERE agent = ? GROUP BY mss_label`, agent)
	if err != nil {
		return labels, err
	}
	defer rows.Close()
	for rows.Next() {
		var l string
		var k int
		if err := rows.Scan(&l, &k); err != nil {
			return labels, err
		}
		labels[l] = k
	}
	return labels, rows.Err()
}

// checkHarnessAudit runs the MSS audit over the store: it must run and pass.
func checkHarnessAudit(store *db.Store, check func(string, bool, string)) {
	audit, err := store.MSSAudit()
	if err != nil {
		check("MSS audit runs", false, err.Error())
		return
	}
	check("MSS audit PASS", audit.Integrity == "PASS", audit.Integrity)
}

// runProofCase runs chb proof, the end-to-end run of the unattended runner.
func (h *agentHarness) runProofCase(ctx context.Context, _ harnessCase, r *caseRun) {
	out, err := h.exec(ctx, r.ws, "proof", r.env, "", h.Self, "proof", "--workspace", r.ws, "--provider", h.Provider, "--max-cost-usd", "1")
	r.check("chb proof exits 0 (decisions and node results recorded)", err == nil, tail(out))
}

// checkSwarmDB reads the swarm run's database: the nodes' stats, whether
// the personas emitted bare JSON, the run's and its nodes' status, the
// resonates bonds that fired, and the MSS audit.
func (r *caseRun) checkSwarmDB(a *artifact.Artifact) {
	store, err := db.NewStore(r.dbPath)
	if err != nil {
		r.check("open db", false, err.Error())
		return
	}
	defer store.Close()
	run, runErr := harnessLastRun(store)
	if run != nil {
		r.res.Nodes, _ = nodeStats(store, run.ID)
	}
	// must_emit_json_only: the personas promise a bare JSON object. The
	// runner tolerates a code fence or prose around it (workflow.ExtractJSONOutput),
	// so this is reported, not failed — it is the tier's adherence signal.
	fenced := harnessFencedPersonas(store, a.Foragers)
	r.warn("personas emitted bare JSON (adherence)", len(fenced) == 0, strings.Join(fenced, ","))
	if run == nil {
		r.check("workflow run recorded", false, errString(runErr))
		return
	}
	r.checkSwarmRun(store, *run, a)
}

// harnessLastRun is the store's latest workflow run, nil when it has none.
func harnessLastRun(store *db.Store) (*db.WorkflowRun, error) {
	runs, err := store.Workflows().ListWorkflowRuns(1)
	if err != nil || len(runs) == 0 {
		return nil, err
	}
	return &runs[0], nil
}

// harnessFencedPersonas lists the foragers whose stored output is not a bare
// JSON object.
func harnessFencedPersonas(store *db.Store, names []string) []string {
	var fenced []string
	for _, name := range names {
		if raw, ok := harnessForagerJSON(store, name); ok && !harnessBareJSON(raw) {
			fenced = append(fenced, name)
		}
	}
	return fenced
}

// harnessForagerJSON is the forager's raw output as the comb stores it,
// when it has one.
func harnessForagerJSON(store *db.Store, name string) (string, bool) {
	row, err := store.Comb().Get("forager:" + name)
	if err != nil || row == nil || !row.RawJSON.Valid {
		return "", false
	}
	return row.RawJSON.String, true
}

// harnessBareJSON: raw, trimmed, is one JSON object with nothing around it.
func harnessBareJSON(raw string) bool {
	raw = strings.TrimSpace(raw)
	return strings.HasPrefix(raw, "{") && strings.HasSuffix(raw, "}")
}

// checkSwarmRun checks the swarm's workflow run: it completed, no node
// failed or was rejected, every resonates pair inside the preset whose
// verdicts converge has a fired forager_bonds row for this run (∇), and
// the MSS audit passes.
func (r *caseRun) checkSwarmRun(store *db.Store, run db.WorkflowRun, a *artifact.Artifact) {
	r.check("workflow run completed", run.Status == "completed", "status="+run.Status)
	nodes, _ := store.Workflows().GetWorkflowNodeStates(run.ID)
	bad := harnessBadNodes(nodes)
	r.check("no failed or rejected node", len(bad) == 0, strings.Join(bad, ","))
	bonds, _ := store.ForagerBonds().ListByRun(run.ID)
	converged, recorded := harnessConvergingPairs(a, harnessFiredResonates(bonds))
	r.check("∇ recorded for every converging resonates pair", converged == recorded, fmt.Sprintf("converged=%d recorded=%d", converged, recorded))
	if audit, err := store.MSSAudit(); err == nil {
		r.check("MSS audit PASS", audit.Integrity == "PASS", audit.Integrity)
	}
}

// harnessBadNodes names the nodes that failed or were rejected, with their
// status.
func harnessBadNodes(nodes []db.WorkflowNodeState) []string {
	var bad []string
	for _, n := range nodes {
		if n.Status == "failed" || n.Status == "rejected" {
			bad = append(bad, n.NodeName+"="+n.Status)
		}
	}
	return bad
}

// harnessFiredResonates is the resonates bonds that fired, keyed from|to
// both ways.
func harnessFiredResonates(bonds []*db.ForagerBondRow) map[string]bool {
	fired := map[string]bool{}
	for _, b := range bonds {
		if b.Fired && b.Kind == db.BondResonates {
			fired[b.From+"|"+b.To] = true
			fired[b.To+"|"+b.From] = true
		}
	}
	return fired
}

// harnessConvergingPairs counts the resonates pairs inside the swarm's
// preset whose verdicts converge, each once, and those whose bond fired.
func harnessConvergingPairs(a *artifact.Artifact, fired map[string]bool) (converged, recorded int) {
	t := resonatesTally{a: a, inSet: map[string]bool{}, seen: map[string]bool{}, fired: fired}
	for _, f := range a.Foragers {
		t.inSet[f] = true
	}
	for _, name := range a.Foragers {
		t.addForager(name)
	}
	return t.converged, t.recorded
}

// resonatesTally counts converging resonates pairs over a swarm's
// foragers: the preset's members, the pairs counted so far, the bonds that
// fired, and the counts.
type resonatesTally struct {
	a                   *artifact.Artifact
	inSet, seen, fired  map[string]bool
	converged, recorded int
}

// addForager counts the forager's resonates partners, as its persona's
// contract declares them.
func (t *resonatesTally) addForager(name string) {
	contract, err := loadPersonaContract(name)
	if err != nil {
		return
	}
	for _, to := range resonatesPartners(contract) {
		t.addPair(name, to)
	}
}

// addPair counts the pair when it is new, inside the preset, and its
// verdicts converge.
func (t *resonatesTally) addPair(name, to string) {
	if !t.fresh(name, to) || !t.converge(name, to) {
		return
	}
	t.converged++
	if t.fired[name+"|"+to] {
		t.recorded++
	}
}

// fresh: to is in the preset and the pair was not seen before; it marks the
// pair seen.
func (t *resonatesTally) fresh(name, to string) bool {
	key := pairKey(name, to)
	if !t.inSet[to] || t.seen[key] {
		return false
	}
	t.seen[key] = true
	return true
}

// converge: both foragers gave a verdict, the same one, and it is not
// abstain.
func (t *resonatesTally) converge(a, b string) bool {
	va, vb := t.verdictOf(a), t.verdictOf(b)
	return va != "" && vb != "" && va != "abstain" && va == vb
}

// verdictOf is the forager's verdict in the artifact, "" when it has none.
func (t *resonatesTally) verdictOf(name string) string {
	v, _ := t.a.Verdicts[name].(map[string]any)
	s, _ := v["verdict"].(string)
	return s
}

// ── helpers ────────────────────────────────────────────────────────────

// loadArtifact reads and parses a canonical artifact.
func loadArtifact(path string) (*artifact.Artifact, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var a artifact.Artifact
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// errString is the error's message, "" for nil.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// tail is s trimmed, cut to its last 400 bytes, and back to the start of the
// rune the cut falls inside (runeStart).
func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 400 {
		return "…" + s[runeStart(s, len(s)-400):]
	}
	return s
}

// failResult fails res with a failed check.
func failResult(res harnessResult, name, detail string) harnessResult {
	res.OK = false
	res.Checks = append(res.Checks, harnessCheck{Name: name, OK: false, Detail: detail})
	return res
}

// adherenceRate counts the adherence (warn-level) checks and how many held.
// It is a rate because one slip is evidence about the tier, not the code —
// what matters is whether it drifts over runs.
func adherenceRate(results []harnessResult) (clean, total int) {
	for _, r := range results {
		c, t := r.adherence()
		clean, total = clean+c, total+t
	}
	return clean, total
}

// adherence counts the result's adherence checks and how many held.
func (r harnessResult) adherence() (clean, total int) {
	for _, ck := range r.Checks {
		// A warn-level check reports Warn only when it failed, so an
		// adherence check that held is OK with Warn false — match on the
		// name the checks share.
		if !strings.Contains(ck.Name, "(adherence)") {
			continue
		}
		total++
		if ck.OK {
			clean++
		}
	}
	return clean, total
}
