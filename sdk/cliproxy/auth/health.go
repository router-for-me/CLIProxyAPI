package auth

import (
	"sync"
	"time"
)

// HealthStatus represents the health state of an account.
type HealthStatus string

const (
	// HealthAvailable indicates the account is ready for use.
	HealthAvailable HealthStatus = "available"
	// HealthRateLimited indicates the account hit a 429 and is in backoff.
	HealthRateLimited HealthStatus = "rate_limited"
	// HealthErroring indicates repeated failures on this account.
	HealthErroring HealthStatus = "erroring"
)

// DefaultBackoffs for rate limiting and error recovery.
const (
	InitialBackoff       = 30 * time.Second
	MaxBackoff           = 5 * time.Minute
	ErrorRecoveryTimeout = 30 * time.Minute
	ConsecutiveFailures  = 3 // failures before marking as erroring
)

// AccountHealth tracks the health state of a single account.
type AccountHealth struct {
	Status             HealthStatus
	LastError          error
	BackoffUntil       time.Time
	ConsecutiveErrors  int
	LastRateLimitedAt  time.Time
	BackoffLevel       int // for exponential backoff: 0, 1, 2, ...
}

// IsAvailable returns true if the account can be used for requests.
func (ah *AccountHealth) IsAvailable(now time.Time) bool {
	if ah == nil {
		return true
	}
	switch ah.Status {
	case HealthAvailable:
		return true
	case HealthRateLimited, HealthErroring:
		return now.After(ah.BackoffUntil)
	default:
		return true
	}
}

// HealthTracker manages health state for multiple accounts.
type HealthTracker struct {
	mu     sync.RWMutex
	health map[string]*AccountHealth
}

// NewHealthTracker creates a new health tracker.
func NewHealthTracker() *HealthTracker {
	return &HealthTracker{
		health: make(map[string]*AccountHealth),
	}
}

// GetHealth returns the health state for an account.
func (ht *HealthTracker) GetHealth(accountID string) *AccountHealth {
	ht.mu.RLock()
	defer ht.mu.RUnlock()
	return ht.health[accountID]
}

// IsAvailable checks if an account is available for use.
func (ht *HealthTracker) IsAvailable(accountID string) bool {
	ht.mu.RLock()
	h := ht.health[accountID]
	ht.mu.RUnlock()
	return h.IsAvailable(time.Now())
}

// MarkRateLimited marks an account as rate limited with exponential backoff.
func (ht *HealthTracker) MarkRateLimited(accountID string, retryAfter *time.Duration) {
	ht.mu.Lock()
	defer ht.mu.Unlock()

	h := ht.getOrCreateLocked(accountID)
	h.Status = HealthRateLimited
	h.LastRateLimitedAt = time.Now()

	var backoff time.Duration
	if retryAfter != nil && *retryAfter > 0 {
		backoff = *retryAfter
	} else {
		backoff = calculateBackoff(h.BackoffLevel)
		h.BackoffLevel++
	}

	if backoff > MaxBackoff {
		backoff = MaxBackoff
	}
	h.BackoffUntil = time.Now().Add(backoff)
}

// MarkError records a failure for an account.
func (ht *HealthTracker) MarkError(accountID string, err error) {
	ht.mu.Lock()
	defer ht.mu.Unlock()

	h := ht.getOrCreateLocked(accountID)
	h.ConsecutiveErrors++
	h.LastError = err

	if h.ConsecutiveErrors >= ConsecutiveFailures {
		h.Status = HealthErroring
		h.BackoffUntil = time.Now().Add(ErrorRecoveryTimeout)
	}
}

// MarkSuccess records a successful request, resetting error counts.
func (ht *HealthTracker) MarkSuccess(accountID string) {
	ht.mu.Lock()
	defer ht.mu.Unlock()

	h := ht.health[accountID]
	if h == nil {
		return
	}

	h.Status = HealthAvailable
	h.ConsecutiveErrors = 0
	h.LastError = nil
	h.BackoffLevel = 0
	h.BackoffUntil = time.Time{}
}

// Reset manually resets an account to available state.
func (ht *HealthTracker) Reset(accountID string) {
	ht.mu.Lock()
	defer ht.mu.Unlock()

	h := ht.health[accountID]
	if h == nil {
		return
	}

	h.Status = HealthAvailable
	h.ConsecutiveErrors = 0
	h.LastError = nil
	h.BackoffLevel = 0
	h.BackoffUntil = time.Time{}
}

// ResetAll resets all accounts to available state.
func (ht *HealthTracker) ResetAll() {
	ht.mu.Lock()
	defer ht.mu.Unlock()
	ht.health = make(map[string]*AccountHealth)
}

// GetAvailableAccounts returns account IDs that are currently available.
func (ht *HealthTracker) GetAvailableAccounts(accountIDs []string) []string {
	ht.mu.RLock()
	defer ht.mu.RUnlock()

	now := time.Now()
	available := make([]string, 0, len(accountIDs))
	for _, id := range accountIDs {
		h := ht.health[id]
		if h == nil || h.IsAvailable(now) {
			available = append(available, id)
		}
	}
	return available
}

// GetLeastRecentlyLimited returns the account that was rate limited longest ago.
// Used as fallback when all accounts are rate limited.
func (ht *HealthTracker) GetLeastRecentlyLimited(accountIDs []string) string {
	ht.mu.RLock()
	defer ht.mu.RUnlock()

	var oldest string
	var oldestTime time.Time

	for _, id := range accountIDs {
		h := ht.health[id]
		if h == nil {
			return id
		}
		if oldest == "" || h.BackoffUntil.Before(oldestTime) {
			oldest = id
			oldestTime = h.BackoffUntil
		}
	}
	return oldest
}

func (ht *HealthTracker) getOrCreateLocked(accountID string) *AccountHealth {
	h := ht.health[accountID]
	if h == nil {
		h = &AccountHealth{Status: HealthAvailable}
		ht.health[accountID] = h
	}
	return h
}

func calculateBackoff(level int) time.Duration {
	// Exponential backoff: 30s, 60s, 120s, 240s, max 5min
	backoff := InitialBackoff
	for i := 0; i < level; i++ {
		backoff *= 2
		if backoff > MaxBackoff {
			return MaxBackoff
		}
	}
	return backoff
}
