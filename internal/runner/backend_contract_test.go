package runner

// Tests for the contract every backend honours (runner.md § Backends): a
// tool loop out of turns, the spend of a call that fails, a cancelled call,
// the bound on a tool result, and the provider an error names. Every model
// is an httptest fake or a shell script; no model is called.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
)

// TestSDKBackend_OutOfTurnsIsNotARateLimit: an Anthropic SDK call whose
// model calls a tool on every call, the wrap-up after its turn cap included,
// fails as the OpenAI-compatible and Gemini backends' calls do: an
// incomplete reply with stop reason max_turns and every call's usage. A cap
// of 429 does not make it a rate limit, so the rate-limit retry makes the
// call once.
func TestSDKBackend_OutOfTurnsIsNotARateLimit(t *testing.T) {
	fastBackoff(t)
	ResetRateLimitState()
	t.Setenv("HIVE_RPM_DEFAULT", "60000")
	const maxTurns = 429
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(anthropicToolUseResponse("tu_x", "echo_tool", `{}`, 1, 1)))
	}))
	defer srv.Close()
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	reg := &ToolRegistry{Handlers: map[string]ToolHandler{
		"echo_tool": func(context.Context, map[string]any) (string, error) { return "ok", nil },
	}}
	b := NewRateLimitedBackend(&SDKBackend{APIKey: "k"}, "anthropic")
	b.maxRetry = 1
	res, err := b.Run(context.Background(), RunRequest{Prompt: "loop", MaxTurns: maxTurns, Registry: reg})
	var incomplete *incompleteReplyError
	if !errors.As(err, &incomplete) || isRateLimitError(err) {
		t.Errorf("err = %v: incomplete reply %v, rate limit %v; want an incomplete reply, not a rate limit", err, errors.As(err, &incomplete), isRateLimitError(err))
	}
	if want := fmt.Sprintf("no final reply after %d turns", maxTurns); err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "stop reason max_turns") {
		t.Errorf("err = %v, want it to say %q and name stop reason max_turns", err, want)
	}
	if n := calls.Load(); n != maxTurns+1 {
		t.Errorf("%d calls, want %d, the turns and the wrap-up: the call was retried", n, maxTurns+1)
	}
	if res == nil || res.StopReason != "max_turns" {
		t.Fatalf("result = %+v, want stop reason max_turns", res)
	}
	if res.Turns != maxTurns+1 || res.InputTokens != maxTurns+1 || res.OutputTokens != maxTurns+1 {
		t.Errorf("turns/in/out = %d/%d/%d, want %d each", res.Turns, res.InputTokens, res.OutputTokens, maxTurns+1)
	}
}

// TestIsRateLimitError_ByStatusNeverByNumber: an error counts as a rate limit
// by the status it carries, 429, 503 or 529, or by a rate-limit word in its
// text, never by a number there. A Gemini CLI that reports an error as JSON
// on stderr is classed by the status its code gives.
func TestIsRateLimitError_ByStatusNeverByNumber(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{errors.New("max turns (429) reached without terminal stop"), false},
		{errors.New("openai POST: dial tcp 127.0.0.1:54291: connect: connection refused"), false},
		{errors.New("claude CLI reported is_error=true (error_max_turns): Reached maximum number of turns (529)"), false},
		{errors.New("HTTP 429 from server"), false},
		{&statusCodeErr{code: 529, msg: "wrapped"}, true},
		{&statusCodeErr{code: 429, msg: "wrapped"}, true},
		{&statusCodeErr{code: 500, msg: "429"}, false},
		{errors.New(`API Error: 529 {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`), true},
		{errors.New(`[API Error: Resource has been exhausted (e.g. check quota). (Status: RESOURCE_EXHAUSTED)]`), true},
	} {
		if got := isRateLimitError(c.err); got != c.want {
			t.Errorf("isRateLimitError(%v) = %v, want %v", c.err, got, c.want)
		}
	}
	for _, c := range []struct {
		name, stderr string
		want         bool
	}{
		{"an API error with status 429", `{"error":{"type":"ApiError","message":"quota exhausted","code":429}}`, true},
		{"an API error with status 503", `{"error":{"type":"ApiError","message":"try later","code":503}}`, true},
		{"an exit code, with 429 in the message", `{"error":{"type":"FatalTurnLimitedError","message":"Reached max session turns (429)","code":53}}`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := &GeminiCLIBackend{CLIPath: fakeGeminiScript(t, jsonReplyCLI("echo '"+c.stderr+"' >&2\nexit 1\n"))}
			_, err := b.Run(context.Background(), RunRequest{Prompt: "p"})
			if err == nil || isRateLimitError(err) != c.want {
				t.Errorf("err = %v reads as a rate limit: %v, want %v", err, isRateLimitError(err), c.want)
			}
		})
	}
}

