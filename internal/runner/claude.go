package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/respjson"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
)

// _anthropicSeedWarnOnce deduplicates warnAnthropicSeedOnce's warning.
var _anthropicSeedWarnOnce sync.Once

// warnAnthropicSeedOnce emits a one-time-per-process stderr warning that the
// Anthropic Messages API does not expose a seed parameter. Deduplicated via
// sync.Once so busy parallel fans don't spam the log.
func warnAnthropicSeedOnce() {
	_anthropicSeedWarnOnce.Do(func() {
		fmt.Fprintln(os.Stderr, "[agent-run] anthropic backend ignores --seed (Messages API has no seed param); using temperature=0 only")
	})
}

// ClaudeRunner drives a single Claude Messages conversation to completion,
// resolving every `tool_use` block via the registry until the model returns
// a plain-text response (stop_reason == end_turn).
type ClaudeRunner struct {
	Client     anthropic.Client
	Model      anthropic.Model
	System     string
	Registry   *ToolRegistry
	MaxTurns   int           // safety cap (default 30)
	MaxTokens  int64         // per-turn output cap (default 8192)
	PerCallTTL time.Duration // longest wait for a turn's next stream event (default 5m)
	// Temperature, if non-nil, overrides the API default. Use 0.0 for
	// the most deterministic output the API can provide. Nil means
	// "leave at API default" (do not send the field at all).
	Temperature *float64
	// TopP, if non-nil, is sent as top_p. Nil sends none.
	TopP *float64
	// Logf, when set, is the run log, for a tool result it cuts.
	Logf func(string, ...any)
}

// NewClaudeRunner builds a runner with sensible defaults. apiKey may be empty,
// in which case the SDK falls back to $ANTHROPIC_API_KEY.
func NewClaudeRunner(apiKey, model, system string, registry *ToolRegistry) *ClaudeRunner {
	var opts []option.RequestOption
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}
	return &ClaudeRunner{
		Client:     anthropic.NewClient(opts...),
		Model:      ResolveModelAlias(model),
		System:     system,
		Registry:   registry,
		MaxTurns:   30,
		MaxTokens:  8192,
		PerCallTTL: 5 * time.Minute,
	}
}

// ResolveModelAlias maps friendly names used in workflow YAML to SDK model IDs.
// A name that is an alias in the models config (sonnet, opus and haiku always
// are; empty and the claude- forms count as those three) resolves through it,
// as the pin and the cost meter do, so an alias override there changes the
// model sent too. Version-pinned names stay fixed. Every other name passes
// through unchanged, colons and all (so callers can pin specific snapshots
// or gateway ids).
func ResolveModelAlias(name string) anthropic.Model {
	key := strings.ToLower(strings.TrimSpace(name))
	if m, ok := pinnedClaudeModels[key]; ok {
		return m
	}
	if alias, ok := claudeAliasSpellings[key]; ok {
		key = alias
	}
	if _, ok := models.Load().Aliases[key]; ok {
		return anthropic.Model(resolveAliasForPricing(key))
	}
	return anthropic.Model(name)
}

// pinnedClaudeModels are the version-pinned names ResolveModelAlias keeps
// fixed, in lower case.
var pinnedClaudeModels = map[string]anthropic.Model{
	"sonnet-4-6": anthropic.ModelClaudeSonnet4_6,
	"opus-4-6":   anthropic.ModelClaudeOpus4_6,
	"haiku-4-5":  anthropic.ModelClaudeHaiku4_5_20251001,
}

// claudeAliasSpellings are the other names, in lower case, that
// ResolveModelAlias reads as the sonnet, opus and haiku aliases.
var claudeAliasSpellings = map[string]string{
	"":              "sonnet",
	"claude-sonnet": "sonnet",
	"claude-opus":   "opus",
	"claude-haiku":  "haiku",
}

