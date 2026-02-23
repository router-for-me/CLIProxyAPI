package usage

import (
	"testing"
)

func TestQuotaEnforcerAllowsRequestWithinQuota(t *testing.T) {
	quota := &QuotaLimit{
		MaxTokensPerDay: 100000,
		MaxCostPerDay:   10.0,
	}
	enforcer := NewQuotaEnforcer(quota)

	if !enforcer.CheckQuota(1000, 0.10) {
		t.Error("request should be allowed within quota")
	}
}

func TestQuotaEnforcerBlocksRequestWhenTokenQuotaExhausted(t *testing.T) {
	quota := &QuotaLimit{
		MaxTokensPerDay: 100000,
		MaxCostPerDay:   10.0,
	}
	enforcer := NewQuotaEnforcer(quota)

	enforcer.RecordUsage(&UsageRecord{TokensUsed: 99000, CostUsed: 5.0})

	// 2000 more tokens would exceed 100000
	if enforcer.CheckQuota(2000, 0.01) {
		t.Error("request should be blocked when token quota would be exceeded")
	}
}

func TestQuotaEnforcerBlocksRequestWhenCostQuotaExhausted(t *testing.T) {
	quota := &QuotaLimit{
		MaxTokensPerDay: 100000,
		MaxCostPerDay:   10.0,
	}
	enforcer := NewQuotaEnforcer(quota)

	enforcer.RecordUsage(&UsageRecord{TokensUsed: 1000, CostUsed: 9.95})

	// 0.10 more cost would exceed 10.0
	if enforcer.CheckQuota(100, 0.10) {
		t.Error("request should be blocked when cost quota would be exceeded")
	}
}

func TestQuotaEnforcerTracksAccumulatedUsage(t *testing.T) {
	quota := &QuotaLimit{
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
}
