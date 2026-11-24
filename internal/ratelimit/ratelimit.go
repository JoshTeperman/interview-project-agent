// Package ratelimit provides a small in-memory fixed-window rate limiter.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter allows at most Limit events per key in each Window.
type Limiter struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	buckets map[string]bucket
}

type bucket struct {
	start time.Time
	count int
}

// New returns a limiter allowing limit events per key per window.
func New(limit int, window time.Duration) *Limiter {
	return &Limiter{limit: limit, window: window, now: time.Now, buckets: map[string]bucket{}}
}

// WithClock replaces the time source (for tests) and returns l.
func (l *Limiter) WithClock(now func() time.Time) *Limiter {
	l.now = now
	return l
}

// Allow records an event for key. It returns false, plus the time until the
// window resets, when the key is over its limit; the event is then not counted.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.buckets[key]
	if now.Sub(b.start) >= l.window {
		b = bucket{start: now}
	}
	if b.count >= l.limit {
		return false, b.start.Add(l.window).Sub(now)
	}
	b.count++
	l.buckets[key] = b
	return true, 0
}

// Prune drops buckets whose window has ended, bounding memory.
func (l *Limiter) Prune() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for k, b := range l.buckets {
		if now.Sub(b.start) >= l.window {
			delete(l.buckets, k)
		}
	}
}
