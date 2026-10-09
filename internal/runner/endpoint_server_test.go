package runner

// Tests for what a run asks of, and how it bounds, the server behind an
// endpoint: the thinking capability is asked per run, the constraint probe's
// cap follows the level it sends, and endpoints that reach one server share
// its slots. Every server is an httptest fake; no model is called.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// Each run asks the endpoint afresh and logs its own note, so a long-lived
// process (chb-mcp, chb replicate) sees a model re-pulled between runs.
func TestRun_ThinkingCapabilityIsAskedPerRun(t *testing.T) {
	const model = "ministral-3:8b"
	const yaml = `name: per-run
nodes:
  lens:
    type: agent
    model: ` + model + `
    reasoning: low
    tools: []
    prompt: "lens: answer"
    outputs: [answer]
`
	f := &fakeOpenAI{
		models: []string{model},
		caps:   map[string][]string{model: {"completion", "tools"}},
		reply:  func(map[string]any) string { return chatReply(`{"answer":"ok"}`, "stop", 1, 1) },
	}
	srv := httptest.NewServer(f)
	defer srv.Close()
	note := fmt.Sprintf("reports no thinking capability for %q", model)
	const runs = 2
	for run := 1; run <= runs; run++ {
		_, _, log, err := localRun(t, srv, yaml, Config{})
		if err != nil {
			t.Fatalf("run %d: %v\n%s", run, err, log)
		}
		if n := f.shows.Load(); n != int64(run) {
			t.Errorf("after run %d, /api/show was asked %d times, want %d (once per run)", run, n, run)
		}
		if n := strings.Count(log, note); n != 1 {
			t.Errorf("run %d's log says %s cannot think %d times, want once:\n%s", run, model, n, log)
		}
	}
	for i, body := range f.bodies {
		if r, has := body["reasoning_effort"]; has {
			t.Errorf("call %d carries reasoning_effort %v to a model that cannot think", i+1, r)
		}
	}
}

// The probe sends a node's level as its calls do, and caps its reply at 64
// tokens when no thinking comes first (none is sent, or the server reports
// the model cannot think), else at 1024. Each model stands for one level, on
// a server that reports thinking, reports none, or does not answer
// /api/show.
func TestProbeConstraints_CapFollowsTheLevelSent(t *testing.T) {
	levels := map[string]string{"m-unset": "", "m-none": "none", "m-low": "low"}
	servers := []struct {
		name string
		caps []string // nil: /api/show answers 404
	}{
		{"thinking", []string{"completion", "thinking"}},
		{"no thinking", []string{"completion"}},
		{"not reported", nil},
	}
	var yaml strings.Builder
	yaml.WriteString("name: caps\nnodes:\n")
	for model, level := range levels {
		fmt.Fprintf(&yaml, "  %s:\n    type: agent\n    model: %s\n    prompt: p\n    output_schema: {type: object}\n", strings.TrimPrefix(model, "m-"), model)
		if level != "" {
			fmt.Fprintf(&yaml, "    reasoning: %s\n", level)
		}
	}
	defn, err := workflow.LoadYAMLString(yaml.String())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range servers {
		t.Run(s.name, func(t *testing.T) {
			var mu sync.Mutex
			bodies := map[string]map[string]any{}
			f := &fakeOpenAI{reply: func(body map[string]any) string {
				mu.Lock()
				bodies[body["model"].(string)] = body
				mu.Unlock()
				return probeEnforced
			}}
			if s.caps != nil {
				f.caps = map[string][]string{}
				for model := range levels {
					f.caps[model] = s.caps
				}
			}
			srv := httptest.NewServer(f)
			defer srv.Close()
			t.Setenv("OPENAI_BASE_URL", srv.URL+"/v1")
			t.Setenv("OPENAI_API_KEY", "k")
			t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
			ProbeConstraints(context.Background(), Config{Provider: "openai"}, defn, nil)

			cannot := s.caps != nil && !slices.Contains(s.caps, "thinking")
			for model, level := range levels {
				body := bodies[model]
				if body == nil {
					t.Errorf("no probe of %s", model)
					continue
				}
				want := level
				if cannot {
					want = ""
				}
				if got, _ := body["reasoning_effort"].(string); got != want {
					t.Errorf("probe of %s (reasoning %q) sent reasoning_effort %q, want %q", model, level, got, want)
				}
				wantCap := 1024.0
				if want == "none" || level != "" && cannot {
					wantCap = 64
				}
				if got, _ := body["max_tokens"].(float64); got != wantCap {
					t.Errorf("probe of %s (reasoning %q) capped at %v, want %v", model, level, got, wantCap)
				}
			}
		})
	}
}

// An OpenAI-compatible call to localhost and an Anthropic one to 127.0.0.1
// reach one local server, as Ollama serves both APIs, so they share its one
// default slot.
func TestEndpointSlots_OneServerAcrossBackends(t *testing.T) {
	var held inFlight
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer held.enter()()
		time.Sleep(20 * time.Millisecond)
		if strings.HasSuffix(r.URL.Path, "/messages") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(anthropicTextResponse("ok", "end_turn", 1, 1)))
			return
		}
		_, _ = w.Write([]byte(chatReply("ok", "stop", 1, 1)))
	}))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	openAIBase, anthropicBase := "http://localhost:"+port+"/v1", "http://127.0.0.1:"+port
	t.Setenv("ANTHROPIC_BASE_URL", anthropicBase)
	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "")
	want := endpointLimit(openAIBase)
	if want != 1 || endpointLimit(anthropicBase) != 1 {
		t.Fatalf("the default bounds are %d and %d; the test needs loopback endpoints", want, endpointLimit(anthropicBase))
	}
	openAI := &OpenAIBackend{APIKey: "k", BaseURL: openAIBase, Client: &http.Client{}}
	const each = 2
	errs := make([]error, 2*each)
	var wg sync.WaitGroup
	for i := range 2 * each {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := RunRequest{Prompt: "hi", Model: "sonnet", MaxTokens: 64, Registry: &ToolRegistry{Handlers: map[string]ToolHandler{}}}
			if i%2 == 0 {
				_, errs[i] = openAI.Run(context.Background(), req)
			} else {
				_, errs[i] = (&SDKBackend{APIKey: "k"}).Run(context.Background(), req)
			}
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

// The llm backend: line names the bound on the Anthropic SDK's endpoint as
// it does on the others', since that backend waits for the same slots.
func TestResolveLLMBackend_NamesTheAnthropicBound(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "k")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	for _, base := range []string{"http://localhost:4000", "https://gateway.example.com"} {
		t.Run(base, func(t *testing.T) {
			t.Setenv("ANTHROPIC_BASE_URL", base)
			t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "")
			var log bytes.Buffer
			if _, err := resolveLLMBackend(Config{Provider: "anthropic"}, makeLogf(&log)); err != nil {
				t.Fatal(err)
			}
			n := endpointLimit(base)
			named := strings.Contains(log.String(), fmt.Sprintf("llm backend: anthropic (%s, at most %d call(s) in flight", base, n))
			if named != (n > 0) {
				t.Errorf("bound named = %v with limit %d:\n%s", named, n, log.String())
			}
		})
	}
}