// TestGeminiCLIBackend_FailedReplyIsCharged: a gemini CLI that prints a reply
// with token stats and exits non-zero fails the call, and the reply's
// tokens come back with the error to be charged, as a claude CLI reply's
// are. Through a run, the failed node's row carries them.
func TestGeminiCLIBackend_FailedReplyIsCharged(t *testing.T) {
	wantUsage, wantCalls, _, unreported := cliCall(t, docStats)
	if unreported {
		t.Fatal("precondition: the docs' stats leave the call's usage unreported")
	}
	var in, cached, out int64
	for _, u := range wantUsage {
		in, cached, out = in+u.InputTokens, cached+u.CachedInputTokens, out+u.OutputTokens
	}
	script := jsonReplyCLI(printJSON(t, map[string]any{"response": "half an answer", "stats": cliStats(docStats)}) +
		"echo 'failed after the reply' >&2\nexit 1\n")
	b := &GeminiCLIBackend{CLIPath: fakeGeminiScript(t, script)}
	res, err := b.Run(context.Background(), RunRequest{Prompt: "p"})
	if err == nil || !strings.Contains(err.Error(), "exit status 1") || !strings.Contains(err.Error(), "failed after the reply") {
		t.Fatalf("err = %v, want the exit status and the CLI's stderr", err)
	}
	if res == nil {
		t.Fatal("no result, so the tokens the reply reports are not charged")
	}
	if !slices.Equal(res.ByModel, wantUsage) || res.InputTokens != in || res.CachedInputTokens != cached || res.OutputTokens != out {
		t.Errorf("usage %+v, tokens in/cached/out %d/%d/%d; want %+v, %d/%d/%d", res.ByModel, res.InputTokens, res.CachedInputTokens, res.OutputTokens, wantUsage, in, cached, out)
	}
	if res.Turns != wantCalls || res.FinalText != "" {
		t.Errorf("calls %d, text %q; want %d calls and no answer", res.Turns, res.FinalText, wantCalls)
	}

	yaml := "name: cli\nnodes:\n  cli:\n    type: agent\n    model: gemini-2.5-flash\n    prompt: \"answer\"\n    outputs: [answer]\n"
	_, store, _, runErr := geminiCLIRun(t, script, yaml, Config{})
	if runErr == nil {
		t.Fatal("the run succeeded, want it failed on the node")
	}
	var status string
	var tin, tout, metered int64
	if err := store.ReadDB.QueryRow(`SELECT status, tokens_in, tokens_out, metered_calls FROM workflow_node_states WHERE node_name='cli'`).Scan(&status, &tin, &tout, &metered); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || tin != in || tout != out || metered != int64(wantCalls) {
		t.Errorf("node %s with tokens in/out %d/%d and %d metered calls; want failed with %d/%d and %d", status, tin, tout, metered, in, out, wantCalls)
	}
}

