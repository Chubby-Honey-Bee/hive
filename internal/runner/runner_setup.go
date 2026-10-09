package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// prepare runs Run's setup steps that can refuse, before the run row
// exists: it checks that auto-commit can run, parses the workflow, builds
// the run default's backend and runs the preflights.
func (s *runSetup) prepare(ctx context.Context) error {
	var err error
	// Before bootstrapWorkflow, so a refusal (a dirty tree) leaves no
	// workflow_runs row behind in status running for --resume to find.
	if s.gc, err = committerFor(s.cfg, s.logf); err != nil {
		return err
	}
	// Also before bootstrapWorkflow, for the same reason: a provider the
	// allowlist refuses, or a backend that cannot be built, would leave the
	// run row in status running with nothing to finish it.
	s.parsedDefn, _ = workflow.LoadYAML(s.cfg.WorkflowYAML)
	if s.backend, err = runBackend(s.cfg, s.parsedDefn, s.logf); err != nil {
		return err
	}
	return s.preflight(ctx)
}

// runBackend is the run default's backend (resolveLLMBackend). A workflow no
// node of which calls a model needs no backend, so none is built, and no API
// key is asked for.
func runBackend(cfg Config, parsedDefn map[string]any, logf func(string, ...any)) (LLMBackend, error) {
	if cfg.Backend == nil && parsedDefn != nil && !callsAModel(parsedDefn) {
		logf("llm backend: none (no node calls a model)")
		return nil, nil
	}
	return resolveLLMBackend(cfg, logf)
}

// preflight refuses a run its workflow cannot make: a node provider that
// is unknown or the allowlist bars (PreflightWorkflowProviders), a
// temperature an Anthropic node cannot take (PreflightSampling), a call
// its routing profile does not allow (preflightRouting), or a model an
// OpenAI-compatible endpoint does not serve (preflightModels).
func (s *runSetup) preflight(ctx context.Context) error {
	if err := PreflightWorkflowProviders(s.parsedDefn); err != nil {
		return err
	}
	if err := PreflightSampling(s.cfg, s.parsedDefn); err != nil {
		return err
	}
	if err := s.preflightRouting(); err != nil {
		return err
	}
	return s.preflightModels(ctx)
}

// preflightRouting runs before any endpoint is asked anything: every run
// that calls a model logs where each call goes, and a profile refuses a
// call off this machine on a role it names no evidence for
// (PreflightLocality) and a node provider that cannot serve it
// (preflightProfileBackends). A resume dispatches the workflow its run row
// stores, so that is the one routed.
func (s *runSetup) preflightRouting() error {
	routingDefn, err := s.routingDefinition()
	if err != nil {
		return err
	}
	if err := s.reportRouting(routingDefn); err != nil {
		return err
	}
	if s.profile == nil {
		return nil
	}
	return preflightProfileBackends(s.cfg, routingDefn)
}

// routingDefinition is the workflow the run dispatches: the one a resumed
// run's row stores (resumedDefinition), else the workflow file's.
func (s *runSetup) routingDefinition() (map[string]any, error) {
	if s.cfg.ResumeRunID > 0 {
		return resumedDefinition(s.store, s.cfg, s.profile)
	}
	return s.parsedDefn, nil
}

// reportRouting logs where each call of the workflow goes (Routing,
// RoutingReport), and refuses a call the profile does not allow off this
// machine (PreflightLocality).
func (s *runSetup) reportRouting(routingDefn map[string]any) error {
	if !s.logsRouting(routingDefn) {
		return nil
	}
	lines := Routing(s.cfg, routingDefn)
	for _, l := range RoutingReport(s.profile, lines, routingDefn) {
		s.logf("routing: %s", l)
	}
	return PreflightLocality(s.profile, lines)
}

