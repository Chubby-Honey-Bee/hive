package runner

// Tests for the local-endpoint-honesty change: the HTTP deadline follows the
// TTL, sampling and reasoning fields go out exactly when set, an incomplete
// reply fails its node, the model preflight refuses before any call, and
// provenance names the endpoint. Every server is an httptest fake; no model
// is called.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// fakeOpenAI is an OpenAI-compatible endpoint: GET /models lists models (404
// when models is nil; LM Studio's GET /api/v1/models, which the
// context-window guard asks, is not that list), POST /api/show answers Ollama's capabilities from caps
// (404 when caps is nil or lacks the model), and POST /chat/completions
// answers with reply after delay, recording each request body.
type fakeOpenAI struct {
	models []string
	caps   map[string][]string
	delay  time.Duration
	reply  func(body map[string]any) string

	mu     sync.Mutex
	bodies []map[string]any
	chats  atomic.Int64
	lists  atomic.Int64 // GET /models requests
	shows  atomic.Int64 // POST /api/show requests
}

func (f *fakeOpenAI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/show":
		f.shows.Add(1)
		var req struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		caps, ok := f.caps[req.Model]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": caps})
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") && !strings.HasPrefix(r.URL.Path, "/api/"):
		f.lists.Add(1)
		if f.models == nil {
			http.NotFound(w, r)
			return
		}
		data := make([]map[string]any, 0, len(f.models))
		for _, m := range f.models {
			data = append(data, map[string]any{"id": m, "object": "model"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	case strings.HasSuffix(r.URL.Path, "/chat/completions"):
		f.chats.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.mu.Unlock()
		select {
		case <-time.After(f.delay):
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(f.reply(body)))
	default:
		http.NotFound(w, r)
	}
}

// chatReply renders a one-choice chat completion.
func chatReply(content, finish string, in, out int) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"finish_reason": finish, "message": map[string]any{"content": content}}},
		"usage":   map[string]any{"prompt_tokens": in, "completion_tokens": out},
	})
	return string(b)
}

// localRun runs yaml through Run on the OpenAI-compatible backend pointed at
// srv, and returns the result, the error, the store and the run log.
func localRun(t *testing.T, srv *httptest.Server, yaml string, cfg Config) (*Result, *db.Store, string, error) {
	t.Helper()
	t.Setenv("OPENAI_BASE_URL", srv.URL+"/v1")
	t.Setenv("OPENAI_API_KEY", "local")
	t.Setenv("HIVE_PROVIDER", "")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	t.Setenv("HIVE_DISABLE_RATE_LIMIT", "1")
	dir := t.TempDir()
	wf := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(wf, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	cfg.WorkflowYAML = wf
	cfg.ProjectName = "local"
	cfg.ProjectDir = dir
	cfg.MaxIterations = 10
	cfg.Provider = "openai"
	cfg.Log = &log
	store := newTempStore(t)
	res, err := Run(context.Background(), store, cfg)
	return res, store, log.String(), err
}

// nodeRow reads the columns these tests grade from one node row.
type localNodeRow struct {
	status, errMsg, model, provider, baseURL, rationale string
	in, out                                             int64
}

func readLocalNodeRow(t *testing.T, store *db.Store, node string) localNodeRow {
	t.Helper()
	var r localNodeRow
	if err := store.ReadDB.QueryRow(
		`SELECT status, COALESCE(error,''), COALESCE(resolved_model,''), COALESCE(provider,''),
		        COALESCE(base_url,''), COALESCE(rationale,''), COALESCE(tokens_in,0), COALESCE(tokens_out,0)
		 FROM workflow_node_states WHERE node_name=? ORDER BY id DESC LIMIT 1`, node,
	).Scan(&r.status, &r.errMsg, &r.model, &r.provider, &r.baseURL, &r.rationale, &r.in, &r.out); err != nil {
		t.Fatalf("read node %s: %v", node, err)
	}
	return r
}

const oneNodeYAML = `name: local
nodes:
  only:
    type: agent
    model: %s
    prompt: "answer"
    outputs: [answer]
`

// ---------------------------------------------------------------------------
// 1. The HTTP deadline follows the TTL, and HIVE_HTTP_TIMEOUT overrides it.
// ---------------------------------------------------------------------------

func TestOpenAIBackend_DeadlineIsTheTTL(t *testing.T) {
	cases := []struct {
		delay, ttl, override time.Duration
	}{
		{delay: 50 * time.Millisecond, ttl: 3 * time.Second},
		{delay: 800 * time.Millisecond, ttl: 100 * time.Millisecond},
		{delay: 50 * time.Millisecond, ttl: 10 * time.Millisecond, override: 3 * time.Second},
		{delay: 800 * time.Millisecond, ttl: 30 * time.Second, override: 100 * time.Millisecond},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("delay=%s ttl=%s override=%s", c.delay, c.ttl, c.override), func(t *testing.T) {
			f := &fakeOpenAI{delay: c.delay, reply: func(map[string]any) string { return chatReply("ok", "stop", 1, 1) }}
			srv := httptest.NewServer(f)
			defer srv.Close()
			b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client(), Timeout: c.override}
			_, err := b.Run(context.Background(), RunRequest{Prompt: "hi", TTL: c.ttl, Registry: &ToolRegistry{Handlers: map[string]ToolHandler{}}})

			bound, source := c.ttl, "the call's TTL"
			if c.override > 0 {
				bound, source = c.override, "HIVE_HTTP_TIMEOUT"
			}
			wantOK := c.delay < bound
			if wantOK && err != nil {
				t.Fatalf("a reply after %s within a %s bound failed: %v", c.delay, bound, err)
			}
			if !wantOK {
				want := fmt.Sprintf("no reply within %s (%s)", bound, source)
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("err = %v, want it to contain %q", err, want)
				}
			}
		})
	}
}

