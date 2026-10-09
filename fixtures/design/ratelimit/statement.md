# Implement a token-bucket limiter

Package `ratelimit` (file `limiter.go`) exports `Limiter` with a `Now func() time.Time` field, `func New(rate float64, burst int) *Limiter` and `func (l *Limiter) Allow() bool`. Implement the token bucket and add `AllowN` and `Tokens`. The existing names and signatures stay; `New` keeps setting `Now` to `time.Now`.

- The bucket holds at most `burst` tokens and starts full. A `burst` below 1 is treated as 1.
- Tokens refill continuously at `rate` per second, measured with `Now`, and never beyond `burst`. A `rate` of 0 or less never refills: only the initial burst is ever allowed.
- `Allow` takes one token when at least one is available and returns true; otherwise it takes nothing and returns false.
- `func (l *Limiter) AllowN(n int) bool` takes `n` tokens when at least `n` are available and returns true; otherwise it takes nothing and returns false. `n` of 0 or less is always allowed and takes nothing.
- `func (l *Limiter) Tokens() float64` returns the tokens available now, after refill, exact to within 1e-9.
- All three methods are safe to call from many goroutines at once, and together they never hand out more tokens than were ever available.
