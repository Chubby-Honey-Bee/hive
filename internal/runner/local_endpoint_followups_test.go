package runner

// Tests for the local-endpoint follow-ups: reasoning goes back on tool turns
// in the field the server used, reasoning_effort reaches only a model that can
// take it, calls cut off at the output cap are counted per node, and the
// Anthropic SDK backend waits for an endpoint slot. Every server is an
// httptest fake; no model is called.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// toolTurnWith renders a one-choice chat completion that calls read_file on
// path, with reasoning in field (none when field is "").
func toolTurnWith(field, reasoning, path string) string {
	msg := map[string]any{
		"content": "",
		"tool_calls": []any{map[string]any{
			"id": "call_1", "type": "function",
			"function": map[string]any{"name": "read_file", "arguments": fmt.Sprintf(`{"path":%q}`, path)},
		}},
	}
	if field != "" {
		msg[field] = reasoning
	}
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": msg}},
		"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1},
	})
	return string(b)
}

// assistantMessages are the assistant messages a request body carries.
func assistantMessages(body map[string]any) []map[string]any {
	var out []map[string]any
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		if mm, _ := m.(map[string]any); mm["role"] == "assistant" {
			out = append(out, mm)
		}
	}
	return out
}

// The reasoning a server returns with a tool call goes back on the assistant
// message in the field the server used, and only in that one.
func TestOpenAIBackend_ReasoningGoesBackInItsOwnField(t *testing.T) {
	const reasoning = "I should read the file before answering."
	fields := []string{"reasoning", "reasoning_content"}
	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			f := &fakeOpenAI{}
			f.reply = func(map[string]any) string {
				if f.chats.Load() == 1 {
					return toolTurnWith(field, reasoning, ".")
				}
				return chatReply("done", "stop", 1, 1)
			}
			srv := httptest.NewServer(f)
			defer srv.Close()
			b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
			if _, err := b.Run(context.Background(), RunRequest{Prompt: "hi", Registry: NewToolRegistry(t.TempDir(), nil, NewFSRecorder())}); err != nil {
				t.Fatal(err)
			}
			if len(f.bodies) != 2 {
				t.Fatalf("%d calls, want 2", len(f.bodies))
			}
			echoed := assistantMessages(f.bodies[1])
			if len(echoed) != 1 {
				t.Fatalf("second call carries %d assistant messages, want 1", len(echoed))
			}
			for _, other := range fields {
				wantField(t, echoed[0], other, other == field, reasoning)
			}
		})
	}
	// A final reply whose answer is empty and whose reasoning_content is not
	// fails naming the reasoning's size, as one in `reasoning` does.
	t.Run("reasoning_content only", func(t *testing.T) {
		const thought = "thinking that never closed"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := json.Marshal(map[string]any{
				"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": "", "reasoning_content": thought}}},
			})
			_, _ = w.Write(b)
		}))
		defer srv.Close()
		b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
		_, err := b.Run(context.Background(), RunRequest{Prompt: "hi", Registry: &ToolRegistry{Handlers: map[string]ToolHandler{}}})
		want := fmt.Sprintf("the reply holds %d bytes of reasoning and no answer", len(thought))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want one naming %q", err, want)
		}
	})
}

