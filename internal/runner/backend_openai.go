package runner

// OpenAI-compatible REST backend.
//
// One implementation, many providers: as long as the endpoint speaks
// OpenAI's /v1/chat/completions schema (tool_calls + tool messages), this
// backend works. That includes:
//   • OpenAI proper            (default base URL)
//   • Azure OpenAI              (set OPENAI_BASE_URL to your deployment)
//   • GitHub Copilot            (set OPENAI_BASE_URL to Copilot's endpoint)
//   • OSS gateways: vLLM, Ollama OpenAI shim, LocalAI, etc.
//
// We intentionally implement this with net/http rather than pulling in
// openai-go: keeps the dependency surface small, the request/response
// shape is stable, and it gives us full control over retry + timeout.
//
// Tool-use loop:
//   1. POST messages + tools[] to /chat/completions.
//   2. If response has tool_calls, dispatch each via the registry,
//      append the assistant message + a tool message per call, repeat.
//   3. Stop when the response has no tool_calls (finish_reason="stop"),
//      or when MaxTurns is reached.

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/schema"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// OpenAIBackend implements LLMBackend via the OpenAI-compatible REST API.
type OpenAIBackend struct {
	APIKey  string // Authorization: Bearer <key>; empty falls back to OPENAI_API_KEY env
	BaseURL string // e.g. https://api.openai.com/v1 ; empty falls back to OPENAI_BASE_URL env or default
	Org     string // optional Organization header (OPENAI_ORG)
	Client  *http.Client
	// Timeout, when >0, bounds every HTTP call in place of RunRequest.TTL
	// (HIVE_HTTP_TIMEOUT).
	Timeout time.Duration
	// calib holds, per endpoint and model, the bytes per token the server
	// has counted, which the context-window guard estimates prompts with.
	// nil keeps nothing: every call estimates by text class alone.
	calib *calibration
	// thinking caches what the endpoint says about each model's thinking
	// capability (reasoningToSend). nil keeps nothing: every call asks.
	thinkingCache *thinkingCache
	// name is the provider the run resolved for this backend: local, or the
	// name it gave the OpenAI-compatible provider (openaiName). "" is openai.
	name string
}

// provider names b's provider in its errors, every one of them.
func (b *OpenAIBackend) provider() string {
	return cmp.Or(b.name, string(BackendOpenAI))
}

// openaiName is the name the run gave provider openai, for its backend's
// errors: the first of --provider and HIVE_PROVIDER that names a
// provider, when that is openai or one of its aliases, copilot and
// azure-openai; else openai, as when OPENAI_API_KEY chose it.
func openaiName(cfg Config) string {
	if name := namedProvider(cfg); canonicalKind(name) == BackendOpenAI {
		return name
	}
	return string(BackendOpenAI)
}

// openAIBaseURL is the endpoint the OpenAI-compatible backend sends to:
// OPENAI_BASE_URL without a trailing slash, else OpenAI's own.
func openAIBaseURL() string {
	if base := strings.TrimRight(os.Getenv("OPENAI_BASE_URL"), "/"); base != "" {
		return base
	}
	return "https://api.openai.com/v1"
}

// NewOpenAIBackend constructs an OpenAIBackend. Its client has no timeout of
// its own: each call waits for an endpoint slot (acquireEndpointSlot), then is
// bounded by the request's TTL or HIVE_HTTP_TIMEOUT, a bound that includes
// any time the request waits in the server's own queue. It shares the run's
// thinking-capability cache and calibration when cfg carries them, else
// keeps its own. Its errors name the provider as cfg names it (openaiName).
func NewOpenAIBackend(cfg Config) (*OpenAIBackend, error) {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY not set (export it or set --provider to a different backend)")
	}
	b, err := openAIBackendAt(cfg, openAIBaseURL(), key)
	if err != nil {
		return nil, err
	}
	b.Org = os.Getenv("OPENAI_ORG")
	b.name = openaiName(cfg)
	return b, nil
}

