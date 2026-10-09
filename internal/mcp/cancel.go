package mcp

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Cancellation, per MCP's `notifications/cancelled`: the client may ask the
// server to stop work it already requested. Three things make that mean
// anything:
//
//  1. The notification is *readable* while the request runs. Requests run
//     on their own goroutine, so the stdio read loop reads the next line, a
//     cancellation for the request in flight included, while a handler
//     works, and a slow tool delays no other response, `ping` included.
//  2. The work observes the cancellation. Each request carries a context
//     that is cancelled on notice, and the handlers that shell out use it.
//  3. The response is suppressed. The spec is explicit: a server that
//     cancels "SHOULD NOT send a response" for that request. A late result is
//     dropped rather than written after the client stopped caring.
//
// Notifications are handled inline: they are cheap, and a cancellation must
// never queue behind the request it is trying to cancel.

// inflightRegistry tracks the requests currently being served.
type inflightRegistry struct {
	mu        sync.Mutex
	cancels   map[string]context.CancelFunc
	cancelled map[string]bool
}

func newInflightRegistry() *inflightRegistry {
	return &inflightRegistry{
		cancels:   map[string]context.CancelFunc{},
		cancelled: map[string]bool{},
	}
}

// begin registers a request and returns its context plus the cleanup to run
// when the handler returns.
func (r *inflightRegistry) begin(id string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.cancels[id] = cancel
	r.mu.Unlock()
	return ctx, func() {
		cancel()
		r.mu.Lock()
		delete(r.cancels, id)
		// The cancelled marker outlives the handler on purpose: the response
		// writer consults it as the handler unwinds. It is cleared here, after
		// that window, so ids are not retained for the life of the session.
		delete(r.cancelled, id)
		r.mu.Unlock()
	}
}

// cancel stops the request with this id and records that its response must be
// suppressed. Reports whether the id was actually in flight — an unknown id
// is not an error (the request may have just finished), but it is worth
// logging, because the spec says a cancellation "MUST only reference requests
// previously issued in the same direction".
func (r *inflightRegistry) cancel(id string) bool {
	r.mu.Lock()
	cancelFn, ok := r.cancels[id]
	if ok {
		r.cancelled[id] = true
	}
	r.mu.Unlock()
	if ok {
		cancelFn()
	}
	return ok
}

// isCancelled reports whether this request's response should be dropped.
func (r *inflightRegistry) isCancelled(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancelled[id]
}

// handleCancelled processes a `notifications/cancelled` notification.
func (s *mcpServer) handleCancelled(params json.RawMessage) {
	id, reason, ok := cancelledParams(params)
	if !ok {
		fmt.Fprintf(os.Stderr, "chb-mcp: notifications/cancelled without a requestId; ignoring\n")
		return
	}
	if s.inflight.cancel(id) {
		fmt.Fprintf(os.Stderr, "chb-mcp: cancelled request %s (%s)\n", id, reason)
		return
	}
	// Not in flight: already finished, or never issued. Both are benign —
	// the race is inherent to an asynchronous notification.
	fmt.Fprintf(os.Stderr, "chb-mcp: cancellation for request %s ignored (not in flight)\n", id)
}

// cancelledParams reads a cancellation's request id and reason, "no reason
// given" when it gives none, and reports whether it names a request.
func cancelledParams(params json.RawMessage) (id, reason string, ok bool) {
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
		Reason    string          `json:"reason"`
	}
	if err := json.Unmarshal(params, &p); err != nil || len(p.RequestID) == 0 {
		return "", "", false
	}
	return string(p.RequestID), cmp.Or(p.Reason, "no reason given"), true
}
