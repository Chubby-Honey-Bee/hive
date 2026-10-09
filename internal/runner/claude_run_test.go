package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// anthropicTextResponse returns a minimal valid Anthropic /v1/messages event
// stream for a single text block. ClaudeRunner streams every turn.
func anthropicTextResponse(text, stopReason string, inputTok, outputTok int) string {
	return anthropicStream(inputTok, outputTok, stopReason,
		`{"type":"text","text":""}`,
		`{"type":"text_delta","text":"`+text+`"}`)
}

// anthropicToolUseResponse returns an event stream for one tool_use block
// whose input arrives as an input_json_delta, as the API sends it.
func anthropicToolUseResponse(toolID, toolName, inputJSON string, inputTok, outputTok int) string {
	partial, _ := json.Marshal(inputJSON)
	return anthropicStream(inputTok, outputTok, "tool_use",
		`{"type":"tool_use","id":"`+toolID+`","name":"`+toolName+`","input":{}}`,
		`{"type":"input_json_delta","partial_json":`+string(partial)+`}`)
}

// anthropicStream frames one content block as the SSE events the Messages
// API streams: message_start (input usage), the block, message_delta (stop
// reason and output usage), message_stop.
func anthropicStream(inputTok, outputTok int, stopReason, block, delta string) string {
	ev := func(name, data string) string { return "event: " + name + "\ndata: " + data + "\n\n" }
	return ev("message_start", `{"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":`+claudeItoa(inputTok)+`,"output_tokens":0}}}`) +
		ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":`+block+`}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":`+delta+`}`) +
		ev("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"`+stopReason+`","stop_sequence":null},"usage":{"output_tokens":`+claudeItoa(outputTok)+`}}`) +
		ev("message_stop", `{"type":"message_stop"}`)
}

// claudeItoa is a simple int-to-string helper that avoids importing strconv.
func claudeItoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// newTestClaudeRunner builds a ClaudeRunner whose HTTP client is pointed at the
// given test server, so no real Anthropic API calls are made.
func newTestClaudeRunner(t *testing.T, srv *httptest.Server, reg *ToolRegistry) *ClaudeRunner {
	t.Helper()
	cr := &ClaudeRunner{
		Client: anthropic.NewClient(
			option.WithBaseURL(srv.URL),
			option.WithAPIKey("test-key"),
			option.WithHTTPClient(srv.Client()),
		),
		Model:      anthropic.ModelClaudeSonnet4_6,
		Registry:   reg,
		MaxTurns:   30,
		MaxTokens:  1024,
		PerCallTTL: 5 * time.Second,
	}
	return cr
}

// TestClaudeRunner_Run_NilRegistry confirms that Run returns an error immediately
// when no ToolRegistry is configured (no HTTP call is made).
func TestClaudeRunner_Run_NilRegistry(t *testing.T) {
	cr := &ClaudeRunner{
		Client:   anthropic.NewClient(option.WithAPIKey("test-key")),
		Registry: nil,
		MaxTurns: 5,
	}
	_, err := cr.Run(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error for nil registry, got nil")
	}
	if !strings.Contains(err.Error(), "no tool registry") {
		t.Errorf("error = %v; want 'no tool registry'", err)
	}
}

// TestClaudeRunner_Run_HappyPath exercises the end_turn path: the mock server
// returns a plain text response on the first turn and Run should return the
// trimmed text, correct token counts, and no error.
func TestClaudeRunner_Run_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(anthropicTextResponse("hello world", "end_turn", 10, 5)))
	}))
	defer srv.Close()

	reg := &ToolRegistry{Handlers: map[string]ToolHandler{}}
	cr := newTestClaudeRunner(t, srv, reg)
	res, err := cr.Run(context.Background(), "say hello")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinalText != "hello world" {
		t.Errorf("FinalText = %q; want %q", res.FinalText, "hello world")
	}
	if res.InputTokens != 10 {
		t.Errorf("InputTokens = %d; want 10", res.InputTokens)
	}
	if res.OutputTokens != 5 {
		t.Errorf("OutputTokens = %d; want 5", res.OutputTokens)
	}
	if res.StopReason != "end_turn" {
		t.Errorf("StopReason = %q; want end_turn", res.StopReason)
	}
	if res.Turns != 1 {
		t.Errorf("Turns = %d; want 1", res.Turns)
	}
	if res.ToolUses != 0 {
		t.Errorf("ToolUses = %d; want 0", res.ToolUses)
	}
}

