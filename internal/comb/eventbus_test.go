package comb

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestEventBus_PublishAfterSubscribeIsDelivered(t *testing.T) {
	bus := NewEventBus()
	ch, cancel := bus.Subscribe(8)
	defer cancel()

	bus.Publish(context.Background(), Event{Kind: EventVantageWritten, VantageKey: "d1=0"})

	select {
	case e := <-ch:
		if e.Kind != EventVantageWritten {
			t.Fatalf("wrong event kind: %v", e.Kind)
		}
		if e.VantageKey != "d1=0" {
			t.Fatalf("wrong vantage: %v", e.VantageKey)
		}
		if e.At.IsZero() {
			t.Fatalf("At should auto-populate when Publish is called without one")
		}
	case <-time.After(time.Second):
		t.Fatalf("event not delivered within 1s")
	}
}

func TestEventBus_DropsWhenSubscriberFull(t *testing.T) {
	bus := NewEventBus()
	// Buffer of 1 — second publish must be dropped (no consumer reads).
	ch, cancel := bus.Subscribe(1)
	defer cancel()

	bus.Publish(context.Background(), Event{Kind: EventVantageWritten, VantageKey: "a"})
	bus.Publish(context.Background(), Event{Kind: EventVantageWritten, VantageKey: "b"})
	bus.Publish(context.Background(), Event{Kind: EventVantageWritten, VantageKey: "c"})

	// Publish never blocked (we got here), and the full buffer kept only the
	// first event: b and c were dropped for this subscriber.
	if got := (<-ch).VantageKey; got != "a" {
		t.Fatalf("buffered event = %q, want a", got)
	}
	select {
	case e := <-ch:
		t.Fatalf("a dropped event was delivered: %q", e.VantageKey)
	default:
	}
}

func TestEventBus_CancelledContextSkipsPublish(t *testing.T) {
	bus := NewEventBus()
	ch, cancel := bus.Subscribe(8)
	defer cancel()

	ctx, ctxCancel := context.WithCancel(context.Background())
	ctxCancel()
	bus.Publish(ctx, Event{Kind: EventVantageWritten, VantageKey: "a"})

	select {
	case <-ch:
		t.Fatalf("publish on cancelled ctx should be a no-op")
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

func TestEventBus_FanOutToMultipleSubscribers(t *testing.T) {
	bus := NewEventBus()
	const N = 5
	chans := make([]<-chan Event, N)
	cancels := make([]func(), N)
	for i := 0; i < N; i++ {
		chans[i], cancels[i] = bus.Subscribe(8)
		defer cancels[i]()
	}
	bus.Publish(context.Background(), Event{Kind: EventVantageWritten})

	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		ch := chans[i]
		go func() {
			defer wg.Done()
			select {
			case e := <-ch:
				if e.Kind != EventVantageWritten {
					t.Errorf("subscriber got wrong event: %v", e.Kind)
				}
			case <-time.After(time.Second):
				t.Errorf("subscriber timeout")
			}
		}()
	}
	wg.Wait()
}
