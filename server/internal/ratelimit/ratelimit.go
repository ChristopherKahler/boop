// Package ratelimit caps how often one key (a webhook address, a sender) may
// make requests: each key may send a minute's allowance at once, and the
// allowance comes back evenly over the minute.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter allows perMinute requests a minute per key, in bursts of up to
// perMinute. A nil Limiter, or one with perMinute <= 0, never refuses.
//
// Each key keeps one time: when its allowance would be full again. A request
// pushes that time one interval (a minute / perMinute) later, and is refused
// when that would put it more than a minute ahead of now.
type Limiter struct {
	mu        sync.Mutex
	perMinute int
	full      map[string]time.Time
	refused   map[string]bool // refused since the key's last allowed request
	swept     time.Time
	now       func() time.Time
}

// Result is the answer for one request.
type Result struct {
	OK bool
	// RetryAfter is how long until the next request would be allowed.
	RetryAfter time.Duration
	// First is true on the first refusal after an allowed request, so a
	// caller can log a flood once instead of once per request.
	First bool
}

// New returns a Limiter allowing perMinute requests a minute per key.
func New(perMinute int) *Limiter {
	return &Limiter{perMinute: perMinute, full: map[string]time.Time{}, refused: map[string]bool{}, now: time.Now}
}

// Allow takes one request from key's allowance.
func (l *Limiter) Allow(key string) Result {
	if l == nil || l.perMinute <= 0 {
		return Result{OK: true}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)
	interval := time.Minute / time.Duration(l.perMinute)
	full := l.full[key]
	if full.Before(now) {
		full = now
	}
	next := full.Add(interval)
	if allowAt := next.Add(-time.Minute); now.Before(allowAt) {
		first := !l.refused[key]
		l.refused[key] = true
		return Result{RetryAfter: allowAt.Sub(now), First: first}
	}
	l.full[key] = next
	delete(l.refused, key)
	return Result{OK: true}
}

// sweep forgets keys whose allowance is full again (forgetting them changes
// nothing), at most once a minute, so memory stays bounded by recent senders.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.swept) < time.Minute {
		return
	}
	l.swept = now
	for k, full := range l.full {
		if !full.After(now) {
			delete(l.full, k)
			delete(l.refused, k)
		}
	}
}

// PerMinute is the limit; 0 when there is none.
func (l *Limiter) PerMinute() int {
	if l == nil || l.perMinute < 0 {
		return 0
	}
	return l.perMinute
}

// Len is how many keys are being tracked.
func (l *Limiter) Len() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.full)
}
