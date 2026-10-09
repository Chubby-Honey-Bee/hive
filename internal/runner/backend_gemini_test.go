package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
)

func TestNewGeminiBackend_RequiresAPIKey(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	_, err := NewGeminiBackend(Config{})
	if err == nil {
		t.Fatal("expected error when no key is set")
	}
}

func TestNewGeminiBackend_AcceptsGoogleKeyFallback(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "google-fallback")
	t.Setenv("GEMINI_BASE_URL", "")
	b, err := NewGeminiBackend(Config{})
	if err != nil {
		t.Fatalf("NewGeminiBackend: %v", err)
	}
	if b.APIKey != "google-fallback" {
		t.Errorf("APIKey = %q; want google-fallback", b.APIKey)
	}
	if b.BaseURL != "https://generativelanguage.googleapis.com/v1beta" {
		t.Errorf("BaseURL = %q; want default", b.BaseURL)
	}
}

func TestNewGeminiBackend_RespectsBaseURL(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "k")
	t.Setenv("GEMINI_BASE_URL", "https://my-gateway/v1beta/")
	b, err := NewGeminiBackend(Config{})
	if err != nil {
		t.Fatalf("NewGeminiBackend: %v", err)
	}
	if b.BaseURL != "https://my-gateway/v1beta" {
		t.Errorf("BaseURL = %q; want trailing slash trimmed", b.BaseURL)
	}
}

func TestResolveGeminiModel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "gemini-2.5-pro"},
		{"sonnet", "gemini-2.5-pro"},
		{"opus", "gemini-2.5-pro"},
		{"haiku", "gemini-2.5-flash"},
		{"  HAIKU  ", "gemini-2.5-flash"},
		{"gemini-1.5-pro", "gemini-1.5-pro"},
	}
	for _, tc := range cases {
		if got := ResolveGeminiModel(tc.in); got != tc.want {
			t.Errorf("ResolveGeminiModel(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestGeminiBackend_Run_NoToolCalls_ReturnsFinalText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"hello"}]}}],
			"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2}
		}`))
	}))
	defer srv.Close()

	b := &GeminiBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	reg := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
	res, err := b.Run(context.Background(), RunRequest{
		Prompt:   "hi",
		System:   "be helpful",
		Model:    "haiku",
		Registry: reg,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinalText != "hello" {
		t.Errorf("FinalText = %q; want hello", res.FinalText)
	}
	if res.InputTokens != 4 || res.OutputTokens != 2 {
		t.Errorf("tokens = %d/%d; want 4/2", res.InputTokens, res.OutputTokens)
	}
}

func TestGeminiBackend_Run_RequiresRegistry(t *testing.T) {
	b := &GeminiBackend{APIKey: "k", BaseURL: "http://x", Client: http.DefaultClient}
	_, err := b.Run(context.Background(), RunRequest{Prompt: "hi"})
	if err == nil {
		t.Fatal("expected error when registry is nil")
	}
}

func TestGeminiBackend_Run_NoCandidates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"candidates":[]}`))
	}))
	defer srv.Close()
	b := &GeminiBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	reg := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
	_, err := b.Run(context.Background(), RunRequest{Prompt: "hi", Registry: reg})
	if err == nil {
		t.Fatal("expected error for empty candidates")
	}
}

func TestGeminiBackend_Run_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	b := &GeminiBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	reg := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
	_, err := b.Run(context.Background(), RunRequest{Prompt: "hi", Registry: reg})
	if err == nil {
		t.Fatal("expected error on HTTP 500")
	}
	if !strings.Contains(err.Error(), "gemini HTTP 500") {
		t.Errorf("error = %v; expected gemini HTTP 500", err)
	}
}

func TestGeminiBackend_CallGenerateContent_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()
	b := &GeminiBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	_, err := b.callGenerateContent(context.Background(), "gemini-2.5-pro", map[string]any{})
	if err == nil {
		t.Fatal("expected decode error")
	}
}

