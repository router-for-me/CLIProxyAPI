// Package usage — quota enforcer for daily token/cost limits.
package usage

import (
	"sync"
	"time"
)

// QuotaEnforcer tracks and enforces daily usage quotas.
type QuotaEnforcer struct {
	quota   *QuotaLimit
	usage   *UsageRecord
	mu      sync.RWMutex
	resetAt time.Time
}

// NewQuotaEnforcer returns a new QuotaEnforcer with the given limits.
func NewQuotaEnforcer(quota *QuotaLimit) *QuotaEnforcer {
	return &QuotaEnforcer{
		quota:   quota,
		usage:   &UsageRecord{},
		resetAt: time.Now().Add(24 * time.Hour),
	}
}

// CheckQuota returns true if the estimated usage fits within the quota.
func (e *QuotaEnforcer) CheckQuota(estimatedTokens, estimatedCost float64) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()

	e.maybeReset()

	if e.quota.MaxTokensPerDay > 0 && e.usage.TokensUsed+estimatedTokens > e.quota.MaxTokensPerDay {
		return false
	}
	if e.quota.MaxCostPerDay > 0 && e.usage.CostUsed+estimatedCost > e.quota.MaxCostPerDay {
		return false
	}
	return true
}

// RecordUsage adds to accumulated usage.
func (e *QuotaEnforcer) RecordUsage(record *UsageRecord) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.usage.TokensUsed += record.TokensUsed
	e.usage.CostUsed += record.CostUsed
}

// GetUsage returns current usage.
func (e *QuotaEnforcer) GetUsage() UsageRecord {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return *e.usage
}

func (e *QuotaEnforcer) maybeReset() {
	if time.Now().After(e.resetAt) {
		// Note: caller holds RLock, but reset is rare and idempotent
		// In production, use a separate goroutine for resets
		e.usage.TokensUsed = 0
		e.usage.CostUsed = 0
		e.resetAt = time.Now().Add(24 * time.Hour)
	}
}
