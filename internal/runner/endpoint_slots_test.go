package runner

// Tests for the per-endpoint call bound: calls to one endpoint wait for a
// slot in chb, before the per-call deadline starts, so queue time behind
// chb's own calls does not spend a call's TTL. Every server is an httptest
// fake; no model is called.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEndpointLimit(t *testing.T) {
	cases := []struct {
		endpoint, env string
	}{
		{"http://localhost:11434/v1", ""},
		{"http://127.0.0.1:1234/v1", ""},
		{"http://[::1]:8080/v1", ""},
		{"http://0.0.0.0:11434/v1", ""},
		{"https://api.openai.com/v1", ""},
		{"http://192.168.1.20:11434/v1", ""},
		{"http://localhost:11434/v1", "3"},
		{"https://api.openai.com/v1", "2"},
		{"http://localhost:11434/v1", "0"},
		{"http://localhost:11434/v1", "500"},
		{"http://localhost:11434/v1", "many"},
	}
	for _, c := range cases {
		t.Run(c.endpoint+"/"+c.env, func(t *testing.T) {
			t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", c.env)
			// The rule in runner.md § Backends: the variable, clamped to
			// 1-64, when it parses; else 1 on this machine (the unspecified
			// address reaches it too), else no bound.
			host := strings.TrimPrefix(strings.TrimPrefix(c.endpoint, "http://"), "https://")
			local := strings.HasPrefix(host, "localhost:") || strings.HasPrefix(host, "127.") || strings.HasPrefix(host, "[::1]") || strings.HasPrefix(host, "0.0.0.0:")
			want := 0
			if local {
				want = 1
			}
			var n int
			if _, err := fmt.Sscanf(c.env, "%d", &n); err == nil && fmt.Sprint(n) == c.env {
				want = min(max(n, 1), 64)
			}
			if got := endpointLimit(c.endpoint); got != want {
				t.Errorf("endpointLimit(%q) with HIVE_MAX_PARALLEL_ENDPOINT=%q = %d, want %d", c.endpoint, c.env, got, want)
			}
		})
	}
}

// singleSlotServer answers one chat call at a time, each after service,
// like a local server with one slot. It records the most calls it held at
// once, counting the ones queued behind the slot.
type singleSlotServer struct {
	service time.Duration
	slot    sync.Mutex
	held    atomic.Int64
	peak    atomic.Int64
}

func (s *singleSlotServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := s.held.Add(1)
	defer s.held.Add(-1)
	for {
		p := s.peak.Load()
		if n <= p || s.peak.CompareAndSwap(p, n) {
			break
		}
	}
	s.slot.Lock()
	defer s.slot.Unlock()
	select {
	case <-time.After(s.service):
	case <-r.Context().Done():
		return
	}
	_, _ = w.Write([]byte(chatReply("ok", "stop", 1, 1)))
}

// TestEndpointSlots_QueueTimeIsNotTheCallsBound sends three calls at once
// to a single-slot server whose each answer takes service, under a TTL that
// covers one answer but not three. With one chb slot, every call's bound
// starts when it is sent, so every call completes. With three, the server
// queues them, and a call fails when its wait plus its answer passes the TTL.
func TestEndpointSlots_QueueTimeIsNotTheCallsBound(t *testing.T) {
	const calls = 3
	const service = 300 * time.Millisecond
	const ttl = 700 * time.Millisecond
	for _, slots := range []int{1, calls} {
		t.Run(fmt.Sprintf("slots=%d", slots), func(t *testing.T) {
			t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", fmt.Sprint(slots))
			srv := httptest.NewServer(&singleSlotServer{service: service})
			defer srv.Close()
			b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}

			errs := make([]error, calls)
			var wg sync.WaitGroup
			for i := range calls {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, errs[i] = b.Run(context.Background(), RunRequest{Prompt: "hi", TTL: ttl, Registry: &ToolRegistry{Handlers: map[string]ToolHandler{}}})
				}()
			}
			wg.Wait()

			// With one slot, chb sends a call only once the one before it
			// is answered, so no call waits at the server. With a slot per
			// call, all go at once, and the k-th the server answers (from
			// 0) waits k answers in its queue.
			wantFailed := 0
			for k := range calls {
				var waited time.Duration
				if slots == calls {
					waited = time.Duration(k) * service
				}
				if waited+service > ttl {
					wantFailed++
				}
			}
			failed := 0
			for _, err := range errs {
				if err != nil {
					if !strings.Contains(err.Error(), fmt.Sprintf("no reply within %s (the call's TTL)", ttl)) {
						t.Errorf("err = %v, want the TTL named", err)
					}
					failed++
				}
			}
			if failed != wantFailed {
				t.Errorf("%d of %d calls failed, want %d (errors: %v)", failed, calls, wantFailed, errs)
			}
		})
	}
}