func TestGeminiBackend_CallGenerateContent_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method = %q; want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q; want application/json", ct)
		}
		_, _ = w.Write([]byte(`{
			"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":"ok"}]}}],
			"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1}
		}`))
	}))
	defer srv.Close()
	b := &GeminiBackend{APIKey: "testkey", BaseURL: srv.URL, Client: srv.Client()}
	resp, err := b.callGenerateContent(context.Background(), "gemini-2.5-pro", map[string]any{"contents": []any{}})
	if err != nil {
		t.Fatalf("callGenerateContent: %v", err)
	}
	if len(resp.Candidates) != 1 {
		t.Fatalf("len(Candidates) = %d; want 1", len(resp.Candidates))
	}
	if resp.UsageMetadata.PromptTokenCount != 3 {
		t.Errorf("PromptTokenCount = %d; want 3", resp.UsageMetadata.PromptTokenCount)
	}
}

func TestGeminiBackend_CallGenerateContent_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request"}}`))
	}))
	defer srv.Close()
	b := &GeminiBackend{APIKey: "testkey", BaseURL: srv.URL, Client: srv.Client()}
	_, err := b.callGenerateContent(context.Background(), "gemini-2.5-pro", map[string]any{})
	if err == nil {
		t.Fatal("expected error on HTTP 400")
	}
	if !strings.Contains(err.Error(), "gemini HTTP 400") {
		t.Errorf("error = %v; expected gemini HTTP 400", err)
	}
}

func TestGeminiBackend_CallGenerateContent_NetworkFailure(t *testing.T) {
	// Use a closed server so the Do call fails.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	client := srv.Client()
	srv.Close() // close immediately so the request fails
	b := &GeminiBackend{APIKey: "testkey", BaseURL: srv.URL, Client: client}
	_, err := b.callGenerateContent(context.Background(), "gemini-2.5-pro", map[string]any{})
	if err == nil {
		t.Fatal("expected error on network failure")
	}
	if !strings.Contains(err.Error(), "gemini POST") {
		t.Errorf("error = %v; expected gemini POST prefix", err)
	}
}

// TestGeminiBackend_Run_ToolCallLoop verifies the multi-turn tool-use path:
// turn 1 returns a functionCall, the registry dispatches it, turn 2 returns
// normal text so the loop exits cleanly.
func TestGeminiBackend_Run_ToolCallLoop(t *testing.T) {
	turn := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turn++
		if turn == 1 {
			_, _ = w.Write([]byte(`{
				"candidates":[{"finishReason":"","content":{"role":"model","parts":[
					{"functionCall":{"name":"read_file","args":{"path":"."}}}
				]}}],
				"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":"done"}]}}],
			"usageMetadata":{"promptTokenCount":16,"candidatesTokenCount":2}
		}`))
	}))
	defer srv.Close()

	b := &GeminiBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
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

