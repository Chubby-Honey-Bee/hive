// Package retry runs an operation again until it succeeds.
package retry

import (
	"context"
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

// Do calls fn until it returns nil. Not yet implemented: it calls fn once.
func Do(ctx context.Context, attempts int, backoff func(attempt int) time.Duration, fn func() error) error {
	return fn()
}