// logsRouting reports whether the run logs its routing: a run under a
// profile, or one whose backend Run built rather than a test injected.
func (s *runSetup) logsRouting(routingDefn map[string]any) bool {
	return routingDefn != nil && (s.profile != nil || s.backend != nil && s.cfg.Backend == nil)
}

// preflightModels runs after the allowlist checks, so no endpoint the
// allowlist refuses is asked anything, and before any model call: a model
// the OpenAI-compatible endpoint does not serve would otherwise surface as
// an HTTP 404 on the first node that names it (PreflightEndpointModels). A
// dry run asks nothing.
func (s *runSetup) preflightModels(ctx context.Context) error {
	if s.cfg.DryRun {
		return nil
	}
	checks, err := PreflightEndpointModels(ctx, s.cfg, s.parsedDefn)
	if err != nil {
		return err
	}
	s.logModelChecks(checks)
	return nil
}

// logModelChecks logs each model check, and that the server chooses the
// sampling when a node's endpoint is sent none.
func (s *runSetup) logModelChecks(checks []*ModelCheck) {
	for _, check := range checks {
		s.logf("model preflight: %s", check.Summary())
	}
	if serverChoosesSampling(checks) && samplingUnset(s.cfg) {
		s.logf("sampling: no temperature or top_p is sent, so the server chooses; Ollama's OpenAI endpoint then uses 1.0 for both, not the model's own values (--temperature, --top-p)")
	}
}

// serverChoosesSampling reports whether a check is of an endpoint a node
// runs on (Unused empty), whose server chooses the sampling a run sends
// none of.
func serverChoosesSampling(checks []*ModelCheck) bool {
	for _, check := range checks {
		if check.Unused == "" {
			return true
		}
	}
	return false
}

// samplingUnset reports whether the run sends no sampling: no
// --temperature, --top-p or --deterministic.
func samplingUnset(cfg Config) bool {
	return cfg.Temperature == nil && cfg.TopP == nil && !cfg.Deterministic
}

// applyRunProfile applies the run's profile, cfg.Profile else
// HIVE_PROFILE, to the run's workflow: it resolves the profile, routes
// the workflow file (ApplyProfile) into a file of the same name in a
// temporary directory, which cleanup removes, and points the returned
// Config, whose Profile it sets, at it. So every caller of Run honours the
// variable, chb-mcp and chb replicate among them. The profile's
// provider becomes the run default unless cfg.Provider is set. With no
// profile it returns cfg as it is and a nil profile.
func applyRunProfile(cfg Config) (*Profile, Config, func(), error) {
	noop := func() {}
	cfg.Profile = runProfileName(cfg.Profile)
	if cfg.Profile == "" {
		return nil, cfg, noop, nil
	}
	p, err := ResolveProfile(cfg.Profile)
	if err != nil {
		return nil, cfg, noop, err
	}
	path, cleanup, err := routedWorkflowFile(cfg.WorkflowYAML, p)
	if err != nil {
		return nil, cfg, noop, err
	}
	return p, profiledConfig(cfg, p, path), cleanup, nil
}

// runProfileName is the name of the run's routing profile: name, else
// HIVE_PROFILE's.
func runProfileName(name string) string {
	if name != "" {
		return name
	}
	return strings.TrimSpace(os.Getenv("HIVE_PROFILE"))
}

// routedWorkflowFile routes the workflow file at path through profile p
// (ApplyProfile) into a file of the same name in a temporary directory, and
// returns its path and the cleanup that removes the directory.
func routedWorkflowFile(path string, p *Profile) (string, func(), error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", nil, fmt.Errorf("read workflow: %w", err)
	}
	routed, err := ApplyProfile(string(raw), p)
	if err != nil {
		return "", nil, err
	}
	return writeTempWorkflow(filepath.Base(path), routed)
}

// writeTempWorkflow writes text to a file named base in a new temporary
// directory, and returns its path and the cleanup that removes the
// directory.
func writeTempWorkflow(base, text string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "chb-profile-")
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, base)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	return path, func() { _ = os.RemoveAll(dir) }, nil
}

