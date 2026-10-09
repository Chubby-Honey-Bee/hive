package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/embed"
	"github.com/Chubby-Honey-Bee/hive/internal/endpointslot"
)

// newOneSlotServer starts an httptest server for h on a port whose entry in
// the process-wide slot table holds one slot, as the loopback default gives.
// The table keeps the bound a port was first used with, so a port an earlier
// test used under another HIVE_MAX_PARALLEL_ENDPOINT is passed over. The
// servers passed over stay open until one fits, so none is handed out twice.
func newOneSlotServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	if n := endpointLimit("http://127.0.0.1:1"); n != 1 {
		t.Fatalf("precondition: the loopback bound is %d; the test needs one slot", n)
	}
	var passed []*httptest.Server
	defer func() {
		for _, s := range passed {
			s.Close()
		}
	}()
	for range 50 {
		srv := httptest.NewServer(h)
		// Acquire on an ended context returns the table's bound without
		// waiting, with or without a slot.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		release, n, err := endpointslot.Acquire(ctx, srv.URL)
		if err == nil {
			release()
		}
		if n == 1 {
			return srv
		}
		passed = append(passed, srv)
	}
	t.Fatal("no port whose slot table entry holds one slot")
	return nil
}

// TestEndpointSlots_EmbeddingsWaitForTheBackendsSlot: the backends' endpoint
// slot and the embedding providers' are one: while a model call to a local
// server holds its one slot, an embedding call to that server is not sent.
func TestEndpointSlots_EmbeddingsWaitForTheBackendsSlot(t *testing.T) {
	t.Setenv("HIVE_MAX_PARALLEL_ENDPOINT", "")
	var got atomic.Int64
	srv := newOneSlotServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{1, 2}})
	}))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	release, err := acquireEndpointSlot(context.Background(), "openai", "http://localhost:"+port+"/v1")
	if err != nil {
		t.Fatal(err)
	}
	p, err := embed.NewLocalHTTPProvider(srv.URL, "m")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Embed(ctx, "while the model call runs"); !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "no free slot") {
		t.Errorf("embedding while the model call holds the slot: %v, want it to wait for the slot", err)
	}
	if n := got.Load(); n != 0 {
		t.Errorf("the server got %d embedding requests while the slot was held, want 0", n)
	}
	release()
	if _, err := p.Embed(context.Background(), "after"); err != nil {
		t.Fatalf("embedding once the slot is free: %v", err)
	}
	if n := got.Load(); n != 1 {
		t.Errorf("the server got %d embedding requests, want 1", n)
	}
}
