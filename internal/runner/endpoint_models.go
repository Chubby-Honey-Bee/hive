package runner

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"time"
)

// ModelCheck is what PreflightEndpointModels checked at one endpoint.
type ModelCheck struct {
	// Provider is the provider kind the endpoint serves: openai or local.
	Provider BackendKind
	// BaseURL is the endpoint, without any credentials in it.
	BaseURL string
	// Models are the models the workflow's OpenAI-compatible nodes, and
	// their on_reject repairs, are sent, sorted.
	Models []string
	// Listed is false when the endpoint answered GET /models with 404: it
	// does not list its models, so nothing was checked.
	Listed bool
	// Unused is the run default's provider when OPENAI_BASE_URL is set but
	// no node runs on provider openai; "" otherwise.
	Unused BackendKind
	// ReasoningNotSent names each node whose reasoning level the backend
	// will not send (reasoningToSend) where that changes what the model
	// does: a level other than none to a model the server reports cannot
	// think. Sorted; empty when every such level is sent.
	ReasoningNotSent []string
}

// Summary is one line on what was checked, for the run log and chb
// preflight.
func (c *ModelCheck) Summary() string {
	if c.Unused != "" {
		return c.unusedSummary()
	}
	if c.Listed {
		return c.servedSummary()
	}
	return fmt.Sprintf("%s does not list its models (HTTP 404), so %s not checked%s", c.BaseURL, strings.Join(c.Models, ", "), v1Hint(c.BaseURL))
}

// unusedSummary says OPENAI_BASE_URL is set but no node runs on provider
// openai, and where the run default sends instead.
func (c *ModelCheck) unusedSummary() string {
	if c.Unused == BackendLocal {
		return fmt.Sprintf("OPENAI_BASE_URL is set (%s), but no node runs on provider openai: the run default is local, which sends to HIVE_LOCAL_BASE_URL (%s), and no node sets provider: openai", c.BaseURL, LocalBaseURL())
	}
	return fmt.Sprintf("OPENAI_BASE_URL is set (%s), but no node runs on provider openai: the run default is %s and no node sets provider: openai", c.BaseURL, c.Unused)
}

// servedSummary says the endpoint serves the models checked, and names the
// nodes whose reasoning level is not sent, when there are any.
func (c *ModelCheck) servedSummary() string {
	s := fmt.Sprintf("%s serves %s", c.BaseURL, strings.Join(c.Models, ", "))
	if len(c.ReasoningNotSent) > 0 {
		s += "; reasoning_effort is not sent for " + strings.Join(c.ReasoningNotSent, "; ")
	}
	return s
}

// baseURLVar is the variable that sets kind's endpoint.
func baseURLVar(kind BackendKind) string {
	if kind == BackendLocal {
		return "HIVE_LOCAL_BASE_URL"
	}
	return "OPENAI_BASE_URL"
}

// v1Hint is a note for a base URL without the /v1 suffix that Ollama and LM
// Studio serve their OpenAI-compatible API under.
func v1Hint(base string) string {
	if strings.HasSuffix(base, "/v1") {
		return ""
	}
	return "; an Ollama or LM Studio base URL ends in /v1"
}

// forEachAgentNode calls fn for each node of defn that dispatch sends to a
// backend (agent and parallel_fan, not the dreamer), with the provider kind
// that serves it: its `provider:` when that is a known kind the allowlist
// admits, else runKind.
func forEachAgentNode(defn map[string]any, runKind BackendKind, fn func(name string, node map[string]any, kind BackendKind)) {
	nodes, _ := defn["nodes"].(map[string]any)
	for name, raw := range nodes {
		node, _ := raw.(map[string]any)
		if sentToBackend(node) {
			fn(name, node, nodeProviderKind(node, runKind))
		}
	}
}

// sentToBackend reports whether dispatch sends node to a backend: an agent,
// which a node with no type is, or a parallel_fan, and not the dreamer.
func sentToBackend(node map[string]any) bool {
	t, _ := node["type"].(string)
	a, _ := node["archetype"].(string)
	return slices.Contains([]string{"", "agent", "parallel_fan"}, t) && !strings.EqualFold(strings.TrimSpace(a), "dreamer")
}

