package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestEnforceAPIKeyMonthlyQuota_BlocksWhenExceeded(t *testing.T) {
	apiKey := "quota-test-client-key-enforce"

	h := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		APIKeyQuotas: sdkconfig.APIKeyQuotaConfig{
			Enabled: true,
			MonthlyTokenLimits: []sdkconfig.APIKeyMonthlyModelTokenLimit{
				{APIKey: apiKey, Model: "claude-*", Limit: 1000},
			},
		},
	}, nil)

	ginCtx, _ := gin.CreateTestContext(nil)
	ginCtx.Set("apiKey", apiKey)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	h.Cfg.APIKeyQuotas.Update(func(q *sdkconfig.APIKeyQuotaConfig) {
		q.Enabled = true
		q.MonthlyTokenLimits = []sdkconfig.APIKeyMonthlyModelTokenLimit{{
			APIKey: apiKey,
			Model:  "claude-*",
			Limit:  1000,
		}}
	})

	usage.GetRequestStatistics().MergeSnapshot(usage.StatisticsSnapshot{
		APIs: map[string]usage.APISnapshot{
			apiKey: {
				Models: map[string]usage.ModelSnapshot{
					"claude-sonnet-4-5": {
						Details: []usage.RequestDetail{{
							Timestamp: time.Now().UTC(),
							Tokens:    usage.TokenStats{TotalTokens: 1000},
						}},
					},
				},
			},
		},
	})

	errMsg := h.enforceAPIKeyMonthlyQuota(ctx, "claude-sonnet-4-5")
	if errMsg == nil {
		t.Fatalf("expected quota enforcement error")
	}
	if errMsg.StatusCode != 403 {
		t.Fatalf("status code = %d, want 403", errMsg.StatusCode)
	}
}
