package runner

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/endpointslot"
	"github.com/Chubby-Honey-Bee/hive/internal/schema"
)

// execLookPath is exec.LookPath in a separately-named symbol so tests
// can override `lookPath` without redirecting the standard package var.
func execLookPath(name string) (string, error) { return exec.LookPath(name) }

// BackendKind selects which LLMBackend implementation a runner uses.
type BackendKind string

const (
	// BackendAnthropic uses the official anthropic-sdk-go client. Requires
	// ANTHROPIC_API_KEY (in env or Config.APIKey).
	BackendAnthropic BackendKind = "anthropic"
	// BackendOpenAI uses the OpenAI-compatible /chat/completions REST
	// endpoint (also covers Copilot's compatible endpoint, Azure OpenAI,
	// and OSS gateways like vLLM via OPENAI_BASE_URL). Requires
	// OPENAI_API_KEY.
	BackendOpenAI BackendKind = "openai"
	// BackendGemini uses the Gemini generateContent REST endpoint
	// directly. Requires GEMINI_API_KEY (or GOOGLE_API_KEY).
	BackendGemini BackendKind = "gemini"
	// BackendClaudeCLI shells out to the `claude` CLI (Claude Code headless).
	// Uses the user's existing Claude Code auth — no API key needed.
	BackendClaudeCLI BackendKind = "claude-cli"
	// BackendGeminiCLI shells out to the `gemini` CLI (gemini-cli).
	BackendGeminiCLI BackendKind = "gemini-cli"
	// BackendLocal is the OpenAI-compatible backend at
	// HIVE_LOCAL_BASE_URL (default Ollama's http://localhost:11434/v1),
	// with no key. It lets a server on this machine and OpenAI itself
	// serve different nodes of one run.
	BackendLocal BackendKind = "local"
)

// RunRequest is the per-node input to an LLMBackend. Neutral across backends
// so the runner can swap implementations without changing call sites.
type RunRequest struct {
	System    string
	Prompt    string
	Model     string // friendly alias ("sonnet") or provider-specific ID
	MaxTurns  int
	MaxTokens int64
	TTL       time.Duration
	// Registry is the in-process tool registry. The SDK backend drives
	// tool_use loops through it. The CLI backend ignores this — Claude
	// Code provides built-in tools and HIVE's custom tools are
	// exposed via an MCP sidecar.
	Registry *ToolRegistry
	// Temperature, if non-nil, overrides the backend's default sampling
	// temperature. 0.0 gives the most deterministic output the backend
	// can produce. Pointer so nil is distinguishable from explicitly
	// setting zero.
	Temperature *float64
	// Seed, if non-nil, pins the PRNG seed for reproducibility. Only
	// forwarded when the user explicitly set --seed; never picked
	// silently. OpenAI and Gemini honor this; Anthropic does not expose
	// a seed parameter in the Messages API.
	Seed *int64
	// TopP, if non-nil, is the nucleus-sampling bound the user set with
	// --top-p. Nil sends none.
	TopP *float64
	// Reasoning is the node's `reasoning:` level (none|low|medium|high),
	// sent as reasoning_effort by the OpenAI-compatible backend only, and
	// only to a model that can take it (reasoningToSend). Empty sends none.
	Reasoning string
	// Logf, when set, is the run log, for a note a backend makes about the
	// call.
	Logf func(string, ...any)
	// OutputSchema is the node's output_schema; nil when it has none. The
	// OpenAI-compatible backend sends it as response_format on a call that
	// offers no tools. The other backends send nothing for it; every
	// backend's answer is checked against it after the call.
	OutputSchema *schema.Schema
	// SchemaName names the schema in response_format: the node's name.
	SchemaName string
	// ContextWindow is the model's context window in tokens, and
	// ContextWindowSource where it came from; 0 when none is known. The
	// OpenAI-compatible backend refuses a call whose prompt would not fit
	// it and lowers the output cap to the room left. The other backends
	// ignore it.
	ContextWindow       int64
	ContextWindowSource string
}

// httpTimeoutFromEnv reads HIVE_HTTP_TIMEOUT, the override for the
// per-call HTTP deadline the REST backends otherwise take from
// RunRequest.TTL. Unset is 0 (no override). A value that is not a positive
// Go duration is refused rather than ignored.
func httpTimeoutFromEnv() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("HIVE_HTTP_TIMEOUT"))
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("HIVE_HTTP_TIMEOUT=%q: want a positive duration such as 15m or 900s", raw)
	}
	return d, nil
}

