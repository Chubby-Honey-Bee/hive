// Package cache is an in-memory key-value cache.
package cache

import (
	"container/list"
	"sync"
)

type entry struct {
	key   string
	value any
}

// Cache maps string keys to values, safe for concurrent use. A cache from
// NewLRU evicts its least recently used entry past its capacity.
type Cache struct {
	mu       sync.Mutex
	capacity int // 0 is unbounded
	items    map[string]*list.Element
	order    *list.List // front is the most recently used
}

// New returns an empty unbounded cache.
func New() *Cache {
	return &Cache{items: map[string]*list.Element{}, order: list.New()}
}

// NewLRU returns an empty cache holding at most capacity entries; a
// capacity below 1 is treated as 1.
func NewLRU(capacity int) *Cache {
	if capacity < 1 {
		capacity = 1
	}
	c := New()
	c.capacity = capacity
	return c
}

// Get returns the value stored under key and whether it was present, and
// counts as a use.
func (c *Cache) Get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*entry).value, true
}

// Put stores value under key, evicting the least recently used entry when
// the cache is full and key is new.
func (c *Cache) Put(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		el.Value.(*entry).value = value
		c.order.MoveToFront(el)
		return
	}
	if c.capacity > 0 && c.order.Len() >= c.capacity {
		if last := c.order.Back(); last != nil {
			c.order.Remove(last)
			delete(c.items, last.Value.(*entry).key)
		}
	}
	c.items[key] = c.order.PushFront(&entry{key: key, value: value})
}

// Len is the number of entries.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}
