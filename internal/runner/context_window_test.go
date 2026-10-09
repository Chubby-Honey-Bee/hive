package runner

// Tests for the context-window guard (runner.md § Context window guard).
// Every server is an httptest fake of an Ollama, LM Studio or llama.cpp
// endpoint; no model is called.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeOllama is an Ollama server's OpenAI-compatible API plus the two routes
// the guard reads: GET /api/ps, which lists the loaded models with the
// context_length each runs at, and POST /api/generate, which loads one.
// With notPS it answers only the chat route, as a hosted API would.
type fakeOllama struct {
	window  int64 // the context_length /api/ps reports; 0 lists none
	notPS   bool  // answer everything but the chat route with 404
	loaded  atomic.Bool
	reply   func(body map[string]any) string
	loads   atomic.Int64
	probes  atomic.Int64 // requests to a route the guard asks what server it is by
	mu      sync.Mutex
	chats   []map[string]any
	modelID string
}

func (f *fakeOllama) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/ps", "/api/v1/models", "/props":
		f.probes.Add(1)
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/chat/completions"):
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.chats = append(f.chats, body)
		f.mu.Unlock()
		f.loaded.Store(true)
		_, _ = w.Write([]byte(f.reply(body)))
	case f.notPS:
		http.NotFound(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/ps":
		models := []map[string]any{}
		if f.loaded.Load() {
			m := map[string]any{"name": f.modelID, "model": f.modelID}
			if f.window > 0 {
				m["context_length"] = f.window
			}
			models = append(models, m)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
	case r.Method == http.MethodPost && r.URL.Path == "/api/generate":
		f.loads.Add(1)
		f.loaded.Store(true)
		_ = json.NewEncoder(w).Encode(map[string]any{"model": f.modelID, "done": true, "done_reason": "load"})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeOllama) sent() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.chats...)
}

// longPrompt is n bytes of prose. It holds no brace, so no placeholder, and
// no digit.
func longPrompt(n int) string {
	const s = "The swarm weighs the question through one lens and says what it cannot ground. "
	return strings.Repeat(s, n/len(s)+1)[:n]
}

// guardYAML is one node on model with the given prompt, no tools and no
// agent, so the request holds the prompt alone as its one user message.
func guardYAML(model, prompt string) string {
	return fmt.Sprintf("name: guard\nnodes:\n  only:\n    type: agent\n    model: %s\n    tools: []\n    prompt: %q\n    outputs: [answer]\n", model, prompt)
}

// onlyTheServersWindow makes the window the fake server's alone: no
// OLLAMA_CONTEXT_LENGTH and no models-config entry from the machine running
// the test.
func onlyTheServersWindow(t *testing.T) {
	t.Helper()
	t.Setenv("OLLAMA_CONTEXT_LENGTH", "")
	orig := modelContextWindow
	modelContextWindow = func(string) int64 { return 0 }
	t.Cleanup(func() { modelContextWindow = orig })
}

var (
	wordRE  = regexp.MustCompile(`[A-Za-z]+`)
	markRE  = regexp.MustCompile(`[^A-Za-z\s]`)
	spaceRE = regexp.MustCompile(`\s+`)
)

// proseTokens restates the estimate for ASCII text with no digit, the kind
// longPrompt writes: a token per 4 letters of each word, rounded up, one per
// punctuation mark, and one per run of whitespace other than a lone space.
func proseTokens(s string) float64 {
	var n float64
	for _, w := range wordRE.FindAllString(s, -1) {
		n += math.Ceil(float64(len(w)) / 4)
	}
	n += float64(len(markRE.FindAllString(s, -1)))
	for _, ws := range spaceRE.FindAllString(s, -1) {
		if ws != " " {
			n++
		}
	}
	return n
}

// loneUserPrompt is the estimate of a request that holds prompt as its one
// message, a user message, with no system message and no tools: its text
// and what a template adds to such a request.
func loneUserPrompt(prompt string) float64 {
	return proseTokens(prompt) + templateBaseTokens + templateMessageTokens + templateNoSystemTokens
}

// estimateOf rounds an estimate up to whole tokens, at a scale.
func estimateOf(tokens, scale float64) int64 { return int64(math.Ceil(tokens * scale)) }