// A call on the local provider names local, not openai, when it has no
// reply within its TTL and when it gets no endpoint slot within its budget.
func TestLocalBackend_ErrorsNameLocal(t *testing.T) {
	t.Setenv("HIVE_HTTP_TIMEOUT", "")
	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "1")
	const ttl, delay, wait = 100 * time.Millisecond, 800 * time.Millisecond, 50 * time.Millisecond
	f := &fakeOpenAI{delay: delay, reply: func(map[string]any) string { return chatReply("ok", "stop", 1, 1) }}
	srv := httptest.NewServer(f)
	defer srv.Close()
	t.Setenv("HIVE_LOCAL_BASE_URL", srv.URL+"/v1")
	b, err := NewLocalBackend(Config{})
	if err != nil {
		t.Fatal(err)
	}
	req := RunRequest{Prompt: "hi", TTL: ttl, Registry: &ToolRegistry{Handlers: map[string]ToolHandler{}}}
	_, err = b.Run(context.Background(), req)
	if want := fmt.Sprintf("local: no reply within %s (the call's TTL)", ttl); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("a reply after %s: err = %v, want it to contain %q", delay, err, want)
	}
	release, err := acquireEndpointSlot(context.Background(), "test", b.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	_, err = b.Run(ctx, req)
	if want := "local: no free slot at " + b.BaseURL; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("a held slot: err = %v, want it to contain %q", err, want)
	}
}

