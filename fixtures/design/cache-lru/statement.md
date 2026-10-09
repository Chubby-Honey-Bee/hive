# Make the cache a bounded LRU, safe for concurrent use

Package `cache` (file `cache.go`) holds a plain map behind `New`, `Get`, `Put` and `Len`. Extend it:

- Add `func NewLRU(capacity int) *Cache`. A cache from `NewLRU` holds at most `capacity` entries; a capacity below 1 is treated as 1. `New()` stays and returns an unbounded cache.
- When `Put` adds a key to a full LRU cache, the least recently used entry is evicted first. `Get` of a present key counts as a use. `Put` of a key already present replaces its value and counts as a use; it evicts nothing.
- `Get`, `Put` and `Len` keep their signatures and are safe to call from many goroutines at once, on both kinds of cache.
- `Len` never exceeds the capacity.

Keep the package name and the existing exported names.