// nodeProviderKind is the provider kind that serves node: its `provider:`
// when that is a known kind the allowlist admits, else runKind.
func nodeProviderKind(node map[string]any, runKind BackendKind) BackendKind {
	p, _ := node["provider"].(string)
	if k := canonicalKind(p); k != "" && EnforceProviderAllowlist(k) == nil {
		return k
	}
	return runKind
}

// runDefaultKind is the provider kind of the run default, "" for an injected
// backend or one the allowlist refuses. A refused run default makes no call,
// so nothing is asked of it.
func runDefaultKind(cfg Config) BackendKind {
	if cfg.Backend != nil {
		return ""
	}
	kind := ResolveBackendKind(cfg)
	if EnforceProviderAllowlist(kind) != nil {
		return ""
	}
	return kind
}

// PreflightEndpointModels refuses a workflow, before any model call, when a
// node served by the OpenAI-compatible backend, or that node's on_reject
// repair, is sent a model the endpoint does not serve. It asks only when
// OPENAI_BASE_URL is set, the case where the endpoint is a gateway or a
// local server (Ollama, LM Studio) holding a fixed set of models, and never
// asks a provider HIVE_PROVIDER_ALLOWLIST refuses. A node's model is
// resolved as dispatch resolves it: its `model:`, else its `tier:` under the
// budget mode, else `sonnet`, then the OpenAI alias table. So a Claude alias
// left in a workflow is caught as the gpt-5.1 name it becomes. A repair's
// model is resolved as tryRepair resolves it: the block's `model:`, else its
// `tier:`, then the same alias table, else the node's own.
//
// A name without a tag also matches name:latest, Ollama's default tag. An
// endpoint answering 404 does not list models and is reported unchecked,
// unless the base URL lacks /v1 and <base>/v1/models answers: then the base
// URL is wrong, and the run is refused. Any other failure to list refuses the
// run, whose first call would fail the same way. When every model is served,
// the check names the nodes whose reasoning level will not be sent
// (ModelCheck.ReasoningNotSent). It makes the same check of the local
// endpoint (HIVE_LOCAL_BASE_URL, default Ollama's) whenever a node runs
// on the local provider, a server that also holds a fixed set of models. It
// returns one check per endpoint asked, and nil when there is nothing to
// check or report.
func PreflightEndpointModels(ctx context.Context, cfg Config, defn map[string]any) ([]*ModelCheck, error) {
	var checks []*ModelCheck
	for _, kind := range []BackendKind{BackendOpenAI, BackendLocal} {
		check, err := preflightEndpoint(ctx, cfg, defn, kind)
		if err != nil {
			return nil, err
		}
		if check != nil {
			checks = append(checks, check)
		}
	}
	return checks, nil
}

// preflightEndpoint is PreflightEndpointModels for the nodes one
// OpenAI-compatible provider kind serves, at its endpoint.
func preflightEndpoint(ctx context.Context, cfg Config, defn map[string]any, target BackendKind) (*ModelCheck, error) {
	if openAIEndpointUnset(target) {
		return nil, nil
	}
	runKind := runDefaultKind(cfg)
	want := endpointModelsFor(defn, cfg, runKind, target)
	if len(want.need) == 0 {
		return unusedOpenAICheck(target, runKind), nil
	}
	// Without a key the OpenAI backend cannot be built, and the run fails
	// or falls back on that before any call; there is nothing to ask with.
	b, err := newOpenAICompatible(target, cfg)
	if err != nil {
		return nil, nil
	}
	return checkEndpointModels(ctx, b, target, want)
}

// openAIEndpointUnset reports whether target is provider openai and
// OPENAI_BASE_URL is unset, so its endpoint is OpenAI's own and is not asked.
func openAIEndpointUnset(target BackendKind) bool {
	return target == BackendOpenAI && strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")) == ""
}

// unusedOpenAICheck is the report for OPENAI_BASE_URL set while no node runs
// on provider openai, when target is openai and the run default, runKind, is
// another provider; nil otherwise.
func unusedOpenAICheck(target, runKind BackendKind) *ModelCheck {
	if target == BackendOpenAI && runKind != "" && runKind != BackendOpenAI {
		return &ModelCheck{Provider: target, BaseURL: endpointFor(BackendOpenAI), Unused: runKind}
	}
	return nil
}

// endpointModels is what the nodes one endpoint serves are sent, by model.
type endpointModels struct {
	need   map[string][]string        // model → the nodes sent it
	levels map[string][]nodeReasoning // model → the nodes that send it a reasoning level
	tiers  map[string][]string        // model → the tiers that resolve to it
}

