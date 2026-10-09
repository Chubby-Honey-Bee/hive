package runner

// Tests for replies that are not answers, and for the model listing, on the
// OpenAI-compatible backend: a tool loop that runs out of turns fails its
// node, a thinking model's reasoning goes back on tool-call turns, a reply
// that is only reasoning says so, a listing that is not a model list is an
// error, a base URL missing /v1 is refused, and the allowlist keeps the
// preflight from asking a refused provider. Every server is an httptest
// fake; no model is called.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// toolCallReply renders a one-choice chat completion that calls read_file,
// with reasoning when it is not "".
func toolCallReply(reasoning string, in, out int) string {
	msg := map[string]any{
		"content": "",
		"tool_calls": []any{map[string]any{
			"id": "call_1", "type": "function",
			"function": map[string]any{"name": "read_file", "arguments": `{"path":"."}`},
		}},
	}
	if reasoning != "" {
		msg["reasoning"] = reasoning
	}
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": msg}},
		"usage":   map[string]any{"prompt_tokens": in, "completion_tokens": out},
	})
	return string(b)
}

// TestRun_ToolLoopOutOfTurnsFailsNode: a model that answers every call with
// a tool call, the wrap-up call after the turn cap included, fails its node,
// naming max_turns and the wrap-up, with the tokens of every call charged.
func TestRun_ToolLoopOutOfTurnsFailsNode(t *testing.T) {
	const in, out = 3, 2
	const maxTurns = 30 // the backend's cap when the request names none
	f := &fakeOpenAI{models: []string{"local-model"}, reply: func(map[string]any) string { return toolCallReply("", in, out) }}
	srv := httptest.NewServer(f)
	defer srv.Close()
	_, store, _, err := localRun(t, srv, fmt.Sprintf(oneNodeYAML, "local-model"), Config{})
	row := readLocalNodeRow(t, store, "only")
	if err == nil || row.status != "failed" || !strings.Contains(row.errMsg, fmt.Sprintf("no final reply after %d turns", maxTurns)) || !strings.Contains(row.errMsg, "max_turns") || !strings.Contains(row.errMsg, "wrap-up call gave none") {
		t.Fatalf("run err = %v, node %s error %q; want failed naming max_turns and the wrap-up", err, row.status, row.errMsg)
	}
	if n := f.chats.Load(); n != maxTurns+1 {
		t.Errorf("%d calls, want %d: the turns and the wrap-up", n, maxTurns+1)
	}
	if row.in != (maxTurns+1)*in || row.out != (maxTurns+1)*out {
		t.Errorf("tokens = %d/%d, want %d/%d", row.in, row.out, (maxTurns+1)*in, (maxTurns+1)*out)
	}
}

