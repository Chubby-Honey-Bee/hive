package runner

import (
	"errors"
	"testing"
	"time"
)

func TestComputeBackoff_NoRemainingTime(t *testing.T) {
	got := computeBackoff(1, errors.New("rate limit"), 0)
	if got != 0 {
		t.Errorf("computeBackoff(remaining=0) = %v; want 0", got)
	}
	got = computeBackoff(1, errors.New("rate limit"), -5*time.Second)
	if got != 0 {
		t.Errorf("computeBackoff(remaining=-5s) = %v; want 0", got)
	}
}

func TestComputeBackoff_RetryAfterHintRespected(t *testing.T) {
	// extractRetryAfter parses "Retry-After: Ns" / "retry after Ns" patterns.
	err := errors.New("rate limit; Retry-After: 12s")
	got := computeBackoff(0, err, 60*time.Second)
	if got != 12*time.Second {
		t.Errorf("computeBackoff with Retry-After:12s = %v; want 12s", got)
	}
}

func TestComputeBackoff_RetryAfterExceedsRemaining(t *testing.T) {
	err := errors.New("Retry-After: 100s")
	got := computeBackoff(0, err, 30*time.Second)
	if got != 0 {
		t.Errorf("computeBackoff with hint > remaining = %v; want 0 (give up)", got)
	}
}

func TestComputeBackoff_NoHintReturnsBoundedBackoff(t *testing.T) {
	for attempt := 0; attempt < 8; attempt++ {
		got := computeBackoff(attempt, errors.New("generic 429"), 60*time.Second)
		if got < time.Second {
			t.Errorf("attempt=%d: backoff = %v; want >= 1s", attempt, got)
		}
		if got > 30*time.Second {
			t.Errorf("attempt=%d: backoff = %v; want <= 30s cap", attempt, got)
		}
	}
}

func TestComputeBackoff_ClampedByRemaining(t *testing.T) {
	// remaining = 500ms. Backoff must not exceed it.
	got := computeBackoff(5, errors.New("rate limit"), 500*time.Millisecond)
	if got > 500*time.Millisecond {
		t.Errorf("computeBackoff = %v; want <= remaining (500ms)", got)
	}
}

func TestExtractRetryAfter(t *testing.T) {
	cases := []struct {
		err  string
		want time.Duration
	}{
		{"random error", 0},
		{"429 Retry-After: 5s", 5 * time.Second},
		{"retry after 30 seconds", 30 * time.Second},
		{"Retry-After: 0s", 0},
	}
	for _, tc := range cases {
		t.Run(tc.err, func(t *testing.T) {
			got := extractRetryAfter(errors.New(tc.err))
			if got != tc.want {
				t.Errorf("extractRetryAfter(%q) = %v; want %v", tc.err, got, tc.want)
			}
		})
	}
}