// TestEndpointSlots_WaitEndsWithTheNodesBudget: a call that never gets a
// slot fails when its context ends, naming the bound it waited on.
func TestEndpointSlots_WaitEndsWithTheNodesBudget(t *testing.T) {
	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "1")
	endpoint := "http://127.0.0.1:1/slot-wait-test"
	release, err := acquireEndpointSlot(context.Background(), "openai", endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = acquireEndpointSlot(ctx, "openai", endpoint)
	if err == nil || !strings.Contains(err.Error(), "no free slot at "+endpoint+" (1 at a time, HIVE_MAX_PARALLEL_ENDPOINT)") {
		t.Fatalf("err = %v, want the slot wait named", err)
	}
}

// TestGeminiBackend_DeadlineIsTheTTL: the Gemini backend bounds each call as
// the OpenAI-compatible one does.
func TestGeminiBackend_DeadlineIsTheTTL(t *testing.T) {
	cases := []struct {
		delay, ttl, override time.Duration
	}{
		{delay: 50 * time.Millisecond, ttl: 3 * time.Second},
		{delay: 800 * time.Millisecond, ttl: 100 * time.Millisecond},
		{delay: 800 * time.Millisecond, ttl: 30 * time.Second, override: 100 * time.Millisecond},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("delay=%s ttl=%s override=%s", c.delay, c.ttl, c.override), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-time.After(c.delay):
				case <-r.Context().Done():
					return
				}
				_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`))
			}))
			defer srv.Close()
			b := &GeminiBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client(), Timeout: c.override}
			_, err := b.Run(context.Background(), RunRequest{Prompt: "hi", TTL: c.ttl, Registry: &ToolRegistry{Handlers: map[string]ToolHandler{}}})
			bound, source := c.ttl, "the call's TTL"
			if c.override > 0 {
				bound, source = c.override, "HIVE_HTTP_TIMEOUT"
			}
			if c.delay < bound {
				if err != nil {
					t.Fatalf("a reply after %s within a %s bound failed: %v", c.delay, bound, err)
				}
				return
			}
			want := fmt.Sprintf("gemini: no reply within %s (%s)", bound, source)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want it to contain %q", err, want)
			}
		})
	}
}

// TestResolveLLMBackend_LogsTheEndpointBound: the run log names the bound on
// a run default whose endpoint has one.
func TestResolveLLMBackend_LogsTheEndpointBound(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	for _, base := range []string{"http://localhost:11434/v1", "https://gateway.example.com/v1"} {
		t.Run(base, func(t *testing.T) {
			t.Setenv("OPENAI_BASE_URL", base)
			t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "")
			var log bytes.Buffer
			if _, err := resolveLLMBackend(Config{Provider: "openai"}, makeLogf(&log)); err != nil {
				t.Fatal(err)
			}
			n := endpointLimit(base)
			named := strings.Contains(log.String(), fmt.Sprintf("llm backend: openai (%s, at most %d call(s) in flight", base, n))
			if named != (n > 0) {
				t.Errorf("bound named = %v with limit %d:\n%s", named, n, log.String())
			}
		})
	}
}
