package policy

import (
	"sync"
	"time"
)

// slidingWindow is a fixed-window counter keyed by (principal, minute). The
// counter is reset when the wall-clock minute rolls over. Each entry stores
// the start of the current window and the count of requests seen within it.
//
// We use fixed windows rather than sliding logs because:
//  1. The hot path (Check) must be sub-microsecond; map lookups are O(1).
//  2. Boundary bleed (one request at 23:59:59 + one at 00:00:01 nominally
//     doubles the per-window traffic) is acceptable for proxy throttling.
//  3. The authoritative count is persisted to PG via Consume; the in-memory
//     counter is only the preemptive guard.
type slidingWindow struct {
	mu       sync.Mutex
	counters map[string]*windowEntry
}

type windowEntry struct {
	windowStart time.Time
	count       int64
}

func newSlidingWindow() *slidingWindow {
	return &slidingWindow{counters: make(map[string]*windowEntry)}
}

// AllowAndIncrement checks the limit for the supplied key against the current
// window and increments the counter if allowed. Returns false (without
// incrementing) when the limit would be exceeded.
//
// When limit <= 0 the function is a no-op (no throttling).
func (w *slidingWindow) AllowAndIncrement(key string, limit int, windowSize time.Duration) bool {
	if w == nil || limit <= 0 {
		return true
	}
	now := time.Now()
	bucketStart := now.Truncate(windowSize)

	w.mu.Lock()
	defer w.mu.Unlock()

	entry, ok := w.counters[key]
	if !ok || !entry.windowStart.Equal(bucketStart) {
		entry = &windowEntry{windowStart: bucketStart, count: 0}
		w.counters[key] = entry
	}
	if entry.count >= int64(limit) {
		return false
	}
	entry.count++
	return true
}

// SnapshotCurrent returns the current window count for a principal (for
// introspection / dashboards). Does not mutate state.
func (w *slidingWindow) SnapshotCurrent(key string, windowSize time.Duration) int64 {
	if w == nil {
		return 0
	}
	bucketStart := time.Now().Truncate(windowSize)
	w.mu.Lock()
	defer w.mu.Unlock()
	entry, ok := w.counters[key]
	if !ok || !entry.windowStart.Equal(bucketStart) {
		return 0
	}
	return entry.count
}

// Cleanup drops counters whose window has aged past the retention threshold.
// Called opportunistically from a background goroutine to bound memory.
func (w *slidingWindow) Cleanup(retention time.Duration) {
	if w == nil {
		return
	}
	cutoff := time.Now().Add(-retention)
	w.mu.Lock()
	defer w.mu.Unlock()
	for key, entry := range w.counters {
		if entry.windowStart.Before(cutoff) {
			delete(w.counters, key)
		}
	}
}

// Forget drops the counter for a single key. Used by policy invalidations
// so a key that was disabled then reactivated is not falsely throttled.
func (w *slidingWindow) Forget(key string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.counters, key)
}

// Reset drops every counter. Used by InvalidateAll.
func (w *slidingWindow) Reset() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.counters = make(map[string]*windowEntry)
}
