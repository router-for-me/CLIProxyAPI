package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestKiroCreditSummaryFromUsage(t *testing.T) {
	summary := kiroCreditSummaryFromUsage(&kiro.UsageQuotaResponse{
		UsageBreakdownList: []kiro.UsageBreakdownExtended{{
			ResourceType:              "AGENTIC_REQUEST",
			CurrentUsageWithPrecision: 12.34567,
			UsageLimitWithPrecision:   100,
			FreeTrialInfo: &kiro.FreeTrialInfoExtended{
				CurrentUsageWithPrecision: 2.5,
				UsageLimitWithPrecision:   10,
			},
		}},
		SubscriptionInfo: &kiro.SubscriptionInfo{SubscriptionTitle: "Kiro Pro"},
	})

	if summary.CreditUsed != 14.8457 {
		t.Fatalf("CreditUsed = %v, want 14.8457", summary.CreditUsed)
	}
	if summary.CreditTotal != 110 {
		t.Fatalf("CreditTotal = %v, want 110", summary.CreditTotal)
	}
	if summary.CreditRemaining != 95.1543 {
		t.Fatalf("CreditRemaining = %v, want 95.1543", summary.CreditRemaining)
	}
	if summary.ResourceType != "AGENTIC_REQUEST" {
		t.Fatalf("ResourceType = %q, want AGENTIC_REQUEST", summary.ResourceType)
	}
	if summary.SubscriptionTitle != "Kiro Pro" {
		t.Fatalf("SubscriptionTitle = %q, want Kiro Pro", summary.SubscriptionTitle)
	}
}

func TestKiroQuotaAuthRefreshDue(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)

	if !kiroQuotaAuthRefreshDue(&coreauth.Auth{NextRefreshAfter: now}, now) {
		t.Fatal("refresh due when NextRefreshAfter has arrived")
	}
	if kiroQuotaAuthRefreshDue(&coreauth.Auth{
		Metadata: map[string]any{"expires_at": now.Add(2 * time.Minute).Format(time.RFC3339)},
	}, now) {
		t.Fatal("refresh should not be due when token expires outside lead window")
	}
	if !kiroQuotaAuthRefreshDue(&coreauth.Auth{
		Metadata: map[string]any{"expires_at": now.Add(30 * time.Second).Format(time.RFC3339)},
	}, now) {
		t.Fatal("refresh due when token expires inside lead window")
	}
}

func TestGetKiroQuota(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	previous := getKiroCreditSummaryForAuth
	getKiroCreditSummaryForAuth = func(_ context.Context, _ *Handler, auth *coreauth.Auth) (*kiroCreditSummary, error) {
		if auth == nil || auth.Provider != "kiro" {
			t.Fatalf("auth = %#v, want Kiro auth", auth)
		}
		return &kiroCreditSummary{CreditUsed: 3, CreditTotal: 20, CreditRemaining: 17}, nil
	}
	defer func() { getKiroCreditSummaryForAuth = previous }()

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "kiro-test.json",
		Provider: "kiro",
		FileName: "kiro-test.json",
		Metadata: map[string]any{"type": "kiro", "access_token": "token"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	auth.EnsureIndex()

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/kiro-quota?auth_index="+auth.Index, nil)

	h.GetKiroQuota(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload kiroCreditSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if payload.CreditUsed != 3 || payload.CreditTotal != 20 || payload.CreditRemaining != 17 {
		t.Fatalf("payload = %+v, want used=3 total=20 remaining=17", payload)
	}
}
