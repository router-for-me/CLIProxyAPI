package auth

import (
	"sync"
	"time"
)

// QuotaTracker provides thread-safe quota state updates for Auth records.
// It uses sync.RWMutex to ensure atomic multi-field updates when concurrent
// API responses arrive, preventing race conditions in quota tracking.
//
// The tracker follows the singleton pattern (similar to usageReporter) and
// should be accessed via GetTracker().
type QuotaTracker struct {
	mu sync.RWMutex
}

var (
	quotaTrackerInstance *QuotaTracker
	quotaTrackerOnce     sync.Once
)

// GetTracker returns the global QuotaTracker singleton instance.
// The instance is initialized exactly once using sync.Once, making it safe
// for concurrent access from multiple goroutines.
func GetTracker() *QuotaTracker {
	quotaTrackerOnce.Do(func() {
		quotaTrackerInstance = &QuotaTracker{}
	})
	return quotaTrackerInstance
}

// Update applies quota information from a provider API response to an Auth record.
// It performs atomic multi-field updates under a write lock to prevent race conditions
// when concurrent requests complete.
//
// The update logic handles:
// - Updating Used, Limit, Remaining fields when quota data is available
// - Setting Exceeded status from 429 rate limit responses
// - Incrementing BackoffLevel when quota is exceeded
// - Resetting Exceeded and BackoffLevel when quota recovers (past NextRecoverAt)
// - Updating NextRecoverAt from ResetAt timestamp
// - Updating Auth.UpdatedAt timestamp
//
// Parameters are checked defensively - the function returns early if auth or info is nil.
//
// Thread-safety: This method uses qt.mu.Lock() for exclusive write access during updates.
func (qt *QuotaTracker) Update(auth *Auth, info *QuotaInfo) {
	// Defensive nil checks - return early if parameters are invalid
	if auth == nil || info == nil {
		return
	}

	// Acquire write lock for atomic multi-field update
	qt.mu.Lock()
	defer qt.mu.Unlock()

	// Update quota fields if values are present (> 0)
	// Providers may omit certain fields, so we only update when data is available
	if info.Used > 0 {
		auth.Quota.Used = info.Used
	}
	if info.Limit > 0 {
		auth.Quota.Limit = info.Limit
	}
	if info.Remaining > 0 {
		auth.Quota.Remaining = info.Remaining
	}

	// Update reset time if provided
	if !info.ResetAt.IsZero() {
		auth.Quota.NextRecoverAt = info.ResetAt
	}

	// Handle quota exceeded state
	if info.Exceeded {
		// Mark quota as exceeded
		auth.Quota.Exceeded = true
		// Increment backoff level for progressive cooldown
		auth.Quota.BackoffLevel++
	} else if auth.Quota.Exceeded && !auth.Quota.NextRecoverAt.IsZero() {
		// Check if quota has recovered (we're past the recovery time)
		if time.Now().After(auth.Quota.NextRecoverAt) {
			// Reset exceeded state and backoff level
			auth.Quota.Exceeded = false
			auth.Quota.BackoffLevel = 0
		}
	}

	// Update auth record timestamp to track last quota update
	auth.UpdatedAt = time.Now()
}