// The estimate by text class, on strings short enough to count by hand.
func TestTextTokens_Classes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want float64
		why  string
	}{
		{"a word per 4 letters", "understand", 3, "10 letters → ⌈10/4⌉"},
		{"lone spaces before words are free", "one two three", 4, "⌈3/4⌉+⌈3/4⌉+⌈5/4⌉ = 1+1+2"},
		{"marks are a token each", "a, b.", 4, "a, ',', b, '.'"},
		{"a digit is a token, and so is the lone space before it", "take 12", 4, "take, ' ', 1, 2"},
		{"other whitespace runs are a token", "a\n\nb  c", 5, "a, '\\n\\n', b, '  ', c"},
		{"letters touching a digit are a token each", "a1bc2", 5, "a (next is a digit), 1, b, c (after a digit), 2"},
		{"two-byte letters are a token per 2", "déjà", 2, "⌈2/4 + 2/2⌉ = ⌈1.5⌉"},
		{"Cyrillic", "привет", 3, "6 two-byte letters → ⌈6/2⌉"},
		{"wide characters are 1.5 each", "日本語", 4.5, "3 × 1.5"},
		{"a lone space before a wide character is free", "a 日", 2.5, "a, 日"},
		{"two-byte marks are a token", "«a»", 3, "«, a, »"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := textTokens(c.in); got != c.want {
				t.Errorf("textTokens(%q) = %v, want %v (%s)", c.in, got, c.want, c.why)
			}
		})
	}
	if got, want := textTokens(longPrompt(4000)), proseTokens(longPrompt(4000)); got != want {
		t.Errorf("textTokens(prose) = %v, want %v", got, want)
	}
}

// A request's size: every message's content and tool calls and the tool
// schemas, in bytes and by estimate; the reasoning of an assistant turn
// after the last user message, which a template may render, in the estimate
// only; and what the template adds.
func TestMeasurePrompt(t *testing.T) {
	sys, user, reasoning, answer := "Be brief.", longPrompt(3000), longPrompt(700), longPrompt(300)
	call := []map[string]any{{"id": "c1", "type": "function", "function": map[string]any{"name": "glob", "arguments": `{"pattern":"*.go"}`}}}
	tools := []map[string]any{{"type": "function", "function": map[string]any{"name": "glob"}}}
	callJS, _ := json.Marshal(call)
	toolsJS, _ := json.Marshal(tools)
	loop := map[string]any{
		"messages": []map[string]any{
			{"role": "system", "content": sys},
			{"role": "user", "content": user},
			{"role": "assistant", "content": "", "tool_calls": call, "reasoning": reasoning},
			{"role": "tool", "content": answer},
		},
		"tools": tools,
	}
	finalize := map[string]any{
		"messages": append(append([]map[string]any(nil), loop["messages"].([]map[string]any)...),
			map[string]any{"role": "assistant", "content": answer},
			map[string]any{"role": "user", "content": "Reply with only the JSON object."}),
	}
	text := len(sys) + len(user) + len(callJS) + len(answer)
	textTok := textTokens(sys) + textTokens(user) + textTokens(string(callJS)) + textTokens(answer)

	got := measurePrompt(loop)
	wantBytes := int64(text + len(toolsJS))
	wantTok := textTok + textTokens(string(toolsJS)) + textTokens(reasoning) + templateBaseTokens + 4*templateMessageTokens + templateToolsTokens
	if got.bytes != wantBytes || got.tokens != wantTok {
		t.Errorf("tool-loop turn: %d bytes, %v tokens; want %d, %v", got.bytes, got.tokens, wantBytes, wantTok)
	}

	const ask = "Reply with only the JSON object."
	got = measurePrompt(finalize)
	wantBytes = int64(text + len(answer) + len(ask))
	wantTok = textTok + textTokens(answer) + textTokens(ask) + templateBaseTokens + 6*templateMessageTokens
	if got.bytes != wantBytes || got.tokens != wantTok {
		t.Errorf("finalize: %d bytes, %v tokens; want %d, %v (no reasoning: a user message follows it)", got.bytes, got.tokens, wantBytes, wantTok)
	}

	// A server that returns reasoning as reasoning_content gets it back in
	// that field (runner.md § Reasoning level); it counts the same.
	msgs := append([]map[string]any(nil), loop["messages"].([]map[string]any)...)
	msgs[2] = map[string]any{"role": "assistant", "content": "", "tool_calls": call, "reasoning_content": reasoning}
	got = measurePrompt(map[string]any{"messages": msgs, "tools": tools})
	wantBytes = int64(text + len(toolsJS))
	wantTok = textTok + textTokens(string(toolsJS)) + textTokens(reasoning) + templateBaseTokens + 4*templateMessageTokens + templateToolsTokens
	if got.bytes != wantBytes || got.tokens != wantTok {
		t.Errorf("tool-loop turn with reasoning_content: %d bytes, %v tokens; want %d, %v", got.bytes, got.tokens, wantBytes, wantTok)
	}

	lone := map[string]any{"messages": []map[string]any{{"role": "user", "content": user}}}
	if got := measurePrompt(lone).tokens; got != loneUserPrompt(user) {
		t.Errorf("a lone user message: %v tokens, want %v with the no-system allowance", got, loneUserPrompt(user))
	}
}