// profiledConfig is cfg run under profile p: its workflow is the file p
// routes (path), and p's provider is the run default unless cfg.Provider
// is set or a backend is injected.
func profiledConfig(cfg Config, p *Profile, path string) Config {
	cfg.WorkflowYAML = path
	if cfg.Provider == "" && cfg.Backend == nil {
		cfg.Provider = string(p.Provider)
	}
	return cfg
}

// resumedDefinition is the workflow a resumed run dispatches: the one its
// row stores, not the file on disk. Under a profile it must be the file as
// the profile routes it, or the run would dispatch a routing the profile
// never checked, such as a run started without one on cloud tiers.
func resumedDefinition(store *db.Store, cfg Config, profile *Profile) (map[string]any, error) {
	run, err := store.Workflows().GetWorkflowRun(cfg.ResumeRunID)
	if err != nil {
		return nil, fmt.Errorf("resume run %d: %w", cfg.ResumeRunID, err)
	}
	if profile != nil {
		if err := checkResumeRouting(run.DefinitionYAML, cfg, profile); err != nil {
			return nil, err
		}
	}
	return workflow.LoadYAMLString(run.DefinitionYAML)
}

// checkResumeRouting refuses to resume, under profile, a run whose stored
// workflow is not the workflow file as the profile routes it
// (cfg.WorkflowYAML).
func checkResumeRouting(stored string, cfg Config, profile *Profile) error {
	routed, err := os.ReadFile(cfg.WorkflowYAML)
	if err != nil {
		return fmt.Errorf("read workflow: %w", err)
	}
	if stored != string(routed) {
		return fmt.Errorf("run %d did not start from this workflow as routing profile %s routes it, and a resume dispatches the workflow its run stores: resume it without the profile (--profile, HIVE_PROFILE), whose routing the run log prints, or start a new run", cfg.ResumeRunID, profile.Name)
	}
	return nil
}

// applyConfigDefaults fills in the defaults Run depends on. Returns by
// value so callers see the canonical config.
func applyConfigDefaults(cfg Config) Config {
	if cfg.Log == nil {
		cfg.Log = os.Stderr
	}
	if cfg.MaxIterations <= 0 {
		// 500 is the safety cap, not a target. It bounds runaway loops while
		// being well above the largest workflow ever observed in production
		// (~80 dispatches for a complex multi-wave research). Hitting this is
		// a bug-or-overscoped-workflow signal, not normal completion.
		cfg.MaxIterations = 500
	}
	if cfg.ProjectDir == "" {
		cfg.ProjectDir, _ = os.Getwd()
	}
	cfg.DBPath = runDBPath(cfg.DBPath)
	return cfg
}

