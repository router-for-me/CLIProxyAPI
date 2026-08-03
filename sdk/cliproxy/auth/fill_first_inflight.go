package auth

import (
	"strings"
	"sync"
)

// fillFirstInflightTracker counts concurrent executions per credential so
// fill-first selection can spill when a sticky account is already saturated.
type fillFirstInflightTracker struct {
	mu     sync.Mutex
	counts map[string]int
}

func newFillFirstInflightTracker() *fillFirstInflightTracker {
	return &fillFirstInflightTracker{counts: make(map[string]int)}
}

func (t *fillFirstInflightTracker) get(authID string) int {
	if t == nil {
		return 0
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.counts[authID]
}

// snapshot returns a copy of current in-flight counts for capacity-aware picks.
func (t *fillFirstInflightTracker) snapshot() map[string]int {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.counts) == 0 {
		return nil
	}
	out := make(map[string]int, len(t.counts))
	for authID, count := range t.counts {
		out[authID] = count
	}
	return out
}

// acquire increments the in-flight count for authID and returns a once-only release.
func (t *fillFirstInflightTracker) acquire(authID string) func() {
	_, release := t.tryReserve(authID, 0)
	return release
}

// tryReserve reserves capacity for authID.
// When max > 0 and the credential is already at capacity, it returns false.
// When max <= 0, reservation always succeeds (unlimited / soft-overflow force path).
func (t *fillFirstInflightTracker) tryReserve(authID string, max int) (bool, func()) {
	if t == nil {
		return true, func() {}
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return true, func() {}
	}
	t.mu.Lock()
	if t.counts == nil {
		t.counts = make(map[string]int)
	}
	if max > 0 && t.counts[authID] >= max {
		t.mu.Unlock()
		return false, nil
	}
	t.counts[authID]++
	t.mu.Unlock()

	var once sync.Once
	return true, func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			count := t.counts[authID]
			if count <= 1 {
				delete(t.counts, authID)
				return
			}
			t.counts[authID] = count - 1
		})
	}
}

// pickFillFirstAuthID selects among ordered candidate IDs using fill-first capacity rules.
// ordered must already be in fill-first preference order (non-demoted first, shuffle rank).
// When maxInflight <= 0, the first candidate wins (legacy sticky behavior).
// When maxInflight > 0, the first candidate under capacity wins; if none are under
// capacity, the least-loaded candidate is chosen (soft overflow).
func pickFillFirstAuthID(ordered []string, loads map[string]int, maxInflight int) string {
	if len(ordered) == 0 {
		return ""
	}
	if maxInflight <= 0 {
		return ordered[0]
	}

	bestOverflow := ""
	bestLoad := int(^uint(0) >> 1)
	for _, authID := range ordered {
		authID = strings.TrimSpace(authID)
		if authID == "" {
			continue
		}
		load := 0
		if loads != nil {
			load = loads[authID]
		}
		if load < maxInflight {
			return authID
		}
		if bestOverflow == "" || load < bestLoad {
			bestOverflow = authID
			bestLoad = load
		}
	}
	if bestOverflow != "" {
		return bestOverflow
	}
	return ordered[0]
}
