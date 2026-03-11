package handlers

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestEvaluateAPIKeyQuota_Disabled(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{}
	stats := usage.NewRequestStatistics()
	now := time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC)

	result := evaluateAPIKeyQuota(cfg, stats, "client-key", "claude-sonnet-4-5", now)
	if result.Blocked {
		t.Fatalf("expected quota check to allow when disabled")
	}
}

func TestEvaluateAPIKeyQuota_ExcludedModel(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		APIKeyQuotas: sdkconfig.APIKeyQuotaConfig{
			Enabled:              true,
			ExcludeModelPatterns: []string{"*haiku*", "*flash*"},
			MonthlyTokenLimits: []sdkconfig.APIKeyMonthlyModelTokenLimit{
				{APIKey: "*", Model: "*", Limit: 100},
			},
		},
	}
	stats := usage.NewRequestStatistics()
	now := time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC)

	result := evaluateAPIKeyQuota(cfg, stats, "client-key", "claude-3-5-haiku", now)
	if result.Blocked {
		t.Fatalf("expected excluded model to bypass quota")
	}
}

func TestEvaluateAPIKeyQuota_BlocksWhenMonthlyLimitReached(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		APIKeyQuotas: sdkconfig.APIKeyQuotaConfig{
			Enabled: true,
			MonthlyTokenLimits: []sdkconfig.APIKeyMonthlyModelTokenLimit{
				{APIKey: "client-key", Model: "claude-*", Limit: 1000},
			},
		},
	}
	stats := usage.NewRequestStatistics()
	stats.MergeSnapshot(usage.StatisticsSnapshot{
		APIs: map[string]usage.APISnapshot{
			"client-key": {
				Models: map[string]usage.ModelSnapshot{
					"claude-sonnet-4-5": {
						Details: []usage.RequestDetail{
							{Timestamp: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Tokens: usage.TokenStats{TotalTokens: 600}},
							{Timestamp: time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC), Tokens: usage.TokenStats{TotalTokens: 400}},
						},
					},
				},
			},
		},
	})
	now := time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC)

	result := evaluateAPIKeyQuota(cfg, stats, "client-key", "claude-sonnet-4-5", now)
	if !result.Blocked {
		t.Fatalf("expected quota to block when monthly limit reached")
	}
	if result.Limit != 1000 {
		t.Fatalf("unexpected limit %d", result.Limit)
	}
	if result.Current != 1000 {
		t.Fatalf("unexpected current %d", result.Current)
	}
}

func TestEvaluateAPIKeyQuota_IgnoresOtherMonths(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		APIKeyQuotas: sdkconfig.APIKeyQuotaConfig{
			Enabled: true,
			MonthlyTokenLimits: []sdkconfig.APIKeyMonthlyModelTokenLimit{
				{APIKey: "client-key", Model: "claude-*", Limit: 1000},
			},
		},
	}
	stats := usage.NewRequestStatistics()
	stats.MergeSnapshot(usage.StatisticsSnapshot{
		APIs: map[string]usage.APISnapshot{
			"client-key": {
				Models: map[string]usage.ModelSnapshot{
					"claude-sonnet-4-5": {
						Details: []usage.RequestDetail{
							{Timestamp: time.Date(2026, 2, 28, 23, 59, 0, 0, time.UTC), Tokens: usage.TokenStats{TotalTokens: 1000}},
						},
					},
				},
			},
		},
	})
	now := time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC)

	result := evaluateAPIKeyQuota(cfg, stats, "client-key", "claude-sonnet-4-5", now)
	if result.Blocked {
		t.Fatalf("expected quota not to block from previous month usage")
	}
}
