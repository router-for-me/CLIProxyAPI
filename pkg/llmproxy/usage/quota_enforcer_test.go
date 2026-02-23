package usage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// @trace FR-QUOTA-001 FR-QUOTA-002

func TestQuotaEnforcerAllowsRequestWithinQuota(t *testing.T) {
	quota := &QuotaLimit{
		MaxTokensPerDay: 100000,
		MaxCostPerDay:   10.0,
	}

	enforcer := NewQuotaEnforcer(quota)

	allowed, err := enforcer.CheckQuota(context.Background(), &QuotaCheckRequest{
		EstimatedTokens: 1000,
		EstimatedCost:   0.01,
	})

	require.NoError(t, err)
	assert.True(t, allowed, "request should be allowed within quota")
}

func TestQuotaEnforcerBlocksRequestWhenTokenQuotaExhausted(t *testing.T) {
	quota := &QuotaLimit{
		MaxTokensPerDay: 100000,
		MaxCostPerDay:   10.0,
	}

	enforcer := NewQuotaEnforcer(quota)

	// Record usage close to the limit.
	err := enforcer.RecordUsage(context.Background(), &Usage{
		TokensUsed: 99000,
		CostUsed:   0.0,
	})
	require.NoError(t, err)

	// Request that would exceed token quota.
	allowed, err := enforcer.CheckQuota(context.Background(), &QuotaCheckRequest{
		EstimatedTokens: 2000, // 99000 + 2000 = 101000 > 100000
		EstimatedCost:   0.01,
	})

	require.NoError(t, err)
	assert.False(t, allowed, "request should be blocked when token quota exhausted")
}

func TestQuotaEnforcerBlocksRequestWhenCostQuotaExhausted(t *testing.T) {
	quota := &QuotaLimit{
		MaxTokensPerDay: 100000,
		MaxCostPerDay:   10.0,
	}

	enforcer := NewQuotaEnforcer(quota)

	err := enforcer.RecordUsage(context.Background(), &Usage{
		TokensUsed: 0,
		CostUsed:   9.90,
	})
	require.NoError(t, err)

	// Request that would exceed cost quota.
	allowed, err := enforcer.CheckQuota(context.Background(), &QuotaCheckRequest{
		EstimatedTokens: 500,
		EstimatedCost:   0.20, // 9.90 + 0.20 = 10.10 > 10.0
	})

	require.NoError(t, err)
	assert.False(t, allowed, "request should be blocked when cost quota exhausted")
}

func TestQuotaEnforcerTracksAccumulatedUsage(t *testing.T) {
	quota := &QuotaLimit{
		MaxTokensPerDay: 100,
		MaxCostPerDay:   1.0,
	}

	enforcer := NewQuotaEnforcer(quota)

	// Record in two batches.
	require.NoError(t, enforcer.RecordUsage(context.Background(), &Usage{TokensUsed: 40}))
	require.NoError(t, enforcer.RecordUsage(context.Background(), &Usage{TokensUsed: 40}))

	// 40+40=80 used; 30 more would exceed 100.
	allowed, err := enforcer.CheckQuota(context.Background(), &QuotaCheckRequest{
		EstimatedTokens: 30,
	})
	require.NoError(t, err)
	assert.False(t, allowed)

	// But 19 more is fine (80+19=99 <= 100).
	allowed, err = enforcer.CheckQuota(context.Background(), &QuotaCheckRequest{
		EstimatedTokens: 19,
	})
	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestQuotaEnforcerAllowsWhenExactlyAtLimit(t *testing.T) {
	quota := &QuotaLimit{MaxTokensPerDay: 100}
	enforcer := NewQuotaEnforcer(quota)

	require.NoError(t, enforcer.RecordUsage(context.Background(), &Usage{TokensUsed: 50}))

	// Exactly 50 more = 100, which equals the cap (not exceeds).
	allowed, err := enforcer.CheckQuota(context.Background(), &QuotaCheckRequest{
		EstimatedTokens: 50,
	})
	require.NoError(t, err)
	assert.True(t, allowed, "exactly at limit should be allowed")
}
