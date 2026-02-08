package auth

import (
	"context"
	"math"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// QuotaTracker provides thread-safe quota state updates for Auth records.
// It uses sync.RWMutex to ensure atomic multi-field updates when concurrent
// API responses arrive, preventing race conditions in quota tracking.
//
// The tracker follows the singleton pattern (similar to usageReporter) and
// should be accessed via GetTracker().
type QuotaTracker struct {
	mu              sync.RWMutex
	persistCallback func(ctx context.Context, auth *Auth) error // callback to trigger persistence
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

// SetPersistCallback injects a callback function to trigger persistence when quota state changes significantly.
// The callback is invoked asynchronously in a goroutine to avoid blocking the request path.
// This method should be called during application initialization (e.g., by the watcher).
func (qt *QuotaTracker) SetPersistCallback(callback func(ctx context.Context, auth *Auth) error) {
	qt.mu.Lock()
	defer qt.mu.Unlock()
	qt.persistCallback = callback
}

// shouldPersist determines whether quota state changes are significant enough to warrant persistence.
// Returns true if:
// - Exceeded status changed (critical state transition)
// - Quota percentage dropped by more than 10% (significant usage change)
// Returns false for minor changes to avoid excessive persistence operations.
func (qt *QuotaTracker) shouldPersist(oldQuota, newQuota QuotaState) bool {
	// Always persist exceeded status changes
	if oldQuota.Exceeded != newQuota.Exceeded {
		return true
	}

	// Persist significant quota changes (>10% drop in percentage)
	if oldQuota.Limit > 0 {
		oldPct := float64(oldQuota.Remaining) / float64(oldQuota.Limit)
		newPct := float64(newQuota.Remaining) / float64(newQuota.Limit)
		if math.Abs(oldPct-newPct) > 0.10 {
			return true
		}
	}

	return false
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

	// Capture old quota state before modifications for persistence check
	oldQuota := auth.Quota

	// Limit and Remaining are updated together because Remaining=0 is a valid
	// value (quota exhausted) and must not be skipped.
	if info.Used > 0 {
		auth.Quota.Used = info.Used
	}
	if info.Limit > 0 {
		auth.Quota.Limit = info.Limit
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

	// Trigger async persistence if quota change is significant
	if qt.shouldPersist(oldQuota, auth.Quota) && qt.persistCallback != nil {
		// Clone auth to avoid concurrent modification during async persistence
		authCopy := auth.Clone()
		go func(authCopy *Auth) {
			ctx := context.Background()
			if err := qt.persistCallback(ctx, authCopy); err != nil {
				log.Warnf("Failed to persist quota state: %v", err)
			}
		}(authCopy)
	}
}
