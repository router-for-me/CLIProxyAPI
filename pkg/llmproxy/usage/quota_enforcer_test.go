package usage

import (
<<<<<<< HEAD
	"testing"
)

=======
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// @trace FR-QUOTA-001 FR-QUOTA-002

>>>>>>> ci-compile-fix
func TestQuotaEnforcerAllowsRequestWithinQuota(t *testing.T) {
	quota := &QuotaLimit{
		MaxTokensPerDay: 100000,
		MaxCostPerDay:   10.0,
	}
<<<<<<< HEAD
	enforcer := NewQuotaEnforcer(quota)

	if !enforcer.CheckQuota(1000, 0.10) {
		t.Error("request should be allowed within quota")
	}
=======

	enforcer := NewQuotaEnforcer(quota)

	allowed, err := enforcer.CheckQuota(context.Background(), &QuotaCheckRequest{
		EstimatedTokens: 1000,
		EstimatedCost:   0.01,
	})

	require.NoError(t, err)
	assert.True(t, allowed, "request should be allowed within quota")
>>>>>>> ci-compile-fix
}

func TestQuotaEnforcerBlocksRequestWhenTokenQuotaExhausted(t *testing.T) {
	quota := &QuotaLimit{
		MaxTokensPerDay: 100000,
		MaxCostPerDay:   10.0,
	}
<<<<<<< HEAD
	enforcer := NewQuotaEnforcer(quota)

	enforcer.RecordUsage(&UsageRecord{TokensUsed: 99000, CostUsed: 5.0})

	// 2000 more tokens would exceed 100000
	if enforcer.CheckQuota(2000, 0.01) {
		t.Error("request should be blocked when token quota would be exceeded")
	}
=======

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
>>>>>>> ci-compile-fix
}

func TestQuotaEnforcerBlocksRequestWhenCostQuotaExhausted(t *testing.T) {
	quota := &QuotaLimit{
		MaxTokensPerDay: 100000,
		MaxCostPerDay:   10.0,
	}
<<<<<<< HEAD
	enforcer := NewQuotaEnforcer(quota)

	enforcer.RecordUsage(&UsageRecord{TokensUsed: 1000, CostUsed: 9.95})

	// 0.10 more cost would exceed 10.0
	if enforcer.CheckQuota(100, 0.10) {
		t.Error("request should be blocked when cost quota would be exceeded")
	}
=======

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
>>>>>>> ci-compile-fix
}

func TestQuotaEnforcerTracksAccumulatedUsage(t *testing.T) {
	quota := &QuotaLimit{
<<<<<<< HEAD
		MaxTokensPerDay: 100000,
		MaxCostPerDay:   10.0,
	}
	enforcer := NewQuotaEnforcer(quota)

	enforcer.RecordUsage(&UsageRecord{TokensUsed: 5000, CostUsed: 0.50})
	enforcer.RecordUsage(&UsageRecord{TokensUsed: 3000, CostUsed: 0.30})

	usage := enforcer.GetUsage()
	if usage.TokensUsed != 8000 {
		t.Errorf("expected 8000 tokens, got %.0f", usage.TokensUsed)
	}
	if usage.CostUsed != 0.80 {
		t.Errorf("expected 0.80 cost, got %.2f", usage.CostUsed)
	}
}

func TestQuotaEnforcerAllowsWhenExactlyAtLimit(t *testing.T) {
	quota := &QuotaLimit{
		MaxTokensPerDay: 100000,
		MaxCostPerDay:   10.0,
	}
	enforcer := NewQuotaEnforcer(quota)

	enforcer.RecordUsage(&UsageRecord{TokensUsed: 99000, CostUsed: 9.0})

	// Exactly at limit
	if !enforcer.CheckQuota(1000, 1.0) {
		t.Error("request should be allowed when exactly at limit")
	}
=======
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
>>>>>>> ci-compile-fix
}