// openAIBackendAt is the OpenAI-compatible backend at baseURL, sending key
// as its bearer token. Its client has no timeout of its own: each call is
// bounded by HIVE_HTTP_TIMEOUT or the request's TTL. It shares the run's
// thinking-capability cache and calibration when cfg carries them, else
// keeps its own.
func openAIBackendAt(cfg Config, baseURL, key string) (*OpenAIBackend, error) {
	timeout, err := httpTimeoutFromEnv()
	if err != nil {
		return nil, err
	}
	return &OpenAIBackend{
		APIKey:        key,
		BaseURL:       baseURL,
		Client:        &http.Client{},
		Timeout:       timeout,
		calib:         cmp.Or(cfg.calibration, &calibration{}),
		thinkingCache: cmp.Or(cfg.thinkingCache, &thinkingCache{}),
	}, nil
}

// defaultLocalBaseURL is where the local provider's calls go when
// HIVE_LOCAL_BASE_URL is unset: Ollama's OpenAI-compatible API.
const defaultLocalBaseURL = "http://localhost:11434/v1"

// localBaseURL is the endpoint the local provider sends to:
// HIVE_LOCAL_BASE_URL without a trailing slash, else defaultLocalBaseURL.
func localBaseURL() string {
	if base := strings.TrimRight(os.Getenv("HIVE_LOCAL_BASE_URL"), "/"); base != "" {
		return base
	}
	return defaultLocalBaseURL
}

// LocalBaseURL is the local provider's endpoint, without any credentials in
// it, for what a command prints.
func LocalBaseURL() string { return withoutUserinfo(localBaseURL()) }

// NewLocalBackend constructs the OpenAI-compatible backend for the local
// provider: localBaseURL, and no key, since Ollama and LM Studio take none.
// It sends the placeholder "local" as its bearer token, so no provider
// credential reaches whatever listens there. Its calls are bounded and share
// the run's thinking cache and calibration as NewOpenAIBackend's are.
func NewLocalBackend(cfg Config) (*OpenAIBackend, error) {
	b, err := openAIBackendAt(cfg, localBaseURL(), "local")
	if err != nil {
		return nil, err
	}
	b.name = string(BackendLocal)
	return b, nil
}

// newOpenAICompatible builds the OpenAI-compatible backend that serves kind:
// NewOpenAIBackend for openai, NewLocalBackend for local.
func newOpenAICompatible(kind BackendKind, cfg Config) (*OpenAIBackend, error) {
	if kind == BackendLocal {
		return NewLocalBackend(cfg)
	}
	return NewOpenAIBackend(cfg)
}

// ResolveOpenAIModel maps friendly aliases to OpenAI model ids. Free-form
// pass-through preserved so callers can pin any specific model.
func ResolveOpenAIModel(alias string) string {
	switch strings.ToLower(strings.TrimSpace(alias)) {
	case "", "sonnet":
		return "gpt-5.1"
	case "opus":
		return "gpt-5.1-pro"
	case "haiku":
		return "gpt-5.1-mini"
	}
	return alias
}

// Run drives req's prompt through chat completions, running each tool call
// the model makes through the request's registry, until a reply calls none
// or the turns run out. Each call passes the context-window guard
// (chatTurn).
func (b *OpenAIBackend) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	if req.Registry == nil {
		return nil, fmt.Errorf("%s backend requires a tool registry", b.provider())
	}
	model := ResolveOpenAIModel(req.Model)
	maxTurns := requestTurnCap(req.MaxTurns)
	req.Reasoning = b.reasoningLevel(ctx, model, req)
	l := newOpenAILoop(b, req, model)
	res := l.res
	// Every Invoke below records into the registry, which is built fresh per
	// dispatch; the log is copied onto the result at every exit, the error
	// ones included, so a node served by this backend keeps its
	// tool_invocations provenance.
	defer func() { res.Invocations = req.Registry.Invocations() }()
	for turn := 0; turn < maxTurns; turn++ {
		if done, err := l.turn(ctx); done {
			return res, err
		}
	}
	return res, l.wrapUp(ctx, maxTurns)
}

// reasoningLevel is the reasoning level b sends model for req
// (reasoningToSend). The note it makes goes to the run log.
func (b *OpenAIBackend) reasoningLevel(ctx context.Context, model string, req RunRequest) string {
	level, note := b.reasoningToSend(ctx, model, req.Reasoning)
	if note != "" && req.Logf != nil {
		req.Logf("%s", note)
	}
	return level
}