// callDeadline bounds one HTTP call of a REST backend: by override when it
// is set, else by the request's TTL. With neither, the call is bounded only
// by ctx. The bound runs from when the request is sent, so it includes any
// time the request waits in the server's own queue; the wait for an endpoint
// slot (acquireEndpointSlot) comes before it and does not count. The
// returned wrap names the bound when it, and not ctx, ended the call.
func callDeadline(ctx context.Context, provider string, ttl, override time.Duration) (context.Context, context.CancelFunc, func(error) error) {
	d, source := callBound(ttl, override)
	if d <= 0 {
		return ctx, func() {}, func(err error) error { return err }
	}
	callCtx, cancel := context.WithTimeout(ctx, d)
	wrap := func(err error) error {
		if callBoundEnded(ctx, callCtx, err) {
			return fmt.Errorf("%s: no reply within %s (%s): %w", provider, d, source, err)
		}
		return err
	}
	return callCtx, cancel, wrap
}

// callBound is the bound on one call, override when it is set, else ttl, and
// what names it in an error.
func callBound(ttl, override time.Duration) (time.Duration, string) {
	if override > 0 {
		return override, "HIVE_HTTP_TIMEOUT"
	}
	return ttl, "the call's TTL"
}

// callBoundEnded reports whether err ended a call because callCtx's deadline
// passed while ctx, its parent, was still live.
func callBoundEnded(ctx, callCtx context.Context, err error) bool {
	return err != nil && ctx.Err() == nil && errors.Is(callCtx.Err(), context.DeadlineExceeded)
}

// The slot table lives in internal/endpointslot, which the embedding
// providers share, so their calls to a server wait for the same slots.
func endpointLimit(endpoint string) int  { return endpointslot.Limit(endpoint) }
func isLoopbackURL(endpoint string) bool { return endpointslot.IsLoopbackURL(endpoint) }

// acquireEndpointSlot waits, within ctx, for a free slot at endpoint
// (endpointslot.Acquire) and returns its release. The wait happens in chb,
// before callDeadline starts the call's bound, so a call queued behind chb's
// own calls to a single-slot server does not spend its TTL waiting.
func acquireEndpointSlot(ctx context.Context, provider, endpoint string) (func(), error) {
	release, n, err := endpointslot.Acquire(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("%s: no free slot at %s (%d at a time, HIVE_MAX_PARALLEL_ENDPOINT) before the node's budget ran out: %w",
			provider, withoutUserinfo(endpoint), n, err)
	}
	return release, nil
}

// doProviderRequest sends req, a REST backend's call, with client and
// decodes the reply into out. An error status is an httpStatusError, and a
// reply that does not decode an error that shows its start; each error
// names provider.
func doProviderRequest(client *http.Client, req *http.Request, provider string, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s POST: %w", provider, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return newHTTPStatusError(provider, resp, respBody)
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("%s decode: %w (body: %s)", provider, err, truncate(string(respBody), 200))
	}
	return nil
}

// withoutUserinfo is endpoint with any user:password@ removed, for what is
// recorded and logged: a gateway's URL can carry credentials.
func withoutUserinfo(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.User == nil {
		return endpoint
	}
	u.User = nil
	return u.String()
}

// incompleteReplyError is a reply that is not an answer: cut off at the
// output cap, empty, or still calling tools when the turns ran out. It is
// never a rate limit, whatever numbers its text holds (isRateLimitError).
type incompleteReplyError struct{ msg string }

func (e *incompleteReplyError) Error() string { return e.msg }

// incompleteReply reports a final reply that must not count as an answer:
// one the provider cut off at the output cap (stop equals truncatedStop), or
// one with no text. The caller has already established the reply asks for
// no tool call. maxTokens is the cap chb sent; 0 means it sent none.
func incompleteReply(provider, stop, truncatedStop, text string, maxTokens int64) error {
	if truncatedStop != "" && stop == truncatedStop {
		return &incompleteReplyError{cutOffReplyMessage(provider, stop, maxTokens)}
	}
	if strings.TrimSpace(text) == "" {
		return &incompleteReplyError{fmt.Sprintf("%s: empty reply with no tool call (stop reason %q)", provider, stop)}
	}
	return nil
}

