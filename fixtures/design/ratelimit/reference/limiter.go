// Package ratelimit limits how often an action may happen.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter is a token bucket: burst tokens at most, refilled at rate per
// second as the clock Now advances.
type Limiter struct {
	// Now is the clock the limiter reads; tests replace it.
	Now func() time.Time

	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
	primed bool
}

// New returns a full limiter allowing rate events per second in bursts of
// up to burst events; a burst below 1 is treated as 1.
func New(rate float64, burst int) *Limiter {
	if burst < 1 {
		burst = 1
	}
	return &Limiter{Now: time.Now, rate: rate, burst: float64(burst), tokens: float64(burst)}
}

// refill adds the tokens the clock has earned since the last call. The
// caller holds mu.
func (l *Limiter) refill() {
	now := l.Now()
	if !l.primed {
		l.last, l.primed = now, true
		return
	}
	if l.rate > 0 {
		if elapsed := now.Sub(l.last).Seconds(); elapsed > 0 {
			l.tokens += elapsed * l.rate
			if l.tokens > l.burst {
				l.tokens = l.burst
			}
		}
	}
	l.last = now
}

// Allow takes one token when one is available.
func (l *Limiter) Allow() bool { return l.AllowN(1) }

// AllowN takes n tokens when n are available, else nothing.
func (l *Limiter) AllowN(n int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refill()
	if n <= 0 {
		return true
	}
	if l.tokens+1e-9 < float64(n) {
		return false
	}
	l.tokens -= float64(n)
	return true
}

// Tokens is the tokens available now.
func (l *Limiter) Tokens() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refill()
	return l.tokens
}
