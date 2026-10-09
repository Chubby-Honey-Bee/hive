package retry

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// fakeSleep records every wait and returns what the test says.
type fakeSleep struct {
	waits []time.Duration
	err   error
}

func (f *fakeSleep) install(t *testing.T) {
	t.Helper()
	old := Sleep
	Sleep = func(_ context.Context, d time.Duration) error {
		f.waits = append(f.waits, d)
		return f.err
	}
	t.Cleanup(func() { Sleep = old })
}

func linear(i int) time.Duration { return time.Duration(i) * time.Second }

func TestHiddenSucceedsFirstTime(t *testing.T) {
	fs := &fakeSleep{}
	fs.install(t)
	calls := 0
	if err := Do(context.Background(), 3, linear, func() error { calls++; return nil }); err != nil || calls != 1 || len(fs.waits) != 0 {
		t.Errorf("err %v calls %d waits %v; want nil, 1, none", err, calls, fs.waits)
	}
}

func TestHiddenRetriesWithBackoffThenSucceeds(t *testing.T) {
	fs := &fakeSleep{}
	fs.install(t)
	calls := 0
	err := Do(context.Background(), 5, linear, func() error {
		calls++
		if calls < 3 {
			return fmt.Errorf("fail %d", calls)
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("err %v calls %d; want nil, 3", err, calls)
	}
	if len(fs.waits) != 2 || fs.waits[0] != time.Second || fs.waits[1] != 2*time.Second {
		t.Errorf("waits %v; want backoff(1), backoff(2)", fs.waits)
	}
}

func TestHiddenExhausted(t *testing.T) {
	fs := &fakeSleep{}
	fs.install(t)
	last := errors.New("third")
	calls := 0
	err := Do(context.Background(), 3, linear, func() error {
		calls++
		if calls == 3 {
			return last
		}
		return fmt.Errorf("fail %d", calls)
	})
	var ex *Exhausted
	if !errors.As(err, &ex) {
		t.Fatalf("err %v; want *Exhausted", err)
	}
	if ex.Attempts != 3 || !errors.Is(err, last) || ex.Last != last {
		t.Errorf("Exhausted %+v; want 3 attempts and the last error", ex)
	}
	if len(fs.waits) != 2 {
		t.Errorf("waits %v; want two (none after the last attempt)", fs.waits)
	}
	if err.Error() == "" {
		t.Error("Exhausted.Error() is empty")
	}
}

func TestHiddenPermanentStopsAtOnce(t *testing.T) {
	fs := &fakeSleep{}
	fs.install(t)
	inner := errors.New("no such user")
	calls := 0
	err := Do(context.Background(), 5, linear, func() error {
		calls++
		return fmt.Errorf("wrapped: %w", &Permanent{Err: inner})
	})
	if calls != 1 || len(fs.waits) != 0 {
		t.Errorf("calls %d waits %v; want 1 call and no wait", calls, fs.waits)
	}
	if !errors.Is(err, inner) {
		t.Errorf("err %v does not unwrap to the inner error", err)
	}
	var perm *Permanent
	if !errors.As(err, &perm) || perm.Error() != inner.Error() {
		t.Errorf("err %v; want a *Permanent whose Error() is the inner's", err)
	}
}

func TestHiddenNoAttempts(t *testing.T) {
	calls := 0
	for _, n := range []int{0, -1} {
		if err := Do(context.Background(), n, linear, func() error { calls++; return nil }); !errors.Is(err, ErrNoAttempts) {
			t.Errorf("attempts %d: err %v; want ErrNoAttempts", n, err)
		}
	}
	if calls != 0 {
		t.Errorf("fn called %d times with no attempts", calls)
	}
}

func TestHiddenSleepErrorReturned(t *testing.T) {
	fs := &fakeSleep{err: context.Canceled}
	fs.install(t)
	calls := 0
	err := Do(context.Background(), 5, linear, func() error { calls++; return errors.New("fail") })
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Errorf("err %v calls %d; want context.Canceled after one call", err, calls)
	}
}

func TestHiddenNilBackoffNeverSleeps(t *testing.T) {
	fs := &fakeSleep{}
	fs.install(t)
	calls := 0
	err := Do(context.Background(), 3, nil, func() error { calls++; return errors.New("fail") })
	var ex *Exhausted
	if !errors.As(err, &ex) || calls != 3 || len(fs.waits) != 0 {
		t.Errorf("err %v calls %d waits %v; want Exhausted, 3, none", err, calls, fs.waits)
	}
}