// TestRun_ToolLoopOutOfTurnsWrapsUp: a model that calls a tool on every turn
// it is offered tools gets one wrap-up call after the turn cap, with no tools
// and a last message saying no tool call is left, and its reply there is the
// node's answer: the node completes, charged every call.
func TestRun_ToolLoopOutOfTurnsWrapsUp(t *testing.T) {
	const in, out = 3, 2
	const maxTurns = 30
	f := &fakeOpenAI{models: []string{"local-model"}, reply: func(body map[string]any) string {
		if body["tools"] != nil {
			return toolCallReply("", in, out)
		}
		return chatReply(`{"answer":"wrapped up"}`, "stop", in, out)
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	if _, store, _, err := localRun(t, srv, fmt.Sprintf(oneNodeYAML, "local-model"), Config{}); err != nil {
		t.Fatalf("run: %v (node %+v)", err, readLocalNodeRow(t, store, "only"))
	} else if row := readLocalNodeRow(t, store, "only"); row.status != "completed" || row.in != (maxTurns+1)*in || row.out != (maxTurns+1)*out {
		t.Errorf("node %s with tokens %d/%d, want completed with %d/%d", row.status, row.in, row.out, (maxTurns+1)*in, (maxTurns+1)*out)
	}
	if n := f.chats.Load(); n != maxTurns+1 {
		t.Errorf("%d calls, want %d: the turns and the wrap-up", n, maxTurns+1)
	}
	f.mu.Lock()
	last := f.bodies[len(f.bodies)-1]
	f.mu.Unlock()
	msgs, _ := last["messages"].([]any)
	lastMsg, _ := msgs[len(msgs)-1].(map[string]any)
	if last["tools"] != nil || lastMsg["role"] != "user" || !strings.Contains(fmt.Sprint(lastMsg["content"]), "no tool call is left") {
		t.Errorf("the wrap-up call sent tools %v and last message %v; want no tools and the wrap-up message", last["tools"], lastMsg)
	}
}

// TestOpenAIBackend_ReasoningGoesBackOnToolTurns: the reasoning a thinking
// model returns with a tool call is sent back on its assistant message, and
// an assistant message without reasoning carries none.
func TestOpenAIBackend_ReasoningGoesBackOnToolTurns(t *testing.T) {
	for _, reasoning := range []string{"", "I should read the directory first."} {
		t.Run(fmt.Sprintf("reasoning=%q", reasoning), func(t *testing.T) {
			f := &fakeOpenAI{}
			f.reply = func(map[string]any) string {
				if f.chats.Load() == 1 {
					return toolCallReply(reasoning, 1, 1)
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
			var assistant map[string]any
			msgs, _ := f.bodies[1]["messages"].([]any)
			for _, m := range msgs {
				if mm, _ := m.(map[string]any); mm["role"] == "assistant" {
					assistant = mm
				}
			}
			if assistant == nil {
				t.Fatal("second call carries no assistant message")
			}
			wantField(t, assistant, "reasoning", reasoning != "", reasoning)
		})
	}
}

// TestOpenAIBackend_ReasoningOnlyReplySaysSo: a reply whose content is empty
// while its reasoning is not fails naming the reasoning's size.
func TestOpenAIBackend_ReasoningOnlyReplySaysSo(t *testing.T) {
	const reasoning = "thinking that never closed"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": "", "reasoning": reasoning}}},
		})
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	_, err := b.Run(context.Background(), RunRequest{Prompt: "hi", Registry: &ToolRegistry{Handlers: map[string]ToolHandler{}}})
	want := fmt.Sprintf("the reply holds %d bytes of reasoning and no answer", len(reasoning))
	if err == nil || !strings.Contains(err.Error(), "empty reply") || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want an empty reply naming %q", err, want)
	}
}

// TestIncompleteReply_NeverARateLimit: a cut-off reply's error names its cap,
// and a cap holding 429 or 529 must not read as a rate limit, so the call is
// made once.
func TestIncompleteReply_NeverARateLimit(t *testing.T) {
	for _, maxTokens := range []int64{4290, 15290, 8192} {
		t.Run(fmt.Sprint(maxTokens), func(t *testing.T) {
			var calls atomic.Int64
			inner := &callableBackend{fns: []func(RunRequest) (*RunResult, error){
				func(req RunRequest) (*RunResult, error) {
					calls.Add(1)
					return &RunResult{FinalText: "half", OutputTokens: req.MaxTokens}, incompleteReply("openai", "length", "length", "half", req.MaxTokens)
				},
			}}
			b := NewRateLimitedBackend(inner, "openai")
			res, err := b.Run(context.Background(), RunRequest{MaxTokens: maxTokens})
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("max tokens %d", maxTokens)) {
				t.Fatalf("err = %v, want the cut-off naming its cap", err)
			}
			if isRateLimitError(err) {
				t.Errorf("a cut-off reply under cap %d reads as a rate limit", maxTokens)
			}
			if n := calls.Load(); n != 1 {
				t.Errorf("%d calls, want 1", n)
			}
			if res == nil || res.OutputTokens != maxTokens {
				t.Errorf("result = %+v, want its usage kept", res)
			}
		})
	}
}

// TestCLIBackend_CutOffNamesNoCapItWasNotSent: the Claude CLI is not sent
// chb's output cap, so its cut-off error does not name one.
func TestCLIBackend_CutOffNamesNoCapItWasNotSent(t *testing.T) {
	const maxTokens = 777
	script := filepath.Join(t.TempDir(), "claude")
	body := "#!/bin/sh\ncat > /dev/null\nprintf '%s' '{\"type\":\"result\",\"is_error\":false,\"num_turns\":1,\"result\":\"half\",\"stop_reason\":\"max_tokens\",\"usage\":{\"input_tokens\":1,\"output_tokens\":5}}'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	b := &CLIBackend{CLIPath: script, ScratchDir: t.TempDir()}
	_, err := b.Run(context.Background(), RunRequest{Prompt: "hi", MaxTokens: maxTokens})
	if err == nil || !strings.Contains(err.Error(), "stop reason max_tokens") {
		t.Fatalf("err = %v, want a max_tokens error", err)
	}
	if strings.Contains(err.Error(), fmt.Sprint(maxTokens)) || strings.Contains(err.Error(), "max tokens") {
		t.Errorf("err = %v names a cap the CLI was not sent", err)
	}
}

