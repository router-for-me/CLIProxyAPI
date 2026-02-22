package codex

import (
	"sync"
	"time"
)

const (
	CooldownReason429            = "usage_limit_reached"
	CooldownReasonSuspended      = "account_suspended"
	CooldownReasonQuotaExhausted = "quota_exhausted"

	DefaultShortCooldown = 1 * time.Minute
	MaxShortCooldown     = 5 * time.Minute
	LongCooldown         = 24 * time.Hour
)

var (
	globalCooldownManager     *CooldownManager
	globalCooldownManagerOnce sync.Once
	cooldownStopCh            chan struct{}
)

// CooldownManager tracks cooldown state for Codex auth tokens.
type CooldownManager struct {
	mu        sync.RWMutex
	cooldowns map[string]time.Time
	reasons   map[string]string
}

// GetGlobalCooldownManager returns the singleton CooldownManager instance.
func GetGlobalCooldownManager() *CooldownManager {
	globalCooldownManagerOnce.Do(func() {
		globalCooldownManager = NewCooldownManager()
		cooldownStopCh = make(chan struct{})
		go globalCooldownManager.StartCleanupRoutine(5*time.Minute, cooldownStopCh)
	})
	return globalCooldownManager
}

// ShutdownCooldownManager stops the cooldown cleanup routine.
func ShutdownCooldownManager() {
	if cooldownStopCh != nil {
		close(cooldownStopCh)
	}
}

// NewCooldownManager creates a new CooldownManager.
func NewCooldownManager() *CooldownManager {
	return &CooldownManager{
		cooldowns: make(map[string]time.Time),
		reasons:   make(map[string]string),
	}
}

// SetCooldown sets a cooldown for the given token key.
func (cm *CooldownManager) SetCooldown(tokenKey string, duration time.Duration, reason string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cooldowns[tokenKey] = time.Now().Add(duration)
	cm.reasons[tokenKey] = reason
}

// IsInCooldown checks if the token is currently in cooldown.
func (cm *CooldownManager) IsInCooldown(tokenKey string) bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	endTime, exists := cm.cooldowns[tokenKey]
	if !exists {
		return false
	}
	return time.Now().Before(endTime)
}

// GetRemainingCooldown returns the remaining cooldown duration for the token.
func (cm *CooldownManager) GetRemainingCooldown(tokenKey string) time.Duration {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	endTime, exists := cm.cooldowns[tokenKey]
	if !exists {
		return 0
	}
	remaining := time.Until(endTime)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// GetCooldownReason returns the reason for the cooldown.
func (cm *CooldownManager) GetCooldownReason(tokenKey string) string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.reasons[tokenKey]
}

// ClearCooldown clears the cooldown for the given token.
func (cm *CooldownManager) ClearCooldown(tokenKey string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	delete(cm.cooldowns, tokenKey)
	delete(cm.reasons, tokenKey)
}

// CleanupExpired removes expired cooldowns.
func (cm *CooldownManager) CleanupExpired() {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	now := time.Now()
	for tokenKey, endTime := range cm.cooldowns {
		if now.After(endTime) {
			delete(cm.cooldowns, tokenKey)
			delete(cm.reasons, tokenKey)
		}
	}
}

// StartCleanupRoutine starts a periodic cleanup of expired cooldowns.
func (cm *CooldownManager) StartCleanupRoutine(interval time.Duration, stopCh <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			cm.CleanupExpired()
		case <-stopCh:
			return
		}
	}
}

// CalculateCooldownFor429 calculates the cooldown duration for a 429 error.
// If resetDuration is provided (from resets_at/resets_in_seconds), it uses that.
// Otherwise, it uses exponential backoff.
func CalculateCooldownFor429(retryCount int, resetDuration time.Duration) time.Duration {
	// If we have an explicit reset duration from the server, use it
	if resetDuration > 0 {
		// Cap at 24 hours to prevent excessive cooldowns
		if resetDuration > LongCooldown {
			return LongCooldown
		}
		return resetDuration
	}
	// Otherwise use exponential backoff
	duration := DefaultShortCooldown * time.Duration(1<<retryCount)
	if duration > MaxShortCooldown {
		return MaxShortCooldown
	}
	return duration
}

// CalculateCooldownUntilNextDay calculates the duration until midnight.
func CalculateCooldownUntilNextDay() time.Duration {
	now := time.Now()
	nextDay := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
	return time.Until(nextDay)
}
