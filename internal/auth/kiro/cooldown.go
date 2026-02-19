package kiro

import (
	"sync"
	"time"
)

const (
	CooldownReason429         = "rate_limit_exceeded"
	CooldownReasonSuspended   = "account_suspended"
	CooldownReasonQuotaExhausted = "quota_exhausted"

	DefaultShortCooldown = 1 * time.Minute
	MaxShortCooldown     = 5 * time.Minute
	LongCooldown         = 24 * time.Hour
)

type CooldownManager struct {
	mu        sync.RWMutex
	cooldowns map[string]time.Time
	reasons   map[string]string
}

func NewCooldownManager() *CooldownManager {
	return &CooldownManager{
		cooldowns: make(map[string]time.Time),
		reasons:   make(map[string]string),
	}
}

func (cm *CooldownManager) SetCooldown(tokenKey string, duration time.Duration, reason string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cooldowns[tokenKey] = time.Now().Add(duration)
	cm.reasons[tokenKey] = reason
}

func (cm *CooldownManager) IsInCooldown(tokenKey string) bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	endTime, exists := cm.cooldowns[tokenKey]
	if !exists {
		return false
	}
	return time.Now().Before(endTime)
}

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

func (cm *CooldownManager) GetCooldownReason(tokenKey string) string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.reasons[tokenKey]
}

func (cm *CooldownManager) ClearCooldown(tokenKey string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	delete(cm.cooldowns, tokenKey)
	delete(cm.reasons, tokenKey)
}

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

func CalculateCooldownFor429(retryCount int) time.Duration {
	duration := DefaultShortCooldown * time.Duration(1<<retryCount)
	if duration > MaxShortCooldown {
		return MaxShortCooldown
	}
	return duration
}

func CalculateCooldownUntilNextDay() time.Duration {
	now := time.Now()
	nextDay := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
	return time.Until(nextDay)
}