// TestListModels_BodyMustBeAModelList: a 2xx body that carries an error, or
// holds no data list, is an error naming what came back.
func TestListModels_BodyMustBeAModelList(t *testing.T) {
	cases := []struct {
		body    string
		wantErr string // "" for a list
		want    []string
	}{
		{`{"object":"list","data":[{"id":"a"},{"id":"b"}]}`, "", []string{"a", "b"}},
		{`{"object":"list","data":[]}`, "", []string{}},
		{`{"error":"Unexpected endpoint or method. (GET /models)"}`, "Unexpected endpoint or method", nil},
		{`{"error":{"message":"bad key"}}`, "bad key", nil},
		{`{"object":"list"}`, "no model list", nil},
	}
	for _, c := range cases {
		t.Run(c.body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(c.body)) }))
			defer srv.Close()
			b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
			got, err := b.ListModels(context.Background())
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want it to name %q", err, c.wantErr)
				}
				return
			}
			if err != nil || fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Fatalf("got %v, %v; want %v", got, err, c.want)
			}
		})
	}
}

// TestRun_ModelPreflightOnABaseURLWithoutV1: a base URL that lacks /v1, on a
// server that lists its models under /v1, is refused before any call and
// names the URL to use; a server that lists nowhere is left unchecked.
func TestRun_ModelPreflightOnABaseURLWithoutV1(t *testing.T) {
	for _, underV1 := range []bool{true, false} {
		t.Run(fmt.Sprintf("lists under /v1=%v", underV1), func(t *testing.T) {
			var chats atomic.Int64
			var base string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/v1/models" && underV1:
					_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"local-model"}]}`))
				case strings.HasSuffix(r.URL.Path, "/chat/completions"):
					chats.Add(1)
					_, _ = w.Write([]byte(chatReply(`{"answer":"ok"}`, "stop", 1, 1)))
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			base = srv.URL
			store := newTempStore(t)
			dir := t.TempDir()
			wf := filepath.Join(dir, "wf.yaml")
			if err := os.WriteFile(wf, []byte(fmt.Sprintf(oneNodeYAML, "local-model")), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("OPENAI_BASE_URL", base)
			t.Setenv("OPENAI_API_KEY", "local")
			t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
			t.Setenv("HIVE_DISABLE_RATE_LIMIT", "1")
			var log strings.Builder
			_, err := Run(context.Background(), store, Config{WorkflowYAML: wf, ProjectDir: dir, MaxIterations: 5, Provider: "openai", Log: &log})
			if underV1 {
				want := "set OPENAI_BASE_URL to " + base + "/v1"
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("err = %v, want it to say %q", err, want)
				}
				if n := chats.Load(); n != 0 {
					t.Errorf("%d chat calls, want none", n)
				}
				return
			}
			// Nothing lists models, so the run goes on unchecked; its call
			// reaches the server's /chat/completions.
			if !strings.Contains(log.String(), "does not list its models") || !strings.Contains(log.String(), "base URL ends in /v1") {
				t.Errorf("log does not report the unchecked listing with the /v1 note:\n%s", log.String())
			}
			if err != nil {
				t.Fatalf("run: %v", err)
			}
		})
	}
}

// TestRun_ModelPreflightAsksNoRefusedProvider: with the allowlist refusing
// the run default, the run is refused and the endpoint is never asked for
// its models.
func TestRun_ModelPreflightAsksNoRefusedProvider(t *testing.T) {
	f := &fakeOpenAI{models: []string{"local-model"}, reply: func(map[string]any) string { return chatReply("ok", "stop", 1, 1) }}
	srv := httptest.NewServer(f)
	defer srv.Close()
	dir := t.TempDir()
	wf := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(wf, []byte(fmt.Sprintf(oneNodeYAML, "local-model")), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_BASE_URL", srv.URL+"/v1")
	t.Setenv("OPENAI_API_KEY", "local")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "anthropic")
	_, err := Run(context.Background(), newTempStore(t), Config{WorkflowYAML: wf, ProjectDir: dir, MaxIterations: 5, Provider: "openai", Log: discardWriter{}})
	if err == nil || !strings.Contains(err.Error(), "HIVE_PROVIDER_ALLOWLIST") {
		t.Fatalf("err = %v, want the allowlist refusal", err)
	}
	if n := f.lists.Load(); n != 0 {
		t.Errorf("%d model listings sent to a provider the allowlist refuses, want none", n)
	}
	// chb preflight's check asks nothing of it either.
	checks, err := PreflightEndpointModels(context.Background(), Config{Provider: "openai"}, map[string]any{"nodes": map[string]any{"only": map[string]any{"type": "agent", "model": "local-model"}}})
	if err != nil || len(checks) != 0 || f.lists.Load() != 0 {
		t.Errorf("preflight = %+v, %v with %d listings; want nothing asked", checks, err, f.lists.Load())
	}
}

// TestRun_BaseURLCredentialsAreNotRecorded: a base URL carrying user:password
// is recorded, and logged, without them.
func TestRun_BaseURLCredentialsAreNotRecorded(t *testing.T) {
	f := &fakeOpenAI{models: []string{"local-model"}, reply: func(map[string]any) string { return chatReply(`{"answer":"ok"}`, "stop", 1, 1) }}
	srv := httptest.NewServer(f)
	defer srv.Close()
	const user, pass = "gateway-user", "s3cret-pass"
	withCreds := strings.Replace(srv.URL, "://", "://"+user+":"+pass+"@", 1) + "/v1"
	dir := t.TempDir()
	wf := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(wf, []byte(fmt.Sprintf(oneNodeYAML, "local-model")), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_BASE_URL", withCreds)
	t.Setenv("OPENAI_API_KEY", "local")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	t.Setenv("HIVE_DISABLE_RATE_LIMIT", "1")
	store := newTempStore(t)
	var log strings.Builder
	if _, err := Run(context.Background(), store, Config{WorkflowYAML: wf, ProjectDir: dir, MaxIterations: 5, Provider: "openai", Log: &log}); err != nil {
		t.Fatal(err)
	}
	row := readLocalNodeRow(t, store, "only")
	if want := srv.URL + "/v1"; row.baseURL != want {
		t.Errorf("base_url = %q, want %q", row.baseURL, want)
	}
	if strings.Contains(log.String(), pass) {
		t.Errorf("the run log holds the password:\n%s", log.String())
	}
}

// TestRun_ModelPreflightNamesReasoningNotSent: the model preflight lets every
// such run go ahead, and its log line names each node whose level will not
// be sent where that changes what the model does: a level other than none to
// a model /api/show lists without the thinking capability. A server with no
// /api/show is sent every level. The dispatch then sends the field exactly
// when the rule says to (reasoningToSend).
func TestRun_ModelPreflightNamesReasoningNotSent(t *testing.T) {
	const plain, thinker = "ministral-3:8b", "qwen3.5:4b"
	caps := map[string][]string{plain: {"completion", "tools"}, thinker: {"completion", "tools", "thinking"}}
	cases := []struct {
		name, model, reasoning, repairModel string
		noShow                              bool // the server has no /api/show
	}{
		{"low on a model without thinking", plain, "low", "", false},
		{"none on a model without thinking", plain, "none", "", false},
		{"high on a thinking model", thinker, "high", "", false},
		{"a repair on a model without thinking", thinker, "medium", plain, false},
		{"low on a server with no /api/show", plain, "low", "", true},
		{"none on a server with no /api/show", thinker, "none", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeOpenAI{models: []string{plain, thinker}, reply: func(map[string]any) string { return chatReply(`{"answer":"ok"}`, "stop", 1, 1) }}
			if !c.noShow {
				f.caps = caps
			}
			srv := httptest.NewServer(f)
			defer srv.Close()
			yaml := fmt.Sprintf("name: t\nnodes:\n  lens:\n    type: agent\n    model: %s\n    reasoning: %s\n    prompt: \"answer\"\n    outputs: [answer]\n", c.model, c.reasoning)
			if c.repairModel != "" {
				yaml += "    on_reject:\n      model: " + c.repairModel + "\n"
			}
			_, _, log, err := localRun(t, srv, yaml, Config{})
			if err != nil {
				t.Fatalf("run: %v\n%s", err, log)
			}

			// sent is the rule: nothing when the server says the model cannot
			// think; else the level, none included.
			sent := func(model string) bool {
				return c.noShow || slices.Contains(caps[model], "thinking")
			}
			named := func(model string) bool {
				return !sent(model) && c.reasoning != "none"
			}
			preflight := ""
			for _, l := range strings.Split(log, "\n") {
				if strings.Contains(l, "model preflight:") {
					preflight = l
				}
			}
			for _, n := range []struct{ model, node string }{{c.model, "lens"}, {c.repairModel, "lens on_reject"}} {
				if n.model == "" {
					continue
				}
				label := fmt.Sprintf("%q (node %s, reasoning %s)", n.model, n.node, c.reasoning)
				if got := strings.Contains(preflight, label); got != named(n.model) {
					t.Errorf("preflight names %s = %v, want %v: %q", label, got, named(n.model), preflight)
				}
			}
			if len(f.bodies) != 1 {
				t.Fatalf("%d chat calls, want 1", len(f.bodies))
			}
			wantField(t, f.bodies[0], "reasoning_effort", sent(c.model), c.reasoning)
		})
	}
}

// TestHTTPStatusErrorIs404 guards the 404 test the preflight relies on.
func TestHTTPStatusErrorIs404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(http.NotFound))
	defer srv.Close()
	b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	_, err := b.ListModels(context.Background())
	var st interface{ StatusCode() int }
	if !errors.As(err, &st) || st.StatusCode() != http.StatusNotFound {
		t.Fatalf("err = %v, want one carrying status 404", err)
	}
}
