package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewOpenAIBackend_RequiresAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	_, err := NewOpenAIBackend(Config{})
	if err == nil {
		t.Fatal("expected error when OPENAI_API_KEY is unset")
	}
	if !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Errorf("error = %v; expected mention of OPENAI_API_KEY", err)
	}
}

func TestNewOpenAIBackend_HappyPath(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_ORG", "org-x")
	b, err := NewOpenAIBackend(Config{})
	if err != nil {
		t.Fatalf("NewOpenAIBackend: %v", err)
	}
	if b.APIKey != "sk-test" {
		t.Errorf("APIKey = %q; want sk-test", b.APIKey)
	}
	if b.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("BaseURL = %q; want default", b.BaseURL)
	}
	if b.Org != "org-x" {
		t.Errorf("Org = %q; want org-x", b.Org)
	}
}

func TestNewOpenAIBackend_RespectsBaseURL(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_BASE_URL", "https://my-azure.example/v1/")
	b, err := NewOpenAIBackend(Config{})
	if err != nil {
		t.Fatalf("NewOpenAIBackend: %v", err)
	}
	if b.BaseURL != "https://my-azure.example/v1" {
		t.Errorf("BaseURL = %q; want trailing slash trimmed", b.BaseURL)
	}
}

func TestResolveOpenAIModel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "gpt-5.1"},
		{"sonnet", "gpt-5.1"},
		{"  Sonnet ", "gpt-5.1"},
		{"opus", "gpt-5.1-pro"},
		{"haiku", "gpt-5.1-mini"},
		{"gpt-4o", "gpt-4o"}, // pass-through
	}
	for _, tc := range cases {
		if got := ResolveOpenAIModel(tc.in); got != tc.want {
			t.Errorf("ResolveOpenAIModel(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestOpenAIBackend_Run_NoToolCalls_ReturnsFinalText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("missing Authorization header")
		}
		_, _ = w.Write([]byte(`{
			"choices":[{"finish_reason":"stop","message":{"content":"hello world"}}],
			"usage":{"prompt_tokens":5,"completion_tokens":3}
		}`))
	}))
	defer srv.Close()

	b := &OpenAIBackend{
		APIKey:  "sk-test",
		BaseURL: srv.URL,
		Client:  srv.Client(),
	}
	reg := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
	res, err := b.Run(context.Background(), RunRequest{
		System:   "be helpful",
		Prompt:   "hi",
		Model:    "sonnet",
		Registry: reg,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinalText != "hello world" {
		t.Errorf("FinalText = %q; want %q", res.FinalText, "hello world")
	}
	if res.InputTokens != 5 || res.OutputTokens != 3 {
		t.Errorf("tokens = %d/%d; want 5/3", res.InputTokens, res.OutputTokens)
	}
	if res.StopReason != "stop" {
		t.Errorf("StopReason = %q; want stop", res.StopReason)
	}
}

func TestOpenAIBackend_Run_RequiresRegistry(t *testing.T) {
	b := &OpenAIBackend{APIKey: "k", BaseURL: "http://localhost", Client: http.DefaultClient}
	_, err := b.Run(context.Background(), RunRequest{Prompt: "hi"})
	if err == nil {
		t.Fatal("expected error when registry is nil")
	}
}

func TestOpenAIBackend_Run_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limit", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	reg := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
	_, err := b.Run(context.Background(), RunRequest{Prompt: "hi", Registry: reg})
	if err == nil {
		t.Fatal("expected error for 429 response")
	}
}

func TestOpenAIBackend_Run_EmptyChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[],"usage":{}}`))
	}))
	defer srv.Close()
	b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	reg := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
	_, err := b.Run(context.Background(), RunRequest{Prompt: "hi", Registry: reg})
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
}

func TestOpenAIBackend_Run_OrgHeader(t *testing.T) {
	gotOrg := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOrg = r.Header.Get("OpenAI-Organization")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"ok"}}],"usage":{}}`))
	}))
	defer srv.Close()
	b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL, Org: "org-foo", Client: srv.Client()}
	reg := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
	_, err := b.Run(context.Background(), RunRequest{Prompt: "hi", Registry: reg})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotOrg != "org-foo" {
		t.Errorf("OpenAI-Organization header = %q; want org-foo", gotOrg)
	}
}

func TestOpenAIToolsFromRegistry_EmptyOnEmptyRegistry(t *testing.T) {
	reg := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
	tools := openaiToolsFromRegistry(reg)
	for _, tool := range tools {
		// Each tool emitted must have a function block.
		fn, ok := tool["function"].(map[string]any)
		if !ok {
			t.Errorf("tool missing function block: %v", tool)
			continue
		}
		if fn["name"] == "" {
			t.Errorf("tool name empty")
		}
	}
}