// openaiLoop is one node's tool loop on the OpenAI-compatible backend: the
// conversation so far, the tools it offers, and the result it charges.
type openaiLoop struct {
	b        *OpenAIBackend
	req      RunRequest
	model    string
	messages []map[string]any
	tools    []map[string]any
	// constrain is set when the calls carry the request's schema. A grammar
	// and tool calls cannot share one call on a llama.cpp server (under
	// Ollama and LM Studio too). So only a call that offers no tools carries
	// the schema; a tool loop gets it on its finalize call.
	constrain bool
	res       *RunResult
}

// newOpenAILoop is the loop for req on b: the system prompt when there is
// one, then the prompt, and the registry's tools.
func newOpenAILoop(b *OpenAIBackend, req RunRequest, model string) *openaiLoop {
	messages := []map[string]any{}
	if req.System != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.System})
	}
	messages = append(messages, map[string]any{"role": "user", "content": req.Prompt})
	tools := openaiToolsFromRegistry(req.Registry)
	return &openaiLoop{
		b:         b,
		req:       req,
		model:     model,
		messages:  messages,
		tools:     tools,
		constrain: req.OutputSchema != nil && len(tools) == 0,
		res:       &RunResult{},
	}
}

// request is the next call as it stands: the conversation so far, the
// tools, and the schema when the call carries it.
func (l *openaiLoop) request() map[string]any {
	body := chatBody(l.model, l.messages, l.req)
	if len(l.tools) > 0 {
		body["tools"] = l.tools
	}
	if l.constrain {
		body["response_format"] = responseFormat(l.req.OutputSchema, l.req.SchemaName)
	}
	return body
}

// turn makes the loop's next call. A reply with no tool call is the answer,
// and ends the loop: done, with the error the answer ends it with. A reply
// that calls tools joins the conversation with their results.
func (l *openaiLoop) turn(ctx context.Context) (done bool, err error) {
	choice, fit, err := l.b.chatTurn(ctx, l.req, l.request(), l.res)
	if err != nil {
		return true, err
	}
	if len(choice.Message.ToolCalls) == 0 {
		return true, l.answer(ctx, choice, fit)
	}
	l.messages = append(l.messages, assistantToolTurn(choice.Message))
	if err := l.runTools(ctx, choice.Message.ToolCalls); err != nil {
		return true, err
	}
	return false, nil
}

// answer records choice, a reply with no tool call, as the loop's answer,
// and returns the error the loop ends with. An answer the loop offered
// tools for was not constrained to the request's schema: one that already
// holds its schema stands, and one that breaks it gets one call with the
// tools dropped and the schema set (finalize).
func (l *openaiLoop) answer(ctx context.Context, choice openaiChoice, fit contextFit) error {
	if err := finalReply(l.b.provider(), l.res, choice, fit); err != nil {
		return err
	}
	l.res.SchemaAtDecode = l.constrain
	violations := l.unconstrainedViolations()
	if len(violations) == 0 {
		return nil
	}
	return l.b.finalize(ctx, l.req, l.model, l.messages, violations, l.res)
}

// wrapUp asks for the loop's answer after its maxTurns turns ran out with
// the model still calling tools (wrapUpAnswer). A wrap-up that gives none
// ends the loop out of turns (wrapUpFailed).
func (l *openaiLoop) wrapUp(ctx context.Context, maxTurns int) error {
	if err := l.wrapUpAnswer(ctx, maxTurns); err != nil {
		l.res.StopReason = "max_turns"
		return wrapUpFailed(l.b.provider(), maxTurns, err)
	}
	l.res.WrappedUp = true
	return nil
}

