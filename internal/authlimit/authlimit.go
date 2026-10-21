// Package authlimit provides a small in-memory login failure limiter.
package authlimit

import (
	"sync"
	"time"
)

type entry struct {
	failures int
	first    time.Time
	last     time.Time
}

// Limiter blocks repeated failed logins for the same key within a window.
type Limiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	entries map[string]entry
}

// New returns a limiter that blocks after max failures within window.
func New(max int, window time.Duration) *Limiter {
	if max <= 0 {
		max = 10
	}
	if window <= 0 {
		window = 5 * time.Minute
	}
	return &Limiter{max: max, window: window, entries: make(map[string]entry)}
}

// Allowed reports whether key may attempt authentication now.
func (l *Limiter) Allowed(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok {
		return true
	}
	if time.Since(e.first) > l.window {
		delete(l.entries, key)
		return true
	}
	return e.failures < l.max
}

// Failure records a failed attempt.
func (l *Limiter) Failure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	e, ok := l.entries[key]
	if !ok || now.Sub(e.first) > l.window {
		l.entries[key] = entry{failures: 1, first: now, last: now}
		return
	}
	e.failures++
	e.last = now
	l.entries[key] = e
}

// Success clears the failure history for key.
func (l *Limiter) Success(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}