func TestOpenAIBackend_CallChatCompletions_DecodesUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got["model"] != "gpt-5.1" {
			t.Errorf("model = %v; want gpt-5.1", got["model"])
		}
		_, _ = w.Write([]byte(`{
			"choices":[{"finish_reason":"length","message":{"content":"abc"}}],
			"usage":{"prompt_tokens":10,"completion_tokens":7}
		}`))
	}))
	defer srv.Close()
	b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	resp, err := b.callChatCompletions(context.Background(), map[string]any{"model": "gpt-5.1"})
	if err != nil {
		t.Fatalf("callChatCompletions: %v", err)
	}
	if resp.Usage.PromptTokens != 10 || resp.Usage.CompletionTokens != 7 {
		t.Errorf("usage = %d/%d; want 10/7", resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
	}
}

func TestOpenAIBackend_CallChatCompletions_HTTPError(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", http.StatusUnauthorized, "invalid api key"},
		{"rate_limit", http.StatusTooManyRequests, "rate limit exceeded"},
		{"server_error", http.StatusInternalServerError, "internal error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, tc.body, tc.status)
			}))
			defer srv.Close()
			b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
			_, err := b.callChatCompletions(context.Background(), map[string]any{"model": "gpt-5.1"})
			if err == nil {
				t.Fatalf("expected error for HTTP %d", tc.status)
			}
			if !strings.Contains(err.Error(), "openai HTTP") {
				t.Errorf("error %q does not mention 'openai HTTP'", err.Error())
			}
		})
	}
}

func TestOpenAIBackend_CallChatCompletions_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not-valid-json`))
	}))
	defer srv.Close()
	b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	_, err := b.callChatCompletions(context.Background(), map[string]any{"model": "gpt-5.1"})
	if err == nil {
		t.Fatal("expected error for malformed JSON response")
	}
	if !strings.Contains(err.Error(), "openai decode") {
		t.Errorf("error %q does not mention 'openai decode'", err.Error())
	}
}

func TestOpenAIBackend_CallChatCompletions_TransportError(t *testing.T) {
	// Use a server that is immediately closed so the client gets a connection error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	cli := srv.Client()
	srv.Close() // closed before the request is made
	b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL, Client: cli}
	_, err := b.callChatCompletions(context.Background(), map[string]any{"model": "gpt-5.1"})
	if err == nil {
		t.Fatal("expected error when server is unreachable")
	}
	if !strings.Contains(err.Error(), "openai POST") {
		t.Errorf("error %q does not mention 'openai POST'", err.Error())
	}
}

func TestOpenAIBackend_CallChatCompletions_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Never respond; the cancelled context should cut the request short.
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before issuing the request
	b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	_, err := b.callChatCompletions(ctx, map[string]any{"model": "gpt-5.1"})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

// TestOpenAIBackend_Run_ToolCallLoop verifies the multi-turn tool-use path: the
// server returns a tool_call on turn 1, the registry dispatches it, and the
// server returns a normal stop on turn 2.
func TestOpenAIBackend_Run_ToolCallLoop(t *testing.T) {
	turn := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turn++
		if turn == 1 {
			// Return a tool_call the registry knows about (read_file is always registered).
			_, _ = w.Write([]byte(`{
				"choices":[{"finish_reason":"tool_calls","message":{
					"content":"",
					"tool_calls":[{
						"id":"call_1","type":"function",
						"function":{"name":"read_file","arguments":"{\"path\":\".\"}"}
					}]
				}}],
				"usage":{"prompt_tokens":10,"completion_tokens":5}
			}`))
			return
		}
		// Turn 2: normal stop.
		_, _ = w.Write([]byte(`{
			"choices":[{"finish_reason":"stop","message":{"content":"done"}}],
			"usage":{"prompt_tokens":20,"completion_tokens":4}
		}`))
	}))
	defer srv.Close()

	b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	reg := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
	res, err := b.Run(context.Background(), RunRequest{Prompt: "hi", Registry: reg})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinalText != "done" {
		t.Errorf("FinalText = %q; want done", res.FinalText)
	}
	if res.ToolUses != 1 {
		t.Errorf("ToolUses = %d; want 1", res.ToolUses)
	}
	if res.Turns != 2 {
		t.Errorf("Turns = %d; want 2", res.Turns)
	}
}

// TestOpenAIBackend_Run_MaxTurns verifies that the loop exits with
// StopReason="max_turns" and an error naming the turn cap when the server
// keeps returning tool_calls, the wrap-up call's reply included: the node
// has no answer.
func TestOpenAIBackend_Run_MaxTurns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"choices":[{"finish_reason":"tool_calls","message":{
				"content":"",
				"tool_calls":[{
					"id":"call_x","type":"function",
					"function":{"name":"read_file","arguments":"{\"path\":\".\"}"}
				}]
			}}],
			"usage":{"prompt_tokens":1,"completion_tokens":1}
		}`))
	}))
	defer srv.Close()

	b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	reg := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
	const maxTurns = 2 // small cap so the test is fast
	res, err := b.Run(context.Background(), RunRequest{
		Prompt:   "loop forever",
		Registry: reg,
		MaxTurns: maxTurns,
	})
	want := fmt.Sprintf("no final reply after %d turns", maxTurns)
	if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "max_turns") {
		t.Fatalf("err = %v; want it to name max_turns and %q", err, want)
	}
	if res == nil || res.StopReason != "max_turns" {
		t.Fatalf("result = %+v; want StopReason max_turns", res)
	}
	// Every call's usage (1 in, 1 out), the wrap-up's included, comes back
	// with the error.
	if res.Turns != maxTurns+1 || res.InputTokens != maxTurns+1 || res.OutputTokens != maxTurns+1 {
		t.Errorf("turns/in/out = %d/%d/%d; want %d each", res.Turns, res.InputTokens, res.OutputTokens, maxTurns+1)
	}
}