// A prompt that would not fit the window Ollama reports for a small model
// is refused before any call: the node fails naming the estimate and the
// window, and the server gets no chat request. The model, not loaded yet,
// was loaded to read its window.
func TestContextGuard_RefusesAnOversizedPrompt(t *testing.T) {
	onlyTheServersWindow(t)
	const window = 512
	f := &fakeOllama{window: window, modelID: "tiny:1b", reply: func(map[string]any) string { return chatReply(`{"answer":"x"}`, "stop", 1, 1) }}
	srv := httptest.NewServer(f)
	defer srv.Close()
	prompt := longPrompt(4000)
	_, store, log, err := localRun(t, srv, guardYAML("tiny:1b", prompt), Config{})
	if err == nil {
		t.Fatalf("the run succeeded; want the node refused\n%s", log)
	}
	row := readLocalNodeRow(t, store, "only")
	est := estimateOf(loneUserPrompt(prompt), 1)
	if est < window {
		t.Fatalf("the test prompt is estimated at %d tokens, inside the %d window", est, window)
	}
	for _, want := range []string{"context window", fmt.Sprintf("about %d tokens by estimate", est), fmt.Sprintf("%d bytes", len(prompt)), fmt.Sprintf("is %d tokens (%s)", window, windowFromOllama)} {
		if !strings.Contains(row.errMsg, want) {
			t.Errorf("node error %q lacks %q", row.errMsg, want)
		}
	}
	if row.status != "failed" {
		t.Errorf("node is %s, want failed", row.status)
	}
	if n := len(f.sent()); n != 0 {
		t.Errorf("%d chat requests reached the server, want none", n)
	}
	if f.loads.Load() != 1 {
		t.Errorf("%d loads, want 1 to read the window of a model not yet loaded", f.loads.Load())
	}
	if !strings.Contains(log, fmt.Sprintf("context window: tiny:1b at %s/v1 holds %d tokens (%s)", srv.URL, window, windowFromOllama)) {
		t.Errorf("the run log does not name the window:\n%s", log)
	}
}

// A prompt that fits but leaves less room than a reply needs is refused
// unsent too, naming the room; a call that asks for less than that room
// needs only what it asks for.
func TestContextGuard_RefusesWithoutRoomForTheReply(t *testing.T) {
	onlyTheServersWindow(t)
	prompt := longPrompt(3000)
	est := estimateOf(loneUserPrompt(prompt), 1)
	window := est + minReplyRoom - 1
	for _, c := range []struct {
		name    string
		asked   int64
		refused bool
	}{
		{"the default cap", 0, true},
		{"a cap under the room left", minReplyRoom - 2, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeOllama{window: window, modelID: "m:4b", reply: func(map[string]any) string { return chatReply(`{"answer":"x"}`, "stop", len(prompt)/5, 5) }}
			f.loaded.Store(true)
			srv := httptest.NewServer(f)
			defer srv.Close()
			_, store, log, _ := localRun(t, srv, guardYAML("m:4b", prompt), Config{MaxOutputTokens: c.asked})
			row := readLocalNodeRow(t, store, "only")
			if !c.refused {
				if row.status != "completed" || len(f.sent()) != 1 {
					t.Errorf("node is %s (%s) after %d calls, want completed after 1\n%s", row.status, row.errMsg, len(f.sent()), log)
				}
				return
			}
			room := window - est
			for _, want := range []string{fmt.Sprintf("leaves %d of", room), fmt.Sprintf("fewer than the %d a reply needs", minReplyRoom)} {
				if !strings.Contains(row.errMsg, want) {
					t.Errorf("node error %q lacks %q", row.errMsg, want)
				}
			}
			if n := len(f.sent()); n != 0 {
				t.Errorf("%d chat requests reached the server, want none", n)
			}
		})
	}
}

