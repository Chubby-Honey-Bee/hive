# Implement Do with its stopping rules

Package `retry` (file `retry.go`) exports `var Sleep func(ctx context.Context, d time.Duration) error` and `func Do(ctx context.Context, attempts int, backoff func(attempt int) time.Duration, fn func() error) error`. Implement `Do` and add the types below. The existing names and signatures stay; every wait goes through `Sleep`.

- `Do` calls `fn` up to `attempts` times and returns nil as soon as a call returns nil.
- After a failed call that is not the last, `Do` waits `backoff(i)`, where `i` is the 1-based number of the call that just failed, by calling `Sleep(ctx, d)`. A nil `backoff` means no wait. When `Sleep` returns an error, `Do` returns it at once and calls `fn` no more.
- `type Permanent struct{ Err error }` with `Error() string` returning `Err.Error()` and `Unwrap() error` returning `Err`. When `fn` returns a `*Permanent` (directly or wrapped), `Do` stops at once and returns that error.
- `var ErrNoAttempts = errors.New("retry: attempts must be at least 1")`: `Do` returns it, without calling `fn`, when `attempts` is below 1.
- When every call failed, `Do` returns `*Exhausted`: `type Exhausted struct{ Attempts int; Last error }`, where `Attempts` is the number of calls made and `Last` the last error; its `Error()` names both, and `Unwrap()` returns `Last`.
