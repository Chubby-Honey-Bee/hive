// Package cache is an in-memory key-value cache.
package cache

import (
	"sync"
	"time"
)

type entry struct {
	value any
	put   time.Time
}

// Cache maps string keys to values, safe for concurrent use. A cache from
// NewTTL expires each entry a fixed time after it was last put.
type Cache struct {
	// Now is the clock every method reads.
	Now func() time.Time

	mu    sync.Mutex
	ttl   time.Duration // 0 is never
	items map[string]entry
}

// New returns an empty cache whose entries never expire.
func New() *Cache {
	return NewTTL(0)
}

// NewTTL returns an empty cache whose entries expire ttl after they were
// last put; 0 means never.
func NewTTL(ttl time.Duration) *Cache {
	return &Cache{Now: time.Now, ttl: ttl, items: map[string]entry{}}
}

func (c *Cache) expired(e entry, now time.Time) bool {
	return c.ttl > 0 && !now.Before(e.put.Add(c.ttl))
}

// Get returns the value stored under key and whether it was present and
// not expired; an expired entry is removed.
func (c *Cache) Get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok {
		return nil, false
	}
	if c.expired(e, c.Now()) {
		delete(c.items, key)
		return nil, false
	}
	return e.value, true
}

// Put stores value under key and restarts its time to live.
func (c *Cache) Put(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = entry{value: value, put: c.Now()}
}

// Len is the number of entries not expired now.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.Now()
	n := 0
	for _, e := range c.items {
		if !c.expired(e, now) {
			n++
		}
	}
	return n
}

// Purge removes every expired entry and returns how many it removed.
func (c *Cache) Purge() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.Now()
	n := 0
	for k, e := range c.items {
		if c.expired(e, now) {
			delete(c.items, k)
			n++
		}
	}
	return n
}
