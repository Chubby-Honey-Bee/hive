package cli

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/runner"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
	"github.com/spf13/cobra"
)

// newPreflightCmd wires `chb preflight <workflow.yaml>`. It is the
// project-launch gate for autonomous runs: every check must pass before
// agent-run is launched on the same YAML, otherwise the run risks
// stalling for reasons the operator could have caught earlier.
//
// `chb workflow validate` is a pure YAML linter; preflight is the broader
// pre-launch checklist: provider auth, target dir hygiene, accept: coverage,
// prompt language, pricing freshness.
//
// Exit codes:
//
//	0 — every required check passed
//	1 — at least one ✗ (required check failed)
func newPreflightCmd() *cobra.Command {
	var o preflightOptions
	cmd := &cobra.Command{
		Use:   "preflight <workflow.yaml>",
		Short: "Verify a workflow is ready for autonomous launch",
		Long: `preflight runs a cross-cutting checklist over a workflow file
before launching agent-run on it. Each check emits ✓ / ✗ / ⚠.

Checks:
  ✓/✗  YAML parses (delegated to workflow.Validate)
  ✓/✗  Every agent / parallel_fan node has accept:
  ✓/✗  Every output an accept: judges is named in its node's prompt
  ✓/✗  Every parallel_fan's fan_source is an input or declared upstream,
       and its prompt holds its fan_placeholder
  ⚠     Prompt lacks negative-evidence language (advisory only)
  ✓     Every command node's argv, listed (only when there are some)
  ✓     Every calibrate node, listed (only when there are some)
  ✓/✗  At least one provider authenticated
  ✓/✗  Per-node provider: overrides resolve cleanly
  ✓/⚠/✗ Each route is listed with its model, provider, endpoint and
       reasoning, ⚠ for one that sends data off this machine. With --profile
       (or HIVE_PROFILE), every check reads the workflow as the profile
       routes it, and ✗ when a call would leave the machine on a role the
       profile has no cloud route for; no endpoint is then asked anything
  ✓/✗  With OPENAI_BASE_URL set, or a node on the local provider, each
       endpoint serves every model its nodes and repairs are sent,
       and on Ollama each such model takes its node's reasoning level
  ⚠     ... or the endpoint lists no models, or no node is sent to it
  ✓/⚠  At each such endpoint, one probe per model and reasoning level that a
       node with an output_schema sends. It measures the server: ✓ when it
       constrains a call that carries the schema, in the declared key order;
       ⚠ when it sorts the keys, or does not constrain, so its nodes are
       checked only after the call. A node that keeps any tool carries its
       schema only on the finalize call after an answer that breaks it
  ✓/✗  Required PATH binaries: git, chb
  ✓/✗  --target-dir empty + git-initialized (or --init-target to auto-init)
  ⚠     fixtures/cli-behavior.jsonl missing, in a HIVE source checkout
       (one whose go.mod names the module); said nowhere else
  ⚠     Pricing-table date-stamp > 60 days old

Exits 0 only when no ✗ check fails.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.run(cmd, args[0])
		},
	}
	cmd.Flags().StringVar(&o.targetDir, "target-dir", "", "directory the workflow will write into (must be empty + git-initialized)")
	cmd.Flags().BoolVar(&o.initTarget, "init-target", false, "auto-create + git-init the target dir if missing")
	// Provider auth is a property of the machine about to launch the run, not
	// of the workflow. CI checks that every shipped workflow is well-formed on
	// a runner with no credentials at all, which is a different question.
	cmd.Flags().BoolVar(&o.skipProviderCheck, "skip-provider-check", false,
		"don't require a resolvable provider (for checking workflow well-formedness on a machine that won't run it)")
	cmd.Flags().StringVar(&o.providerArg, "provider", "", "expect this provider to be the resolved one (anthropic|gemini|openai|claude-cli|gemini-cli|local)")
	cmd.Flags().StringVar(&o.budgetMode, "budget-mode", "", "resolve `tier:` nodes as agent-run --budget-mode would: premium|standard|cheap|free (default: HIVE_BUDGET_MODE, else standard)")
	cmd.Flags().StringVar(&o.profileFlag, "profile", "", profileUsage)
	return cmd
}

// preflightOptions holds the flags of chb preflight.
type preflightOptions struct {
	targetDir         string
	initTarget        bool
	providerArg       string
	budgetMode        string
	skipProviderCheck bool
	profileFlag       string
}

// run checks the workflow name names, on disk or else the copy the binary
// carries.
func (o *preflightOptions) run(cmd *cobra.Command, name string) error {
	yamlPath, shipped, cleanup, err := workflow.ResolveFile(name)
	if err != nil {
		return err
	}
	defer cleanup()
	if shipped {
		fmt.Fprintf(cmd.ErrOrStderr(), "workflow %s: not on disk; checking the copy the binary carries\n", name)
	}
	return o.check(cmd, yamlPath)
}

// check runs every check on the workflow at yamlPath and prints the report,
// failing when a required check failed.
func (o *preflightOptions) check(cmd *cobra.Command, yamlPath string) error {
	profile, yamlPath, cleanup, err := o.applyProfile(yamlPath)
	if err != nil {
		return err
	}
	defer cleanup()
	r := newPreflightReport()
	o.runChecks(cmd.Context(), r, profile, yamlPath)
	r.print(cmd.OutOrStderr())
	if r.hasFailures() {
		return fmt.Errorf("preflight failed: %d check(s) failed", r.failures())
	}
	return nil
}

// applyProfile resolves the routing profile. Under a profile, every check
// reads the workflow as the profile routes it, as agent-run would run it:
// applyProfile returns the path of that copy, which cleanup removes, and
// the profile's provider stands in for an empty --provider.
func (o *preflightOptions) applyProfile(yamlPath string) (*runner.Profile, string, func(), error) {
	profileName, err := useProfile(o.profileFlag)
	if err != nil {
		return nil, "", nil, err
	}
	if profileName == "" {
		return nil, yamlPath, func() {}, nil
	}
	profile, routedPath, cleanup, err := routedWorkflow(profileName, yamlPath)
	if err != nil {
		return nil, "", nil, err
	}
	o.providerArg = cmp.Or(o.providerArg, string(profile.Provider))
	return profile, routedPath, cleanup, nil
}

// runChecks adds the result of every check to r, in the order the report
// prints them.
func (o *preflightOptions) runChecks(ctx context.Context, r *preflightReport, profile *runner.Profile, yamlPath string) {
	r.checkYAML(yamlPath)
	defn := r.loadDefinition(yamlPath)

	r.checkAcceptPresence(defn)
	r.checkAcceptAsksForItsOutputs(defn)
	r.checkFanSources(defn)
	r.checkPromptLanguage(defn)
	r.listCommandNodes(defn)
	r.listCalibrateNodes(defn)
	// The routing asks no endpoint anything, so it is checked on a
	// machine that will not run the workflow too. When the profile
	// refuses it, no endpoint is asked anything, as agent-run asks
	// none.
	routed := r.checkRouting(profile, o.providerArg, o.budgetMode, defn)
	o.checkProvider(ctx, r, routed, defn)
	r.checkPathBinaries()
	r.checkTargetDir(o.targetDir, o.initTarget)
	r.checkBehaviorFixture()
	r.checkPricingFreshness()
}

// checkProvider checks provider auth and then, when the routing lets them
// be asked, the endpoints. --skip-provider-check skips both.
func (o *preflightOptions) checkProvider(ctx context.Context, r *preflightReport, routed bool, defn map[string]any) {
	if o.skipProviderCheck {
		r.pass("provider auth", "skipped (--skip-provider-check)")
		return
	}
	r.checkProviderAuth(o.providerArg, defn)
	if !routed {
		r.warn("endpoint models", "not asked: the routing profile refuses a call off this machine")
		return
	}
	r.checkEndpointModels(ctx, o.providerArg, o.budgetMode, defn)
	r.checkConstraintProbes(ctx, o.providerArg, o.budgetMode, defn)
}

// routedWorkflow resolves the named profile and writes the workflow at path,
// as the profile routes it, to a file of the same name in a temporary
// directory, which cleanup removes.
func routedWorkflow(name, path string) (*runner.Profile, string, func(), error) {
	p, err := runner.ResolveProfile(name)
	if err != nil {
		return nil, "", nil, err
	}
	routed, err := routeWorkflowFile(path, p)
	if err != nil {
		return nil, "", nil, err
	}
	out, cleanup, err := writePreflightTemp(filepath.Base(path), routed)
	if err != nil {
		return nil, "", nil, err
	}
	return p, out, cleanup, nil
}

// routeWorkflowFile is the text of the workflow at path as profile p routes
// it.
func routeWorkflowFile(path string, p *runner.Profile) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return runner.ApplyProfile(string(raw), p)
}

// writePreflightTemp writes content to a file called name in a new
// temporary directory, which cleanup removes.
func writePreflightTemp(name, content string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "chb-preflight-")
	if err != nil {
		return "", nil, err
	}
	out := filepath.Join(dir, name)
	if err := os.WriteFile(out, []byte(content), 0o644); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	return out, func() { _ = os.RemoveAll(dir) }, nil
}

// checkRouting prints where every model call goes (one ✓ line per route,
// runner.RoutingReport; ⚠ for each that leaves the machine), under a
// profile or not, for a workflow that calls a model. Under a profile it
// fails, as agent-run refuses, a call off this machine on a role the profile
// has no cloud route for. It reports whether the endpoints may be asked:
// false when it refused, so an endpoint off the machine is asked nothing.
func (r *preflightReport) checkRouting(p *runner.Profile, provider, budgetMode string, defn map[string]any) bool {
	lines, ok := r.routes(p, provider, budgetMode, defn)
	if !ok {
		return true
	}
	r.reportRoutes(runner.RoutingReport(p, lines, defn))
	if err := runner.PreflightLocality(p, lines); err != nil {
		r.fail("routing stays on this machine", err.Error())
		return false
	}
	return true
}

// routes are the workflow's routes, or false when there is nothing to
// report: no workflow, a budget mode that does not parse (which fails), or
// no model call and no profile.
func (r *preflightReport) routes(p *runner.Profile, provider, budgetMode string, defn map[string]any) ([]runner.RouteLine, bool) {
	if defn == nil {
		return nil, false
	}
	bm, err := runner.ResolveBudgetMode(budgetMode)
	if err != nil {
		r.fail("routing", err.Error())
		return nil, false
	}
	lines := runner.Routing(runner.Config{Provider: provider, BudgetMode: bm}, defn)
	return lines, len(lines) > 0 || p != nil
}

// reportRoutes adds one line per route: ✓, or ⚠ for one that sends data
// off this machine.
func (r *preflightReport) reportRoutes(lines []string) {
	for _, l := range lines {
		if routeLeavesMachine(l) {
			r.warn("routing", l)
			continue
		}
		r.pass("routing", l)
	}
}

// routeLeavesMachine reports whether a routing report line names calls that
// leave this machine.
func routeLeavesMachine(line string) bool {
	return strings.HasPrefix(line, "off this machine: ") && !strings.HasPrefix(line, "off this machine: nothing")
}

// checkEndpointModels asks an OPENAI_BASE_URL endpoint which models it serves
// and fails a workflow that sends one it does not: the run would get an HTTP
// 404 on that node. agent-run makes the same check before its first call.
func (r *preflightReport) checkEndpointModels(ctx context.Context, provider, budgetMode string, defn map[string]any) {
	checks, err := endpointModelChecks(ctx, provider, budgetMode, defn)
	if err != nil {
		r.fail("endpoint models", err.Error())
		return
	}
	// None: no OPENAI_BASE_URL and no local node, or nothing to report.
	for _, check := range checks {
		r.reportEndpointModels(check)
	}
}

// endpointModelChecks asks each endpoint the workflow sends calls to which
// models it serves.
func endpointModelChecks(ctx context.Context, provider, budgetMode string, defn map[string]any) ([]*runner.ModelCheck, error) {
	bm, err := runner.ResolveBudgetMode(budgetMode)
	if err != nil {
		return nil, err
	}
	return runner.PreflightEndpointModels(ctx, runner.Config{Provider: provider, BudgetMode: bm}, defn)
}

// reportEndpointModels passes an endpoint that serves every model and
// reasoning level its nodes send. It warns for one set but unused by any
// node, one that lists no models, or a node whose reasoning level will not
// be sent.
func (r *preflightReport) reportEndpointModels(check *runner.ModelCheck) {
	if check.Listed && len(check.ReasoningNotSent) == 0 {
		r.pass("endpoint models", check.Summary())
		return
	}
	r.warn("endpoint models", check.Summary())
}

// checkConstraintProbes runs the constraint probe agent-run runs before its
// first dispatch, and reports each model and reasoning level a schema'd node
// sends. It measures the server, not the nodes: ✓ when the server constrains
// a call that carries the schema and keeps its declared key order; ⚠ when it
// sorts the keys, or does not constrain, or the probe could not tell. A node
// that keeps any tool carries its schema only on the finalize call after an
// answer that breaks it. A probe never fails preflight.
func (r *preflightReport) checkConstraintProbes(ctx context.Context, provider, budgetMode string, defn map[string]any) {
	bm, err := runner.ResolveBudgetMode(budgetMode)
	if err != nil {
		return
	}
	for _, p := range runner.ProbeConstraints(ctx, runner.Config{Provider: provider, BudgetMode: bm}, defn, nil) {
		r.reportConstraintProbe(p)
	}
}

// reportConstraintProbe reports one probe: ✓ when the server constrains a
// call that carries the schema and keeps its declared key order, ⚠ when it
// sorts the keys or does not constrain.
func (r *preflightReport) reportConstraintProbe(p runner.ConstraintProbe) {
	const offered = "; a node that keeps any tool carries its schema only on the finalize call after an answer that breaks it"
	switch {
	case p.Enforced() && p.Order == runner.OrderDeclared:
		r.pass("server constrains output schema", p.Summary()+offered)
	case p.Enforced():
		r.warn("server constrains output schema", p.Summary()+": properties reach decoding in alphabetical order, not the declared one, so a node's reasons can come after its decision"+offered)
	default:
		r.warn("server constrains output schema", p.Summary()+"; these nodes' outputs are checked only after the call")
	}
}
