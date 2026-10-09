package ratelimit

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newLimiter(rate float64, burst int) (*Limiter, *clock) {
	ck := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	l := New(rate, burst)
	l.Now = ck.now
	return l, ck
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestHiddenStartsFullAndDrains(t *testing.T) {
	l, _ := newLimiter(1, 3)
	for i := 0; i < 3; i++ {
		if !l.Allow() {
			t.Fatalf("Allow %d denied with a full burst of 3", i+1)
		}
	}
	if l.Allow() {
		t.Error("fourth Allow granted with an empty bucket")
	}
	if got := l.Tokens(); !near(got, 0) {
		t.Errorf("Tokens = %v, want 0", got)
	}
}

func TestHiddenRefillsAtRateUpToBurst(t *testing.T) {
	l, ck := newLimiter(2, 4)
	for i := 0; i < 4; i++ {
		l.Allow()
	}
	ck.advance(500 * time.Millisecond)
	if got := l.Tokens(); !near(got, 1) {
		t.Errorf("Tokens after 0.5 s at rate 2 = %v, want 1", got)
	}
	if !l.Allow() || l.Allow() {
		t.Error("one token should be allowed, then none")
	}
	ck.advance(time.Hour)
	if got := l.Tokens(); !near(got, 4) {
		t.Errorf("Tokens after an hour = %v, want the burst 4", got)
	}
}

func TestHiddenZeroRateNeverRefills(t *testing.T) {
	l, ck := newLimiter(0, 2)
	l.Allow()
	l.Allow()
	ck.advance(time.Hour)
	if l.Allow() {
		t.Error("rate 0 refilled")
	}
	l2, ck2 := newLimiter(-5, 1)
	l2.Allow()
	ck2.advance(time.Hour)
	if got := l2.Tokens(); !near(got, 0) {
		t.Errorf("negative rate: Tokens = %v, want 0", got)
	}
}

func TestHiddenBurstFloor(t *testing.T) {
	l, _ := newLimiter(1, 0)
	if !l.Allow() {
		t.Error("burst 0 should be treated as 1 and allow once")
	}
	if l.Allow() {
		t.Error("burst floor is 1, not more")
	}
}

func TestHiddenAllowNTakesAllOrNothing(t *testing.T) {
	l, _ := newLimiter(1, 5)
	if l.AllowN(6) {
		t.Error("AllowN(6) granted on a burst of 5")
	}
	if got := l.Tokens(); !near(got, 5) {
		t.Errorf("a denied AllowN took tokens: %v", got)
	}
	if !l.AllowN(3) || !near(l.Tokens(), 2) {
		t.Errorf("AllowN(3) then Tokens = %v, want 2", l.Tokens())
	}
	if !l.AllowN(0) || !l.AllowN(-1) || !near(l.Tokens(), 2) {
		t.Errorf("AllowN of 0 or less should be free; Tokens = %v", l.Tokens())
	}
}

func TestHiddenConcurrentAllowHandsOutExactlyTheBurst(t *testing.T) {
	l, _ := newLimiter(0, 50)
	var granted int64
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if l.Allow() {
					atomic.AddInt64(&granted, 1)
				}
				l.Tokens()
			}
		}()
	}
	wg.Wait()
	if granted != 50 {
		t.Errorf("granted %d of 1600 concurrent Allows at burst 50, rate 0; want exactly 50", granted)
	}
}