// reasoning_effort goes to a model unless the server reports it cannot
// think: then never. When the server does not say, every level goes as set,
// none included. The server is asked once per model for a backend's cache,
// and a level not sent is logged once.
func TestOpenAIBackend_ReasoningEffortOnlyToAThinkingModel(t *testing.T) {
	const model = "m:1b"
	servers := []struct {
		name string
		caps []string // nil: /api/show answers 404
		v1   bool     // the base URL ends in /v1, as Ollama's does
	}{
		{"ollama, thinking", []string{"completion", "thinking"}, true},
		{"ollama, no thinking", []string{"completion", "tools"}, true},
		{"no /api/show", nil, true},
		{"base URL without /v1", []string{"completion", "tools"}, false},
	}
	for _, s := range servers {
		for _, level := range []string{"", "none", "low", "high"} {
			t.Run(fmt.Sprintf("%s/%q", s.name, level), func(t *testing.T) {
				f := &fakeOpenAI{reply: func(map[string]any) string { return chatReply("ok", "stop", 1, 1) }}
				if s.caps != nil {
					f.caps = map[string][]string{model: s.caps}
				}
				srv := httptest.NewServer(f)
				defer srv.Close()
				base := srv.URL
				if s.v1 {
					base += "/v1"
				}
				b := &OpenAIBackend{APIKey: "k", BaseURL: base, Client: srv.Client(), thinkingCache: &thinkingCache{}}
				var notes atomic.Int64
				logf := func(format string, a ...any) {
					if strings.HasPrefix(fmt.Sprintf(format, a...), "reasoning: ") {
						notes.Add(1)
					}
				}
				const calls = 2
				for range calls {
					if _, err := b.Run(context.Background(), RunRequest{Prompt: "hi", Model: model, Reasoning: level, Logf: logf,
						Registry: &ToolRegistry{Handlers: map[string]ToolHandler{}}}); err != nil {
						t.Fatal(err)
					}
				}

				// What the endpoint says, computed from the fake's setup.
				asked := s.v1 && level != ""
				thinks := asked && slices.Contains(s.caps, "thinking")
				cannot := asked && s.caps != nil && !thinks
				sent := level != "" && !cannot
				for i, body := range f.bodies {
					if _, has := body["reasoning_effort"]; has != sent {
						t.Errorf("call %d carries reasoning_effort = %v, want %v", i+1, has, sent)
					}
				}
				wantShows := int64(0)
				if asked {
					wantShows = 1
				}
				if n := f.shows.Load(); n != wantShows {
					t.Errorf("/api/show asked %d times over %d calls, want %d", n, calls, wantShows)
				}
				wantNotes := int64(0)
				if level != "" && !sent {
					wantNotes = 1
				}
				if n := notes.Load(); n != wantNotes {
					t.Errorf("%d notes logged, want %d", n, wantNotes)
				}
			})
		}
	}
}

// Calls that race to a model not yet asked about ask once between them. A
// request that got no answer, or a 5xx, is not kept: the next call asks
// again.
func TestOpenAIBackend_ThinkingCapabilityAskedOnce(t *testing.T) {
	t.Run("concurrent calls", func(t *testing.T) {
		f := &fakeOpenAI{caps: map[string][]string{"m": {"completion"}}, reply: func(map[string]any) string { return chatReply("ok", "stop", 1, 1) }}
		srv := httptest.NewServer(f)
		defer srv.Close()
		t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "8")
		b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL + "/v1", Client: srv.Client(), thinkingCache: &thinkingCache{}}
		const calls = 8
		var wg sync.WaitGroup
		for range calls {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = b.Run(context.Background(), RunRequest{Prompt: "hi", Model: "m", Reasoning: "low", Registry: &ToolRegistry{Handlers: map[string]ToolHandler{}}})
			}()
		}
		wg.Wait()
		if n := f.shows.Load(); n != 1 {
			t.Errorf("/api/show asked %d times by %d calls, want 1", n, calls)
		}
		for i, body := range f.bodies {
			if _, has := body["reasoning_effort"]; has {
				t.Errorf("call %d sent reasoning_effort to a model without thinking", i+1)
			}
		}
	})
	t.Run("a failed ask is not kept", func(t *testing.T) {
		var shows atomic.Int64
		var bodies []map[string]any
		var mu sync.Mutex
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/show":
				if shows.Add(1) == 1 {
					http.Error(w, "busy", http.StatusServiceUnavailable)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion"}})
			default:
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				mu.Lock()
				bodies = append(bodies, body)
				mu.Unlock()
				_, _ = w.Write([]byte(chatReply("ok", "stop", 1, 1)))
			}
		}))
		defer srv.Close()
		b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL + "/v1", Client: srv.Client(), thinkingCache: &thinkingCache{}}
		for range 3 {
			if _, err := b.Run(context.Background(), RunRequest{Prompt: "hi", Model: "m", Reasoning: "low", Registry: &ToolRegistry{Handlers: map[string]ToolHandler{}}}); err != nil {
				t.Fatal(err)
			}
		}
		// The first ask got a 503, so its call knew nothing and sent the
		// level; the second ask was answered and kept.
		if n := shows.Load(); n != 2 {
			t.Errorf("/api/show asked %d times, want 2", n)
		}
		for i, body := range bodies {
			_, has := body["reasoning_effort"]
			if want := i == 0; has != want {
				t.Errorf("call %d carries reasoning_effort = %v, want %v", i+1, has, want)
			}
		}
	})
}

