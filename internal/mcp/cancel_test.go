package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// notifications/cancelled cancels an in-flight request and suppresses its
// response, so a client that stopped waiting is not answered.
func TestInflightRegistry_CancelsAndSuppresses(t *testing.T) {
	r := newInflightRegistry()
	ctx, done := r.begin("7")

	if r.isCancelled("7") {
		t.Fatal("a fresh request must not be marked cancelled")
	}
	if !r.cancel("7") {
		t.Fatal("cancel should report the request was in flight")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("the request's context was not cancelled")
	}
	if !r.isCancelled("7") {
		t.Error("a cancelled request's response must be suppressed")
	}

	// An unknown id is benign — the request may have just finished.
	if r.cancel("nope") {
		t.Error("cancelling an unknown id should report not-in-flight")
	}
	done()
	if r.isCancelled("7") {
		t.Error("the marker should be cleared once the handler has unwound")
	}
}

// The response for a cancelled request is dropped rather than written.
func TestWrite_DropsCancelledResponse(t *testing.T) {
	var buf bytes.Buffer
	s := &mcpServer{out: json.NewEncoder(&buf), inflight: newInflightRegistry()}
	id := json.RawMessage(`4`)

	_, done := s.inflight.begin("4")
	s.writeResult(id, map[string]any{"ok": true})
	if !strings.Contains(buf.String(), `"ok"`) {
		t.Fatal("an uncancelled request's response must be written")
	}

	buf.Reset()
	s.inflight.cancel("4")
	s.writeResult(id, map[string]any{"late": true})
	if buf.Len() != 0 {
		t.Errorf("a cancelled request's response was written: %s", buf.String())
	}
	done()
}

// Requests are served concurrently now, so the encoder must be serialised —
// two interleaved writes would corrupt the stream. Run with -race.
func TestWrite_ConcurrentResponsesAreNotInterleaved(t *testing.T) {
	var buf bytes.Buffer
	s := &mcpServer{out: json.NewEncoder(&buf), inflight: newInflightRegistry()}
	var wg sync.WaitGroup
	const n = 64
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.writeResult(json.RawMessage(`"x"`), map[string]any{"i": i, "pad": strings.Repeat("y", 200)})
		}(i)
	}
	wg.Wait()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != n {
		t.Fatalf("got %d lines, want %d", len(lines), n)
	}
	for i, l := range lines {
		var v map[string]any
		if err := json.Unmarshal([]byte(l), &v); err != nil {
			t.Fatalf("line %d is not valid JSON (interleaved write): %v", i, err)
		}
	}
}
