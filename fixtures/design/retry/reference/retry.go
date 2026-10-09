// Package retry runs an operation again until it succeeds.
package retry

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Sleep waits for d or until ctx is done. Tests replace it.
var Sleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ErrNoAttempts is returned when attempts is below 1.
var ErrNoAttempts = errors.New("retry: attempts must be at least 1")

// Permanent marks an error that no retry can fix.
type Permanent struct{ Err error }

func (p *Permanent) Error() string { return p.Err.Error() }
func (p *Permanent) Unwrap() error { return p.Err }

// Exhausted is returned when every attempt failed.
type Exhausted struct {
	Attempts int
	Last     error
}

func (e *Exhausted) Error() string {
	return fmt.Sprintf("retry: %d attempt(s) failed, last error: %v", e.Attempts, e.Last)
}

func (e *Exhausted) Unwrap() error { return e.Last }

// Do calls fn up to attempts times, waiting backoff(i) through Sleep after
// failed call i when another follows. It stops at once on a *Permanent
// error, which it returns, and on a Sleep error, which it returns.
func Do(ctx context.Context, attempts int, backoff func(attempt int) time.Duration, fn func() error) error {
	if attempts < 1 {
		return ErrNoAttempts
	}
	var last error
	for i := 1; i <= attempts; i++ {
		last = fn()
		if last == nil {
			return nil
		}
		var perm *Permanent
		if errors.As(last, &perm) {
			return last
		}
		if i == attempts {
			break
		}
		if backoff != nil {
			if err := Sleep(ctx, backoff(i)); err != nil {
				return err
			}
		}
	}
	return &Exhausted{Attempts: attempts, Last: last}
}