// wrapUpAnswer makes the wrap-up call: the tools dropped, the schema set when
// the request has one, after the message that no tool turn is left
// (wrapUpPrompt). Its reply is the loop's answer, checked as any is
// (answer); the error says why it gives none.
func (l *openaiLoop) wrapUpAnswer(ctx context.Context, maxTurns int) error {
	l.tools, l.constrain = nil, l.req.OutputSchema != nil
	l.messages = append(l.messages, map[string]any{"role": "user", "content": wrapUpPrompt(maxTurns)})
	choice, fit, err := l.b.chatTurn(ctx, l.req, l.request(), l.res)
	if err != nil {
		return err
	}
	if len(choice.Message.ToolCalls) > 0 {
		return errWrapUpCalledTool
	}
	return l.answer(ctx, choice, fit)
}

// unconstrainedViolations are the ways the loop's answer breaks the
// request's schema, when it has one the calls did not carry; none
// otherwise.
func (l *openaiLoop) unconstrainedViolations() []string {
	if l.req.OutputSchema == nil || l.constrain {
		return nil
	}
	return schemaViolations(l.req.OutputSchema, l.res.FinalText)
}

// assistantToolTurn is msg, a reply that calls tools, as it joins the
// conversation: verbatim, so the next turn has the same context. tool_calls
// must be preserved JSON-faithfully, and a thinking model's reasoning goes
// back with them, in the field the server returned it in: Ollama returns
// `reasoning` and reads it back from the same field; other servers use
// `reasoning_content`.
func assistantToolTurn(msg openaiMessage) map[string]any {
	assistant := map[string]any{
		"role":       "assistant",
		"content":    msg.Content,
		"tool_calls": msg.ToolCalls,
	}
	if msg.Reasoning != "" {
		assistant["reasoning"] = msg.Reasoning
	}
	if msg.ReasoningContent != "" {
		assistant["reasoning_content"] = msg.ReasoningContent
	}
	return assistant
}

// runTools runs each tool call, in order, adding its result to the
// conversation.
func (l *openaiLoop) runTools(ctx context.Context, calls []openaiToolCall) error {
	for _, tc := range calls {
		l.res.ToolUses++
		if err := l.runTool(ctx, tc); err != nil {
			return err
		}
	}
	return nil
}

// runTool runs one tool call and adds its result to the conversation. Its
// arguments must be a JSON object: running the tool on {} would answer a
// different call than the one the model made, so the model is told, and the
// call recorded.
func (l *openaiLoop) runTool(ctx context.Context, tc openaiToolCall) error {
	input, argErr := toolArguments(tc.Function.Arguments)
	if argErr != nil {
		l.req.Registry.refuse(tc.Function.Name, tc.Function.Arguments, argErr.Error())
		l.messages = append(l.messages, map[string]any{
			"role":         "tool",
			"tool_call_id": tc.ID,
			"content":      "ERROR: " + argErr.Error(),
		})
		return nil
	}
	out, isErr, err := l.req.Registry.Invoke(ctx, tc.Function.Name, input)
	if err != nil {
		return fmt.Errorf("tool %s: %w", tc.Function.Name, err)
	}
	content := out
	if isErr {
		content = "ERROR: " + out
	}
	l.addToolResult(tc, input, content)
	return nil
}

// addToolResult adds content, the result of tool call tc made with input,
// to the conversation, then holds it to the room the next call leaves for it
// (fitToolResult), so no result pushes that call past the context window,
// or, when no window is known, to toolResultCap.
func (l *openaiLoop) addToolResult(tc openaiToolCall, input map[string]any, content string) {
	msg := map[string]any{
		"role":         "tool",
		"tool_call_id": tc.ID,
		"content":      "",
	}
	l.messages = append(l.messages, msg)
	var kept int
	msg["content"], kept = fitToolResult(l.b.calib, l.b.BaseURL, l.request(), l.req, content, l.req.Registry.restOf(tc.Function.Name, input))
	logToolCut(l.req.Logf, tc.Function.Name, kept, len(content), l.req.ContextWindow > 0)
}

// chatBody is one chat-completions request: the model, the conversation, and
// the request's sampling, seed, reasoning and output cap, each sent only
// when set.
func chatBody(model string, messages []map[string]any, req RunRequest) map[string]any {
	body := map[string]any{
		"model":    model,
		"messages": messages,
	}
	setChatSampling(body, req)
	if req.Seed != nil {
		body["seed"] = *req.Seed
	}
	if req.Reasoning != "" {
		body["reasoning_effort"] = req.Reasoning
	}
	if req.MaxTokens > 0 {
		// OpenAI Chat Completions API: newer reasoning models (gpt-5,
		// o-series) use "max_completion_tokens"; legacy models use
		// "max_tokens". Set both — the API ignores the one it doesn't
		// recognise.
		body["max_completion_tokens"] = req.MaxTokens
		body["max_tokens"] = req.MaxTokens
	}
	return body
}