// runDBPath is the run's database path, path else HIVE_DB_PATH, made
// absolute. Agents and command nodes run in ProjectDir, where a relative
// database path names another file than the one this run opened. Made
// absolute once here, every node's HIVE_DB_PATH names the same
// database.
func runDBPath(path string) string {
	if path == "" {
		path = os.Getenv("HIVE_DB_PATH")
	}
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// committerFor returns the auto-commit committer for a run, or nil under
// --dry-run: a dry run skips file mutations, and creating and checking out
// the branch is one.
func committerFor(cfg Config, logf func(string, ...any)) (*GitCommitter, error) {
	if cfg.DryRun {
		if cfg.Branch != "" {
			logf("dry-run: auto-commit branch %s not created", cfg.Branch)
		}
		return nil, nil
	}
	return prepareGitCommitter(cfg, logf)
}

// prepareGitCommitter checks that auto-commit can run. The committer is
// disabled with no branch or outside a git working tree, and a dirty tree
// is an error. It leaves the branch alone: enterBranch switches to it once
// the run exists.
func prepareGitCommitter(cfg Config, logf func(string, ...any)) (*GitCommitter, error) {
	gc := NewGitCommitter(cfg.ProjectDir, cfg.Branch)
	if !usableCommitter(gc, logf) {
		return gc, nil
	}
	// A dirty tree is a hard stop, not a downgrade: silently continuing
	// without auto-commit would still leave --auto-pr expecting a branch.
	if cfg.AllowDirty {
		return gc, nil
	}
	if err := gc.EnsureCleanTree(); err != nil {
		return nil, err
	}
	return gc, nil
}

// usableCommitter reports whether gc can commit: it is enabled, which takes
// a branch, and its project lies inside a git working tree. Not a git repo
// at all degrades as before: it logs, disables gc and the run goes on
// without commits. A project in a subdirectory of a repository is inside
// one.
func usableCommitter(gc *GitCommitter, logf func(string, ...any)) bool {
	if !gc.Enabled {
		return false
	}
	if !gc.inWorkTree() {
		logf("git: not a git repo: %s — continuing without auto-commit", gc.ProjectDir)
		gc.Enabled = false
		return false
	}
	return true
}

// callsAModel reports whether any node of the workflow calls a model
// (nodeCallsAModel).
func callsAModel(defn map[string]any) bool {
	nodes, _ := defn["nodes"].(map[string]any)
	for _, raw := range nodes {
		node, _ := raw.(map[string]any)
		if nodeCallsAModel(node) {
			return true
		}
	}
	return false
}

// nodeCallsAModel reports whether a workflow node calls a model: an agent
// or parallel_fan node, a node with no type included, that is not the
// dreamer. Command, decision and human_review nodes call none.
func nodeCallsAModel(node map[string]any) bool {
	t, _ := node["type"].(string)
	if t != "" && t != "agent" && t != "parallel_fan" {
		return false
	}
	a, _ := node["archetype"].(string)
	return !strings.EqualFold(strings.TrimSpace(a), "dreamer")
}

// resolveLLMBackend picks the backend per env+config. Test-injected
// backends (cfg.Backend) take precedence so smoke tests can stub the LLM.
func resolveLLMBackend(cfg Config, logf func(string, ...any)) (LLMBackend, error) {
	if cfg.Backend != nil {
		logf("llm backend: injected")
		return cfg.Backend, nil
	}
	backend, kind, err := buildRunBackend(cfg)
	if err != nil {
		return nil, err
	}
	logf("llm backend: %s", backendLabel(kind))
	return backend, nil
}

// buildRunBackend builds the backend of the run's provider kind
// (ResolveBackendKind) once the allowlist admits it. A dry run goes on
// without one that cannot be built.
func buildRunBackend(cfg Config) (LLMBackend, BackendKind, error) {
	kind := ResolveBackendKind(cfg)
	if err := EnforceProviderAllowlist(kind); err != nil {
		return nil, kind, err
	}
	backend, err := NewBackend(kind, cfg)
	if err != nil && !cfg.DryRun {
		return nil, kind, backendInitError(kind, err)
	}
	return backend, kind, nil
}

// backendInitError is the error a backend that cannot be built stops the
// run with: errNoProvider as it is, anything else naming the kind.
func backendInitError(kind BackendKind, err error) error {
	if errors.Is(err, errNoProvider) {
		return err
	}
	return fmt.Errorf("init backend %s: %w", kind, err)
}

// backendLabel names a provider kind for the run log: with the endpoint
// its calls go to where chb chooses it (endpointFor), and the bound on the
// calls in flight there when one is set (HIVE_MAX_PARALLEL_ENDPOINT).
func backendLabel(kind BackendKind) string {
	base := endpointFor(kind)
	if base == "" {
		return string(kind)
	}
	if n := endpointLimit(base); n > 0 {
		return fmt.Sprintf("%s (%s, at most %d call(s) in flight: HIVE_MAX_PARALLEL_ENDPOINT)", kind, base, n)
	}
	return fmt.Sprintf("%s (%s)", kind, base)
}
