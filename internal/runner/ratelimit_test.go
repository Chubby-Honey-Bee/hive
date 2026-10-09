package runner

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// fakeBackend records each Run call + returns scripted errors / results.
type fakeBackend struct {
	calls    atomic.Int64
	scripted []error // one per call, empty = nil
}

func (f *fakeBackend) Run(_ context.Context, _ RunRequest) (*RunResult, error) {
	idx := int(f.calls.Add(1)) - 1
	if idx < len(f.scripted) {
		if err := f.scripted[idx]; err != nil {
			return nil, err
		}
	}
	return &RunResult{FinalText: "ok"}, nil
}

func TestRateLimited_PassesThroughOnSuccess(t *testing.T) {
	t.Setenv("HIVE_RPM_DEFAULT", "6000") // 100 RPS so the bucket isn't the test
	ResetRateLimitState()
	inner := &fakeBackend{}
	wrapped := NewRateLimitedBackend(inner, "test-success")

	res, err := wrapped.Run(context.Background(), RunRequest{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinalText != "ok" {
		t.Fatalf("FinalText = %q", res.FinalText)
	}
	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("expected 1 inner call, got %d", got)
	}
}

func TestRateLimited_RetriesOn429(t *testing.T) {
	fastBackoff(t)
	t.Setenv("HIVE_RPM_DEFAULT", "6000")
	ResetRateLimitState()
	inner := &fakeBackend{
		scripted: []error{
			errors.New("HTTP 429: rate limit exceeded"),
			errors.New("rate_limit_error: too many requests"),
			nil,
		},
	}
	wrapped := NewRateLimitedBackend(inner, "test-retry")
	wrapped.maxWall = 5 * time.Second

	start := time.Now()
	_, err := wrapped.Run(context.Background(), RunRequest{})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if got := inner.calls.Load(); got != 3 {
		t.Errorf("expected 3 inner calls, got %d", got)
	}
	// Decorrelated jitter: at least 1s of cumulative sleep across 2 retries.
	if elapsed < backoffBase {
		t.Errorf("expected backoff to take ≥ backoffBase (%v), got %v", backoffBase, elapsed)
	}
}

func TestRateLimited_NonRateLimitErrorPassesThrough(t *testing.T) {
	t.Setenv("HIVE_RPM_DEFAULT", "6000")
	ResetRateLimitState()
	inner := &fakeBackend{
		scripted: []error{errors.New("backend connection refused")},
	}
	wrapped := NewRateLimitedBackend(inner, "test-passthrough")

	_, err := wrapped.Run(context.Background(), RunRequest{})
	if err == nil {
		t.Fatalf("expected error to propagate")
	}
	if got := inner.calls.Load(); got != 1 {
		t.Errorf("expected exactly 1 attempt for non-RL error, got %d", got)
	}
}

func TestRateLimited_RetryExhaustionReturns(t *testing.T) {
	fastBackoff(t)
	t.Setenv("HIVE_RPM_DEFAULT", "6000")
	ResetRateLimitState()
	rlErr := errors.New("HTTP 429: rate limit")
	inner := &fakeBackend{
		scripted: []error{rlErr, rlErr, rlErr, rlErr, rlErr, rlErr, rlErr},
	}
	wrapped := NewRateLimitedBackend(inner, "test-exhaust")
	wrapped.maxWall = 30 * time.Second
	wrapped.maxRetry = 3

	_, err := wrapped.Run(context.Background(), RunRequest{})
	if err == nil {
		t.Fatalf("expected exhaustion error")
	}
	// 1 initial + 3 retries = 4 calls.
	if got := inner.calls.Load(); got != 4 {
		t.Errorf("expected 4 inner calls (1 initial + 3 retries), got %d", got)
	}
}

func TestIsRateLimitError_PatternsAndStatusCodes(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("HTTP 429: too many requests"), true},
		{errors.New("rate_limit_exceeded"), true},
		{errors.New("Quota exceeded for project"), true},
		{errors.New("Resource exhausted: per-minute limit"), true},
		{errors.New("Anthropic API overloaded (529)"), true},
		{errors.New("Throttled: please retry"), true},
		{errors.New("connection refused"), false},
		{errors.New("invalid request: missing field"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := isRateLimitError(c.err); got != c.want {
			t.Errorf("isRateLimitError(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestExtractRetryAfter_HonoursHint(t *testing.T) {
	cases := []struct {
		msg  string
		want time.Duration
	}{
		{"HTTP 429: retry-after: 12s", 12 * time.Second},
		{"rate limited; retry after 5 seconds", 5 * time.Second},
		{"retry-after: 250ms please", 250 * time.Millisecond},
		{"rate limited", 0},
	}
	for _, c := range cases {
		got := extractRetryAfter(errors.New(c.msg))
		if got != c.want {
			t.Errorf("extractRetryAfter(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

func TestTokenBucket_BlocksUntilTokenAvailable(t *testing.T) {
	// 60 RPM = 1 RPS — slow enough that asking for a 2nd token forces wait.
	b := newTokenBucket(1, 1)
	ctx := context.Background()
	if err := b.acquire(ctx); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	start := time.Now()
	if err := b.acquire(ctx); err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 800*time.Millisecond {
		t.Errorf("second acquire should have waited ≥0.8s, got %v", elapsed)
	}
}

func TestTokenBucket_RespectsContextCancellation(t *testing.T) {
	b := newTokenBucket(0.01, 1) // 1 token per 100s — effectively never replenishes
	if err := b.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := b.acquire(ctx)
	if err == nil {
		t.Fatalf("expected ctx-cancelled error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want DeadlineExceeded", err)
	}
}

func TestTokenBucketFor_PerProviderIsolation(t *testing.T) {
	t.Setenv("HIVE_RPM_PROVIDER_A", "60")
	t.Setenv("HIVE_RPM_PROVIDER_B", "6000")
	ResetRateLimitState()

	bA := tokenBucketFor("provider-a")
	bB := tokenBucketFor("provider-b")
	if bA == bB {
		t.Fatalf("expected separate buckets per provider")
	}
	if bA.rate >= bB.rate {
		t.Errorf("provider-a (60 RPM) should be slower than provider-b (6000 RPM); got %v vs %v", bA.rate, bB.rate)
	}
}

func TestTokenBucket_RefundReturnsToken(t *testing.T) {
	// Slow rate so refunds are observable: 60 RPM = 1 RPS, burst 3.
	b := newTokenBucket(1.0, 3)
	ctx := context.Background()
	// Drain the bucket.
	for i := 0; i < 3; i++ {
		if err := b.acquire(ctx); err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
	}
	// Without refund, the next acquire would block ~1s. With refund,
	// it should succeed immediately.
	b.refund()
	start := time.Now()
	if err := b.acquire(ctx); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed > 100*time.Millisecond {
		t.Errorf("post-refund acquire should be near-instant, took %v", elapsed)
	}
}

func TestTokenBucket_RefundClampsAtBurst(t *testing.T) {
	// Over-refund: even after 1000 spurious refunds, the bucket
	// should never exceed burst. Otherwise a flood of 429s could
	// hand us a budget surplus.
	b := newTokenBucket(1.0, 5)
	for i := 0; i < 1000; i++ {
		b.refund()
	}
	b.mu.Lock()
	tokens := b.tokens
	b.mu.Unlock()
	if tokens > 5 {
		t.Fatalf("refund clamping broken: tokens=%v, burst=5", tokens)
	}
}

func TestRateLimited_RefundsBudgetOn429(t *testing.T) {
	fastBackoff(t)
	// Verify the end-to-end contract: a long retry sequence on 429
	// doesn't permanently deplete the bucket. We drain the bucket,
	// fire a 429-returning request that ultimately succeeds after
	// several retries, then verify a fresh request can still acquire
	// without the multi-second wait that would happen if every retry
	// charged a token.
	t.Setenv("HIVE_RPM_DEFAULT", "60") // 1 RPS, small burst
	ResetRateLimitState()
	rlErr := errors.New("HTTP 429: rate limit")
	inner := &fakeBackend{scripted: []error{rlErr, rlErr, rlErr, nil}}
	wrapped := NewRateLimitedBackend(inner, "test-refund")
	wrapped.maxWall = 30 * time.Second

	if _, err := wrapped.Run(context.Background(), RunRequest{}); err != nil {
		t.Fatalf("Run with retries: %v", err)
	}
	// The bucket should now have approximately the same tokens as
	// before (3 retries refunded; 1 final success consumed). At
	// burst=6, we should still have plenty.
	wrapped.bucket.mu.Lock()
	tokens := wrapped.bucket.tokens
	wrapped.bucket.mu.Unlock()
	if tokens < 4.0 {
		t.Errorf("after 3 refunded retries + 1 consume, expected ≥4 tokens, got %v", tokens)
	}
}

// Stress: 12 concurrent goroutines through the same wrapped backend.
// All should succeed; no race detector violations.
func TestRateLimited_ConcurrentDispatch(t *testing.T) {
	t.Setenv("HIVE_RPM_DEFAULT", "60000") // basically uncapped for this test
	ResetRateLimitState()
	inner := &fakeBackend{}
	wrapped := NewRateLimitedBackend(inner, "test-concurrent")

	const N = 12
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func() {
			_, err := wrapped.Run(context.Background(), RunRequest{})
			errs <- err
		}()
	}
	for i := 0; i < N; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent dispatch %d: %v", i, err)
		}
	}
	if got := inner.calls.Load(); got != int64(N) {
		t.Errorf("expected %d inner calls, got %d", N, got)
	}
	_ = fmt.Sprintf // silence unused import in narrow builds
}

// fastBackoff runs the decorrelated-jitter algorithm at millisecond scale for
// the duration of a test. The retry logic is unchanged; only the sleeps are.
func fastBackoff(t *testing.T) {
	t.Helper()
	prevBase, prevCap := backoffBase, backoffCap
	backoffBase, backoffCap = 20*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { backoffBase, backoffCap = prevBase, prevCap })
}

// ResetRateLimitState clears the process-wide limiter state between tests.
// Test-only: nothing in production resets it.
// this between cases to avoid bucket leakage; production never calls it.
func ResetRateLimitState() {
	bucketsMu.Lock()
	defer bucketsMu.Unlock()
	buckets = map[string]*tokenBucket{}
}

func TestIsRateLimitError_Nil(t *testing.T) {
	if isRateLimitError(nil) {
		t.Error("nil error should not be a rate limit")
	}
}

func TestIsRateLimitError_Patterns(t *testing.T) {
	patterns := []string{
		"rate limit exceeded",
		"rate_limit hit",
		"RateLimit detected",
		"too many requests sent",
		"quota exceeded for project",
		"resource exhausted (gemini)",
		"request was throttled",
		"throttling applied",
		"server overloaded",
		"HTTP 529 service overloaded",
	}
	for _, p := range patterns {
		t.Run(p, func(t *testing.T) {
			if !isRateLimitError(errors.New(p)) {
				t.Errorf("isRateLimitError(%q) = false; want true", p)
			}
		})
	}
}

func TestIsRateLimitError_NonRateLimit(t *testing.T) {
	cases := []string{
		"some other error",
		"connection refused",
		"file not found",
		"",
	}
	for _, c := range cases {
		if isRateLimitError(errors.New(c)) {
			t.Errorf("isRateLimitError(%q) = true; want false", c)
		}
	}
}

// statusCodeErr implements the StatusCode() interface used by some SDKs.
type statusCodeErr struct {
	code int
	msg  string
}

func (e *statusCodeErr) Error() string   { return e.msg }
func (e *statusCodeErr) StatusCode() int { return e.code }

func TestIsRateLimitError_TypedSDKErrors(t *testing.T) {
	cases := []struct {
		code int
		want bool
	}{
		{http.StatusTooManyRequests, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusBadRequest, false},
		{http.StatusInternalServerError, false},
		{http.StatusOK, false},
	}
	for _, tc := range cases {
		err := &statusCodeErr{code: tc.code, msg: "wrapped"}
		if got := isRateLimitError(err); got != tc.want {
			t.Errorf("StatusCode=%d → isRateLimit = %v; want %v", tc.code, got, tc.want)
		}
	}
}