// A prompt that fits is sent with its output cap lowered to the room the
// window leaves, so the reply cannot run past it either.
func TestContextGuard_LowersTheCap(t *testing.T) {
	onlyTheServersWindow(t)
	const window = 4096
	prompt := longPrompt(3000)
	f := &fakeOllama{window: window, modelID: "small:4b", reply: func(body map[string]any) string {
		return chatReply(`{"answer":"x"}`, "stop", len(prompt)/4, 5)
	}}
	f.loaded.Store(true)
	srv := httptest.NewServer(f)
	defer srv.Close()
	_, store, log, err := localRun(t, srv, guardYAML("small:4b", prompt), Config{MaxOutputTokens: 8192})
	if err != nil {
		t.Fatalf("run: %v\n%s", err, log)
	}
	chats := f.sent()
	if len(chats) != 1 {
		t.Fatalf("%d chat requests, want 1", len(chats))
	}
	want := window - estimateOf(loneUserPrompt(prompt), 1)
	for _, k := range []string{"max_tokens", "max_completion_tokens"} {
		if got, _ := chats[0][k].(float64); int64(got) != want {
			t.Errorf("%s = %v, want the %d-token window less the %d-token estimate, %d", k, chats[0][k], window, window-want, want)
		}
	}
	if f.loads.Load() != 0 {
		t.Errorf("a loaded model was loaded again")
	}
	if row := readLocalNodeRow(t, store, "only"); row.status != "completed" {
		t.Errorf("node is %s (%s), want completed", row.status, row.errMsg)
	}
	if !strings.Contains(log, fmt.Sprintf("output cap lowered from 8192 to %d", want)) {
		t.Errorf("the run log does not say the cap was lowered:\n%s", log)
	}
}

// A reply cut off at a cap the guard lowered fails naming the lowering, so
// the operator sees the window, not only the cap.
func TestContextGuard_CutAtALoweredCap(t *testing.T) {
	onlyTheServersWindow(t)
	const window = 4096
	prompt := longPrompt(3000)
	sent := window - estimateOf(loneUserPrompt(prompt), 1)
	f := &fakeOllama{window: window, modelID: "small:4b", reply: func(map[string]any) string {
		return chatReply(`{"answer":"x`, "length", len(prompt)/4, int(sent))
	}}
	f.loaded.Store(true)
	srv := httptest.NewServer(f)
	defer srv.Close()
	_, store, _, _ := localRun(t, srv, guardYAML("small:4b", prompt), Config{MaxOutputTokens: 8192})
	row := readLocalNodeRow(t, store, "only")
	for _, want := range []string{fmt.Sprintf("max tokens %d", sent), "the cap was lowered from 8192", fmt.Sprintf("the %d-token context window", window)} {
		if !strings.Contains(row.errMsg, want) {
			t.Errorf("node error %q lacks %q", row.errMsg, want)
		}
	}
}

// The check after a call under a known window. A count under half the
// window shows nothing was dropped, however few tokens a byte (Russian
// counts 9.66 bytes a token on qwen3.5). A prompt and reply past the
// window, or a count in the upper half of the window above the estimate,
// fails the node.
func TestContextGuard_ServerDroppedPart(t *testing.T) {
	const window = 8192
	prompt := longPrompt(6000)
	est := estimateOf(loneUserPrompt(prompt), 1)
	cases := []struct {
		name        string
		in, out     int
		wantInError string
	}{
		{"window overrun while answering", 3000, window - 3000 + 1, "while it answered"},
		{"sparse text under half the window", int(math.Ceil(float64(len(prompt)) / 9.66)), 50, ""},
		{"above the estimate in the upper half", window / 2, 50, "may rest on part of the prompt"},
		{"within the window", len(prompt) / 4, 50, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			onlyTheServersWindow(t)
			switch c.wantInError {
			case "may rest on part of the prompt":
				if int64(c.in) <= est || 2*c.in < window {
					t.Fatalf("%d tokens is not above the %d estimate and in the upper half of %d", c.in, est, window)
				}
			case "":
				if 2*c.in >= window && int64(c.in) > est {
					t.Fatalf("%d tokens is in the upper half and above the estimate", c.in)
				}
			}
			f := &fakeOllama{window: window, modelID: "m:8b", reply: func(map[string]any) string { return chatReply(`{"answer":"x"}`, "stop", c.in, c.out) }}
			f.loaded.Store(true)
			srv := httptest.NewServer(f)
			defer srv.Close()
			_, store, log, _ := localRun(t, srv, guardYAML("m:8b", prompt), Config{})
			row := readLocalNodeRow(t, store, "only")
			if c.wantInError == "" {
				if row.status != "completed" {
					t.Errorf("node is %s (%s), want completed\n%s", row.status, row.errMsg, log)
				}
				return
			}
			if row.status != "failed" || !strings.Contains(row.errMsg, c.wantInError) {
				t.Errorf("node is %s with %q, want failed naming %q", row.status, row.errMsg, c.wantInError)
			}
			if row.in != int64(c.in) || row.out != int64(c.out) {
				t.Errorf("node row carries %d/%d tokens, want the call's %d/%d", row.in, row.out, c.in, c.out)
			}
		})
	}
}

