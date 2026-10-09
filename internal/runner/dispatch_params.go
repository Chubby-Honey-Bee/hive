package runner

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// dispatchParams is what one call for a node needs beyond its prompt, and
// what is recorded against it.
type dispatchParams struct {
	backend  LLMBackend
	kind     BackendKind // the serving provider; "" for an injected backend
	model    string      // the model the provider is sent
	maxOut   int64
	temp     *float64
	topP     *float64
	seed     *int64
	provider string // the provider label recorded on the node row
	baseURL  string // the endpoint recorded on the node row; "" where chb does not choose it
}

// unbuiltBackend serves a node whose provider's backend could not be built
// under a routing profile: every call fails with why.
type unbuiltBackend struct{ err error }

// Run fails the call with why the backend could not be built.
func (b unbuiltBackend) Run(context.Context, RunRequest) (*RunResult, error) { return nil, b.err }

// resolveDispatchParams resolves everything a single dispatch needs that
// depends on the node + run config: the backend (honoring a per-node
// `provider:` override), the concrete model (tier-resolved under the budget
// mode, then pinned to its canonical SDK id in the DB), the per-turn output
// cap, and the sampling fields. Side-effect-free except for the
// best-effort resolved-model pin, which is logged-but-not-fatal on failure.
func (rc *runtimeContext) resolveDispatchParams(node workflow.DispatchNode) (p dispatchParams) {
	backend, provider, overrideKind := rc.dispatchBackend(node)
	kind := rc.dispatchKind(overrideKind)
	resolvedModel := rc.dispatchModel(node, kind)

	// Record the canonical model ID of this dispatch. Every dispatch
	// writes it, so it names the latest; nothing re-dispatches from it.
	// resolveAliasForPricing canonicalises bare aliases (e.g. "haiku" →
	// "claude-haiku-4-5") so the stored value is the models config's id,
	// not an alias that could re-resolve differently if the config changes.
	rc.pinDispatchModel(node, resolveAliasForPricing(resolvedModel))

	temp, topP := rc.dispatchSampling()
	return dispatchParams{
		backend:  backend,
		kind:     kind,
		model:    resolvedModel,
		maxOut:   rc.dispatchMaxOutput(resolvedModel),
		temp:     temp,
		topP:     topP,
		seed:     rc.cfg.Seed,
		provider: provider,
		baseURL:  endpointFor(kind),
	}
}

// dispatchBackend is the backend that serves the node, the provider label
// recorded against it, and the provider kind its `provider:` override
// names ("" without one): the run default (rc.backend), unless the node
// overrides it with a provider the allowlist admits, which serves the node
// on a one-shot backend (dispatchOverrideBackend).
func (rc *runtimeContext) dispatchBackend(node workflow.DispatchNode) (LLMBackend, string, BackendKind) {
	if node.Provider == "" || canonicalKind(node.Provider) == "" {
		return rc.backend, providerLabel(rc.cfg), ""
	}
	// The allowlist gates the run default in resolveLLMBackend; this
	// override path builds its own backend, so it needs the same gate
	// or a workflow YAML could route prompts past the policy.
	// PreflightWorkflowProviders refuses such a run up front — this is
	// the second line of defence for a node the preflight cannot see.
	if err := EnforceProviderAllowlist(canonicalKind(node.Provider)); err != nil {
		rc.logf("per-node provider %q refused (%v) — using the run default", node.Provider, err)
		return rc.backend, providerLabel(rc.cfg), ""
	}
	return rc.dispatchOverrideBackend(node, canonicalKind(node.Provider))
}

// dispatchOverrideBackend builds the backend of the provider kind a node's
// `provider:` names. The override changes which provider actually serves the
// node, so it also changes what is recorded against it. A backend that
// cannot be built falls back to the run default, except under a routing
// profile.
func (rc *runtimeContext) dispatchOverrideBackend(node workflow.DispatchNode, kind BackendKind) (LLMBackend, string, BackendKind) {
	nodeCfg := rc.cfg
	nodeCfg.Provider = node.Provider
	alt, err := NewBackend(kind, nodeCfg)
	if err == nil {
		return alt, providerLabel(nodeCfg), kind
	}
	if rc.cfg.Profile != "" {
		// Under a routing profile the node runs where it is routed
		// or fails: the run default may send its data where the
		// profile does not route its role. preflightProfileBackends
		// refuses such a run before it starts; this holds should a
		// backend stop building mid-run.
		rc.logf("per-node provider %q init failed (%v) — the node fails: under routing profile %s it does not fall back to the run default", node.Provider, err, rc.cfg.Profile)
		return unbuiltBackend{fmt.Errorf("provider %s cannot serve node %s: %w", node.Provider, node.Node, err)}, providerLabel(nodeCfg), kind
	}
	rc.logf("per-node provider %q init failed (%v) — falling back to run default", node.Provider, err)
	return rc.backend, providerLabel(rc.cfg), ""
}