// TestGeminiBackend_Run_MaxTurns verifies that the loop exits with
// StopReason="max_turns" and an error naming the turn cap when the server
// keeps returning functionCalls, the wrap-up call's reply included: the node
// has no answer.
func TestGeminiBackend_Run_MaxTurns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"candidates":[{"finishReason":"","content":{"role":"model","parts":[
				{"functionCall":{"name":"read_file","args":{"path":"."}}}
			]}}],
			"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}
		}`))
	}))
	defer srv.Close()

	b := &GeminiBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
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

// After its turns run out the loop makes one wrap-up call with function
// calling set to NONE and, in the last user turn after the function
// responses, the message that no tool call is left; the reply there is the
// answer.
func TestGeminiBackend_Run_MaxTurnsWrapsUp(t *testing.T) {
	var last map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		last = body
		cfg, _ := body["toolConfig"].(map[string]any)
		if fc, _ := cfg["functionCallingConfig"].(map[string]any); fc["mode"] == "NONE" {
			_, _ = w.Write([]byte(`{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":"wrapped up"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"candidates":[{"finishReason":"","content":{"role":"model","parts":[{"functionCall":{"name":"read_file","args":{"path":"."}}}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`))
	}))
	defer srv.Close()
	b := &GeminiBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	const maxTurns = 2
	res, err := b.Run(context.Background(), RunRequest{Prompt: "loop", Registry: NewToolRegistry(t.TempDir(), nil, NewFSRecorder()), MaxTurns: maxTurns})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinalText != "wrapped up" || !res.WrappedUp || res.Turns != maxTurns+1 {
		t.Errorf("final %q, wrapped up %v, turns %d; want the wrap-up's answer after %d calls", res.FinalText, res.WrappedUp, res.Turns, maxTurns+1)
	}
	contents, _ := last["contents"].([]any)
	lastTurn, _ := contents[len(contents)-1].(map[string]any)
	parts, _ := lastTurn["parts"].([]any)
	first, _ := parts[0].(map[string]any)
	text, _ := parts[len(parts)-1].(map[string]any)
	if lastTurn["role"] != "user" || first["functionResponse"] == nil || !strings.Contains(fmt.Sprint(text["text"]), "no tool call is left") {
		t.Errorf("the wrap-up's last turn is %v; want the user turn holding the function responses, then the wrap-up text", lastTurn)
	}
}

// TestGeminiBackend_Run_UnknownToolError verifies that a functionCall to a
// tool the registry does not hold is answered with an error
// functionResponse, and the model's next answer ends the call.
func TestGeminiBackend_Run_UnknownToolError(t *testing.T) {
	var second map[string]any
	turn := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turn++
		if turn == 1 {
			_, _ = w.Write([]byte(`{
				"candidates":[{"finishReason":"","content":{"role":"model","parts":[
					{"functionCall":{"name":"no_such_tool","args":null}}
				]}}],
				"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1}
			}`))
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&second)
		_, _ = w.Write([]byte(`{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":"ok"}]}}],"usageMetadata":{}}`))
	}))
	defer srv.Close()

	b := &GeminiBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	reg := &ToolRegistry{Handlers: map[string]ToolHandler{}}
	res, err := b.Run(context.Background(), RunRequest{Prompt: "hi", Registry: reg})
	if err != nil {
		t.Fatalf("Run: %v; want the refusal handed back to the model", err)
	}
	if res.FinalText != "ok" {
		t.Errorf("FinalText = %q; want ok", res.FinalText)
	}
	contents, _ := second["contents"].([]any)
	last, _ := contents[len(contents)-1].(map[string]any)
	parts, _ := last["parts"].([]any)
	part, _ := parts[0].(map[string]any)
	fr, _ := part["functionResponse"].(map[string]any)
	resp, _ := fr["response"].(map[string]any)
	out, _ := resp["output"].(string)
	if fr["name"] != "no_such_tool" || resp["error"] != true || !strings.Contains(out, "unknown tool: no_such_tool") {
		t.Errorf("the model's next turn ends with %v; want an error functionResponse naming no_such_tool", last)
	}
}

// TestGeminiBackend_Run_ToolCallNilArgs verifies that a functionCall with null
// args (nil map) is handled without panic — the backend substitutes an empty map.
func TestGeminiBackend_Run_ToolCallNilArgs(t *testing.T) {
	turn := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turn++
		if turn == 1 {
			// args omitted → JSON null → nil map in Go
			_, _ = w.Write([]byte(`{
				"candidates":[{"finishReason":"","content":{"role":"model","parts":[
					{"functionCall":{"name":"read_file"}}
				]}}],
				"usageMetadata":{}
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":"ok"}]}}],
			"usageMetadata":{}
		}`))
	}))
	defer srv.Close()

	b := &GeminiBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	reg := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
	res, err := b.Run(context.Background(), RunRequest{Prompt: "hi", Registry: reg})
	if err != nil {
		t.Fatalf("Run with nil args: %v", err)
	}
	if res.FinalText != "ok" {
		t.Errorf("FinalText = %q; want ok", res.FinalText)
	}
}