func TestNewOpenAIBackend_NoFixedClientTimeout(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "k")
	cases := []struct {
		env     string
		wantErr bool
	}{
		{"", false}, {"45s", false}, {"15m", false}, {"abc", true}, {"-1s", true}, {"0", true},
	}
	for _, c := range cases {
		t.Run("env="+c.env, func(t *testing.T) {
			t.Setenv("HIVE_HTTP_TIMEOUT", c.env)
			b, err := NewOpenAIBackend(Config{})
			if c.wantErr {
				if err == nil || !strings.Contains(err.Error(), "HIVE_HTTP_TIMEOUT") {
					t.Fatalf("err = %v, want HIVE_HTTP_TIMEOUT refused", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if b.Client.Timeout != 0 {
				t.Errorf("client timeout = %s, want none: the call's TTL bounds it", b.Client.Timeout)
			}
			var want time.Duration
			if c.env != "" {
				want, _ = time.ParseDuration(c.env)
			}
			if b.Timeout != want {
				t.Errorf("override = %s, want %s", b.Timeout, want)
			}
		})
	}
}

// TestRun_SlowLocalReply: through a whole run, a slow reply completes when it
// is under the bound and fails its node naming the bound when it is over.
func TestRun_SlowLocalReply(t *testing.T) {
	cases := []struct {
		delay time.Duration
		bound string
	}{
		{150 * time.Millisecond, "3s"},
		{1500 * time.Millisecond, "200ms"},
	}
	for _, c := range cases {
		t.Run(c.delay.String()+"/"+c.bound, func(t *testing.T) {
			t.Setenv("HIVE_HTTP_TIMEOUT", c.bound)
			f := &fakeOpenAI{models: []string{"local-model"}, delay: c.delay,
				reply: func(map[string]any) string { return chatReply(`{"answer":"42"}`, "stop", 3, 2) }}
			srv := httptest.NewServer(f)
			defer srv.Close()
			_, store, _, err := localRun(t, srv, fmt.Sprintf(oneNodeYAML, "local-model"), Config{})
			bound, _ := time.ParseDuration(c.bound)
			row := readLocalNodeRow(t, store, "only")
			if c.delay < bound {
				if err != nil || row.status != "completed" {
					t.Fatalf("run err = %v, node %s; want completed", err, row.status)
				}
				if row.baseURL != srv.URL+"/v1" {
					t.Errorf("base_url = %q, want the endpoint %q", row.baseURL, srv.URL+"/v1")
				}
				return
			}
			want := fmt.Sprintf("no reply within %s (HIVE_HTTP_TIMEOUT)", bound)
			if err == nil || row.status != "failed" || !strings.Contains(row.errMsg, want) {
				t.Fatalf("run err = %v, node %s error %q; want failed naming %q", err, row.status, row.errMsg, want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 2. An incomplete reply fails its node, naming the reason.
// ---------------------------------------------------------------------------

func TestRun_IncompleteReplyFailsNode(t *testing.T) {
	cases := []struct {
		content, finish, want string
	}{
		{`{"answer":"4`, "length", "cut off at the output cap (stop reason length"},
		{"", "stop", `empty reply with no tool call (stop reason "stop")`},
		{"   \n", "stop", `empty reply with no tool call (stop reason "stop")`},
	}
	for _, c := range cases {
		t.Run(c.finish+"/"+fmt.Sprintf("%q", c.content), func(t *testing.T) {
			const in, out = 11, 7
			f := &fakeOpenAI{models: []string{"local-model"},
				reply: func(map[string]any) string { return chatReply(c.content, c.finish, in, out) }}
			srv := httptest.NewServer(f)
			defer srv.Close()
			_, store, _, err := localRun(t, srv, fmt.Sprintf(oneNodeYAML, "local-model"), Config{})
			row := readLocalNodeRow(t, store, "only")
			if err == nil || row.status != "failed" {
				t.Fatalf("run err = %v, node %s; want the node FAILED", err, row.status)
			}
			if !strings.Contains(row.errMsg, c.want) {
				t.Errorf("node error = %q, want it to name %q", row.errMsg, c.want)
			}
			// The failed call's spend is still charged to the node.
			if row.in != in || row.out != out {
				t.Errorf("tokens = %d/%d, want %d/%d", row.in, row.out, in, out)
			}
		})
	}
}

func TestOtherBackends_IncompleteReplyIsAnError(t *testing.T) {
	t.Run("gemini MAX_TOKENS", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"half"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":9}}`))
		}))
		defer srv.Close()
		b := &GeminiBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
		res, err := b.Run(context.Background(), RunRequest{Prompt: "hi", MaxTokens: 9, Registry: &ToolRegistry{Handlers: map[string]ToolHandler{}}})
		if err == nil || !strings.Contains(err.Error(), "stop reason MAX_TOKENS, max tokens 9") {
			t.Fatalf("err = %v, want a MAX_TOKENS error naming the cap", err)
		}
		if res == nil || res.OutputTokens != 9 {
			t.Errorf("result = %+v, want its usage kept for the spend record", res)
		}
	})
	t.Run("anthropic max_tokens and empty", func(t *testing.T) {
		for _, c := range []struct{ text, stop, want string }{
			{"half", "max_tokens", "stop reason max_tokens"},
			{"", "end_turn", "empty reply"},
		} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(anthropicTextResponse(c.text, c.stop, 1, 1)))
			}))
			cr := newTestClaudeRunner(t, srv, &ToolRegistry{Handlers: map[string]ToolHandler{}})
			_, err := cr.Run(context.Background(), "hi")
			srv.Close()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s/%q: err = %v, want %q", c.stop, c.text, err, c.want)
			}
		}
	})
	t.Run("claude CLI max_tokens", func(t *testing.T) {
		script := filepath.Join(t.TempDir(), "claude")
		body := "#!/bin/sh\ncat > /dev/null\nprintf '%s' '{\"type\":\"result\",\"is_error\":false,\"num_turns\":1,\"result\":\"half\",\"stop_reason\":\"max_tokens\",\"usage\":{\"input_tokens\":1,\"output_tokens\":5}}'\n"
		if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		b := &CLIBackend{CLIPath: script, ScratchDir: t.TempDir()}
		_, err := b.Run(context.Background(), RunRequest{Prompt: "hi"})
		if err == nil || !strings.Contains(err.Error(), "stop reason max_tokens") {
			t.Fatalf("err = %v, want a max_tokens error", err)
		}
	})
}

