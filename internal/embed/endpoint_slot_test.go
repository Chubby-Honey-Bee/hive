package embed

// Tests for embedding calls and the endpoint slot: each request waits for
// the slot chb's model calls to the same server wait for
// (internal/endpointslot, runner.md § Backends), so a local server that
// answers one request at a time gets one. Every server is an httptest fake.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/endpointslot"
)

// localPoolSize is BatchEmbed's worker pool, four requests at once
// (comb.md § Embeddings).
const localPoolSize = 4

// concurrencyServer answers Ollama's /api/embeddings and OpenAI's
// /embeddings, and records the most requests it held at once. The first
// requests wait until want of them are in flight, then a little longer, so
// a client that allows more than want sends them into that window.
type concurrencyServer struct {
	want    int64
	held    atomic.Int64
	peak    atomic.Int64
	total   atomic.Int64
	reached atomic.Bool
}

func (s *concurrencyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := s.held.Add(1)
	defer s.held.Add(-1)
	s.total.Add(1)
	for p := s.peak.Load(); n > p && !s.peak.CompareAndSwap(p, n); p = s.peak.Load() {
	}
	if !s.reached.Load() {
		// No tight budget: a correct client gets want requests here however
		// slow the machine; the cap only ends a wait for requests that a
		// client bounding them too tightly never sends.
		for deadline := time.Now().Add(10 * time.Second); s.held.Load() < s.want && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
		s.reached.Store(true)
	}
	var body struct {
		Prompt string   `json:"prompt"`
		Input  []string `json:"input"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if r.URL.Path == "/api/embeddings" {
		_ = json.NewEncoder(w).Encode(localResp{Embedding: []float32{float32(len(body.Prompt)), 1}})
		return
	}
	data := make([]map[string]any, len(body.Input))
	for i, in := range body.Input {
		data[i] = map[string]any{"index": i, "embedding": []float32{float32(len(in)), 1}}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// newSlotServer starts an httptest server for h on a port whose entry in the
// process-wide slot table holds the bound endpointslot.Limit gives it now.
// The table keeps the bound a port was first used with, so a port an earlier
// test used under another HIVE_MAX_PARALLEL_ENDPOINT is passed over. The
// servers passed over stay open until one fits, so none is handed out twice.
func newSlotServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	var passed []*httptest.Server
	defer func() {
		for _, s := range passed {
			s.Close()
		}
	}()
	for range 50 {
		srv := httptest.NewServer(h)
		if slotBound(srv.URL) == endpointslot.Limit(srv.URL) {
			return srv
		}
		passed = append(passed, srv)
	}
	t.Fatal("no port whose slot bound is the current one")
	return nil
}

// slotBound is the bound the slot table holds for endpoint's server, read
// without waiting: Acquire on an ended context returns it with or without a
// slot.
func slotBound(endpoint string) int {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	release, n, err := endpointslot.Acquire(ctx, endpoint)
	if err == nil {
		release()
	}
	return n
}

// TestLocalHTTPProvider_BatchEmbedHoldsTheEndpointSlot: a batch sends at most
// as many requests at once as the endpoint's slot allows, and as many as the
// pool allows when the slot allows more. httptest listens on 127.0.0.1, so
// with HIVE_MAX_PARALLEL_ENDPOINT unset the bound is one.
func TestLocalHTTPProvider_BatchEmbedHoldsTheEndpointSlot(t *testing.T) {
	for _, env := range []string{"", "2", "8"} {
		t.Run("HIVE_MAX_PARALLEL_ENDPOINT="+env, func(t *testing.T) {
			t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", env)
			s := &concurrencyServer{}
			srv := newSlotServer(t, s)
			defer srv.Close()
			want := int64(localPoolSize)
			if n := endpointslot.Limit(srv.URL); n > 0 && n < localPoolSize {
				want = int64(n)
			}
			s.want = want

			texts := make([]string, 12)
			for i := range texts {
				texts[i] = strings.Repeat("x", i+1)
			}
			p, err := NewLocalHTTPProvider(srv.URL, "m")
			if err != nil {
				t.Fatal(err)
			}
			got, err := p.BatchEmbed(context.Background(), texts)
			if err != nil {
				t.Fatal(err)
			}
			for i, v := range got {
				if len(v) == 0 || v[0] != float32(len(texts[i])) {
					t.Errorf("embedding %d = %v, want the one for %q", i, v, texts[i])
				}
			}
			if peak := s.peak.Load(); peak != want {
				t.Errorf("the server held %d requests at once, want %d", peak, want)
			}
			if total := s.total.Load(); total != int64(len(texts)) {
				t.Errorf("the server got %d requests, want %d", total, len(texts))
			}
		})
	}
}

// TestEmbedProviders_WaitForTheModelCallsSlot: while a model call holds the
// one slot of a local server, an embedding call to it waits, whichever name
// for this machine either uses, and is sent once the slot is free.
func TestEmbedProviders_WaitForTheModelCallsSlot(t *testing.T) {
	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "")
	t.Setenv("OPENAI_API_KEY", "k")
	for _, provider := range []string{"localhttp", "openai"} {
		t.Run(provider, func(t *testing.T) {
			s := &concurrencyServer{want: 1}
			srv := newSlotServer(t, s)
			defer srv.Close()
			// The model call goes to http://localhost:<port>/v1, as the
			// OpenAI-compatible backend's does; the embedding call to
			// http://127.0.0.1:<port>.
			port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
			var p Provider
			var err error
			if provider == "openai" {
				t.Setenv("OPENAI_BASE_URL", srv.URL+"/v1")
				p, err = NewOpenAIProvider("k", "m")
			} else {
				p, err = NewLocalHTTPProvider(srv.URL, "m")
			}
			if err != nil {
				t.Fatal(err)
			}
			release, n, err := endpointslot.Acquire(context.Background(), "http://localhost:"+port+"/v1")
			if err != nil || n != 1 {
				t.Fatalf("model call's slot: n=%d err=%v, want the one slot", n, err)
			}

			// A context that has ended: the call either waits for the slot
			// and gives up, or, holding none, fails at the server.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, waitErr := p.Embed(ctx, "while the model call runs")
			if !errors.Is(waitErr, context.Canceled) || !strings.Contains(waitErr.Error(), "no free slot") {
				t.Errorf("embedding while the slot is held: %v, want it to wait for the slot", waitErr)
			}
			if got := s.total.Load(); got != 0 {
				t.Errorf("the server got %d requests while the model call held its slot, want 0", got)
			}

			release()
			if _, err := p.Embed(context.Background(), "after"); err != nil {
				t.Fatalf("embedding once the slot is free: %v", err)
			}
			if got := s.total.Load(); got != 1 {
				t.Errorf("the server got %d requests, want 1", got)
			}
		})
	}
}