// A call the server stopped at the cap counts in cutoff_calls even when the
// guard's check then fails it as cut, so the count holds every call stopped
// at the cap (runner.md § Incomplete replies fail).
func TestContextGuard_CutCallStillCountsAsCutOff(t *testing.T) {
	onlyTheServersWindow(t)
	const window = 8192
	prompt := longPrompt(6000)
	in := window / 2
	if est := estimateOf(loneUserPrompt(prompt), 1); int64(in) <= est {
		t.Fatalf("%d tokens is not above the %d estimate", in, est)
	}
	f := &fakeOllama{window: window, modelID: "m:8b", reply: func(map[string]any) string { return chatReply(`{"answer":"x`, "length", in, 50) }}
	f.loaded.Store(true)
	srv := httptest.NewServer(f)
	defer srv.Close()
	_, store, _, _ := localRun(t, srv, guardYAML("m:8b", prompt), Config{})
	row := readLocalNodeRow(t, store, "only")
	if row.status != "failed" || !strings.Contains(row.errMsg, "may rest on part of the prompt") {
		t.Errorf("node is %s with %q, want failed by the guard's check", row.status, row.errMsg)
	}
	if got := readCutoffCalls(t, store, "only"); got != len(f.sent()) || got != 1 {
		t.Errorf("cutoff_calls = %d over %d call(s), want 1: the one call stopped at the cap", got, len(f.sent()))
	}
}

// A count above the estimate raises later estimates on that model to the
// most tokens counted per estimated token, over the margin. One count below
// it lowers nothing: the estimate falls below its class count only after
// minCalibrationCalls counts (TestContextGuard_LowersAfterThreeCounts).
func TestContextGuard_Calibrates(t *testing.T) {
	const window = 16384
	first, second := longPrompt(6000), longPrompt(9000)
	est1, est2 := loneUserPrompt(first), loneUserPrompt(second)
	for _, c := range []struct {
		name    string
		counted int
		scale   float64
	}{
		{"a denser count raises", int(math.Ceil(est1 * 1.2)), 0},
		{"a sparser count does not lower", int(est1 / 2), 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			onlyTheServersWindow(t)
			scale := c.scale
			if scale == 0 {
				scale = float64(c.counted) / est1 / calibrationMargin
			}
			f := &fakeOllama{window: window, modelID: "m:8b", reply: func(map[string]any) string {
				return chatReply(`{"answer":"x"}`, "stop", c.counted, 5)
			}}
			f.loaded.Store(true)
			srv := httptest.NewServer(f)
			defer srv.Close()
			yaml := fmt.Sprintf("name: guard\nnodes:\n  one:\n    type: agent\n    model: m:8b\n    tools: []\n    prompt: %q\n    outputs: [answer]\n  two:\n    type: agent\n    model: m:8b\n    tools: []\n    prompt: %q\n    outputs: [answer]\nedges:\n  - {from: one, to: two}\n", first, second)
			_, _, log, err := localRun(t, srv, yaml, Config{MaxOutputTokens: 16384})
			if err != nil {
				t.Fatalf("run: %v\n%s", err, log)
			}
			chats := f.sent()
			if len(chats) != 2 {
				t.Fatalf("%d chat requests, want 2", len(chats))
			}
			wants := []int64{window - estimateOf(est1, 1), window - estimateOf(est2, scale)}
			for i, want := range wants {
				if got, _ := chats[i]["max_tokens"].(float64); int64(got) != want {
					t.Errorf("call %d: max_tokens %v, want %d", i+1, chats[i]["max_tokens"], want)
				}
			}
		})
	}
}