// RunResult is the outcome of a single-prompt run (i.e. one workflow node).
// Backend-neutral: both SDK and CLI backends populate this shape.
type RunResult struct {
	FinalText    string
	Turns        int
	ToolUses     int
	Invocations  []ToolInvocation
	InputTokens  int64
	OutputTokens int64
	// StopReason mirrors Anthropic stop reasons ("end_turn", "tool_use",
	// "max_tokens", "stop_sequence"). The CLI backend reports "end_turn"
	// on clean exit and "max_tokens" or "error" when truncated.
	StopReason string
	// CutOffCalls counts the calls whose reply the provider stopped at the
	// output cap: finish_reason length, MAX_TOKENS or max_tokens. The Gemini
	// CLI reports no stop reason; it counts one for a reply whose
	// INVALID_STREAM error says the model's response was truncated at the
	// token limit: a lower bound, since gemini-cli retries such a stream.
	CutOffCalls int
	// SchemaAtDecode is true when FinalText came from a call that sent the
	// request's OutputSchema for the server to constrain decoding with
	// (response_format on the OpenAI-compatible backend). Whether the server
	// did is what the constraint probe measures.
	SchemaAtDecode bool
	// ToolCallsUnreported is true when the backend ran its own tool loop and
	// reports none of its calls, as the claude and gemini CLIs do:
	// Invocations is then empty whether or not a tool ran.
	ToolCallsUnreported bool
	// CachedInputTokens is the part of InputTokens the provider read from
	// its prompt cache, for a backend that reports it (Gemini, the gemini
	// CLI, the claude CLI and the Anthropic SDK). It is priced at the
	// model's cached rate (usageCostUSDx10000).
	CachedInputTokens int64
	// CacheWriteTokens is the part of InputTokens the provider wrote to its
	// prompt cache, and CacheWrite1hTokens the part of those written with a
	// 1-hour lifetime, for a backend that reports them (the claude CLI and
	// the Anthropic SDK). They are priced at the model's cache-write rates.
	CacheWriteTokens   int64
	CacheWrite1hTokens int64
	// ByModel splits the tokens by the model that spent them, for a backend
	// that names them: the gemini and claude CLIs, whose one run may call
	// more than one model. When set, each entry is priced at its own model,
	// not the node's, and the token counts are the entries' sums.
	ByModel []ModelUsage
	// UsageUnreported is true when the backend reports no token counts, as
	// a gemini CLI without --output-format json does: InputTokens and
	// OutputTokens are then 0 whatever the call used, so its cost is unknown
	// and it is counted unmetered. UsageUnreportedWhy says why.
	UsageUnreported    bool
	UsageUnreportedWhy string
	// ItemResults are a parallel_fan's item results, one per item that
	// returned one, or the attempts a rate-limit retry made that answered
	// calls, the last one included (foldSpent). recordSpend prices each as
	// its own call, since one reply may report usage, or name models, that
	// another's does not. The token counts, Turns and CutOffCalls are their
	// sums.
	ItemResults []*RunResult
	// FinalizeError is why the OpenAI-compatible backend's finalize call,
	// made after a tool loop whose answer broke the schema, gave no answer:
	// FinalText is then that tool loop's answer. Empty when no finalize call
	// was made or it answered.
	FinalizeError string
	// WrappedUp is true when FinalText came from a wrap-up call: the tool
	// loop used all its turns with the model still calling tools, and one
	// more call, with no tool call possible, asked for the answer.
	WrappedUp bool
	// ContextNotes are the OpenAI-compatible backend's lines for the run
	// log, one per call in order, the tool loop's turns and the finalize
	// call included: the prompt's estimated size, the server's count and
	// the context window (runner.md § Context window guard).
	ContextNotes []string
}

// ModelUsage is one model's tokens in a call. InputTokens counts the ones
// read from and written to the prompt cache too, and OutputTokens the
// thought tokens.
type ModelUsage struct {
	Model              string
	InputTokens        int64
	CachedInputTokens  int64
	CacheWriteTokens   int64
	CacheWrite1hTokens int64
	OutputTokens       int64
}

// spentTokens reports whether u counts any input or output tokens.
func (u ModelUsage) spentTokens() bool { return u.InputTokens > 0 || u.OutputTokens > 0 }

// Run sends `prompt` as the first user message and drives the tool-use loop
// until the model either (a) produces a non-tool-use stop, or (b) hits
// MaxTurns. A loop that hits MaxTurns asks for its answer with one wrap-up
// call, as the OpenAI-compatible and Gemini backends' do (wrapUp); one that
// gives none fails: stop reason max_turns, an incomplete reply, never a rate
// limit, with every call's usage charged.
func (c *ClaudeRunner) Run(ctx context.Context, prompt string) (*RunResult, error) {
	if c.Registry == nil {
		return nil, fmt.Errorf("no tool registry")
	}
	result := &RunResult{}
	msgs := []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
	}
	for turn := 0; turn < c.MaxTurns; turn++ {
		var done bool
		var err error
		msgs, done, err = c.turn(ctx, turn, msgs, result)
		if done {
			return result, err
		}
	}
	return result, c.wrapUp(ctx, msgs, result)
}