// cutOffReplyMessage says provider cut a reply off at the output cap, with
// its stop reason and maxTokens, the cap chb sent, when it sent one.
func cutOffReplyMessage(provider, stop string, maxTokens int64) string {
	if maxTokens <= 0 {
		return fmt.Sprintf("%s: reply cut off at the output cap (stop reason %s)", provider, stop)
	}
	return fmt.Sprintf("%s: reply cut off at the output cap (stop reason %s, max tokens %d)", provider, stop, maxTokens)
}

// wrapUpPrompt is what a tool loop's wrap-up call adds to the conversation
// once the loop has used its maxTurns turns with the model still calling
// tools: no tool turn is left, so the model answers from what it has done.
func wrapUpPrompt(maxTurns int) string {
	return fmt.Sprintf("You have used all %d tool turns, and no tool call is left. Reply now with your final answer, in the form the task asks for, from what you have done so far, and say in it what you did not finish.", maxTurns)
}

// errWrapUpCalledTool is why a wrap-up call gives no answer when its reply
// calls a tool, though none can be called.
var errWrapUpCalledTool = errors.New("its reply calls a tool, and none was offered")

// wrapUpFailed reports a tool loop that used all maxTurns turns with the
// model still calling tools, and whose wrap-up call gave no answer, err
// saying why.
func wrapUpFailed(provider string, maxTurns int, err error) error {
	return &incompleteReplyError{fmt.Sprintf("%s: no final reply after %d turns, the model still calling tools (stop reason max_turns), and the wrap-up call gave none: %v", provider, maxTurns, err)}
}

// LLMBackend drives a single node's prompt to completion, returning the
// model's final text + audit metadata.
type LLMBackend interface {
	Run(ctx context.Context, req RunRequest) (*RunResult, error)
}

// ResolveBackendKind picks the backend per env + config. Precedence:
//
//  1. cfg.Provider (per-run --provider flag)
//  2. HIVE_PROVIDER env
//  3. First SDK env key found:
//     ANTHROPIC_API_KEY → anthropic
//     GEMINI_API_KEY    → gemini
//     OPENAI_API_KEY    → openai
//  4. First CLI binary on PATH:
//     claude → claude-cli
//     gemini → gemini-cli
//  5. Fall through to claude-cli, whose backend then names what to
//     configure (noProviderConfigured).
//
// Every name a user may type for a provider normalizes to one canonical
// kind: callers downstream branch on that kind, so adding an alias never
// expands the switch in NewBackend.
func ResolveBackendKind(cfg Config) BackendKind {
	if c := namedProviderKind(cfg); c != "" {
		return c
	}
	if c := sdkKeyKind(cfg); c != "" {
		return c
	}
	return cmp.Or(cliOnPathKind(), BackendClaudeCLI)
}

// namedProviderKind is the first known kind that cfg.Provider or
// HIVE_PROVIDER names, in that order; "" when neither names one.
func namedProviderKind(cfg Config) BackendKind {
	return canonicalKind(namedProvider(cfg))
}

// namedProvider is the first of cfg.Provider and HIVE_PROVIDER that
// names a known kind or an alias of one, as written, trimmed and in lower
// case; "" when neither names one.
func namedProvider(cfg Config) string {
	for _, v := range []string{cfg.Provider, os.Getenv("HIVE_PROVIDER")} {
		if name := strings.ToLower(strings.TrimSpace(v)); canonicalKind(name) != "" {
			return name
		}
	}
	return ""
}

// sdkKeyKinds are the SDK keys ResolveBackendKind looks for, in order, with
// the kind each selects.
var sdkKeyKinds = []struct {
	env  string
	kind BackendKind
}{
	{"ANTHROPIC_API_KEY", BackendAnthropic},
	{"GEMINI_API_KEY", BackendGemini},
	{"GOOGLE_API_KEY", BackendGemini},
	{"OPENAI_API_KEY", BackendOpenAI},
}

// sdkKeyKind is the kind of the first SDK key that is set, cfg.APIKey
// counting as Anthropic's; "" when none is.
func sdkKeyKind(cfg Config) BackendKind {
	if cfg.APIKey != "" {
		return BackendAnthropic
	}
	for _, k := range sdkKeyKinds {
		if os.Getenv(k.env) != "" {
			return k.kind
		}
	}
	return ""
}

// cliOnPathKind is the kind of the first CLI on PATH, claude then gemini;
// "" when neither is.
func cliOnPathKind() BackendKind {
	for _, cli := range []struct {
		name string
		kind BackendKind
	}{{"claude", BackendClaudeCLI}, {"gemini", BackendGeminiCLI}} {
		if _, err := lookPath(cli.name); err == nil {
			return cli.kind
		}
	}
	return ""
}

