// Package cache is an in-memory key-value cache.
package cache

// Cache maps string keys to values. It is a plain map today: no bound, no
// expiry, and not safe for concurrent use.
type Cache struct {
	items map[string]any
}

// New returns an empty cache.
func New() *Cache {
	return &Cache{items: map[string]any{}}
}

// Get returns the value stored under key and whether it was present.
func (c *Cache) Get(key string) (any, bool) {
	v, ok := c.items[key]
	return v, ok
}

// Put stores value under key.
func (c *Cache) Put(key string, value any) {
	c.items[key] = value
}

// Len is the number of entries.
func (c *Cache) Len() int {
	return len(c.items)
}