// setChatSampling sets the request's temperature and top_p in body, each
// only when set. Greedy decoding pins top_p to 1 too, as --deterministic
// always has, unless the request sets its own.
func setChatSampling(body map[string]any, req RunRequest) {
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if greedyDecoding(req) {
		body["top_p"] = 1.0
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
}

// chatTurn sends one request, within an endpoint slot and the call's bound,
// charges its usage to res, and returns its first choice. Once it has the
// slot the context-window guard runs (fitContext): a prompt that would not
// fit the request's window is refused unsent, and the output cap lowered to
// the room left. After the call, before the slot is released, it reads the
// server's count (contextFit.check), so a call queued behind this one on the
// same model estimates with it. Each call's line for the run log is added
// to res.ContextNotes.
func (b *OpenAIBackend) chatTurn(ctx context.Context, req RunRequest, body map[string]any, res *RunResult) (openaiChoice, contextFit, error) {
	release, err := acquireEndpointSlot(ctx, b.provider(), b.BaseURL)
	if err != nil {
		return openaiChoice{}, contextFit{sent: req.MaxTokens, asked: req.MaxTokens}, err
	}
	// Released by a defer, so a panic in the call frees the slot as it
	// unwinds.
	defer release()
	fit, err := fitContext(b.calib, b.BaseURL, body, req)
	if err != nil {
		res.ContextNotes = append(res.ContextNotes, fit.note(0))
		return openaiChoice{}, fit, err
	}
	choice, err := b.sendChat(ctx, req, body, &fit, res)
	return choice, fit, err
}

// sendChat sends body within the call's bound, reads the server's count of
// it into fit (contextFit.check), charges its usage to res, and returns its
// first choice. A reply the guard fails is an error, its usage charged.
func (b *OpenAIBackend) sendChat(ctx context.Context, req RunRequest, body map[string]any, fit *contextFit, res *RunResult) (openaiChoice, error) {
	callCtx, cancel, wrap := callDeadline(ctx, b.provider(), req.TTL, b.Timeout)
	raw, err := b.callChatCompletions(callCtx, body)
	cancel()
	if err != nil {
		return openaiChoice{}, wrap(err)
	}
	fitErr := fit.check(b.calib, raw.Usage.PromptTokens, raw.Usage.CompletionTokens)
	chargeChatCall(res, raw, fit)
	if fitErr != nil {
		return openaiChoice{}, fitErr
	}
	if len(raw.Choices) == 0 {
		return openaiChoice{}, fmt.Errorf("%s: empty choices array", b.provider())
	}
	return raw.Choices[0], nil
}

// chargeChatCall adds an answered call's usage to res, with its line for the
// run log. A reply stopped at length counts as cut off whatever the guard's
// check found, so a call it fails as cut still counts as stopped at the
// cap.
func chargeChatCall(res *RunResult, raw *openaiResponse, fit *contextFit) {
	res.Turns++
	res.InputTokens += raw.Usage.PromptTokens
	res.OutputTokens += raw.Usage.CompletionTokens
	if len(raw.Choices) > 0 && raw.Choices[0].FinishReason == "length" {
		res.CutOffCalls++
	}
	res.ContextNotes = append(res.ContextNotes, fit.note(raw.Usage.PromptTokens))
}

// finalReply records a reply with no tool call as res's answer, and returns
// the error for one that is not an answer: cut off at the cap the call sent
// or at the context window, or empty. The error names provider.
func finalReply(provider string, res *RunResult, choice openaiChoice, fit contextFit) error {
	res.FinalText = choice.Message.Content
	res.StopReason = choice.FinishReason
	err := incompleteReply(provider, choice.FinishReason, "length", choice.Message.Content, fit.sent)
	if err == nil {
		return nil
	}
	if choice.FinishReason == "length" {
		err = cutOffReplyError(provider, err, fit)
	}
	return withUnclosedReasoning(err, choice.Message)
}

// cutOffReplyError is err, provider's error for a reply stopped at length,
// told more exactly where the guard knows more: the context window cut the
// reply short of the cap (windowStop), or the cap was one the guard set or
// lowered (capNote).
func cutOffReplyError(provider string, err error, fit contextFit) error {
	if stop := fit.windowStop(); stop != "" {
		return &incompleteReplyError{provider + ": " + stop}
	}
	if note := fit.capNote(); note != "" {
		return &incompleteReplyError{err.Error() + note}
	}
	return err
}

// withUnclosedReasoning is err, the error for msg, saying how much reasoning
// msg holds when it holds reasoning and no answer: a thinking model that
// never closed its reasoning puts everything there and leaves content empty.
func withUnclosedReasoning(err error, msg openaiMessage) error {
	if r := msg.thinking(); strings.TrimSpace(msg.Content) == "" && strings.TrimSpace(r) != "" {
		return &incompleteReplyError{fmt.Sprintf("%s; the reply holds %d bytes of reasoning and no answer", err, len(r))}
	}
	return err
}

// finalize is the one call a tool loop gets when its answer, res.FinalText,
// breaks the request's schema: the conversation so far, the answer, and a
// request for the JSON object alone, with no tools offered and the schema
// set. A complete reply replaces the answer. A call refused as a rate limit
// returns its error, as any other turn's does, so RateLimitedBackend retries
// the node's call. Any other failed call, or a reply that is cut off, empty
// or calls a tool, leaves the answer standing, with why in
// res.FinalizeError: the answer then fails its schema check after the call,
// and on_reject repairs it, as it would with no finalize call. Its usage is
// charged to res either way.
func (b *OpenAIBackend) finalize(ctx context.Context, req RunRequest, model string, messages []map[string]any, violations []string, res *RunResult) error {
	convo := append(append([]map[string]any(nil), messages...),
		map[string]any{"role": "assistant", "content": res.FinalText},
		map[string]any{"role": "user", "content": "That answer does not match the required JSON schema (" +
			workflow.SummarizeViolations(violations) + "). Reply with only the JSON object the schema describes."},
	)
	body := chatBody(model, convo, req)
	body["response_format"] = responseFormat(req.OutputSchema, req.SchemaName)
	choice, fit, err := b.chatTurn(ctx, req, body, res)
	if err != nil {
		return finalizeCallError(err, res)
	}
	reply, err := finalizeReply(b.provider(), choice, fit)
	if err != nil {
		res.FinalizeError = err.Error()
		return nil
	}
	res.FinalText, res.StopReason, res.SchemaAtDecode = reply.FinalText, reply.StopReason, true
	return nil
}

// finalizeCallError is what a finalize call that failed with err returns: the
// error, for one refused as a rate limit; else nil, with why in
// res.FinalizeError.
func finalizeCallError(err error, res *RunResult) error {
	if isRateLimitError(err) {
		return fmt.Errorf("the finalize call: %w", err)
	}
	res.FinalizeError = err.Error()
	return nil
}

// finalizeReply is the answer choice, the finalize call's reply, gives, or
// why it gives none: it calls a tool, though none was offered, or it is not
// an answer (finalReply), an error naming provider.
func finalizeReply(provider string, choice openaiChoice, fit contextFit) (RunResult, error) {
	var reply RunResult
	if len(choice.Message.ToolCalls) > 0 {
		return reply, errors.New("the reply calls a tool, and none was offered")
	}
	err := finalReply(provider, &reply, choice, fit)
	return reply, err
}

// responseFormat asks an OpenAI-compatible server to constrain decoding to s.
// strict is true only for a schema that qualifies for OpenAI's strict mode,
// which refuses any other; Ollama and LM Studio ignore it.
func responseFormat(s *schema.Schema, name string) map[string]any {
	return map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name":   responseFormatName(name),
			"schema": s,
			"strict": s.Strict(),
		},
	}
}