// A call queued behind another on the same model, at a single-slot
// endpoint, estimates with what that one counted: the count is recorded
// before the slot is released.
func TestContextGuard_QueuedCallUsesTheEarlierCount(t *testing.T) {
	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "1")
	const window = 16384
	prompt := longPrompt(6000)
	est := loneUserPrompt(prompt)
	counted := int(math.Ceil(est * 1.3))
	f := &fakeOllama{modelID: "m:8b", reply: func(map[string]any) string { return chatReply("done", "stop", counted, 5) }}
	srv := httptest.NewServer(f)
	defer srv.Close()
	b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL + "/v1", Client: srv.Client(), calib: &calibration{}}
	req := RunRequest{Model: "m:8b", Prompt: prompt, Registry: &ToolRegistry{Handlers: map[string]ToolHandler{}}, MaxTokens: window, ContextWindow: window, ContextWindowSource: "a test"}
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := b.Run(context.Background(), req); err != nil {
				t.Errorf("run: %v", err)
			}
		}()
	}
	wg.Wait()
	chats := f.sent()
	if len(chats) != 2 {
		t.Fatalf("%d chat requests, want 2", len(chats))
	}
	wants := []int64{window - estimateOf(est, 1), window - estimateOf(est, float64(counted)/est/calibrationMargin)}
	for i, want := range wants {
		if got, _ := chats[i]["max_tokens"].(float64); int64(got) != want {
			t.Errorf("call %d to arrive: max_tokens %v, want %d", i+1, chats[i]["max_tokens"], want)
		}
	}
}

// fakeLMStudio is LM Studio's OpenAI-compatible API beside its REST API:
// GET /api/v1/models lists each model with its loaded instances, and POST
// /api/v1/models/load loads one. Like LM Studio, it answers a route it does
// not know with 200 and an error object.
type fakeLMStudio struct {
	key    string
	window int64
	loaded atomic.Bool
	loads  atomic.Int64
}

func (f *fakeLMStudio) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/models":
		inst := []map[string]any{}
		if f.loaded.Load() {
			inst = append(inst, map[string]any{"id": f.key, "config": map[string]any{"context_length": f.window, "parallel": 4}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{
			{"type": "llm", "key": "other/model", "loaded_instances": []any{}, "max_context_length": 131072},
			{"type": "llm", "key": f.key, "variants": []string{f.key + "@q4_k_m"}, "loaded_instances": inst, "max_context_length": 262144},
		}})
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/models/load":
		f.loads.Add(1)
		f.loaded.Store(true)
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "llm", "instance_id": f.key, "status": "loaded", "load_config": map[string]any{"context_length": f.window}})
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "Unexpected endpoint or method. (" + r.Method + " " + r.URL.Path + ")"})
	}
}

// fakeLlamaCpp is llama.cpp's server: GET /props reports the context each
// slot runs with, and every other route but its own is a 404.
type fakeLlamaCpp struct{ nCtx int64 }

func (f *fakeLlamaCpp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/props" {
		_ = json.NewEncoder(w).Encode(map[string]any{"default_generation_settings": map[string]any{"n_ctx": f.nCtx}, "total_slots": 1})
		return
	}
	http.NotFound(w, r)
}

