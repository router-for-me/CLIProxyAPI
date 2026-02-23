// Package usage provides provider-level metrics for OpenRouter-style routing.
// quota_enforcer.go implements daily quota enforcement for token count and cost.
//
// Ported from thegent/src/thegent/integrations/connector_quota.py.
package usage

import (
	"context"
	"sync"
	"time"
)

// QuotaEnforcer tracks daily usage and blocks requests that would exceed configured limits.
//
// Thread-safe: uses RWMutex for concurrent reads and exclusive writes.
// Daily window resets automatically when the reset timestamp is reached.
type QuotaEnforcer struct {
	quota   *QuotaLimit
	current *Usage
	mu      sync.RWMutex
	resetAt time.Time
}

// NewQuotaEnforcer creates a QuotaEnforcer with a 24-hour rolling window.
func NewQuotaEnforcer(quota *QuotaLimit) *QuotaEnforcer {
	return &QuotaEnforcer{
		quota:   quota,
		current: &Usage{},
		resetAt: time.Now().Add(24 * time.Hour),
	}
}

// RecordUsage accumulates observed usage after a successful request completes.
func (e *QuotaEnforcer) RecordUsage(_ context.Context, usage *Usage) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.maybeResetLocked()
	e.current.TokensUsed += usage.TokensUsed
	e.current.CostUsed += usage.CostUsed
	return nil
}

// CheckQuota returns (true, nil) when the request is within quota, (false, nil) when
// it would exceed a limit. An error is returned only for internal failures.
//
// The check uses the accumulated usage at the time of the call. If the daily window
// has expired, it is reset before checking.
//
// Token estimation: 1 message character ≈ 0.25 tokens (rough proxy when exact counts
// are unavailable). Cost estimation is omitted (0) when not provided.
func (e *QuotaEnforcer) CheckQuota(_ context.Context, req *QuotaCheckRequest) (bool, error) {
	e.mu.Lock()
	e.maybeResetLocked()
	tokensUsed := e.current.TokensUsed
	costUsed := e.current.CostUsed
	e.mu.Unlock()

	if e.quota.MaxTokensPerDay > 0 {
		if tokensUsed+req.EstimatedTokens > e.quota.MaxTokensPerDay {
			return false, nil
		}
	}
	if e.quota.MaxCostPerDay > 0 {
		if costUsed+req.EstimatedCost > e.quota.MaxCostPerDay {
			return false, nil
		}
	}

	return true, nil
}

// maybeResetLocked resets accumulated usage when the daily window has elapsed.
// Caller must hold e.mu (write lock).
func (e *QuotaEnforcer) maybeResetLocked() {
	if time.Now().After(e.resetAt) {
		e.current = &Usage{}
		e.resetAt = time.Now().Add(24 * time.Hour)
	}
}
