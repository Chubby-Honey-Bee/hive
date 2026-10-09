# Give the cache entries a time to live, safe for concurrent use

Package `cache` (file `cache.go`) holds a plain map behind `New`, `Get`, `Put` and `Len`. Extend it:

- Add `func NewTTL(ttl time.Duration) *Cache`. Every entry of such a cache expires `ttl` after it was last put: it is expired once the clock reads the put time plus `ttl` or later. `New()` stays and returns a cache whose entries never expire. There is no capacity bound.
- `Cache` gets an exported field `Now func() time.Time`, the clock every method reads; `New` and `NewTTL` set it to `time.Now`, and tests replace it.
- `Get` of an expired entry returns `(nil, false)` and removes the entry. `Put` of an existing key replaces its value and restarts its time to live.
- `Len` counts only the entries that are not expired at the time of the call.
- Add `func (c *Cache) Purge() int`, which removes every expired entry and returns how many it removed.
- `Get`, `Put`, `Len` and `Purge` are safe to call from many goroutines at once, on both kinds of cache.

Keep the package name and the existing exported names.