// dispatchKind is the provider kind that serves a node: its override, else
// the run default's.
//
// Each backend resolves an empty or aliased model to a concrete id of its
// own at request time, out of the runner's sight, so the model is resolved
// here instead, once, where every later step can see the answer: a node that
// overrides the provider without naming a model is costed, capped and
// audited against the model that serves it, not the run default's. The run
// default needs the same: a `model: sonnet` node on an openai run is served
// by gpt-5.1, and must be pinned, capped and costed as gpt-5.1. An injected
// backend (cfg.Backend) has no provider kind to resolve for.
func (rc *runtimeContext) dispatchKind(override BackendKind) BackendKind {
	if override == "" && rc.cfg.Backend == nil {
		return ResolveBackendKind(rc.cfg)
	}
	return override
}

// dispatchModel is the model a node's call is sent on the provider kind
// (nodeModel), logging a tier it resolved and a reasoning level the
// provider ignores.
func (rc *runtimeContext) dispatchModel(node workflow.DispatchNode, kind BackendKind) string {
	model := nodeModel(node.Model, node.Tier, rc.cfg.BudgetMode, kind)
	if dispatchTierResolved(node, model) {
		rc.logf("tier %q → model %q (budget=%s)", node.Tier, model, rc.cfg.BudgetMode)
	}
	if dispatchReasoningIgnored(node, kind) {
		rc.logf("node %s: reasoning %s has no effect on provider %s (only the OpenAI-compatible backend sends reasoning_effort)", node.Node, node.Reasoning, kind)
	}
	return model
}

// dispatchTierResolved reports whether a node's model came from its tier.
func dispatchTierResolved(node workflow.DispatchNode, model string) bool {
	return node.Model == "" && node.Tier != "" && model != ""
}

// dispatchReasoningIgnored reports whether a node sets a reasoning level
// its provider kind does not send: only the OpenAI-compatible backend
// sends one.
func dispatchReasoningIgnored(node workflow.DispatchNode, kind BackendKind) bool {
	return node.Reasoning != "" && kind != "" && !openAICompatible(kind)
}

// pinDispatchModel records pinned, a canonical model id, as the node's
// resolved_model; nothing for "". Best-effort: a write failure is logged
// but must not abort the dispatch.
func (rc *runtimeContext) pinDispatchModel(node workflow.DispatchNode, pinned string) {
	if pinned == "" {
		return
	}
	if err := rc.store.Workflows().UpdateNodeResolvedModel(rc.runID, node.Node, pinned); err != nil {
		rc.logf("pin resolved_model (run %d node %s): %v", rc.runID, node.Node, err)
	}
}

// dispatchSampling is the sampling fields both single-node and fan-out
// paths send: --deterministic sends temperature 0. The CLI refuses
// --temperature or --top-p together with --deterministic.
func (rc *runtimeContext) dispatchSampling() (temp, topP *float64) {
	if rc.cfg.Deterministic {
		return ptrFloat64(0), rc.cfg.TopP
	}
	return rc.cfg.Temperature, rc.cfg.TopP
}

// dispatchMaxOutput is the per-turn output cap a call on model is sent.
// The CLI/env override (runner.Config.MaxOutputTokens) wins over the
// per-model lookup in models.yaml; the lookup itself falls back to 8192
// for unconfigured models.
func (rc *runtimeContext) dispatchMaxOutput(model string) int64 {
	if rc.cfg.MaxOutputTokens > 0 {
		return rc.cfg.MaxOutputTokens
	}
	return models.MaxOutputFor(model)
}

// nodeModel is the model a node's call is sent: its `model:`, else its
// `tier:` under the budget mode (tierModel), resolved by the provider that
// serves it. kind "" (an injected backend) resolves nothing past the tier.
func nodeModel(model, tier string, mode BudgetMode, kind BackendKind) string {
	if model == "" && tier != "" {
		model = tierModel(kind, tier, mode)
	}
	if kind != "" {
		model = resolveModelForProvider(kind, model)
	}
	return model
}

// endpointBaseURLs gives the base URL of each provider kind whose host chb
// chooses.
var endpointBaseURLs = map[BackendKind]func() string{
	BackendOpenAI:    openAIBaseURL,
	BackendLocal:     localBaseURL,
	BackendGemini:    geminiBaseURL,
	BackendAnthropic: anthropicBaseURL,
}

// endpointFor is the base URL a provider's calls go to, for the providers
// whose host chb chooses, without any credentials in it. The CLI backends'
// hosts are the CLIs' own business, so they, and an injected backend, get "".
func endpointFor(kind BackendKind) string {
	base, ok := endpointBaseURLs[kind]
	if !ok {
		return ""
	}
	return withoutUserinfo(base())
}

// anthropicBaseURL is ANTHROPIC_BASE_URL, which anthropic-sdk-go reads
// itself, else the API's own host.
func anthropicBaseURL() string {
	if base := strings.TrimRight(os.Getenv("ANTHROPIC_BASE_URL"), "/"); base != "" {
		return base
	}
	return "https://api.anthropic.com"
}

// resolveModelForProvider names the concrete model a provider will use for a
// given alias, including the empty alias — each backend has its own default
// and resolves it internally at request time. The claude CLI gets the alias
// as written, since it resolves aliases itself; the pin, cap and cost resolve
// it through the models config.
func resolveModelForProvider(kind BackendKind, alias string) string {
	switch kind {
	case BackendOpenAI, BackendLocal:
		return ResolveOpenAIModel(alias)
	case BackendGemini, BackendGeminiCLI:
		return ResolveGeminiModel(alias)
	case BackendAnthropic:
		return string(ResolveModelAlias(alias))
	}
	return alias
}