// The server behind the endpoint and the window it reports. On Ollama the
// window is what /api/ps reports for the loaded model;
// OLLAMA_CONTEXT_LENGTH in chb's environment stands in only when it
// reports none, since chb's environment is not the server's. On LM Studio
// it is the loaded instance's context_length, a model not loaded being
// loaded first; on llama.cpp, the n_ctx of /props. The models config caps
// any of them. A server that answers none of the three is none of them, and
// the variable does not apply to it.
func TestResolveContextWindow_Sources(t *testing.T) {
	ollama := func(window int64) func() http.Handler {
		return func() http.Handler {
			f := &fakeOllama{window: window, modelID: "m:8b"}
			f.loaded.Store(true)
			return f
		}
	}
	lmStudio := func(loaded bool) func() http.Handler {
		return func() http.Handler {
			f := &fakeLMStudio{key: "m:8b", window: 8192}
			f.loaded.Store(loaded)
			return f
		}
	}
	hosted := func() http.Handler { return &fakeOllama{notPS: true} }
	cases := []struct {
		name       string
		server     func() http.Handler
		model      string
		env        string
		config     int64
		wantServer localServer
		wantTokens int64
		wantSource string
	}{
		{"Ollama alone", ollama(65536), "m:8b", "", 0, serverOllama, 65536, windowFromOllama},
		{"chb's variable does not override Ollama", ollama(65536), "m:8b", "16384", 0, serverOllama, 65536, windowFromOllama},
		{"nor does a larger one", ollama(4096), "m:8b", "16384", 0, serverOllama, 4096, windowFromOllama},
		{"the config is smaller", ollama(65536), "m:8b", "", 2048, serverOllama, 2048, windowFromConfig},
		{"Ollama reports none: the variable", ollama(0), "m:8b", "8192", 0, serverOllama, 8192, windowFromEnv},
		{"LM Studio, loaded", lmStudio(true), "m:8b", "", 0, serverLMStudio, 8192, windowFromLMStudio},
		{"LM Studio, loaded first", lmStudio(false), "m:8b", "", 0, serverLMStudio, 8192, windowFromLMStudio},
		{"LM Studio, by variant", lmStudio(true), "m:8b@q4_k_m", "", 0, serverLMStudio, 8192, windowFromLMStudio},
		{"LM Studio, not listed", lmStudio(true), "absent:1b", "", 0, serverLMStudio, 0, ""},
		{"llama.cpp", func() http.Handler { return &fakeLlamaCpp{nCtx: 32768} }, "any", "8192", 0, serverLlamaCpp, 32768, windowFromLlamaCpp},
		{"none: the variable does not apply", hosted, "m:8b", "8192", 0, serverNone, 0, ""},
		{"none: the config", hosted, "m:8b", "8192", 32768, serverNone, 32768, windowFromConfig},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := c.server()
			srv := httptest.NewServer(h)
			defer srv.Close()
			t.Setenv("OLLAMA_CONTEXT_LENGTH", c.env)
			orig := modelContextWindow
			modelContextWindow = func(string) int64 { return c.config }
			defer func() { modelContextWindow = orig }()
			b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL + "/v1", Client: srv.Client()}
			server := b.findServer(context.Background())
			if server != c.wantServer {
				t.Fatalf("server %q, want %q", server, c.wantServer)
			}
			w, src := resolveContextWindow(context.Background(), b, server, c.model)
			if w != c.wantTokens || src != c.wantSource {
				t.Errorf("window %d (%s), want %d (%s)", w, src, c.wantTokens, c.wantSource)
			}
			if f, ok := h.(*fakeLMStudio); ok {
				wantLoads := int64(0)
				if c.name == "LM Studio, loaded first" {
					wantLoads = 1
				}
				if f.loads.Load() != wantLoads {
					t.Errorf("%d loads, want %d", f.loads.Load(), wantLoads)
				}
			}
		})
	}
}

// With no window known the guard refuses nothing; the run log says the
// window is unknown and, for an endpoint on this machine, how to declare
// it. The endpoint is asked what server it is once a run, not once a model.
func TestContextGuard_UnknownWindowIsNamed(t *testing.T) {
	onlyTheServersWindow(t)
	prompt := longPrompt(40000)
	f := &fakeOllama{notPS: true, modelID: "m:8b", reply: func(map[string]any) string { return chatReply(`{"answer":"x"}`, "stop", len(prompt)/4, 5) }}
	srv := httptest.NewServer(f)
	defer srv.Close()
	yaml := fmt.Sprintf("name: guard\nnodes:\n  one:\n    type: agent\n    model: m:8b\n    tools: []\n    prompt: %q\n    outputs: [answer]\n  two:\n    type: agent\n    model: n:4b\n    tools: []\n    prompt: %q\n    outputs: [answer]\nedges:\n  - {from: one, to: two}\n", prompt, prompt)
	_, _, log, err := localRun(t, srv, yaml, Config{})
	if err != nil {
		t.Fatalf("run: %v\n%s", err, log)
	}
	for _, model := range []string{"m:8b", "n:4b"} {
		if !strings.Contains(log, "context window: "+model+" at "+srv.URL+"/v1 is unknown") || !strings.Contains(log, "set context_window for "+model+" in the models config") {
			t.Errorf("the run log does not say %s's window is unknown and how to declare it:\n%s", model, log)
		}
	}
	if n := f.probes.Load(); n != 3 {
		t.Errorf("%d requests asked the endpoint what it is, want 3 (Ollama, LM Studio, llama.cpp) once for the run", n)
	}
}

// With no window known, a count far below any tokenizer's still fails the
// node: the server dropped part of the prompt.
func TestContextGuard_UnknownWindowStillCatchesAGrossCut(t *testing.T) {
	onlyTheServersWindow(t)
	prompt := longPrompt(40000)
	counted := len(prompt)/cutBytesPerToken - 1
	f := &fakeOllama{notPS: true, modelID: "m:8b", reply: func(map[string]any) string { return chatReply(`{"answer":"x"}`, "stop", counted, 5) }}
	srv := httptest.NewServer(f)
	defer srv.Close()
	_, store, _, _ := localRun(t, srv, guardYAML("m:8b", prompt), Config{})
	row := readLocalNodeRow(t, store, "only")
	for _, want := range []string{fmt.Sprintf("counted %d prompt tokens for %d bytes", counted, len(prompt)), "dropped part of the prompt", "context window is unknown"} {
		if row.status != "failed" || !strings.Contains(row.errMsg, want) {
			t.Errorf("node is %s with %q, want failed naming %q", row.status, row.errMsg, want)
		}
	}
}

