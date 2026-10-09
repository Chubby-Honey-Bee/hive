package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSDKBackend_RunsAtSonnetCap sends a turn at max_tokens 65536, the models
// config's sonnet cap. anthropic-sdk-go refuses a non-streaming request whose
// max_tokens implies more than ten minutes (anything above 21,333) before it
// leaves the process, so this only reaches the stub when the turn is streamed.
func TestSDKBackend_RunsAtSonnetCap(t *testing.T) {
	const maxTokens = 65536
	var sent struct {
		Model     string `json:"model"`
		MaxTokens int64  `json:"max_tokens"`
		Stream    bool   `json:"stream"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&sent)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(anthropicTextResponse("streamed", "end_turn", 7, 3)))
	}))
	defer srv.Close()
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)

	b := &SDKBackend{APIKey: "test-key"}
	res, err := b.Run(context.Background(), RunRequest{
		Prompt:    "hi",
		Model:     "sonnet",
		MaxTokens: maxTokens,
		Registry:  &ToolRegistry{Handlers: map[string]ToolHandler{}},
	})
	if err != nil {
		t.Fatalf("Run at max_tokens %d: %v", maxTokens, err)
	}
	if sent.MaxTokens != maxTokens {
		t.Errorf("max_tokens sent = %d, want %d", sent.MaxTokens, maxTokens)
	}
	if !sent.Stream {
		t.Error("request did not set stream: true")
	}
	if want := resolveAliasForPricing("sonnet"); sent.Model != want {
		t.Errorf("model sent = %q, want %q", sent.Model, want)
	}
	if res.FinalText != "streamed" || res.InputTokens != 7 || res.OutputTokens != 3 || res.StopReason != "end_turn" {
		t.Errorf("result = %+v, want text streamed, tokens 7/3, end_turn", res)
	}
}

// TestClaudeRunner_StreamCutBeforeStop: a stream that ends without
// message_stop is a failed turn, not a short answer.
func TestClaudeRunner_StreamCutBeforeStop(t *testing.T) {
	full := anthropicTextResponse("partial", "end_turn", 1, 1)
	cut := full[:strings.Index(full, "event: message_stop")]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(cut))
	}))
	defer srv.Close()

	cr := newTestClaudeRunner(t, srv, &ToolRegistry{Handlers: map[string]ToolHandler{}})
	if _, err := cr.Run(context.Background(), "hi"); err == nil || !strings.Contains(err.Error(), "message_stop") {
		t.Fatalf("err = %v, want a stream-ended-before-message_stop error", err)
	}
}

// TestClaudeRunner_LongStreamOutlivesTTL: PerCallTTL bounds the wait for each
// stream event, not the turn. The stub spaces its events so the turn streams
// for twice the TTL while every gap stays under it.
func TestClaudeRunner_LongStreamOutlivesTTL(t *testing.T) {
	const ttl = 300 * time.Millisecond
	const text, in, out = "long", 5, 9
	events := strings.SplitAfter(anthropicTextResponse(text, "end_turn", in, out), "\n\n")
	events = events[:len(events)-1] // SplitAfter leaves an empty tail
	gap := 2 * ttl / time.Duration(len(events))
	if gap >= ttl {
		t.Fatalf("precondition: gap %s must be under the TTL %s", gap, ttl)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range events {
			select {
			case <-time.After(gap):
			case <-r.Context().Done():
				return
			}
			_, _ = w.Write([]byte(ev))
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()

	cr := newTestClaudeRunner(t, srv, &ToolRegistry{Handlers: map[string]ToolHandler{}})
	cr.PerCallTTL = ttl
	res, err := cr.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("a %s stream with %s gaps failed under a %s TTL: %v", time.Duration(len(events))*gap, gap, ttl, err)
	}
	if res.FinalText != text || res.InputTokens != in || res.OutputTokens != out {
		t.Errorf("result = %+v, want text %q, tokens %d/%d", res, text, in, out)
	}
}

// TestClaudeRunner_StalledStreamFails: a stream that goes quiet for longer
// than PerCallTTL fails the turn and says why.
func TestClaudeRunner_StalledStreamFails(t *testing.T) {
	body := anthropicTextResponse("stalled", "end_turn", 1, 1)
	first := body[:strings.Index(body, "\n\n")+2]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(first))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	cr := newTestClaudeRunner(t, srv, &ToolRegistry{Handlers: map[string]ToolHandler{}})
	cr.PerCallTTL = 100 * time.Millisecond
	if _, err := cr.Run(context.Background(), "hi"); err == nil || !strings.Contains(err.Error(), "no stream event for "+cr.PerCallTTL.String()) {
		t.Fatalf("err = %v, want a no-stream-event error naming the TTL", err)
	}
}

// TestResolveModelAlias_PassesFullIdsThrough: a name that is no alias reaches
// the SDK exactly as given, including gateway and Bedrock-style ids whose
// colon the models config would read as a provider prefix.
func TestResolveModelAlias_PassesFullIdsThrough(t *testing.T) {
	for _, id := range []string{
		"anthropic.claude-opus-4-1-20250805-v1:0",
		"us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		"anthropic:claude-opus-4-8",
		"claude-sonnet-4-5-20250929",
		"My-Gateway/Claude-Sonnet",
	} {
		if got := string(ResolveModelAlias(id)); got != id {
			t.Errorf("ResolveModelAlias(%q) = %q, want it unchanged", id, got)
		}
		if got := resolveModelForProvider(BackendAnthropic, id); got != id {
			t.Errorf("anthropic dispatch resolves %q to %q, want it unchanged", id, got)
		}
	}
}

// TestResolveModelAlias_FollowsModelsConfig overrides the sonnet alias in a
// models.yaml and checks the SDK backend sends the model the pin and the cost
// meter name. The models config is cached per process, so the check runs in
// a child test process that loads the override.
func TestResolveModelAlias_FollowsModelsConfig(t *testing.T) {
	const override = "claude-sonnet-4-5"
	if os.Getenv("HIVE_ALIAS_CHILD") == "1" {
		if got := string(ResolveModelAlias("sonnet")); got != override {
			t.Fatalf("ResolveModelAlias(sonnet) = %q, want the models config's %q", got, override)
		}
		if got := resolveModelForProvider(BackendAnthropic, "sonnet"); got != resolveAliasForPricing("sonnet") {
			t.Fatalf("sent %q but pinned %q", got, resolveAliasForPricing("sonnet"))
		}
		return
	}
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte("aliases:\n  sonnet: "+override+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestResolveModelAlias_FollowsModelsConfig$", "-test.count=1")
	cmd.Env = append(os.Environ(), "HIVE_ALIAS_CHILD=1", "HIVE_MODELS_PATH="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
}