// TestBackends_CancelIsAContextError: a call cancelled while it is in flight
// fails with an error that wraps context.Canceled, on every backend: a
// gemini CLI call as a claude CLI call does, not with the signal its CLI
// was killed by, and an SDK backend's call through its transport.
func TestBackends_CancelIsAContextError(t *testing.T) {
	// startedCLI is a CLI body that marks it has started, then runs on, and
	// the wait for the mark.
	startedCLI := func(t *testing.T) (string, func() bool) {
		mark := filepath.Join(t.TempDir(), "started")
		return fmt.Sprintf("cat >/dev/null\ntouch '%s'\nsleep 30\n", mark), func() bool {
			for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
				if _, err := os.Stat(mark); err == nil {
					return true
				}
			}
			return false
		}
	}
	// blockingServer is a provider that holds every request until its
	// caller gives up, and the wait for the first request. The body is read
	// first: only then does the server watch for the caller's close.
	blockingServer := func(t *testing.T) (*httptest.Server, func() bool) {
		arrived := make(chan struct{}, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case arrived <- struct{}{}:
			default:
			}
			<-r.Context().Done()
		}))
		t.Cleanup(srv.Close)
		return srv, func() bool {
			select {
			case <-arrived:
				return true
			case <-time.After(10 * time.Second):
				return false
			}
		}
	}
	backends := map[string]func(t *testing.T) (LLMBackend, func() bool){
		"gemini CLI": func(t *testing.T) (LLMBackend, func() bool) {
			body, wait := startedCLI(t)
			return &GeminiCLIBackend{CLIPath: fakeGeminiScript(t, jsonReplyCLI(body))}, wait
		},
		"claude CLI": func(t *testing.T) (LLMBackend, func() bool) {
			body, wait := startedCLI(t)
			path := filepath.Join(t.TempDir(), "claude")
			if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
				t.Fatal(err)
			}
			return &CLIBackend{CLIPath: path, ScratchDir: t.TempDir()}, wait
		},
		"anthropic": func(t *testing.T) (LLMBackend, func() bool) {
			srv, wait := blockingServer(t)
			t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
			return &SDKBackend{APIKey: "k"}, wait
		},
		"gemini": func(t *testing.T) (LLMBackend, func() bool) {
			srv, wait := blockingServer(t)
			return &GeminiBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}, wait
		},
		"openai-compatible": func(t *testing.T) (LLMBackend, func() bool) {
			srv, wait := blockingServer(t)
			return &OpenAIBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}, wait
		},
	}
	for _, name := range []string{"gemini CLI", "claude CLI", "anthropic", "gemini", "openai-compatible"} {
		t.Run(name, func(t *testing.T) {
			b, inFlight := backends[name](t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cancelled := make(chan bool, 1)
			go func() {
				defer cancel()
				cancelled <- inFlight()
			}()
			t0 := time.Now()
			_, err := b.Run(ctx, RunRequest{Prompt: "p", Registry: &ToolRegistry{Handlers: map[string]ToolHandler{}}})
			if !<-cancelled {
				t.Fatal("precondition: the call never got under way, so it was not cancelled in flight")
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("err = %v, want one that wraps context.Canceled", err)
			}
			if took := time.Since(t0); took > 20*time.Second {
				t.Errorf("the call took %s, waiting out its provider", took)
			}
		})
	}
}

// capCut checks got, a tool result one backend sent, against full, the
// tool's whole result, under the cap on a result when no window is known:
// what was kept is a prefix of full, the whole is within the cap, one more
// byte would not be, and the note names the bytes kept, the total, the cap
// and the read_file offset that gets the rest.
func capCut(t *testing.T, got, full string) {
	t.Helper()
	kept, note := cutResult(t, got)
	if !strings.HasPrefix(full, kept) || len(kept) == len(full) {
		t.Fatalf("kept %d of %d bytes, which is not a prefix of the result cut short", len(kept), len(full))
	}
	if len(got) > toolResultCap {
		t.Errorf("the result is %d bytes, over the %d-byte cap", len(got), toolResultCap)
	}
	more := full[:len(kept)+1] + strings.Replace(note, fmt.Sprintf("kept %d of", len(kept)), fmt.Sprintf("kept %d of", len(kept)+1), 1)
	if len(more) <= toolResultCap {
		t.Errorf("kept %d bytes, and one more would still fit: %d bytes", len(kept), len(more))
	}
	want := fmt.Sprintf("\n[cut: kept %d of %d bytes; the rest is past the %d-byte cap on a tool result. For the rest, call read_file again with offset %d.]",
		len(kept), len(full), toolResultCap, 1+strings.Count(kept, "\n"))
	if note != want {
		t.Errorf("note %q, want %q", note, want)
	}
}

