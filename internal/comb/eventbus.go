package comb

import (
	"context"
	"sync"
	"time"
)

// EventKind classifies an Comb event.
type EventKind string

const (
	// EventVantageWritten fires after every vantage upsert — forager
	// (BuildForagerVantage) and region (WriteRegionVantage) alike.
	// Subscriber: the quorum sensor.
	EventVantageWritten EventKind = "vantage_written"
	// There are no tick events. Two were declared, and nothing ever
	// published them.
)

// Event is one message on the Comb event bus.
type Event struct {
	Kind        EventKind `json:"kind"`
	VantageKey  string    `json:"vantage_key,omitempty"`
	VantageKind string    `json:"vantage_kind,omitempty"`
	// RunID names the workflow run that produced the event, or 0 when it
	// came from outside one. The bus is process-global, so a sensor reads it
	// to take only its own run's verdicts.
	RunID   int64          `json:"run_id,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`
	At      time.Time      `json:"at"`
}

// EventBus is a non-blocking, drop-on-full broadcaster. Subscribers
// receive a buffered channel; if the channel fills (slow consumer),
// publishes drop the event for that subscriber rather than blocking
// the publisher. This keeps swarm dispatch latency-bounded
// regardless of how many subscribers are attached.
//
// Concurrency model:
//   - subs map is guarded by mu (RWMutex — many publishers, infrequent
//     subscribe/unsubscribe).
//   - Per-subscriber channels are owned by the subscriber goroutine;
//     the bus only sends.
//   - Cancel hook returned by Subscribe drains and closes the channel
//     under the write lock so a publish in flight can't write to a
//     closed channel.
//
// Zero value is unusable — call NewEventBus.
type EventBus struct {
	mu   sync.RWMutex
	subs map[chan Event]struct{}
}

// NewEventBus returns a ready-to-use bus.
func NewEventBus() *EventBus {
	return &EventBus{subs: map[chan Event]struct{}{}}
}

// Subscribe returns a channel that receives every published event,
// plus a cancel function the caller MUST invoke when done (defer is
// the canonical pattern).
//
// `buf` is the per-subscriber buffer size. 0 picks a reasonable
// default (32). Larger buffers tolerate more publisher bursts before
// the bus starts dropping.
func (b *EventBus) Subscribe(buf int) (<-chan Event, func()) {
	if buf <= 0 {
		buf = 32
	}
	ch := make(chan Event, buf)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	cancel := func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
	return ch, cancel
}

// Publish broadcasts an event to every subscriber. Non-blocking —
// drops the event for any subscriber whose channel buffer is full.
// Returns immediately; callers don't need to backoff or check.
//
// ctx is observed for cancellation; if it's already cancelled the
// publish is skipped.
func (b *EventBus) Publish(ctx context.Context, e Event) {
	if cancelled(ctx) {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		offer(ch, e)
	}
}

// cancelled reports whether ctx is set and already done.
func cancelled(ctx context.Context) bool {
	return ctx != nil && ctx.Err() != nil
}

// offer sends e on ch unless its buffer is full. A slow subscriber misses
// the event, unlogged: a high-volume publisher must never block on one.
func offer(ch chan Event, e Event) {
	select {
	case ch <- e:
	default:
	}
}

// Default is the process-wide singleton bus. Stateless code paths
// (like CombRepo writes) publish here so subscribers don't need to
// thread the bus through every Store accessor.
//
// Tests that need isolation should construct their own EventBus and
// not touch Default.
var Default = NewEventBus()
