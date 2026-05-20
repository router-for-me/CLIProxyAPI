package management

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	kiroauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kiro"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const kiroQuotaRefreshLead = time.Minute

type kiroCreditSummary struct {
	CreditUsed        float64   `json:"credit_used"`
	CreditTotal       float64   `json:"credit_total"`
	CreditRemaining   float64   `json:"credit_remaining"`
	ResourceType      string    `json:"resource_type,omitempty"`
	SubscriptionTitle string    `json:"subscription_title,omitempty"`
	NextReset         time.Time `json:"next_reset,omitempty"`
}

var getKiroCreditSummaryForAuth = defaultKiroCreditSummaryForAuth

// GetKiroQuota returns Kiro credit usage for one auth file.
func (h *Handler) GetKiroQuota(c *gin.Context) {
	authIndex := strings.TrimSpace(c.Query("auth_index"))
	if authIndex == "" {
		authIndex = strings.TrimSpace(c.Query("authIndex"))
	}
	if authIndex == "" {
		authIndex = strings.TrimSpace(c.Query("AuthIndex"))
	}
	if authIndex == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
		return
	}

	auth := h.authByIndex(authIndex)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "kiro") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth is not a Kiro credential"})
		return
	}

	summary, err := getKiroCreditSummaryForAuth(c.Request.Context(), h, auth)
	if err != nil {
		log.WithError(err).Debug("failed to get Kiro credit usage")
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, summary)
}

func defaultKiroCreditSummaryForAuth(ctx context.Context, h *Handler, auth *coreauth.Auth) (*kiroCreditSummary, error) {
	if h == nil || auth == nil {
		return nil, fmt.Errorf("auth not found")
	}
	auth, _ = refreshKiroQuotaAuth(ctx, h, auth, false)
	tokenData := kiroTokenDataFromAuth(auth)
	if strings.TrimSpace(tokenData.AccessToken) == "" {
		return nil, fmt.Errorf("Kiro access token is missing")
	}

	client := &http.Client{Transport: h.apiCallTransport(auth)}
	checker := kiroauth.NewUsageCheckerWithClient(client)
	usage, err := checker.CheckUsage(ctx, tokenData)
	if err != nil {
		refreshedAuth, errRefresh := refreshKiroQuotaAuth(ctx, h, auth, true)
		if errRefresh != nil || refreshedAuth == auth {
			return nil, err
		}
		usage, err = checker.CheckUsage(ctx, kiroTokenDataFromAuth(refreshedAuth))
		if err != nil {
			return nil, err
		}
	}
	return kiroCreditSummaryFromUsage(usage), nil
}

func refreshKiroQuotaAuth(ctx context.Context, h *Handler, auth *coreauth.Auth, force bool) (*coreauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("auth not found")
	}
	if !force && !kiroQuotaAuthRefreshDue(auth, time.Now()) {
		return auth, nil
	}
	if strings.TrimSpace(metadataString(auth.Metadata, "refresh_token", "refreshToken")) == "" {
		return auth, nil
	}
	refresher := sdkauth.NewKiroAuthenticator()
	refreshed, err := refresher.Refresh(ctx, h.cfg, auth)
	if err != nil {
		return auth, err
	}
	if refreshed == nil {
		return auth, nil
	}
	if h.authManager != nil {
		if updated, errUpdate := h.authManager.Update(ctx, refreshed); errUpdate == nil && updated != nil {
			refreshed = updated
		}
	}
	return refreshed, nil
}

func kiroQuotaAuthRefreshDue(auth *coreauth.Auth, now time.Time) bool {
	if auth == nil {
		return false
	}
	if !auth.NextRefreshAfter.IsZero() && !now.Before(auth.NextRefreshAfter) {
		return true
	}
	expiresAt, ok := auth.ExpirationTime()
	if !ok || expiresAt.IsZero() {
		return false
	}
	return !now.Add(kiroQuotaRefreshLead).Before(expiresAt)
}

func kiroTokenDataFromAuth(auth *coreauth.Auth) *kiroauth.KiroTokenData {
	if auth == nil {
		return &kiroauth.KiroTokenData{}
	}
	metadata := auth.Metadata
	return &kiroauth.KiroTokenData{
		AccessToken:  tokenValueForAuth(auth),
		RefreshToken: metadataString(metadata, "refresh_token", "refreshToken"),
		ProfileArn:   metadataString(metadata, "profile_arn", "profileArn"),
		ClientID:     metadataString(metadata, "client_id", "clientId"),
	}
}

func metadataString(metadata map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := metadata[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func kiroCreditSummaryFromUsage(usage *kiroauth.UsageQuotaResponse) *kiroCreditSummary {
	summary := &kiroCreditSummary{}
	if usage == nil {
		return summary
	}

	if usage.SubscriptionInfo != nil {
		summary.SubscriptionTitle = strings.TrimSpace(usage.SubscriptionInfo.SubscriptionTitle)
	}
	if usage.NextDateReset > 0 {
		summary.NextReset = time.Unix(int64(usage.NextDateReset/1000), 0).UTC()
	}

	for _, breakdown := range usage.UsageBreakdownList {
		summary.CreditUsed += breakdown.CurrentUsageWithPrecision
		summary.CreditTotal += breakdown.UsageLimitWithPrecision
		if summary.ResourceType == "" {
			summary.ResourceType = strings.TrimSpace(breakdown.ResourceType)
		}
		if breakdown.FreeTrialInfo != nil {
			summary.CreditUsed += breakdown.FreeTrialInfo.CurrentUsageWithPrecision
			summary.CreditTotal += breakdown.FreeTrialInfo.UsageLimitWithPrecision
		}
	}
	summary.CreditUsed = roundKiroCredit(summary.CreditUsed)
	summary.CreditTotal = roundKiroCredit(summary.CreditTotal)
	summary.CreditRemaining = roundKiroCredit(math.Max(0, summary.CreditTotal-summary.CreditUsed))

	return summary
}

func roundKiroCredit(value float64) float64 {
	return math.Round(value*10000) / 10000
}
