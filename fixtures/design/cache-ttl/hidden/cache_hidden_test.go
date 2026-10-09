package cache

import (
	"fmt"
	"sync"
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

func newClockCache(ttl time.Duration) (*Cache, *clock) {
	ck := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	c := NewTTL(ttl)
	c.Now = ck.now
	return c, ck
}

func TestHiddenExpiresAtTTL(t *testing.T) {
	c, ck := newClockCache(10 * time.Second)
	c.Put("a", 1)
	ck.advance(9 * time.Second)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatalf("Get before ttl = %v, %v; want 1, true", v, ok)
	}
	ck.advance(time.Second) // exactly ttl: expired
	if _, ok := c.Get("a"); ok {
		t.Error("Get at ttl should miss")
	}
	if c.Len() != 0 {
		t.Errorf("Len = %d after expiry, want 0", c.Len())
	}
}

func TestHiddenPutRestartsTTL(t *testing.T) {
	c, ck := newClockCache(10 * time.Second)
	c.Put("a", 1)
	ck.advance(8 * time.Second)
	c.Put("a", 2)
	ck.advance(8 * time.Second) // 16 s after the first put, 8 after the second
	if v, ok := c.Get("a"); !ok || v != 2 {
		t.Errorf("Get after a refresh = %v, %v; want 2, true", v, ok)
	}
}

func TestHiddenLenCountsOnlyLive(t *testing.T) {
	c, ck := newClockCache(10 * time.Second)
	c.Put("a", 1)
	ck.advance(5 * time.Second)
	c.Put("b", 2)
	ck.advance(5 * time.Second) // a expired, b alive
	if c.Len() != 1 {
		t.Errorf("Len = %d, want 1", c.Len())
	}
}

func TestHiddenPurge(t *testing.T) {
	c, ck := newClockCache(10 * time.Second)
	for i := 0; i < 5; i++ {
		c.Put(fmt.Sprint(i), i)
	}
	ck.advance(5 * time.Second)
	for i := 5; i < 8; i++ {
		c.Put(fmt.Sprint(i), i)
	}
	ck.advance(5 * time.Second)
	if n := c.Purge(); n != 5 {
		t.Errorf("Purge removed %d, want 5", n)
	}
	if n := c.Purge(); n != 0 {
		t.Errorf("second Purge removed %d, want 0", n)
	}
	if c.Len() != 3 {
		t.Errorf("Len = %d after purge, want 3", c.Len())
	}
}

func TestHiddenNewNeverExpires(t *testing.T) {
	c := New()
	ck := &clock{t: time.Unix(0, 0)}
	c.Now = ck.now
	c.Put("a", 1)
	ck.advance(1000 * time.Hour)
	if _, ok := c.Get("a"); !ok {
		t.Error("New() entry expired")
	}
	if n := c.Purge(); n != 0 {
		t.Errorf("Purge on New() removed %d", n)
	}
	if c.Len() != 1 {
		t.Errorf("Len = %d, want 1", c.Len())
	}
}

func TestHiddenConcurrentUse(t *testing.T) {
	c, ck := newClockCache(time.Second)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				k := fmt.Sprint(i % 40)
				c.Put(k, g)
				c.Get(k)
				if i%50 == 0 {
					ck.advance(300 * time.Millisecond)
					c.Purge()
				}
				c.Len()
			}
		}(g)
	}
	wg.Wait()
	if n := c.Len(); n > 40 {
		t.Errorf("Len = %d after concurrent puts of 40 keys", n)
	}
}
