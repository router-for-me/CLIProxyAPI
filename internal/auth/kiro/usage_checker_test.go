package kiro

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestUsageChecker_CheckUsageJSON(t *testing.T) {
	resetTime := 1800000000.0
	limit := 100.0
	used := 25.0
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/getUsageLimits" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-access-token" {
			t.Errorf("Authorization header = %q, want Bearer test-access-token", r.Header.Get("Authorization"))
		}

		w.Header().Set("Content-Type", "application/json")
		resp := UsageQuotaResponse{
			NextDateReset: &resetTime,
			SubscriptionInfo: &SubscriptionInfo{
				SubscriptionTitle: "Kiro Pro",
			},
			UsageBreakdownList: []UsageBreakdown{
				{
					DisplayName:               "Agentic Requests",
					ResourceType:              "AGENTIC_REQUEST",
					UsageLimitWithPrecision:   &limit,
					CurrentUsageWithPrecision: &used,
					NextDateReset:             &resetTime,
				},
			},
		}
		_ = cbor.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	checker := &UsageChecker{
		httpClient: mockServer.Client(),
		apiBaseURL: mockServer.URL,
	}

	tokenData := &KiroTokenData{
		AccessToken: "test-access-token",
		ProfileArn:  "arn:aws:codewhisperer:us-east-1:123456789012:profile/P123",
	}

	usageResp, err := checker.CheckUsage(context.Background(), tokenData)
	if err != nil {
		t.Fatalf("CheckUsage failed: %v", err)
	}

	if usageResp.SubscriptionInfo == nil || usageResp.SubscriptionInfo.SubscriptionTitle != "Kiro Pro" {
		t.Errorf("SubscriptionTitle = %v, want Kiro Pro", usageResp.SubscriptionInfo)
	}

	if len(usageResp.UsageBreakdownList) != 1 {
		t.Fatalf("UsageBreakdownList length = %d, want 1", len(usageResp.UsageBreakdownList))
	}

	item := usageResp.UsageBreakdownList[0]
	if item.Limit() != 100.0 || item.Used() != 25.0 {
		t.Errorf("Limit = %f, Used = %f, want 100.0 and 25.0", item.Limit(), item.Used())
	}
}

func TestNormalizeQuotaResponse(t *testing.T) {
	resetTime := 1800000000.0
	limit := 500.0
	used := 150.0
	trialLimit := 50.0
	trialUsed := 10.0

	resp := &UsageQuotaResponse{
		NextDateReset: &resetTime,
		SubscriptionInfo: &SubscriptionInfo{
			SubscriptionTitle: "Kiro Unlimited",
		},
		UsageBreakdownList: []UsageBreakdown{
			{
				DisplayName:               "Agentic Requests",
				ResourceType:              "AGENTIC_REQUEST",
				UsageLimitWithPrecision:   &limit,
				CurrentUsageWithPrecision: &used,
				NextDateReset:             &resetTime,
				FreeTrialInfo: &FreeTrialInfoData{
					UsageLimitWithPrecision:   &trialLimit,
					CurrentUsageWithPrecision: &trialUsed,
				},
			},
		},
	}

	quota := NormalizeQuotaResponse(resp)
	if quota.Subscription == nil || quota.Subscription.Plan != "Kiro Unlimited" {
		t.Fatalf("Subscription Plan = %v, want Kiro Unlimited", quota.Subscription)
	}

	if len(quota.Groups) != 1 {
		t.Fatalf("Groups length = %d, want 1", len(quota.Groups))
	}

	bucket := quota.Groups[0].Buckets[0]
	expectedTotalLimit := 550.0
	expectedTotalUsed := 160.0
	expectedRemaining := expectedTotalLimit - expectedTotalUsed
	expectedFraction := expectedRemaining / expectedTotalLimit

	if bucket.RemainingFraction != expectedFraction {
		t.Errorf("RemainingFraction = %f, want %f", bucket.RemainingFraction, expectedFraction)
	}

	expectedResetTime := time.Unix(int64(resetTime), 0).UTC().Format(time.RFC3339)
	if bucket.ResetTime != expectedResetTime {
		t.Errorf("ResetTime = %q, want %q", bucket.ResetTime, expectedResetTime)
	}

	if len(quota.Summary) != 3 {
		t.Fatalf("Summary metrics count = %d, want 3", len(quota.Summary))
	}
}

func TestFetchNormalizedQuota_ExhaustionFlag(t *testing.T) {
	resetTime := 1800000000.0
	limit := 100.0
	used := 100.0

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/cbor")
		resp := UsageQuotaResponse{
			NextDateReset: &resetTime,
			UsageBreakdownList: []UsageBreakdown{
				{
					DisplayName:               "Agentic Requests",
					UsageLimitWithPrecision:   &limit,
					CurrentUsageWithPrecision: &used,
				},
			},
		}
		_ = cbor.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	auth := &coreauth.Auth{
		ID:       "kiro-test",
		Provider: "kiro",
		Metadata: map[string]any{
			"access_token": "token-xyz",
			"profile_arn":  "arn:aws:codewhisperer:us-east-1:123456789012:profile/P123",
		},
	}

	cfg := &config.Config{}
	checker := &UsageChecker{
		httpClient: mockServer.Client(),
		cfg:        cfg,
		apiBaseURL: mockServer.URL,
	}

	tokenData := TokenDataFromAuth(auth)
	usageResp, err := checker.CheckUsage(context.Background(), tokenData)
	if err != nil {
		t.Fatalf("CheckUsage failed: %v", err)
	}

	normalized := NormalizeQuotaResponse(usageResp)
	if normalized.Groups[0].Buckets[0].RemainingFraction != 0.0 {
		t.Errorf("RemainingFraction = %f, want 0.0", normalized.Groups[0].Buckets[0].RemainingFraction)
	}
}