// cutOffServer answers each chat call by reply and counts, per node label,
// the replies it cut off at the output cap.
type cutOffServer struct {
	fakeOpenAI
	mu  sync.Mutex
	cut map[string]int
}

// label names the node a prompt belongs to: its first word.
func promptLabel(body map[string]any) string {
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		if mm, _ := m.(map[string]any); mm["role"] == "user" {
			s, _ := mm["content"].(string)
			if strings.HasPrefix(s, "The previous attempt failed") {
				return "repair"
			}
			return strings.Fields(s)[0]
		}
	}
	return ""
}

func newCutOffServer(finish func(label string, body map[string]any) (content, reason string)) *cutOffServer {
	s := &cutOffServer{cut: map[string]int{}}
	s.models = []string{"local-model"}
	s.reply = func(body map[string]any) string {
		label := promptLabel(body)
		content, reason := finish(label, body)
		if reason == "length" {
			s.mu.Lock()
			s.cut[label]++
			s.mu.Unlock()
		}
		return chatReply(content, reason, 1, 1)
	}
	return s
}

func readCutoffCalls(t *testing.T, store *db.Store, node string) int {
	t.Helper()
	var n int
	if err := store.ReadDB.QueryRow(`SELECT COALESCE(cutoff_calls, 0) FROM workflow_node_states WHERE node_name = ?`, node).Scan(&n); err != nil {
		t.Fatalf("read cutoff_calls of %s: %v", node, err)
	}
	return n
}

// Every call the server cut off at the output cap adds to its node's
// cutoff_calls: a dispatch, a fan item and a repair attempt alike.
func TestRun_CutOffCallsAreCounted(t *testing.T) {
	const yaml = `name: cut
inputs: [items]
nodes:
  whole:
    type: agent
    model: local-model
    prompt: "whole answer"
    outputs: [answer]
  fan:
    type: parallel_fan
    model: local-model
    prompt_template: "fan {item}"
    fan_source: items
    outputs: [finding]
  mended:
    type: agent
    model: local-model
    prompt: "mended answer"
    outputs: [ok]
    accept:
      - "outputs.ok == true"
    on_reject:
      max_repair_iterations: 2
`
	items := []any{"keep-1", "cut-2", "keep-3", "cut-4"}
	var repairs atomic.Int64
	srv := newCutOffServer(func(label string, body map[string]any) (string, string) {
		msgs, _ := body["messages"].([]any)
		last, _ := msgs[len(msgs)-1].(map[string]any)
		prompt, _ := last["content"].(string)
		switch label {
		case "whole":
			return `{"answer": "half`, "length"
		case "fan":
			if strings.Contains(prompt, "cut-") {
				return `{"finding": "half`, "length"
			}
			return `{"finding": "whole"}`, "stop"
		case "mended":
			return `{"ok": false}`, "stop"
		case "repair":
			if repairs.Add(1) == 1 {
				return `{"ok": tr`, "length"
			}
			return `{"ok": true}`, "stop"
		}
		return "", "stop"
	})
	hs := httptest.NewServer(srv)
	defer hs.Close()
	_, store, log, _ := localRun(t, hs, yaml, Config{Inputs: map[string]any{"items": items}})

	// The node a repair belongs to is the one that has on_reject.
	want := map[string]int{"whole": srv.cut["whole"], "fan": srv.cut["fan"], "mended": srv.cut["repair"]}
	if want["whole"] == 0 || want["fan"] == 0 || want["mended"] == 0 {
		t.Fatalf("the server cut off %v; the test needs a cut-off call on each node\n%s", srv.cut, log)
	}
	for node, n := range want {
		if got := readCutoffCalls(t, store, node); got != n {
			t.Errorf("node %s cutoff_calls = %d, want %d (the server's count)", node, got, n)
		}
	}
	// A fan survives its failed items and a repair its failed attempt; a
	// node whose every call is cut off fails.
	wantStatus := map[string]string{"whole": "failed", "fan": "completed", "mended": "completed"}
	for node, status := range wantStatus {
		if row := readLocalNodeRow(t, store, node); row.status != status {
			t.Errorf("%s is %s, want %s (%s)", node, row.status, status, row.errMsg)
		}
	}
}

