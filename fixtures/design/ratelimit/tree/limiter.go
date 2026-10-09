// Package ratelimit limits how often an action may happen.
package ratelimit

import "time"

// Limiter decides whether an action may proceed now. Not yet implemented:
// it allows everything.
type Limiter struct {
	// Now is the clock the limiter reads; tests replace it.
	Now func() time.Time
}

// New returns a limiter that allows rate events per second, in bursts of
// up to burst events.
func New(rate float64, burst int) *Limiter {
	return &Limiter{Now: time.Now}
}

// Allow reports whether one event may proceed now.
func (l *Limiter) Allow() bool {
	return true
}