// schemaNameRunes are the runes OpenAI takes in a schema's name.
const schemaNameRunes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-"

// responseFormatName is name in the form OpenAI takes, a-z, A-Z, 0-9, _ and
// -, at most 64 characters; "output" when nothing is left.
func responseFormatName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if b.Len() == 64 {
			break
		}
		if !strings.ContainsRune(schemaNameRunes, r) {
			r = '_'
		}
		b.WriteRune(r)
	}
	return cmp.Or(b.String(), "output")
}

// schemaViolations checks a reply's text against s, as the engine checks a
// node's outputs: its JSON object, and a reply with none is one violation.
func schemaViolations(s *schema.Schema, text string) []string {
	return workflow.CheckOutput(s, workflow.ExtractJSONOutput(text))
}

// toolArguments decodes a tool call's arguments. Empty or null arguments are
// a call with none; anything else must be a JSON object.
func toolArguments(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return nil, fmt.Errorf("the tool was not run: its arguments are not a JSON object (%w): %s", err, truncate(raw, 200))
	}
	if input == nil {
		input = map[string]any{}
	}
	return input, nil
}

func openaiToolsFromRegistry(r *ToolRegistry) []map[string]any {
	tools := r.NeutralSchemas()
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.JSONSchema,
			},
		})
	}
	return out
}