// A repair's prompt holds the node's prompt and the failed outputs, so it is
// larger than the dispatch's, and the guard holds it to the window too.
func TestContextGuard_HoldsTheRepair(t *testing.T) {
	onlyTheServersWindow(t)
	prompt := longPrompt(3000)
	failed := `{"answer":"` + strings.Repeat("y", 3000) + `"}`
	window := estimateOf(loneUserPrompt(prompt), 1) + minReplyRoom
	f := &fakeOllama{window: window, modelID: "m:8b", reply: func(map[string]any) string { return chatReply(failed, "stop", len(prompt)/5, 100) }}
	f.loaded.Store(true)
	srv := httptest.NewServer(f)
	defer srv.Close()
	yaml := guardYAML("m:8b", prompt) + "    accept:\n      - \"outputs.answer == 'ok'\"\n    on_reject:\n      max_repair_iterations: 1\n"
	_, store, log, _ := localRun(t, srv, yaml, Config{})
	if n := len(f.sent()); n != 1 {
		t.Errorf("%d chat requests, want the dispatch alone: the repair is refused unsent\n%s", n, log)
	}
	if !strings.Contains(log, "repair only attempt 1 backend error: context window: the prompt is about") {
		t.Errorf("the log does not name the repair's refusal:\n%s", log)
	}
	if row := readLocalNodeRow(t, store, "only"); row.status != "rejected" {
		t.Errorf("node is %s, want rejected once its repair is refused", row.status)
	}
}

// Every call of a tool loop gets its own line in the run log, numbered.
func TestContextGuard_EveryCallIsNoted(t *testing.T) {
	counts := []int{10, 20}
	var turn atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if turn.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\".\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
			return
		}
		_, _ = w.Write([]byte(chatReply("done", "stop", counts[1], 4)))
	}))
	defer srv.Close()
	b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	res, err := b.Run(context.Background(), RunRequest{Prompt: "hi", Registry: NewToolRegistry(t.TempDir(), nil, NewFSRecorder())})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.ContextNotes) != len(counts) {
		t.Fatalf("%d notes, want one per call: %q", len(res.ContextNotes), res.ContextNotes)
	}
	var log bytes.Buffer
	rc := &runtimeContext{logf: makeLogf(&log)}
	rc.noteContext("node loop", res)
	for i, n := range counts {
		if want := fmt.Sprintf("node loop call %d: prompt about", i+1); !strings.Contains(log.String(), want) {
			t.Errorf("the log lacks %q", want)
		}
		if want := fmt.Sprintf("the server counted %d", n); !strings.Contains(res.ContextNotes[i], want) {
			t.Errorf("note %d %q lacks %q", i+1, res.ContextNotes[i], want)
		}
	}
}

// The advice to declare a window goes to an endpoint on this machine or a
// private network, where a server that drops the start of a prompt runs.
func TestIsLocalNetworkURL(t *testing.T) {
	for url, want := range map[string]bool{
		"http://localhost:11434/v1":    true,
		"http://127.0.0.1:1234/v1":     true,
		"http://192.168.1.20:11434/v1": true,
		"http://10.0.0.5:8080/v1":      true,
		"http://[fe80::1]:8080/v1":     true,
		"https://api.openai.com/v1":    false,
		"https://openrouter.ai/api/v1": false,
		"http://8.8.8.8/v1":            false,
	} {
		if got := isLocalNetworkURL(url); got != want {
			t.Errorf("isLocalNetworkURL(%q) = %v, want %v", url, got, want)
		}
	}
}

// The guard's errors carry token counts; none is read as a rate limit.
func TestContextWindowError_IsNotARateLimit(t *testing.T) {
	err := fmt.Errorf("node: %w", &contextWindowError{"context window: the prompt is about 4290 tokens"})
	if isRateLimitError(err) {
		t.Error("a context-window error was read as a rate limit")
	}
	var cw *contextWindowError
	if !errors.As(err, &cw) {
		t.Error("the wrapped error lost its type")
	}
}