func TestGeminiToolsFromRegistry_Shape(t *testing.T) {
	reg := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
	tools := geminiToolsFromRegistry(reg)
	for _, tool := range tools {
		funcs, ok := tool["functionDeclarations"]
		if !ok {
			t.Errorf("tool missing functionDeclarations: %v", tool)
			continue
		}
		if funcs == nil {
			t.Error("functionDeclarations is nil")
		}
	}
}

func makeGeminiTestTool(name, desc string, props map[string]any, required []string) anthropic.ToolUnionParam {
	tp := anthropic.ToolParam{
		Name: name,
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: props,
			Required:   required,
		},
	}
	if desc != "" {
		tp.Description = param.NewOpt(desc)
	}
	return anthropic.ToolUnionParam{OfTool: &tp}
}

func TestGeminiToolsFromRegistry(t *testing.T) {
	t.Run("empty registry returns nil", func(t *testing.T) {
		reg := &ToolRegistry{Handlers: map[string]ToolHandler{}}
		got := geminiToolsFromRegistry(reg)
		if got != nil {
			t.Fatalf("want nil for empty registry, got %v", got)
		}
	})

	t.Run("single tool produces correct structure", func(t *testing.T) {
		props := map[string]any{"path": map[string]any{"type": "string"}}
		req := []string{"path"}
		reg := &ToolRegistry{
			Handlers: map[string]ToolHandler{},
			Schemas: []anthropic.ToolUnionParam{
				makeGeminiTestTool("read_file", "reads a file", props, req),
			},
		}
		got := geminiToolsFromRegistry(reg)
		if len(got) != 1 {
			t.Fatalf("want 1 top-level entry, got %d", len(got))
		}
		decls, ok := got[0]["functionDeclarations"]
		if !ok {
			t.Fatal("missing functionDeclarations key")
		}
		declSlice, ok := decls.([]map[string]any)
		if !ok {
			t.Fatalf("functionDeclarations type = %T; want []map[string]any", decls)
		}
		if len(declSlice) != 1 {
			t.Fatalf("want 1 declaration, got %d", len(declSlice))
		}
		d := declSlice[0]
		if d["name"] != "read_file" {
			t.Errorf("name = %v; want read_file", d["name"])
		}
		if d["description"] != "reads a file" {
			t.Errorf("description = %v; want 'reads a file'", d["description"])
		}
		if d["parameters"] == nil {
			t.Error("parameters is nil; want schema map")
		}
	})

	t.Run("multiple tools all appear in declarations", func(t *testing.T) {
		reg := &ToolRegistry{
			Handlers: map[string]ToolHandler{},
			Schemas: []anthropic.ToolUnionParam{
				makeGeminiTestTool("tool_a", "alpha", nil, nil),
				makeGeminiTestTool("tool_b", "beta", nil, nil),
				makeGeminiTestTool("tool_c", "gamma", nil, nil),
			},
		}
		got := geminiToolsFromRegistry(reg)
		if len(got) != 1 {
			t.Fatalf("want exactly 1 wrapper entry, got %d", len(got))
		}
		decls := got[0]["functionDeclarations"].([]map[string]any)
		if len(decls) != 3 {
			t.Fatalf("want 3 declarations, got %d", len(decls))
		}
		names := map[string]bool{}
		for _, d := range decls {
			names[d["name"].(string)] = true
		}
		for _, want := range []string{"tool_a", "tool_b", "tool_c"} {
			if !names[want] {
				t.Errorf("declaration %q missing from output", want)
			}
		}
	})

	t.Run("nil OfTool entries are skipped", func(t *testing.T) {
		reg := &ToolRegistry{
			Handlers: map[string]ToolHandler{},
			Schemas: []anthropic.ToolUnionParam{
				{OfTool: nil}, // non-custom variant — should be skipped by NeutralSchemas
				makeGeminiTestTool("kept_tool", "stays", nil, nil),
			},
		}
		got := geminiToolsFromRegistry(reg)
		if len(got) != 1 {
			t.Fatalf("want 1 wrapper entry, got %d", len(got))
		}
		decls := got[0]["functionDeclarations"].([]map[string]any)
		if len(decls) != 1 {
			t.Fatalf("want 1 declaration after skipping nil OfTool, got %d", len(decls))
		}
		if decls[0]["name"] != "kept_tool" {
			t.Errorf("name = %v; want kept_tool", decls[0]["name"])
		}
	})
}