// noProviderConfigured reports whether ResolveBackendKind would reach its
// last step: nothing names a provider, no SDK key is set and neither CLI is
// on PATH. The claude CLI is then a fallback nobody asked for, and its
// absence is a missing configuration, not a missing binary.
func noProviderConfigured(cfg Config) bool {
	return namedProviderKind(cfg) == "" && sdkKeyKind(cfg) == "" && cliOnPathKind() == ""
}

// providerAliases maps each name a user may type for a provider, in lower
// case, to its canonical kind. Copilot and Azure expose OpenAI-compatible
// APIs (set OPENAI_BASE_URL accordingly).
var providerAliases = map[string]BackendKind{
	"anthropic":    BackendAnthropic,
	"openai":       BackendOpenAI,
	"copilot":      BackendOpenAI,
	"azure-openai": BackendOpenAI,
	"gemini":       BackendGemini,
	"google":       BackendGemini,
	"claude-cli":   BackendClaudeCLI,
	"claude-code":  BackendClaudeCLI,
	"gemini-cli":   BackendGeminiCLI,
	"local":        BackendLocal,
}

// canonicalKind normalizes user-typed strings (CLI flags, env vars,
// workflow YAML provider: fields) to one of the canonical BackendKind
// values. Returns "" for empty / unrecognized input — callers fall
// through to the next precedence step in that case.
func canonicalKind(s string) BackendKind {
	return providerAliases[strings.ToLower(strings.TrimSpace(s))]
}

// openAICompatible reports whether kind is served by the OpenAI-compatible
// backend, at OPENAI_BASE_URL (openai) or HIVE_LOCAL_BASE_URL (local).
func openAICompatible(kind BackendKind) bool {
	return kind == BackendOpenAI || kind == BackendLocal
}

// EnforceProviderAllowlist gates which LLM providers a run may use. When
// HIVE_PROVIDER_ALLOWLIST is set (a comma-separated list of provider
// names or aliases, e.g. "claude-cli,anthropic"), the resolved backend
// kind must canonicalize to one of the listed entries or the run is
// refused before any model call. This turns egress into an enforced
// policy instead of an inferred default: a deployment can pin itself to a
// sanctioned path (say the Copilot/CLI route) and refuse a silent
// fall-through to a direct SDK key the moment one is present in the env.
// It gates the provider kind only: OPENAI_BASE_URL, GEMINI_BASE_URL and the
// claude CLI's ANTHROPIC_BASE_URL still choose the host (SECURITY.md).
//
// When the variable is empty or unset, no restriction applies: the allowlist
// is opt-in.
func EnforceProviderAllowlist(kind BackendKind) error {
	raw := strings.TrimSpace(os.Getenv("HIVE_PROVIDER_ALLOWLIST"))
	if raw == "" {
		return nil
	}
	want := cmp.Or(canonicalKind(string(kind)), kind) // unknown/injected kind: compare verbatim
	allowed := allowlistKinds(raw)
	if slices.Contains(allowed, string(want)) {
		return nil
	}
	return fmt.Errorf("provider %q is not in HIVE_PROVIDER_ALLOWLIST %v: refusing to run (add it to the allowlist, or pass --provider with an allowed value)", want, allowed)
}

// allowlistKinds is each entry of raw, HIVE_PROVIDER_ALLOWLIST's value,
// as a kind: the canonical one for a name it knows, else the entry as
// written in lower case. Empty entries are left out.
func allowlistKinds(raw string) []string {
	allowed := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		if c := cmp.Or(canonicalKind(part), BackendKind(strings.ToLower(strings.TrimSpace(part)))); c != "" {
			allowed = append(allowed, string(c))
		}
	}
	return allowed
}

// PreflightWorkflowProviders refuses a run whose workflow pins any node to a
// provider outside HIVE_PROVIDER_ALLOWLIST. EnforceProviderAllowlist in
// resolveLLMBackend gates only the run default; a node-level `provider:`
// override builds its own backend and would otherwise reach an unsanctioned
// provider. Running this before the first dispatch is what makes the
// allowlist's "refused before any model call" contract true for overrides too.
// It also refuses a `provider:` that names no known provider, which dispatch
// would otherwise serve on the run default without a word.
func PreflightWorkflowProviders(defn map[string]any) error {
	nodes, _ := defn["nodes"].(map[string]any)
	var refused []string
	for name, raw := range nodes {
		if why := nodeProviderRefusal(name, raw); why != "" {
			refused = append(refused, why)
		}
	}
	if len(refused) == 0 {
		return nil
	}
	sort.Strings(refused)
	return fmt.Errorf("workflow provider preflight failed:\n  %s", strings.Join(refused, "\n  "))
}