// wrapUp asks for the answer after the loop's MaxTurns turns ran out with the
// model still calling tools (wrapUpReply). A wrap-up that gives none ends the
// loop out of turns (wrapUpFailed).
func (c *ClaudeRunner) wrapUp(ctx context.Context, msgs []anthropic.MessageParam, result *RunResult) error {
	answer, stop, err := c.wrapUpReply(ctx, msgs, result)
	if err != nil {
		result.StopReason = "max_turns"
		return wrapUpFailed("anthropic", c.MaxTurns, err)
	}
	result.FinalText, result.StopReason, result.WrappedUp = answer, stop, true
	return nil
}

// wrapUpReply makes the wrap-up call, with tool_choice none, after the
// message that no tool turn is left (wrapUpPrompt), added to the last user
// turn so the turns still alternate. It returns the answer and its stop
// reason, held to the checks any final reply is (incompleteReply); the error
// says why it gives none.
func (c *ClaudeRunner) wrapUpReply(ctx context.Context, msgs []anthropic.MessageParam, result *RunResult) (answer, stop string, err error) {
	params := c.params(withWrapUpText(msgs, c.MaxTurns))
	params.ToolChoice = anthropic.ToolChoiceUnionParam{OfNone: &anthropic.ToolChoiceNoneParam{}}
	resp, err := c.call(ctx, c.MaxTurns, params)
	if err != nil {
		return "", "", err
	}
	chargeClaudeCall(result, resp)
	_, toolUses, finalText := claudeReplyBlocks(resp.Content)
	if len(toolUses) > 0 {
		return "", "", errWrapUpCalledTool
	}
	answer, stop = strings.TrimSpace(finalText), string(resp.StopReason)
	return answer, stop, incompleteReply("anthropic", stop, string(anthropic.StopReasonMaxTokens), answer, c.MaxTokens)
}

// withWrapUpText is msgs with wrapUpPrompt added to their last message: after
// a tool turn, the user turn that holds the tool results.
func withWrapUpText(msgs []anthropic.MessageParam, maxTurns int) []anthropic.MessageParam {
	out := append([]anthropic.MessageParam(nil), msgs...)
	last := out[len(out)-1]
	last.Content = append(append([]anthropic.ContentBlockParamUnion(nil), last.Content...), anthropic.NewTextBlock(wrapUpPrompt(maxTurns)))
	out[len(out)-1] = last
	return out
}

// turn makes call number turn of the conversation msgs and charges it to
// result. It returns the conversation with the reply and, for a reply that
// calls tools, their results. done is set when the conversation ends, with
// the error it ends with: a reply that calls no tool is the final one.
func (c *ClaudeRunner) turn(ctx context.Context, turn int, msgs []anthropic.MessageParam, result *RunResult) ([]anthropic.MessageParam, bool, error) {
	resp, err := c.call(ctx, turn, c.params(msgs))
	if err != nil {
		return msgs, true, err
	}
	chargeClaudeCall(result, resp)
	assistantBlocks, toolUses, finalText := claudeReplyBlocks(resp.Content)
	if len(assistantBlocks) > 0 {
		msgs = append(msgs, anthropic.NewAssistantMessage(assistantBlocks...))
	}
	if finalClaudeReply(resp.StopReason, toolUses) {
		result.FinalText = strings.TrimSpace(finalText)
		return msgs, true, incompleteReply("anthropic", string(resp.StopReason), string(anthropic.StopReasonMaxTokens), result.FinalText, c.MaxTokens)
	}
	return append(msgs, anthropic.NewUserMessage(c.runTools(ctx, toolUses, result)...)), false, nil
}

// params is the request for the conversation msgs: the runner's model, cap,
// system prompt and tools, and its sampling when set.
func (c *ClaudeRunner) params(msgs []anthropic.MessageParam) anthropic.MessageNewParams {
	var systemBlocks []anthropic.TextBlockParam
	if c.System != "" {
		systemBlocks = []anthropic.TextBlockParam{{Text: c.System}}
	}
	params := anthropic.MessageNewParams{
		Model:     c.Model,
		MaxTokens: c.MaxTokens,
		Messages:  msgs,
		System:    systemBlocks,
		Tools:     c.Registry.Schemas,
	}
	if c.Temperature != nil {
		params.Temperature = anthropic.Float(*c.Temperature)
	}
	if c.TopP != nil {
		params.TopP = anthropic.Float(*c.TopP)
	}
	return params
}

