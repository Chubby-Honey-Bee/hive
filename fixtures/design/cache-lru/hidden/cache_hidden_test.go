package cache

import (
	"fmt"
	"sync"
	"testing"
)

func TestHiddenEvictsLeastRecentlyUsed(t *testing.T) {
	c := NewLRU(2)
	c.Put("a", 1)
	c.Put("b", 2)
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a missing before eviction")
	}
	c.Put("c", 3) // b is the least recently used
	if _, ok := c.Get("b"); ok {
		t.Error("b should have been evicted")
	}
	for _, k := range []string{"a", "c"} {
		if _, ok := c.Get(k); !ok {
			t.Errorf("%s should be present", k)
		}
	}
	if c.Len() != 2 {
		t.Errorf("Len = %d, want 2", c.Len())
	}
}

func TestHiddenUpdateIsAUseAndEvictsNothing(t *testing.T) {
	c := NewLRU(2)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("a", 10) // a becomes most recent; nothing evicted
	if c.Len() != 2 {
		t.Fatalf("Len = %d after an update, want 2", c.Len())
	}
	if v, _ := c.Get("a"); v != 10 {
		t.Errorf("a = %v, want 10", v)
	}
	c.Put("c", 3) // b is now the least recent
	if _, ok := c.Get("b"); ok {
		t.Error("b should have been evicted after a's update")
	}
}

func TestHiddenCapacityFloorAndBound(t *testing.T) {
	c := NewLRU(0)
	c.Put("a", 1)
	c.Put("b", 2)
	if c.Len() != 1 {
		t.Errorf("Len = %d with capacity 0, want 1 (the floor)", c.Len())
	}
	if _, ok := c.Get("b"); !ok {
		t.Error("the latest put should survive at capacity 1")
	}
	c = NewLRU(5)
	for i := 0; i < 100; i++ {
		c.Put(fmt.Sprint(i), i)
		if c.Len() > 5 {
			t.Fatalf("Len = %d exceeds the capacity 5", c.Len())
		}
	}
}

func TestHiddenNewStaysUnbounded(t *testing.T) {
	c := New()
	for i := 0; i < 100; i++ {
		c.Put(fmt.Sprint(i), i)
	}
	if c.Len() != 100 {
		t.Errorf("New() Len = %d, want 100", c.Len())
	}
	if v, ok := c.Get("0"); !ok || v != 0 {
		t.Errorf("Get(0) = %v, %v", v, ok)
	}
}

func TestHiddenConcurrentUse(t *testing.T) {
	for _, c := range []*Cache{NewLRU(16), New()} {
		var wg sync.WaitGroup
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := 0; i < 500; i++ {
					k := fmt.Sprint(i % 40)
					c.Put(k, g)
					c.Get(k)
					c.Len()
				}
			}(g)
		}
		wg.Wait()
		if n := c.Len(); n > 40 {
			t.Errorf("Len = %d after concurrent puts of 40 keys", n)
		}
	}
	if n := NewLRU(16).Len(); n != 0 {
		t.Errorf("fresh Len = %d", n)
	}
}

func TestHiddenLRUBoundHoldsUnderConcurrency(t *testing.T) {
	c := NewLRU(16)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				c.Put(fmt.Sprintf("%d-%d", g, i), i)
			}
		}(g)
	}
	wg.Wait()
	if n := c.Len(); n != 16 {
		t.Errorf("Len = %d after 4000 concurrent puts at capacity 16, want 16", n)
	}
}