// ---------------------------------------------------------------------------
// 3. The model preflight refuses a model the endpoint does not serve.
// ---------------------------------------------------------------------------

func TestRun_ModelPreflight(t *testing.T) {
	cases := []struct {
		name    string
		served  []string // nil: the endpoint answers 404
		model   string   // "" leaves model and tier unset
		refused bool
	}{
		{"served", []string{"qwen3.5:4b", "ministral-3:8b"}, "qwen3.5:4b", false},
		{"missing", []string{"qwen3.5:4b"}, "ministral-3:8b", true},
		{"bare name matches :latest", []string{"llama3.2:latest"}, "llama3.2", false},
		{"a default model is caught as its OpenAI name", []string{"qwen3.5:4b"}, "", true},
		{"unlisted endpoint is not checked", nil, "anything", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeOpenAI{models: c.served, reply: func(map[string]any) string { return chatReply(`{"answer":"ok"}`, "stop", 1, 1) }}
			srv := httptest.NewServer(f)
			defer srv.Close()
			yaml := fmt.Sprintf(oneNodeYAML, c.model)
			if c.model == "" {
				yaml = strings.Replace(yaml, "    model: \n", "", 1)
			}
			_, store, log, err := localRun(t, srv, yaml, Config{})

			sent := c.model
			if sent == "" {
				sent = ResolveOpenAIModel("sonnet") // the engine's default, as the endpoint would see it
			}
			var runs int
			_ = store.ReadDB.QueryRow(`SELECT COUNT(*) FROM workflow_runs`).Scan(&runs)
			if c.refused {
				if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%q (node only)", sent)) {
					t.Fatalf("err = %v, want a refusal naming %q", err, sent)
				}
				if n := f.chats.Load(); n != 0 {
					t.Errorf("%d chat calls made, want none before the refusal", n)
				}
				if runs != 0 {
					t.Errorf("%d workflow_runs rows, want the refusal to leave none", runs)
				}
				return
			}
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if n := f.chats.Load(); n != 1 {
				t.Errorf("%d chat calls, want 1", n)
			}
			if c.served == nil && !strings.Contains(log, "does not list its models") {
				t.Errorf("log does not say the model check was skipped:\n%s", log)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 4. Sampling and reasoning fields appear in the body exactly when set.
// ---------------------------------------------------------------------------

func TestOpenAIBackend_SamplingFieldsExactlyWhenSet(t *testing.T) {
	f64 := func(v float64) *float64 { return &v }
	temps := []*float64{nil, f64(0), f64(0.7)}
	topPs := []*float64{nil, f64(0.9)}
	reasonings := []string{"", "none", "high"}
	for _, temp := range temps {
		for _, topP := range topPs {
			for _, reasoning := range reasonings {
				name := fmt.Sprintf("temp=%v/top_p=%v/reasoning=%q", deref(temp), deref(topP), reasoning)
				t.Run(name, func(t *testing.T) {
					// The server reports the model can think, so every
					// level is sent (reasoningToSend).
					f := &fakeOpenAI{
						caps:  map[string][]string{ResolveOpenAIModel(""): {"completion", "thinking"}},
						reply: func(map[string]any) string { return chatReply("ok", "stop", 1, 1) },
					}
					srv := httptest.NewServer(f)
					defer srv.Close()
					b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL + "/v1", Client: srv.Client()}
					if _, err := b.Run(context.Background(), RunRequest{
						Prompt: "hi", Registry: &ToolRegistry{Handlers: map[string]ToolHandler{}},
						Temperature: temp, TopP: topP, Reasoning: reasoning,
					}); err != nil {
						t.Fatal(err)
					}
					body := f.bodies[0]
					wantField(t, body, "temperature", temp != nil, deref(temp))
					// top_p is the one set, else 1 at temperature 0 (greedy), else absent.
					switch {
					case topP != nil:
						wantField(t, body, "top_p", true, *topP)
					case temp != nil && *temp == 0:
						wantField(t, body, "top_p", true, 1.0)
					default:
						wantField(t, body, "top_p", false, nil)
					}
					wantField(t, body, "reasoning_effort", reasoning != "", reasoning)
				})
			}
		}
	}
}

func TestGeminiBackend_SamplingFieldsExactlyWhenSet(t *testing.T) {
	f64 := func(v float64) *float64 { return &v }
	for _, temp := range []*float64{nil, f64(0), f64(0.7)} {
		for _, topP := range []*float64{nil, f64(0.9)} {
			t.Run(fmt.Sprintf("temp=%v/top_p=%v", deref(temp), deref(topP)), func(t *testing.T) {
				var body map[string]any
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_ = json.NewDecoder(r.Body).Decode(&body)
					_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`))
				}))
				defer srv.Close()
				b := &GeminiBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
				if _, err := b.Run(context.Background(), RunRequest{Prompt: "hi", Registry: &ToolRegistry{Handlers: map[string]ToolHandler{}}, Temperature: temp, TopP: topP}); err != nil {
					t.Fatal(err)
				}
				gen, _ := body["generationConfig"].(map[string]any)
				if gen == nil {
					gen = map[string]any{}
				}
				greedy := temp != nil && *temp == 0
				wantField(t, gen, "temperature", temp != nil, deref(temp))
				switch {
				case topP != nil:
					wantField(t, gen, "topP", true, *topP)
				case greedy:
					wantField(t, gen, "topP", true, 1.0)
				default:
					wantField(t, gen, "topP", false, nil)
				}
				wantField(t, gen, "topK", greedy, 1.0)
			})
		}
	}
}

func TestSDKBackend_TopPSentWhenSet(t *testing.T) {
	for _, topP := range []*float64{nil, func() *float64 { v := 0.8; return &v }()} {
		var body map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(anthropicTextResponse("ok", "end_turn", 1, 1)))
		}))
		t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
		b := &SDKBackend{APIKey: "k"}
		_, err := b.Run(context.Background(), RunRequest{Prompt: "hi", Model: "sonnet", MaxTokens: 64, TopP: topP, Registry: &ToolRegistry{Handlers: map[string]ToolHandler{}}})
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		wantField(t, body, "top_p", topP != nil, deref(topP))
	}
}

// TestRun_ReasoningAndTemperatureReachTheBody: a node's reasoning and the
// run's temperature reach the endpoint, and a node without reasoning sends
// none.
func TestRun_ReasoningAndTemperatureReachTheBody(t *testing.T) {
	const yaml = `name: local
nodes:
  thinker:
    type: agent
    model: local-model
    reasoning: high
    prompt: "think"
  plain:
    type: agent
    model: local-model
    prompt: "plain"
`
	for _, temp := range []*float64{nil, func() *float64 { v := 0.4; return &v }()} {
		t.Run(fmt.Sprintf("temperature=%v", deref(temp)), func(t *testing.T) {
			f := &fakeOpenAI{models: []string{"local-model"}, reply: func(map[string]any) string { return chatReply("done", "stop", 1, 1) }}
			srv := httptest.NewServer(f)
			defer srv.Close()
			if _, _, _, err := localRun(t, srv, yaml, Config{Temperature: temp}); err != nil {
				t.Fatal(err)
			}
			wantReasoning := map[string]string{"think": "high", "plain": ""}
			if len(f.bodies) != len(wantReasoning) {
				t.Fatalf("%d calls, want %d", len(f.bodies), len(wantReasoning))
			}
			for _, body := range f.bodies {
				msgs, _ := body["messages"].([]any)
				last, _ := msgs[len(msgs)-1].(map[string]any)
				prompt, _ := last["content"].(string)
				r := wantReasoning[prompt]
				wantField(t, body, "reasoning_effort", r != "", r)
				wantField(t, body, "temperature", temp != nil, deref(temp))
			}
		})
	}
}

// TestDispatch_ReasoningOnOtherProvidersIsLogged: the field reaches the
// request, and a provider that cannot send it says so.
func TestDispatch_ReasoningOnOtherProvidersIsLogged(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			const node = "w1-reason"
			yaml := "name: t\nnodes:\n  " + node + ":\n    type: agent\n    model: m\n    reasoning: low\n    prompt: p\n"
			store := newTempStore(t)
			runID := seedAgentRun(t, store, node, yaml)
			defn, _ := workflow.LoadYAMLString(yaml)
			backend := &recordingBackend{}
			rc := buildAgentNodeRC(t, store, runID, defn, backend, Config{Provider: provider})
			var log bytes.Buffer
			rc.logf = makeLogf(&log)

			rc.executeAgentNode(workflow.DispatchNode{Node: node, Type: "agent", Model: "m", Reasoning: "low"})

			if backend.req.Reasoning != "low" {
				t.Errorf("request reasoning = %q, want low", backend.req.Reasoning)
			}
			logged := strings.Contains(log.String(), "reasoning low has no effect on provider "+provider)
			if logged != (provider != "openai") {
				t.Errorf("no-effect logged = %v, want %v:\n%s", logged, provider != "openai", log.String())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 5. Fan concurrency from the environment; a failed item is not an answer.
// ---------------------------------------------------------------------------

func TestRunFanOut_ConcurrencyFromEnv(t *testing.T) {
	items := []string{"a", "b", "c", "d"}
	// HIVE_MAX_PARALLEL_NODES bounds a wave's nodes, not a fan's items.
	cases := []struct{ fan, nodes string }{
		{"1", ""},
		{"", "2"},
		{"3", "1"},
		{"", ""},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("fan=%q nodes=%q", c.fan, c.nodes), func(t *testing.T) {
			bound := 4
			if c.fan != "" {
				bound, _ = strconv.Atoi(c.fan)
			}
			t.Setenv("HIVE_MAX_PARALLEL_FAN", c.fan)
			t.Setenv("HIVE_MAX_PARALLEL_NODES", c.nodes)
			backend := newFanRecordingBackend(len(items), 150*time.Millisecond)
			rc := &runtimeContext{ctx: context.Background(), store: newParallelTestStore(t), backend: backend, logf: makeLogf(discardWriter{})}
			node := workflow.DispatchNode{Node: "fan", Type: "parallel_fan", ResolvedPrompt: "x {item}", FanItems: items, FanItemPlaceholder: "{item}"}
			if _, err := rc.runFanOut(context.Background(), node, backend, RunRequest{}); err != nil {
				t.Fatal(err)
			}
			want := min(bound, len(items))
			if peak := backend.peak.Load(); peak != int64(want) {
				t.Errorf("peak in flight = %d, want %d", peak, want)
			}
		})
	}
}

func TestRunFanOut_ItemWithErrorIsNotAnAnswer(t *testing.T) {
	backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
		func(req RunRequest) (*RunResult, error) {
			if strings.HasSuffix(req.Prompt, "cut") {
				return &RunResult{FinalText: "half an answer", OutputTokens: 9}, fmt.Errorf("openai: reply cut off at the output cap")
			}
			return &RunResult{FinalText: "whole answer", OutputTokens: 4}, nil
		},
	}}
	t.Setenv("HIVE_MAX_PARALLEL_FAN", "1")
	rc := &runtimeContext{ctx: context.Background(), store: newParallelTestStore(t), backend: backend, logf: makeLogf(discardWriter{})}
	node := workflow.DispatchNode{Node: "fan", Type: "parallel_fan", ResolvedPrompt: "item {item}", FanItems: []string{"ok", "cut"}, FanItemPlaceholder: "{item}"}
	combined, err := rc.runFanOut(context.Background(), node, backend, RunRequest{})
	if err != nil {
		t.Fatalf("1 of 2 items answered, which meets half: %v", err)
	}
	if strings.Contains(combined.FinalText, "half an answer") || !strings.Contains(combined.FinalText, "whole answer") {
		t.Errorf("joined text = %q, want only the complete item's answer", combined.FinalText)
	}
	if combined.OutputTokens != 9+4 {
		t.Errorf("output tokens = %d, want both items' %d", combined.OutputTokens, 9+4)
	}
}

// ---------------------------------------------------------------------------
// 6. The attempt ledger.
// ---------------------------------------------------------------------------

func TestRun_RepairLedgerCreditsTheAcceptedModel(t *testing.T) {
	const yaml = `name: ledger
nodes:
  lens:
    type: agent
    model: model-a
    prompt: "answer"
    outputs: [ok]
    accept:
      - "outputs.ok == true"
    on_reject:
      model: model-b
      max_repair_iterations: 2
`
	answer := map[string]string{"model-a": `{"ok": false, "by": "a"}`, "model-b": `{"ok": true, "by": "b"}`}
	cases := []struct {
		name         string
		failFirstB   bool     // the first call to model-b errors
		wantTriggers []string // one per workflow_repairs row
	}{
		{"repair passes first time", false, []string{"accept"}},
		{"repair after a backend error", true, []string{"accept", "backend_error"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var bCalls atomic.Int64
			backend := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
				func(req RunRequest) (*RunResult, error) {
					if req.Model == "model-b" && bCalls.Add(1) == 1 && c.failFirstB {
						return nil, fmt.Errorf("connection reset")
					}
					return &RunResult{FinalText: answer[req.Model], InputTokens: 1, OutputTokens: 1}, nil
				},
			}}
			dir := t.TempDir()
			wf := filepath.Join(dir, "wf.yaml")
			if err := os.WriteFile(wf, []byte(yaml), 0o644); err != nil {
				t.Fatal(err)
			}
			store := newTempStore(t)
			cfg := Config{WorkflowYAML: wf, ProjectDir: dir, MaxIterations: 5, Backend: backend, Provider: "openai", Log: discardWriter{}}
			if _, err := Run(context.Background(), store, cfg); err != nil {
				t.Fatal(err)
			}
			rows, err := store.ReadDB.Query(`SELECT attempt, COALESCE(model,''), COALESCE(provider,''), COALESCE(trigger,''), accept_passed FROM workflow_repairs ORDER BY attempt`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			i := 0
			for rows.Next() {
				var attempt, passed int
				var model, provider, trigger string
				if err := rows.Scan(&attempt, &model, &provider, &trigger, &passed); err != nil {
					t.Fatal(err)
				}
				last := i == len(c.wantTriggers)-1
				if model != "model-b" || provider != providerLabel(cfg) || trigger != c.wantTriggers[i] || (passed == 1) != last {
					t.Errorf("row %d = %s/%s/%s passed=%d; want model-b/%s/%s passed=%v",
						attempt, model, provider, trigger, passed, providerLabel(cfg), c.wantTriggers[i], last)
				}
				i++
			}
			if i != len(c.wantTriggers) {
				t.Fatalf("%d repair rows, want %d", i, len(c.wantTriggers))
			}
			row := readLocalNodeRow(t, store, "lens")
			if row.status != "completed" || row.model != "model-b" || row.rationale != answer["model-b"] {
				t.Errorf("node = %s model %q rationale %q; want completed, model-b and model-b's text", row.status, row.model, row.rationale)
			}
		})
	}
}

func deref(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// wantField checks that key is present in m exactly when present is true,
// and then that it holds want (JSON numbers decode as float64).
func wantField(t *testing.T, m map[string]any, key string, present bool, want any) {
	t.Helper()
	got, ok := m[key]
	if ok != present {
		t.Errorf("%s present = %v (value %v), want present = %v", key, ok, got, present)
		return
	}
	if present && fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("%s = %v, want %v", key, got, want)
	}
}