// call sends params as call number turn, within a slot at the endpoint.
// Streamed, not Messages.New: the SDK refuses a non-streaming call whose
// max_tokens implies more than ten minutes (anything above 21,333), which is
// every sonnet and opus cap in the models config. Each call first waits for
// a slot at the endpoint, as the other REST backends' do, so a server on
// this machine behind ANTHROPIC_BASE_URL gets one call at a time.
func (c *ClaudeRunner) call(ctx context.Context, turn int, params anthropic.MessageNewParams) (*anthropic.Message, error) {
	release, err := acquireEndpointSlot(ctx, "anthropic", endpointFor(BackendAnthropic))
	if err != nil {
		return nil, err
	}
	// Released by a defer, so a panic in the call frees the slot as it
	// unwinds.
	resp, err := func() (*anthropic.Message, error) {
		defer release()
		return c.streamTurn(ctx, params)
	}()
	if err != nil {
		return nil, fmt.Errorf("turn %d: messages stream: %w", turn, err)
	}
	return resp, nil
}

// chargeClaudeCall adds an answered call's usage to result, with its stop
// reason. Counted once answered, as the other backends count, so a call
// refused or cut off mid-stream is no turn (callsMade). input_tokens is the
// uncached remainder: the prompt is it plus the cache reads plus the cache
// writes. A server this backend is pointed at may cache without being
// asked, as Ollama does.
func chargeClaudeCall(result *RunResult, resp *anthropic.Message) {
	result.Turns++
	u := resp.Usage
	result.InputTokens += u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	result.CachedInputTokens += u.CacheReadInputTokens
	result.CacheWriteTokens += u.CacheCreationInputTokens
	result.CacheWrite1hTokens += min(u.CacheCreation.Ephemeral1hInputTokens, u.CacheCreationInputTokens)
	result.OutputTokens += u.OutputTokens
	result.StopReason = string(resp.StopReason)
	if resp.StopReason == anthropic.StopReasonMaxTokens {
		result.CutOffCalls++
	}
}

// claudeReplyBlocks walks a reply's content once: the blocks that echo it in
// the conversation, its tool_use blocks, and its text.
func claudeReplyBlocks(content []anthropic.ContentBlockUnion) ([]anthropic.ContentBlockParamUnion, []anthropic.ToolUseBlock, string) {
	assistantBlocks := make([]anthropic.ContentBlockParamUnion, 0, len(content))
	var toolUses []anthropic.ToolUseBlock
	var finalText strings.Builder
	for _, block := range content {
		switch v := block.AsAny().(type) {
		case anthropic.TextBlock:
			assistantBlocks = append(assistantBlocks, anthropic.NewTextBlock(v.Text))
			finalText.WriteString(v.Text)
		case anthropic.ToolUseBlock:
			// Echo the tool_use in history.
			var input any
			_ = json.Unmarshal(v.Input, &input)
			assistantBlocks = append(assistantBlocks, anthropic.NewToolUseBlock(v.ID, input, v.Name))
			toolUses = append(toolUses, v)
		}
	}
	return assistantBlocks, toolUses, finalText.String()
}

// finalClaudeReply reports whether a reply that stopped for stop and holds
// toolUses ends the conversation: any reply but a tool_use stop with a tool
// to run.
func finalClaudeReply(stop anthropic.StopReason, toolUses []anthropic.ToolUseBlock) bool {
	return stop != anthropic.StopReasonToolUse || len(toolUses) == 0
}

// runTools runs every tool_use, in order, and returns their results. Each
// call counts in result and is recorded there and in the registry.
func (c *ClaudeRunner) runTools(ctx context.Context, toolUses []anthropic.ToolUseBlock, result *RunResult) []anthropic.ContentBlockParamUnion {
	toolResults := make([]anthropic.ContentBlockParamUnion, 0, len(toolUses))
	for _, use := range toolUses {
		result.ToolUses++
		block, inv := c.runTool(ctx, use)
		toolResults = append(toolResults, block)
		result.Invocations = append(result.Invocations, inv)
		c.Registry.record(inv)
	}
	return toolResults
}