// TestToolResult_EachBackendHoldsItToTheCap: with no context window known,
// as the Anthropic SDK and Gemini backends never have one, a tool result
// over the cap is cut by each SDK backend to the same text, with the same
// note, and logged, while the audit row keeps the tool's whole output.
func TestToolResult_EachBackendHoldsItToTheCap(t *testing.T) {
	dir := t.TempDir()
	var sb strings.Builder
	for i := 1; i <= 2000; i++ {
		fmt.Fprintf(&sb, "line %d of the file, which says what it says and no more\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "long.txt"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	const args = `{"path":"long.txt"}`
	full, _, err := NewToolRegistry(dir, nil, NewFSRecorder()).Invoke(context.Background(), "read_file", map[string]any{"path": "long.txt"})
	if err != nil || len(full) <= toolResultCap {
		t.Fatalf("precondition: read_file returned %d bytes (%v), want more than the %d-byte cap", len(full), err, toolResultCap)
	}
	backends := map[string]func(t *testing.T, req RunRequest) (*RunResult, string){
		"openai-compatible": func(t *testing.T, req RunRequest) (*RunResult, string) {
			f, b := loopBackend(t, toolCall("read_file", args))
			res, err := b.Run(context.Background(), req)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			return res, toolMessages(f.sent()[1])[0]
		},
		"gemini": func(t *testing.T, req RunRequest) (*RunResult, string) {
			var sent []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				part := `{"functionCall":{"name":"read_file","args":` + args + `}}`
				if strings.Contains(string(body), "functionResponse") {
					sent, part = body, `{"text":"done"}`
				}
				_, _ = w.Write([]byte(`{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[` + part + `]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`))
			}))
			defer srv.Close()
			res, err := (&GeminiBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}).Run(context.Background(), req)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			var body struct {
				Contents []struct {
					Parts []struct {
						FunctionResponse *struct {
							Response struct {
								Output string `json:"output"`
							} `json:"response"`
						} `json:"functionResponse"`
					} `json:"parts"`
				} `json:"contents"`
			}
			if err := json.Unmarshal(sent, &body); err != nil {
				t.Fatalf("the second request: %v", err)
			}
			last := body.Contents[len(body.Contents)-1].Parts[0].FunctionResponse
			if last == nil {
				t.Fatal("the second request's last turn holds no function response")
			}
			return res, last.Response.Output
		},
		"anthropic": func(t *testing.T, req RunRequest) (*RunResult, string) {
			var sent []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				if strings.Contains(string(body), "tool_result") {
					sent = body
					_, _ = w.Write([]byte(anthropicTextResponse("done", "end_turn", 1, 1)))
					return
				}
				_, _ = w.Write([]byte(anthropicToolUseResponse("tu_1", "read_file", args, 1, 1)))
			}))
			defer srv.Close()
			t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
			res, err := (&SDKBackend{APIKey: "k"}).Run(context.Background(), req)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			var body struct {
				Messages []struct {
					Content []struct {
						Type    string `json:"type"`
						Content []struct {
							Text string `json:"text"`
						} `json:"content"`
					} `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(sent, &body); err != nil {
				t.Fatalf("the second request: %v", err)
			}
			last := body.Messages[len(body.Messages)-1].Content[0]
			if last.Type != "tool_result" || len(last.Content) != 1 {
				t.Fatalf("the second request's last block is %+v, want one tool_result with its text", last)
			}
			return res, last.Content[0].Text
		},
	}
	results := map[string]string{}
	for _, name := range []string{"openai-compatible", "gemini", "anthropic"} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var log []string
			req := RunRequest{Model: "m:8b", Prompt: "read it", MaxTokens: 1024, Registry: NewToolRegistry(dir, nil, NewFSRecorder()),
				Logf: func(format string, a ...any) { mu.Lock(); log = append(log, fmt.Sprintf(format, a...)); mu.Unlock() }}
			res, got := backends[name](t, req)
			capCut(t, got, full)
			results[name] = got
			kept, _ := cutResult(t, got)
			if want := fmt.Sprintf("tool read_file: result cut to %d of %d bytes, the %d-byte cap on a tool result", len(kept), len(full), toolResultCap); !slices.Equal(log, []string{want}) {
				t.Errorf("log %q, want one line %q", log, want)
			}
			if len(res.Invocations) != 1 || res.Invocations[0].Output != full {
				t.Errorf("%d invocations, want one recording the tool's whole %d bytes", len(res.Invocations), len(full))
			}
		})
	}
	if results["gemini"] != results["openai-compatible"] || results["anthropic"] != results["openai-compatible"] {
		t.Errorf("the backends sent different results: %d, %d and %d bytes", len(results["openai-compatible"]), len(results["gemini"]), len(results["anthropic"]))
	}
}

// TestOpenAICompatible_ErrorsNameTheProvider: an error of the
// OpenAI-compatible backend names the provider the run resolved. A local
// endpoint's cut-off reply names local, as do its empty reply, its tool
// loop out of turns, its empty choices, its HTTP error status and its
// transport error; none names openai. Provider openai's errors name it as
// the run does: copilot or azure-openai when the run names that alias.
func TestOpenAICompatible_ErrorsNameTheProvider(t *testing.T) {
	t.Setenv("HIVE_PROVIDER", "")
	reg := func() *ToolRegistry { return NewToolRegistry(t.TempDir(), nil, NewFSRecorder()) }
	for _, c := range []struct {
		name  string
		reply func(w http.ResponseWriter)
		want  string
	}{
		{"cut off", func(w http.ResponseWriter) { _, _ = w.Write([]byte(chatReply("half", "length", 1, 1))) }, "local: reply cut off at the output cap (stop reason length, max tokens 64)"},
		{"empty", func(w http.ResponseWriter) { _, _ = w.Write([]byte(chatReply(" ", "stop", 1, 1))) }, "local: empty reply with no tool call"},
		{"out of turns", func(w http.ResponseWriter) { _, _ = w.Write([]byte(toolCallReply("", 1, 1))) }, "local: no final reply after 2 turns"},
		{"empty choices", func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"choices":[],"usage":{}}`)) }, "local: empty choices array"},
		{"HTTP status", func(w http.ResponseWriter) { http.Error(w, "boom", http.StatusInternalServerError) }, "local HTTP 500: boom"},
		{"transport", nil, "local POST: "},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { c.reply(w) }))
			if c.reply == nil {
				srv.Close()
			} else {
				defer srv.Close()
			}
			t.Setenv("HIVE_LOCAL_BASE_URL", srv.URL+"/v1")
			b, err := NewLocalBackend(Config{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = b.Run(context.Background(), RunRequest{Prompt: "hi", MaxTokens: 64, MaxTurns: 2, Registry: reg()})
			if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "openai") {
				t.Errorf("err = %v, want it to name local, %q, and never openai", err, c.want)
			}
		})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(chatReply("half", "length", 1, 1)))
	}))
	defer srv.Close()
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	for _, c := range []struct {
		name, env, provider, want string
	}{
		{"--provider copilot", "", "copilot", "copilot"},
		{"HIVE_PROVIDER=Azure-OpenAI", "Azure-OpenAI", "", "azure-openai"},
		{"the key alone", "", "", "openai"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HIVE_PROVIDER", c.env)
			b, err := NewOpenAIBackend(Config{Provider: c.provider})
			if err != nil {
				t.Fatal(err)
			}
			_, err = b.Run(context.Background(), RunRequest{Prompt: "hi", MaxTokens: 64, Registry: reg()})
			if want := c.want + ": reply cut off at the output cap"; err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Errorf("err = %v, want it to start %q", err, want)
			}
		})
	}
}