// endpointModelsFor is what the nodes of defn that target serves, under cfg
// and runKind, the run default, are sent.
func endpointModelsFor(defn map[string]any, cfg Config, runKind, target BackendKind) endpointModels {
	w := endpointModels{need: map[string][]string{}, levels: map[string][]nodeReasoning{}, tiers: map[string][]string{}}
	forEachAgentNode(defn, runKind, func(name string, node map[string]any, kind BackendKind) {
		if kind == target {
			w.addNode(name, node, cfg.BudgetMode, target)
		}
	})
	return w
}

// addNode notes what node name is sent on target under mode: its model, and
// its repair's when it has a repair that runs.
func (w endpointModels) addNode(name string, node map[string]any, mode BudgetMode, target BackendKind) {
	model, _ := node["model"].(string)
	tier, _ := node["tier"].(string)
	if model == "" && tier == "" {
		model = "sonnet" // the engine's default for a node naming neither
	}
	reasoning, _ := node["reasoning"].(string)
	w.add(nodeModel(model, tier, mode, target), name, tierChose(model, tier), reasoning)
	w.addRepair(name, node, mode, target, reasoning)
}

// tierChose is the tier a node's model was chosen by: its tier, when it
// names no model; "" when it names one.
func tierChose(model, tier string) string {
	if model != "" {
		return ""
	}
	return tier
}

// addRepair notes the model node name's on_reject repair is sent, when the
// node has a repair that runs. A repair runs on the node's provider
// (tryRepair), so its model must be served too. A block naming no model
// repairs on the node's own.
func (w endpointModels) addRepair(name string, node map[string]any, mode BudgetMode, target BackendKind, reasoning string) {
	block, _ := node["on_reject"].(map[string]any)
	if block == nil || intFromMap(block, "max_repair_iterations", 3) <= 0 {
		return
	}
	rm := repairModelFor(block, mode, target)
	if rm == "" {
		return
	}
	w.add(nodeModel(rm, "", mode, target), name+" on_reject", repairTier(block), reasoning)
}

// repairTier is the tier an on_reject block's model was chosen by: its
// `tier:`, when it names no `model:`; "" when it names one.
func repairTier(block map[string]any) string {
	if stringFromMap(block, "model", "") != "" {
		return ""
	}
	return stringFromMap(block, "tier", "")
}

// add notes that node, a node's name or "<node> on_reject", is sent model
// m, with tier when a tier chose m and reasoning when it sends a level.
func (w endpointModels) add(m, node, tier, reasoning string) {
	w.need[m] = append(w.need[m], node)
	if tier != "" && !slices.Contains(w.tiers[m], tier) {
		w.tiers[m] = append(w.tiers[m], tier)
	}
	if reasoning != "" {
		w.levels[m] = append(w.levels[m], nodeReasoning{node, reasoning})
	}
}

// checkEndpointModels asks b's endpoint which models it serves and checks
// want's against them, all within 30 seconds.
func checkEndpointModels(ctx context.Context, b *OpenAIBackend, target BackendKind, want endpointModels) (*ModelCheck, error) {
	check := &ModelCheck{Provider: target, BaseURL: withoutUserinfo(b.BaseURL), Models: slices.Sorted(maps.Keys(want.need))}
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	served, err := b.ListModels(listCtx)
	if err != nil {
		return unlistedModels(listCtx, b, target, check, err)
	}
	check.Listed = true
	missing, missingTiers := want.unserved(check.Models, served)
	if len(missing) == 0 {
		check.ReasoningNotSent = reasoningNotSent(listCtx, b, want.levels)
		return check, nil
	}
	return nil, unservedError(check.BaseURL, missing, missingTiers, served)
}

// unlistedModels is check, unchecked, or the refusal, for an endpoint whose
// model list failed with err. One answering 404 does not list its models,
// unless its base URL lacks /v1 and <base>/v1/models answers: then the base
// URL is wrong. Any other failure refuses the run, whose first call would
// fail the same way.
func unlistedModels(ctx context.Context, b *OpenAIBackend, target BackendKind, check *ModelCheck, err error) (*ModelCheck, error) {
	base := check.BaseURL
	if !isNotFound(err) {
		return nil, fmt.Errorf("model preflight: cannot list the models %s serves: %w%s", base, err, v1Hint(base))
	}
	// Ollama and LM Studio serve the list, and every call, under /v1.
	if b.listsUnderV1(ctx) {
		return nil, fmt.Errorf("model preflight: %s answers 404 for GET /models and %s/v1 lists models, so every call would miss: set %s to %s/v1", base, base, baseURLVar(target), base)
	}
	return check, nil
}