// runTool runs one tool_use with the registry's handler and returns its
// tool_result and its audit row. The name may be an alias; the row names the
// tool. The result is held to toolResultCap, since this backend is given no
// context window (toolResultBound), and the row keeps the tool's whole
// output, as the other backends' rows do.
func (c *ClaudeRunner) runTool(ctx context.Context, use anthropic.ToolUseBlock) (anthropic.ContentBlockParamUnion, ToolInvocation) {
	tool := workflow.CanonicalTool(use.Name)
	inv := ToolInvocation{
		Timestamp: Timestamp(),
		Tool:      tool,
		Input:     string(use.Input),
	}
	var input map[string]any
	_ = json.Unmarshal(use.Input, &input)
	handler, ok := c.Registry.Handlers[tool]
	t0 := time.Now()
	inv.Output, inv.IsError = invokeClaudeTool(ctx, handler, ok, use.Name, input)
	inv.DurationS = time.Since(t0).Seconds()
	result, kept := toolResultBound{}.hold(inv.Output, c.Registry.restOf(use.Name, input))
	logToolCut(c.Logf, use.Name, kept, len(inv.Output), false)
	return anthropic.NewToolResultBlock(use.ID, result, inv.IsError), inv
}

// invokeClaudeTool runs handler, the registry's handler for the tool name
// calls, on input, and returns its output, or its error's text and true
// when it fails; ok is false when the registry has none.
func invokeClaudeTool(ctx context.Context, handler ToolHandler, ok bool, name string, input map[string]any) (string, bool) {
	if !ok {
		return fmt.Sprintf("unknown tool %q", name), true
	}
	out, err := handler(ctx, input)
	if err != nil {
		return err.Error(), true
	}
	return out, false
}

// streamTurn sends one turn as a streaming request and accumulates the
// events into the message Messages.New would have returned. A stream that
// ends before message_stop is an error, not a short answer. PerCallTTL bounds
// the wait for each event, not the whole turn: a turn that writes a full
// 65536-token cap streams for longer than a fixed five minutes, while a
// stalled stream still fails after PerCallTTL.
func (c *ClaudeRunner) streamTurn(ctx context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	idle := time.AfterFunc(c.PerCallTTL, func() {
		cancel(fmt.Errorf("no stream event for %s", c.PerCallTTL))
	})
	defer idle.Stop()
	stream := c.Client.Messages.NewStreaming(ctx, params)
	defer stream.Close()
	msg := &anthropic.Message{}
	stopped, err := c.readStream(stream, msg, idle)
	if err != nil {
		return nil, err
	}
	if err := streamEndError(ctx, stream.Err(), stopped); err != nil {
		return nil, err
	}
	return msg, nil
}

// readStream accumulates stream's events into msg, holding idle, the stall
// timer, off for PerCallTTL at each, and reports whether message_stop
// arrived.
func (c *ClaudeRunner) readStream(stream *ssestream.Stream[anthropic.MessageStreamEventUnion], msg *anthropic.Message, idle *time.Timer) (bool, error) {
	stopped := false
	for stream.Next() {
		idle.Reset(c.PerCallTTL)
		ev := stream.Current()
		if err := msg.Accumulate(ev); err != nil {
			return false, err
		}
		keepCumulativeUsage(msg, ev)
		stopped = stopped || ev.Type == "message_stop"
	}
	return stopped, nil
}

// keepCumulativeUsage keeps the input counts a message_delta event carries.
// message_delta's usage is cumulative. Accumulate keeps only its output
// tokens, but a server may count the input there and not in message_start:
// Ollama 0.34.4 sends an estimate in message_start, and the prompt's count
// and its cache reads in message_delta.
func keepCumulativeUsage(msg *anthropic.Message, ev anthropic.MessageStreamEventUnion) {
	if ev.Type != "message_delta" {
		return
	}
	d := ev.Usage
	msg.Usage.InputTokens = deltaCount(d.JSON.InputTokens, d.InputTokens, msg.Usage.InputTokens)
	msg.Usage.CacheReadInputTokens = deltaCount(d.JSON.CacheReadInputTokens, d.CacheReadInputTokens, msg.Usage.CacheReadInputTokens)
	msg.Usage.CacheCreationInputTokens = deltaCount(d.JSON.CacheCreationInputTokens, d.CacheCreationInputTokens, msg.Usage.CacheCreationInputTokens)
}

// deltaCount is n, a count a message_delta event carries, when field shows
// the event sent it, else kept.
func deltaCount(field respjson.Field, n, kept int64) int64 {
	if field.Valid() {
		return n
	}
	return kept
}

// streamEndError is the error a stream that ended with err ends the turn
// with: the cause that cancelled ctx, such as a stall, when ctx ended it;
// else err; else, when message_stop never arrived, that. nil for a stream
// that stopped.
func streamEndError(ctx context.Context, err error, stopped bool) error {
	if err != nil {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		return err
	}
	if !stopped {
		return fmt.Errorf("stream ended before message_stop")
	}
	return nil
}