// The other backends count their own cut-off stop reason: Gemini's
// MAX_TOKENS, the Anthropic API's and the Claude CLI's max_tokens.
func TestOtherBackends_CountCutOffCalls(t *testing.T) {
	for _, stop := range []string{"MAX_TOKENS", "STOP"} {
		t.Run("gemini "+stop, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprintf(w, `{"candidates":[{"content":{"parts":[{"text":"half"}]},"finishReason":%q}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`, stop)
			}))
			defer srv.Close()
			b := &GeminiBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
			res, _ := b.Run(context.Background(), RunRequest{Prompt: "hi", MaxTokens: 9, Registry: &ToolRegistry{Handlers: map[string]ToolHandler{}}})
			if want := boolInt(stop == "MAX_TOKENS"); res == nil || res.CutOffCalls != want {
				t.Errorf("result %+v, want %d cut-off call(s)", res, want)
			}
		})
	}
	for _, stop := range []string{"max_tokens", "end_turn"} {
		t.Run("anthropic "+stop, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(anthropicTextResponse("half", stop, 1, 1)))
			}))
			defer srv.Close()
			res, _ := newTestClaudeRunner(t, srv, &ToolRegistry{Handlers: map[string]ToolHandler{}}).Run(context.Background(), "hi")
			if want := boolInt(stop == "max_tokens"); res == nil || res.CutOffCalls != want {
				t.Errorf("result %+v, want %d cut-off call(s)", res, want)
			}
		})
		t.Run("claude CLI "+stop, func(t *testing.T) {
			script := filepath.Join(t.TempDir(), "claude")
			body := fmt.Sprintf("#!/bin/sh\ncat > /dev/null\nprintf '%%s' '{\"type\":\"result\",\"is_error\":false,\"num_turns\":1,\"result\":\"half\",\"stop_reason\":\"%s\",\"usage\":{\"input_tokens\":1,\"output_tokens\":5}}'\n", stop)
			if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
			res, _ := (&CLIBackend{CLIPath: script, ScratchDir: t.TempDir()}).Run(context.Background(), RunRequest{Prompt: "hi"})
			if want := boolInt(stop == "max_tokens"); res == nil || res.CutOffCalls != want {
				t.Errorf("result %+v, want %d cut-off call(s)", res, want)
			}
		})
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// inFlight counts the requests a handler holds at once, and the most it held.
type inFlight struct {
	now, peak atomic.Int64
}

func (c *inFlight) enter() func() {
	n := c.now.Add(1)
	for {
		p := c.peak.Load()
		if n <= p || c.peak.CompareAndSwap(p, n) {
			break
		}
	}
	return func() { c.now.Add(-1) }
}

// The Anthropic SDK backend waits for an endpoint slot too, so calls to an
// ANTHROPIC_BASE_URL on this machine go one at a time by default.
func TestSDKBackend_WaitsForAnEndpointSlot(t *testing.T) {
	var held inFlight
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer held.enter()()
		time.Sleep(20 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(anthropicTextResponse("ok", "end_turn", 1, 1)))
	}))
	defer srv.Close()
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "")
	want := endpointLimit(srv.URL)
	if want != 1 {
		t.Fatalf("the default bound on %s is %d; the test needs a loopback endpoint", srv.URL, want)
	}
	const calls = 3
	errs := make([]error, calls)
	var wg sync.WaitGroup
	for i := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = (&SDKBackend{APIKey: "k"}).Run(context.Background(), RunRequest{Prompt: "hi", Model: "sonnet", MaxTokens: 64, Registry: &ToolRegistry{Handlers: map[string]ToolHandler{}}})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("call %d: %v", i+1, err)
		}
	}
	if p := held.peak.Load(); p > int64(want) {
		t.Errorf("the server held %d calls at once, want at most %d", p, want)
	}
}

