package jevgate

import (
	"sync"
	"time"
)

// DefaultBreakerCooldown is how long a router stays on the heuristic after an
// authentication failure.
const DefaultBreakerCooldown = 5 * time.Minute

// Breaker is a per-router cooldown tripped by authentication failures. It stops
// a misconfigured key from hammering the API at request rate while an operator
// notices. In-memory only: a restart clears it, which is the right recovery for
// a credential that has been fixed.
type Breaker struct {
	mu       sync.Mutex
	openTil  map[string]time.Time
	cooldown time.Duration
	now      func() time.Time
}

// NewBreaker builds a Breaker. A non-positive cooldown uses the default.
func NewBreaker(cooldown time.Duration) *Breaker {
	if cooldown <= 0 {
		cooldown = DefaultBreakerCooldown
	}
	return &Breaker{openTil: make(map[string]time.Time), cooldown: cooldown, now: time.Now}
}

// Open reports whether routerID is currently suppressed.
func (b *Breaker) Open(routerID string) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	until, ok := b.openTil[routerID]
	if !ok {
		return false
	}
	if b.now().Before(until) {
		return true
	}
	delete(b.openTil, routerID)
	return false
}

// Trip opens the breaker for routerID for one cooldown period.
func (b *Breaker) Trip(routerID string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.openTil[routerID] = b.now().Add(b.cooldown)
}