// TestOpenAIBackend_Run_UnknownToolError verifies that a call to a tool the
// registry does not hold, such as one a node's allowlist removed, is handed
// back to the model as an error tool message, and the model's next answer
// ends the call.
func TestOpenAIBackend_Run_UnknownToolError(t *testing.T) {
	var second map[string]any
	turn := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turn++
		if turn == 1 {
			_, _ = w.Write([]byte(`{
				"choices":[{"finish_reason":"tool_calls","message":{
					"content":"",
					"tool_calls":[{
						"id":"call_2","type":"function",
						"function":{"name":"bash","arguments":"{\"command\":\"git log\"}"}
					}]
				}}],
				"usage":{"prompt_tokens":2,"completion_tokens":1}
			}`))
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&second)
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"done"}}],"usage":{}}`))
	}))
	defer srv.Close()

	b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	reg := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
	reg.Restrict([]string{"read_file"})
	res, err := b.Run(context.Background(), RunRequest{Prompt: "hi", Registry: reg})
	if err != nil {
		t.Fatalf("Run: %v; want the refusal handed back to the model", err)
	}
	if res.FinalText != "done" {
		t.Errorf("FinalText = %q; want done", res.FinalText)
	}
	msgs, _ := second["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	content, _ := last["content"].(string)
	if last["role"] != "tool" || last["tool_call_id"] != "call_2" || !strings.HasPrefix(content, "ERROR: ") ||
		!strings.Contains(content, "unknown tool: bash") || !strings.Contains(content, "read_file") {
		t.Errorf("the model's next turn ends with %v; want an error tool message naming bash and read_file", last)
	}
}

// TestOpenAIBackend_Deterministic verifies that when Temperature and Seed are
// set on RunRequest, they appear in the outgoing JSON body with the correct
// values, and top_p is forced to 1.0.
func TestOpenAIBackend_Deterministic(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		_, _ = w.Write([]byte(`{
			"choices":[{"finish_reason":"stop","message":{"content":"deterministic answer"}}],
			"usage":{"prompt_tokens":5,"completion_tokens":3}
		}`))
	}))
	defer srv.Close()

	temp := 0.0
	seed := int64(12345)
	b := &OpenAIBackend{
		APIKey:  "sk-test",
		BaseURL: srv.URL,
		Client:  srv.Client(),
	}
	reg := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
	res, err := b.Run(context.Background(), RunRequest{
		Prompt:      "hi",
		Registry:    reg,
		Temperature: &temp,
		Seed:        &seed,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinalText != "deterministic answer" {
		t.Errorf("FinalText = %q; want deterministic answer", res.FinalText)
	}
	if capturedBody["temperature"] != 0.0 {
		t.Errorf("temperature = %v; want 0", capturedBody["temperature"])
	}
	if capturedBody["top_p"] != 1.0 {
		t.Errorf("top_p = %v; want 1.0", capturedBody["top_p"])
	}
	// JSON numbers decode as float64; 12345 == 12345.0
	if capturedBody["seed"] != float64(12345) {
		t.Errorf("seed = %v; want 12345", capturedBody["seed"])
	}
}

// TestOpenAIBackend_DeterministicTemperatureOnly verifies that when only
// Temperature is set (no seed), body contains temperature + top_p but no seed.
func TestOpenAIBackend_DeterministicTemperatureOnly(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"ok"}}],"usage":{}}`))
	}))
	defer srv.Close()

	temp := 0.0
	b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	reg := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
	_, err := b.Run(context.Background(), RunRequest{Prompt: "hi", Registry: reg, Temperature: &temp})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if capturedBody["temperature"] != 0.0 {
		t.Errorf("temperature = %v; want 0", capturedBody["temperature"])
	}
	if capturedBody["top_p"] != 1.0 {
		t.Errorf("top_p = %v; want 1.0", capturedBody["top_p"])
	}
	if _, ok := capturedBody["seed"]; ok {
		t.Errorf("seed should not be present when Seed is nil, got %v", capturedBody["seed"])
	}
}
