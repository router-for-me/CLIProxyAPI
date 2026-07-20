package policy

import "sync"

// parallelLimiter tracks the in-flight request count per principal (the
// plaintext API key string) and rejects Acquire() when the count would
// exceed the configured max_parallel_requests. Used to enforce the
// LiteLLM-equivalent "max concurrent requests" cap at both the per-key and
// per-user level.
//
// Lifecycle: counters stay at zero after Release(); the map grows with the
// number of distinct principals ever seen. Compaction is handled by the
// background cleanup loop (sweeps zero entries).
type parallelLimiter struct {
	mu       sync.Mutex
	inflight map[string]int
}

func newParallelLimiter() *parallelLimiter {
	return &parallelLimiter{inflight: make(map[string]int)}
}

// Acquire returns true when the in-flight count for the principal is below
// limit. limit <= 0 disables the cap (always true). When the limit is hit the
// caller (middleware) surfaces an HTTP 429.
func (l *parallelLimiter) Acquire(key string, limit int) bool {
	if l == nil || limit <= 0 || key == "" {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inflight[key] >= limit {
		return false
	}
	l.inflight[key]++
	return true
}

// Release decrements the in-flight counter. Safe to call once per Acquire.
// Never decrements below zero.
func (l *parallelLimiter) Release(key string) {
	if l == nil || key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inflight[key] <= 1 {
		delete(l.inflight, key)
		return
	}
	l.inflight[key]--
}

// Current exposes the in-flight count for a principal (introspection only).
func (l *parallelLimiter) Current(key string) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inflight[key]
}

// Forget drops the counter entirely. Used when a key is invalidated (so a
// reactivated key is not falsely stuck at its pre-deactivation counter).
func (l *parallelLimiter) Forget(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.inflight, key)
}

// Compact drops zero-or-negative entries so the map does not grow
// indefinitely. Called by the background cleanup goroutine.
func (l *parallelLimiter) Compact() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, v := range l.inflight {
		if v <= 0 {
			delete(l.inflight, k)
		}
	}
}

// Reset drops all counters. Used by InvalidateAll.
func (l *parallelLimiter) Reset() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inflight = make(map[string]int)
}