// TestClaudeRunner_Run_WithSystemPrompt verifies that the System field is sent
// and does not break the happy path.
func TestClaudeRunner_Run_WithSystemPrompt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(anthropicTextResponse("ack", "end_turn", 3, 1)))
	}))
	defer srv.Close()

	reg := &ToolRegistry{Handlers: map[string]ToolHandler{}}
	cr := newTestClaudeRunner(t, srv, reg)
	cr.System = "you are a test assistant"
	res, err := cr.Run(context.Background(), "ping")
	if err != nil {
		t.Fatalf("Run with system prompt: %v", err)
	}
	if res.FinalText != "ack" {
		t.Errorf("FinalText = %q; want ack", res.FinalText)
	}
}

// TestClaudeRunner_Run_APIError verifies that an HTTP error from the Anthropic
// endpoint is surfaced as a non-nil error containing the turn number.
func TestClaudeRunner_Run_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"type":"error","error":{"type":"overloaded_error","message":"overloaded"}}`, http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	reg := &ToolRegistry{Handlers: map[string]ToolHandler{}}
	cr := newTestClaudeRunner(t, srv, reg)
	_, err := cr.Run(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected error for HTTP 503, got nil")
	}
	// Error message should mention the turn.
	if !strings.Contains(err.Error(), "turn 0") {
		t.Errorf("error %q does not mention turn 0", err.Error())
	}
}

// TestClaudeRunner_Run_ToolUseLoop exercises the multi-turn tool-use path:
// turn 1 returns a tool_use block, the registry dispatches it, turn 2 returns
// end_turn. The result should reflect 2 turns, 1 tool use, and the final text.
func TestClaudeRunner_Run_ToolUseLoop(t *testing.T) {
	turn := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		turn++
		if turn == 1 {
			_, _ = w.Write([]byte(anthropicToolUseResponse("tu_1", "echo_tool", `{"msg":"hi"}`, 10, 5)))
			return
		}
		_, _ = w.Write([]byte(anthropicTextResponse("done", "end_turn", 20, 4)))
	}))
	defer srv.Close()

	reg := &ToolRegistry{
		Handlers: map[string]ToolHandler{
			"echo_tool": func(_ context.Context, params map[string]any) (string, error) {
				msg, _ := params["msg"].(string)
				return "echoed: " + msg, nil
			},
		},
	}
	cr := newTestClaudeRunner(t, srv, reg)
	res, err := cr.Run(context.Background(), "use the tool")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinalText != "done" {
		t.Errorf("FinalText = %q; want done", res.FinalText)
	}
	if res.Turns != 2 {
		t.Errorf("Turns = %d; want 2", res.Turns)
	}
	if res.ToolUses != 1 {
		t.Errorf("ToolUses = %d; want 1", res.ToolUses)
	}
	if len(res.Invocations) != 1 {
		t.Fatalf("Invocations = %d; want 1", len(res.Invocations))
	}
	inv := res.Invocations[0]
	if inv.Tool != "echo_tool" {
		t.Errorf("Invocation.Tool = %q; want echo_tool", inv.Tool)
	}
	if inv.IsError {
		t.Errorf("Invocation.IsError should be false")
	}
	if !strings.Contains(inv.Output, "echoed: hi") {
		t.Errorf("Invocation.Output = %q; want 'echoed: hi'", inv.Output)
	}
}

// TestClaudeRunner_Run_UnknownTool verifies that an unknown tool name produces
// an error invocation (IsError=true) and the loop sends the error back to the
// model and continues. The second turn returns end_turn.
func TestClaudeRunner_Run_UnknownTool(t *testing.T) {
	turn := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		turn++
		if turn == 1 {
			_, _ = w.Write([]byte(anthropicToolUseResponse("tu_2", "no_such_tool", `{}`, 5, 3)))
			return
		}
		_, _ = w.Write([]byte(anthropicTextResponse("sorry", "end_turn", 8, 2)))
	}))
	defer srv.Close()

	reg := &ToolRegistry{Handlers: map[string]ToolHandler{}} // no handlers
	cr := newTestClaudeRunner(t, srv, reg)
	res, err := cr.Run(context.Background(), "use unknown tool")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Invocations) != 1 {
		t.Fatalf("Invocations = %d; want 1", len(res.Invocations))
	}
	inv := res.Invocations[0]
	if !inv.IsError {
		t.Errorf("Invocation.IsError should be true for unknown tool")
	}
	if !strings.Contains(inv.Output, "no_such_tool") {
		t.Errorf("Invocation.Output %q should mention the tool name", inv.Output)
	}
}

// TestClaudeRunner_Run_MaxTurns confirms that Run exits with an error after
// MaxTurns turns and the wrap-up call when the model never returns a
// non-tool-use stop reason.
func TestClaudeRunner_Run_MaxTurns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Always return tool_use to keep the loop going.
		_, _ = w.Write([]byte(anthropicToolUseResponse("tu_x", "echo_tool", `{"msg":"x"}`, 1, 1)))
	}))
	defer srv.Close()

	reg := &ToolRegistry{
		Handlers: map[string]ToolHandler{
			"echo_tool": func(_ context.Context, _ map[string]any) (string, error) {
				return "ok", nil
			},
		},
	}
	cr := newTestClaudeRunner(t, srv, reg)
	cr.MaxTurns = 3 // small cap for fast test
	res, err := cr.Run(context.Background(), "loop")
	if err == nil {
		t.Fatal("expected max-turns error, got nil")
	}
	if !strings.Contains(err.Error(), "max_turns") {
		t.Errorf("error %q does not mention 'max_turns'", err.Error())
	}
	if res.Turns != 4 {
		t.Errorf("Turns = %d; want 4, the three turns and the wrap-up", res.Turns)
	}
}

// After MaxTurns turns of tool use the runner makes one wrap-up call with
// tool_choice none and, in the last user turn after the tool results, the
// message that no tool call is left; the model's reply there is the answer.
func TestClaudeRunner_Run_MaxTurnsWrapsUp(t *testing.T) {
	var last map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		last = body
		w.Header().Set("Content-Type", "text/event-stream")
		if choice, _ := body["tool_choice"].(map[string]any); choice["type"] == "none" {
			_, _ = w.Write([]byte(anthropicTextResponse("wrapped up", "end_turn", 1, 1)))
			return
		}
		_, _ = w.Write([]byte(anthropicToolUseResponse("tu_x", "echo_tool", `{"msg":"x"}`, 1, 1)))
	}))
	defer srv.Close()
	reg := &ToolRegistry{Handlers: map[string]ToolHandler{
		"echo_tool": func(context.Context, map[string]any) (string, error) { return "ok", nil },
	}}
	cr := newTestClaudeRunner(t, srv, reg)
	cr.MaxTurns = 3
	res, err := cr.Run(context.Background(), "loop")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinalText != "wrapped up" || !res.WrappedUp || res.Turns != 4 {
		t.Errorf("final %q, wrapped up %v, turns %d; want the wrap-up's answer after 4 calls", res.FinalText, res.WrappedUp, res.Turns)
	}
	msgs, _ := last["messages"].([]any)
	lastMsg, _ := msgs[len(msgs)-1].(map[string]any)
	blocks, _ := lastMsg["content"].([]any)
	first, _ := blocks[0].(map[string]any)
	text, _ := blocks[len(blocks)-1].(map[string]any)
	if lastMsg["role"] != "user" || first["type"] != "tool_result" || !strings.Contains(fmt.Sprint(text["text"]), "no tool call is left") {
		t.Errorf("the wrap-up's last message is %v; want the user turn holding the tool results, then the wrap-up text", lastMsg)
	}
}

// TestClaudeRunner_Run_ContextCancelled verifies that a pre-cancelled context
// surfaces as an error (from the HTTP layer) on the first turn.
func TestClaudeRunner_Run_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Never reached — context is cancelled before the request lands.
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before making the call

	reg := &ToolRegistry{Handlers: map[string]ToolHandler{}}
	cr := newTestClaudeRunner(t, srv, reg)
	_, err := cr.Run(ctx, "hello")
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

// TestClaudeRunner_Run_ToolOutputTruncation verifies that tool handler output
// longer than 50 000 bytes is recorded whole; the result sent is the one cut.
func TestClaudeRunner_Run_ToolOutputTruncation(t *testing.T) {
	turn := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		turn++
		if turn == 1 {
			_, _ = w.Write([]byte(anthropicToolUseResponse("tu_big", "big_tool", `{}`, 1, 1)))
			return
		}
		_, _ = w.Write([]byte(anthropicTextResponse("ok", "end_turn", 1, 1)))
	}))
	defer srv.Close()

	bigOutput := strings.Repeat("x", 51_000)
	reg := &ToolRegistry{
		Handlers: map[string]ToolHandler{
			"big_tool": func(_ context.Context, _ map[string]any) (string, error) {
				return bigOutput, nil
			},
		},
	}
	cr := newTestClaudeRunner(t, srv, reg)
	res, err := cr.Run(context.Background(), "get big output")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Invocations) != 1 {
		t.Fatalf("Invocations = %d; want 1", len(res.Invocations))
	}
	inv := res.Invocations[0]
	if len(inv.Output) <= 50_000 {
		t.Errorf("stored output length %d; want >50000, the whole output", len(inv.Output))
	}
	// The audit row keeps the tool's whole output, as every backend's does;
	// the result sent is the one cut (TestToolResult_EachBackendHoldsItToTheCap).
	if inv.Output != bigOutput {
		t.Errorf("stored output is %d bytes, want the tool's whole %d", len(inv.Output), len(bigOutput))
	}
}

// TestClaudeRunner_Run_TokenAccumulation checks that input/output tokens are
// accumulated across multiple turns.
func TestClaudeRunner_Run_TokenAccumulation(t *testing.T) {
	turn := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		turn++
		if turn == 1 {
			_, _ = w.Write([]byte(anthropicToolUseResponse("tu_3", "noop", `{}`, 10, 5)))
			return
		}
		_, _ = w.Write([]byte(anthropicTextResponse("final", "end_turn", 20, 8)))
	}))
	defer srv.Close()

	reg := &ToolRegistry{
		Handlers: map[string]ToolHandler{
			"noop": func(_ context.Context, _ map[string]any) (string, error) {
				return "done", nil
			},
		},
	}
	cr := newTestClaudeRunner(t, srv, reg)
	res, err := cr.Run(context.Background(), "accumulate tokens")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.InputTokens != 30 { // 10 + 20
		t.Errorf("InputTokens = %d; want 30", res.InputTokens)
	}
	if res.OutputTokens != 13 { // 5 + 8
		t.Errorf("OutputTokens = %d; want 13", res.OutputTokens)
	}
}

// TestClaudeRunner_Run_CacheTokens: a turn's input is its uncached input
// plus its cache reads plus its cache writes, read from message_start, or
// from message_delta where it reports them, as Ollama 0.34.4 does after an
// estimate in message_start. The reads are cached tokens and the writes
// cache writes, with their 1-hour part.
func TestClaudeRunner_Run_CacheTokens(t *testing.T) {
	type counts struct{ in, read, write, write1h int64 }
	usage := func(c counts, out int64) string {
		return fmt.Sprintf(`{"input_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d,"cache_creation":{"ephemeral_1h_input_tokens":%d,"ephemeral_5m_input_tokens":%d},"output_tokens":%d}`,
			c.in, c.read, c.write, c.write1h, c.write-c.write1h, out)
	}
	deltaUsage := func(c counts, out int64) string {
		return fmt.Sprintf(`{"input_tokens":%d,"cache_read_input_tokens":%d,"output_tokens":%d}`, c.in, c.read, out)
	}
	const out = 70
	for _, c := range []struct {
		name              string
		startUsage, delta string
		// want is the turn's counts, from whichever event holds them.
		want counts
	}{
		{"in message_start", usage(counts{40, 9_000, 3_000, 1_000}, 0), `{"output_tokens":70}`, counts{40, 9_000, 3_000, 1_000}},
		{"a warm prompt in message_delta", `{"input_tokens":2010,"output_tokens":0}`, deltaUsage(counts{4, 2_926, 0, 0}, out), counts{4, 2_926, 0, 0}},
		{"a cold prompt in message_delta", `{"input_tokens":2010,"output_tokens":0}`, deltaUsage(counts{2_930, 0, 0, 0}, out), counts{2_930, 0, 0, 0}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ev := func(name, data string) string { return "event: " + name + "\ndata: " + data + "\n\n" }
			body := ev("message_start", `{"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"stop_sequence":null,"usage":`+c.startUsage+`}}`) +
				ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
				ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`) +
				ev("content_block_stop", `{"type":"content_block_stop","index":0}`) +
				ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":`+c.delta+`}`) +
				ev("message_stop", `{"type":"message_stop"}`)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()
			res, err := newTestClaudeRunner(t, srv, &ToolRegistry{Handlers: map[string]ToolHandler{}}).Run(context.Background(), "p")
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			w := c.want
			got := counts{res.InputTokens, res.CachedInputTokens, res.CacheWriteTokens, res.CacheWrite1hTokens}
			if want := (counts{w.in + w.read + w.write, w.read, w.write, w.write1h}); got != want || res.OutputTokens != out {
				t.Errorf("in/cached/written/1h = %+v, out %d; want %+v, %d", got, res.OutputTokens, want, out)
			}
		})
	}
}