// nodeProviderRefusal says why node name, raw as the workflow decodes it,
// may not run on its `provider:`: the provider is not a known one, or the
// allowlist refuses it. "" when the node names no provider, or one it may
// run on.
func nodeProviderRefusal(name string, raw any) string {
	node, _ := raw.(map[string]any)
	p, _ := node["provider"].(string)
	if strings.TrimSpace(p) == "" {
		return ""
	}
	if canonicalKind(p) == "" {
		return fmt.Sprintf("node %q: provider %q is not a known provider (anthropic, openai, gemini, claude-cli, gemini-cli, local, or an alias of one); a local server such as Ollama or LM Studio is provider: local, at HIVE_LOCAL_BASE_URL (default %s)", name, p, defaultLocalBaseURL)
	}
	if err := EnforceProviderAllowlist(BackendKind(p)); err != nil {
		return fmt.Sprintf("node %q: %v", name, err)
	}
	return ""
}

// PreflightSampling refuses, before the run starts, a --temperature above 1
// when any node runs on the Anthropic SDK backend: the Messages API takes 0
// to 1, so every such call would fail with HTTP 400. The other backends are
// sent the value as given.
func PreflightSampling(cfg Config, defn map[string]any) error {
	if cfg.Temperature == nil || *cfg.Temperature <= 1 {
		return nil
	}
	names := agentNodesOnKind(cfg, defn, BackendAnthropic)
	if len(names) == 0 {
		return nil
	}
	return fmt.Errorf("--temperature %g: the Anthropic Messages API takes 0 to 1, and node %s runs on it", *cfg.Temperature, strings.Join(names, ", "))
}

// agentNodesOnKind names, sorted, the nodes of defn that forEachAgentNode
// serves on kind under cfg's run default.
func agentNodesOnKind(cfg Config, defn map[string]any, kind BackendKind) []string {
	var names []string
	forEachAgentNode(defn, runDefaultKind(cfg), func(name string, _ map[string]any, k BackendKind) {
		if k == kind {
			names = append(names, name)
		}
	})
	sort.Strings(names)
	return names
}

// lookPath is exec.LookPath, indirected to make testing without messing
// with PATH possible. Default points at exec.LookPath in production code.
var lookPath = func(name string) (string, error) {
	// Avoid pulling os/exec at the file level only for this; keep the
	// real call inline. Tests can override this var with a stub.
	return execLookPath(name)
}

// NewBackend constructs the concrete backend for `kind`. Aliases are
// resolved to canonical kinds via canonicalKind before the switch.
//
// Every backend is wrapped with a RateLimitedBackend so per-provider
// 429 / quota / overload errors trigger automatic backoff + retry, and
// concurrent dispatch is gated through a per-provider token bucket.
// Set HIVE_RPM_<PROVIDER> (or HIVE_RPM_DEFAULT) to tune the
// bucket rate; default 60 RPM is conservative and well under every
// published per-key floor.
func NewBackend(kind BackendKind, cfg Config) (LLMBackend, error) {
	inner, err := newRawBackend(kind, cfg)
	if err != nil {
		return nil, err
	}
	if os.Getenv("HIVE_DISABLE_RATE_LIMIT") == "1" {
		return inner, nil
	}
	return NewRateLimitedBackend(inner, string(kind)), nil
}

// newRawBackend is the un-wrapped factory. Callers that explicitly do
// not want rate-limit wrapping (e.g. tests that set HIVE_DISABLE_RATE_LIMIT)
// can still get a bare backend by reading the env, but the canonical
// path goes through NewBackend.
func newRawBackend(kind BackendKind, cfg Config) (LLMBackend, error) {
	switch kind {
	case BackendAnthropic:
		return &SDKBackend{APIKey: cfg.APIKey}, nil
	case BackendClaudeCLI:
		return NewCLIBackend(cfg)
	case BackendOpenAI:
		return NewOpenAIBackend(cfg)
	case BackendGemini:
		return NewGeminiBackend(cfg)
	case BackendGeminiCLI:
		return newGeminiCLIBackend(cfg)
	case BackendLocal:
		return NewLocalBackend(cfg)
	default:
		return nil, fmt.Errorf("unknown backend %q", kind)
	}
}