// TestGeminiBackend_Deterministic verifies that Temperature and Seed are
// forwarded as generationConfig in the outgoing Gemini request body.
func TestGeminiBackend_Deterministic(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		_, _ = w.Write([]byte(`{
			"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"ok"}]}}],
			"usageMetadata":{}
		}`))
	}))
	defer srv.Close()

	temp := 0.0
	seed := int64(12345)
	b := &GeminiBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	reg := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
	_, err := b.Run(context.Background(), RunRequest{
		Prompt:      "hi",
		Registry:    reg,
		Temperature: &temp,
		Seed:        &seed,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	genCfgRaw, ok := capturedBody["generationConfig"]
	if !ok {
		t.Fatal("generationConfig missing from request body")
	}
	genCfg, ok := genCfgRaw.(map[string]any)
	if !ok {
		t.Fatalf("generationConfig type = %T; want map[string]any", genCfgRaw)
	}
	if genCfg["temperature"] != 0.0 {
		t.Errorf("generationConfig.temperature = %v; want 0", genCfg["temperature"])
	}
	if genCfg["topP"] != 1.0 {
		t.Errorf("generationConfig.topP = %v; want 1.0", genCfg["topP"])
	}
	if genCfg["topK"] != float64(1) {
		t.Errorf("generationConfig.topK = %v; want 1", genCfg["topK"])
	}
	// JSON numbers decode as float64
	if genCfg["seed"] != float64(12345) {
		t.Errorf("generationConfig.seed = %v; want 12345", genCfg["seed"])
	}
}

// TestGeminiBackend_DeterministicTemperatureOnly verifies that generationConfig
// is injected with temperature + topP + topK even when Seed is nil.
func TestGeminiBackend_DeterministicTemperatureOnly(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		_, _ = w.Write([]byte(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"ok"}]}}],"usageMetadata":{}}`))
	}))
	defer srv.Close()

	temp := 0.0
	b := &GeminiBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	reg := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
	_, err := b.Run(context.Background(), RunRequest{Prompt: "hi", Registry: reg, Temperature: &temp})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	genCfgRaw, ok := capturedBody["generationConfig"]
	if !ok {
		t.Fatal("generationConfig missing from request body")
	}
	genCfg := genCfgRaw.(map[string]any)
	if _, hasSeed := genCfg["seed"]; hasSeed {
		t.Errorf("seed should not be in generationConfig when Seed is nil")
	}
	if genCfg["temperature"] != 0.0 {
		t.Errorf("temperature = %v; want 0", genCfg["temperature"])
	}
}

// TestGeminiBackend_NoDeterministicFields verifies that generationConfig is
// absent when neither Temperature nor Seed is set.
func TestGeminiBackend_NoDeterministicFields(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		_, _ = w.Write([]byte(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"ok"}]}}],"usageMetadata":{}}`))
	}))
	defer srv.Close()

	b := &GeminiBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	reg := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
	_, err := b.Run(context.Background(), RunRequest{Prompt: "hi", Registry: reg})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, ok := capturedBody["generationConfig"]; ok {
		t.Error("generationConfig should be absent when no Temperature/Seed set")
	}
}