type openaiResponse struct {
	Choices []openaiChoice `json:"choices"`
	Usage   struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

type openaiChoice struct {
	FinishReason string        `json:"finish_reason"`
	Message      openaiMessage `json:"message"`
}

type openaiMessage struct {
	Content string `json:"content"`
	// Reasoning is a thinking model's reasoning in the field Ollama returns
	// it in; ReasoningContent is the field other OpenAI-compatible servers
	// use.
	Reasoning        string           `json:"reasoning,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []openaiToolCall `json:"tool_calls,omitempty"`
}

// thinking is the message's reasoning, from whichever field holds it.
func (m openaiMessage) thinking() string {
	if m.Reasoning != "" {
		return m.Reasoning
	}
	return m.ReasoningContent
}

type openaiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// ListModels returns the model ids the endpoint serves, from GET <base>/models.
// A non-2xx answer is an error carrying its HTTP status. So is a 2xx body
// with no `data` list, such as the `{"error": …}` some servers answer a
// route they do not have with; the error shows the body.
func (b *OpenAIBackend) ListModels(ctx context.Context) ([]string, error) {
	return b.listModelsAt(ctx, b.BaseURL)
}

func (b *OpenAIBackend) listModelsAt(ctx context.Context, base string) ([]string, error) {
	req, err := b.newRequest(ctx, "GET", base+"/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s GET models: %w", b.provider(), err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, newHTTPStatusError(b.provider(), resp, body)
	}
	return modelIDs(b.provider(), resp.StatusCode, body)
}

// modelIDs are the ids in body, provider's GET /models reply that came with
// HTTP status. A body with no `data` list is an error that shows it.
func modelIDs(provider string, status int, body []byte) ([]string, error) {
	var out struct {
		Data *[]struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("%s models decode: %w (body: %s)", provider, err, truncate(string(body), 200))
	}
	if out.Data == nil {
		return nil, fmt.Errorf("%s GET models: HTTP %d with no model list (body: %s)", provider, status, truncate(string(body), 200))
	}
	ids := make([]string, 0, len(*out.Data))
	for _, m := range *out.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// newRequest is a request to endpoint carrying b's credentials: its key as
// the bearer token, and its organization when it has one.
func (b *OpenAIBackend) newRequest(ctx context.Context, method, endpoint string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("%s http req: %w", b.provider(), err)
	}
	req.Header.Set("Authorization", "Bearer "+b.APIKey)
	if b.Org != "" {
		req.Header.Set("OpenAI-Organization", b.Org)
	}
	return req, nil
}

func (b *OpenAIBackend) callChatCompletions(ctx context.Context, body map[string]any) (*openaiResponse, error) {
	js, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal %s body: %w", b.provider(), err)
	}
	req, err := b.newRequest(ctx, "POST", b.BaseURL+"/chat/completions", bytes.NewReader(js))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	var out openaiResponse
	if err := doProviderRequest(b.Client, req, b.provider(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}