// isNotFound reports whether err carries HTTP status 404.
func isNotFound(err error) bool {
	var st interface{ StatusCode() int }
	return errors.As(err, &st) && st.StatusCode() == http.StatusNotFound
}

// listsUnderV1 reports whether b's base URL lacks /v1 and <base>/v1/models
// lists models.
func (b *OpenAIBackend) listsUnderV1(ctx context.Context) bool {
	if strings.HasSuffix(b.BaseURL, "/v1") {
		return false
	}
	_, err := b.listModelsAt(ctx, b.BaseURL+"/v1")
	return err == nil
}

// unserved names each of models, in order, that served does not hold, with
// the nodes sent it, and the tiers that resolve to those models.
func (w endpointModels) unserved(models, served []string) (missing, missingTiers []string) {
	have := make(map[string]bool, len(served))
	for _, id := range served {
		have[id] = true
	}
	for _, m := range models {
		if servesModel(have, m) {
			continue
		}
		nodeNames := w.need[m]
		sort.Strings(nodeNames)
		missing = append(missing, fmt.Sprintf("%q (node %s)", m, strings.Join(nodeNames, ", ")))
		missingTiers = appendUnique(missingTiers, w.tiers[m]...)
	}
	return missing, missingTiers
}

// servesModel reports whether have, the models an endpoint serves, holds m:
// as named, or, for a name without a tag, with Ollama's default tag, latest.
func servesModel(have map[string]bool, m string) bool {
	return have[m] || !strings.Contains(m, ":") && have[m+":latest"]
}

// appendUnique appends to list each of items it does not hold yet.
func appendUnique(list []string, items ...string) []string {
	for _, s := range items {
		if !slices.Contains(list, s) {
			list = append(list, s)
		}
	}
	return list
}

// unservedError refuses a run whose endpoint, base, does not serve missing:
// it names what the endpoint serves, and where the tiers that resolve to the
// missing models are mapped.
func unservedError(base string, missing, missingTiers, served []string) error {
	sort.Strings(served)
	listing := strings.Join(served, ", ")
	if len(served) == 0 {
		listing = "no models"
	}
	return fmt.Errorf("model preflight: %s does not serve %s; it serves %s%s", base, strings.Join(missing, "; "), listing, tierHint(missingTiers))
}

// tierHint says where missingTiers, tiers that resolve to models an endpoint
// does not serve, are mapped; "" when there are none.
func tierHint(missingTiers []string) string {
	if len(missingTiers) == 0 {
		return ""
	}
	sort.Strings(missingTiers)
	return fmt.Sprintf(". Tier %s resolves through the models config's tiers: map it to a served model under tiers: in ~/.config/hive/models.yaml (HIVE_MODELS_PATH)",
		strings.Join(missingTiers, ", "))
}

// nodeReasoning is a node, or "<node> on_reject", and the reasoning level it
// sends.
type nodeReasoning struct{ node, level string }

// reasoningNotSent names, sorted, each node whose level the backend will not
// send, where that changes what the model does (ModelCheck.ReasoningNotSent).
// It asks the endpoint as a call would (OpenAIBackend.thinking), so the
// answer is cached for the run's calls.
func reasoningNotSent(ctx context.Context, b *OpenAIBackend, levels map[string][]nodeReasoning) []string {
	var out []string
	for m, nodes := range levels {
		if b.thinking(ctx, m) == thinkingNo {
			out = append(out, unsentLevels(m, nodes)...)
		}
	}
	sort.Strings(out)
	return out
}

// unsentLevels says, for each of nodes that sends model m a level other than
// none, that the server reports no thinking capability for m.
func unsentLevels(m string, nodes []nodeReasoning) []string {
	var out []string
	for _, n := range nodes {
		if n.level != "none" {
			out = append(out, fmt.Sprintf("%q (node %s, reasoning %s): the server reports no thinking capability", m, n.node, n.level))
		}
	}
	return out
}