// The scripted local run: a workflow against a fake Ollama holding a
// thinking model and one without the capability. No call to the model that
// cannot think carries reasoning_effort, and that is logged once; the
// thinking model's reasoning goes back on its tool turn; and with no
// variable set, one call is in flight at a time across nodes and fan items.
func TestRun_LocalOllamaAcceptance(t *testing.T) {
	const thinker, plain = "qwen3.5:4b", "ministral-3:8b"
	const thought = "The notes are in the workflow file; read it first."
	const yaml = `name: local-ollama
inputs: [items]
nodes:
  reader:
    type: agent
    model: ` + thinker + `
    reasoning: low
    tools: [read_file]
    prompt: "reader: read wf.yaml and answer"
    outputs: [answer]
  lens:
    type: agent
    model: ` + plain + `
    reasoning: low
    tools: []
    prompt: "lens: answer"
    outputs: [answer]
  fan:
    type: parallel_fan
    model: ` + plain + `
    reasoning: none
    tools: []
    prompt_template: "fan: {item}"
    fan_source: items
    outputs: [finding]
`
	items := []any{"a", "b", "c"}
	var held inFlight
	f := &fakeOpenAI{
		models: []string{thinker, plain},
		caps:   map[string][]string{thinker: {"completion", "tools", "thinking"}, plain: {"completion", "tools"}},
		delay:  10 * time.Millisecond,
	}
	f.reply = func(body map[string]any) string {
		msgs, _ := body["messages"].([]any)
		answeredTool := false
		for _, m := range msgs {
			if mm, _ := m.(map[string]any); mm["role"] == "tool" {
				answeredTool = true
			}
		}
		switch promptLabel(body) {
		case "reader:":
			if !answeredTool {
				return toolTurnWith("reasoning", thought, "wf.yaml")
			}
			return chatReply(`{"answer":"read"}`, "stop", 1, 1)
		case "fan:":
			return chatReply(`{"finding":"x"}`, "stop", 1, 1)
		}
		return chatReply(`{"answer":"ok"}`, "stop", 1, 1)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			defer held.enter()()
		}
		f.ServeHTTP(w, r)
	}))
	defer srv.Close()
	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "")
	t.Setenv("HIVE_MAX_PARALLEL_NODES", "4")
	t.Setenv("HIVE_MAX_PARALLEL_FAN", fmt.Sprint(len(items)))
	_, store, log, err := localRun(t, srv, yaml, Config{Inputs: map[string]any{"items": items}})
	if err != nil {
		t.Fatalf("run: %v\n%s", err, log)
	}
	for _, node := range []string{"reader", "lens", "fan"} {
		if row := readLocalNodeRow(t, store, node); row.status != "completed" {
			t.Errorf("node %s is %s (%s), want completed", node, row.status, row.errMsg)
		}
	}

	var readerCalls []map[string]any
	for _, body := range f.bodies {
		model, _ := body["model"].(string)
		r, has := body["reasoning_effort"]
		switch model {
		case plain:
			if has {
				t.Errorf("a call to %s carries reasoning_effort %v", plain, r)
			}
		case thinker:
			if r != "low" {
				t.Errorf("a call to %s carries reasoning_effort %v, want low", thinker, r)
			}
			readerCalls = append(readerCalls, body)
		}
	}
	if want := 2 + 1 + len(items); len(f.bodies) != want {
		t.Errorf("%d calls, want %d (two reader turns, the lens, one per item)", len(f.bodies), want)
	}
	if len(readerCalls) != 2 {
		t.Fatalf("%d calls to %s, want 2 (a tool turn and the answer)", len(readerCalls), thinker)
	}
	echoed := assistantMessages(readerCalls[1])
	if len(echoed) != 1 || echoed[0]["reasoning"] != thought {
		t.Errorf("the reader's second call carries assistant messages %v, want one with its reasoning", echoed)
	}
	if n := strings.Count(log, fmt.Sprintf("reports no thinking capability for %q", plain)); n != 1 {
		t.Errorf("the run log says %s cannot think %d times, want once:\n%s", plain, n, log)
	}
	if p := held.peak.Load(); p != 1 {
		t.Errorf("the server held %d calls at once, want 1 (the default bound on a loopback endpoint)", p)
	}
}
