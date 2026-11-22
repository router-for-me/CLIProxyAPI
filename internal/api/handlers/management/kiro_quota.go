package management

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	kiroauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

// kiroQuotaPluginID is the virtual plugin id the management UI uses to fetch Kiro quota
// through the generic POST /plugins/:id/quota route.
const kiroQuotaPluginID = "kiro"

func isKiroAuth(auth *coreauth.Auth) bool {
	return auth != nil && strings.EqualFold(strings.TrimSpace(auth.Provider), "kiro")
}

func kiroAuthTokens(auth *coreauth.Auth) (accessToken, refreshToken, profileArn string) {
	if auth == nil {
		return
	}
	read := func(key string) string {
		if auth.Attributes != nil {
			if v := strings.TrimSpace(auth.Attributes[key]); v != "" {
				return v
			}
		}
		if auth.Metadata != nil {
			if v, ok := auth.Metadata[key].(string); ok {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	return read("access_token"), read("refresh_token"), read("profile_arn")
}

// fetchKiroQuota serves quota for Kiro credentials using the same upstream call as `kiro-cli /usage`.
func (h *Handler) fetchKiroQuota(c *gin.Context, auth *coreauth.Auth) {
	h.mu.Lock()
	cfg := h.cfg
	h.mu.Unlock()

	svc := kiroauth.NewKiroAuthServiceWithProxyURL(cfg, auth.ProxyURL)
	accessToken, refreshToken, profileArn := kiroAuthTokens(auth)
	ctx := c.Request.Context()

	usage, errUsage := svc.GetUsageLimits(ctx, accessToken, profileArn)
	var httpErr *kiroauth.UsageHTTPError
	if errUsage != nil && errors.As(errUsage, &httpErr) &&
		(httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden) && refreshToken != "" {
		bundle, errRefresh := svc.RefreshTokens(ctx, refreshToken)
		if errRefresh == nil && bundle != nil {
			h.persistKiroRefresh(c, auth, bundle)
			if bundle.ProfileArn != "" {
				profileArn = bundle.ProfileArn
			}
			usage, errUsage = svc.GetUsageLimits(ctx, bundle.AccessToken, profileArn)
		} else if errRefresh != nil {
			log.WithError(errRefresh).Warnf("kiro quota: token refresh failed for %s", auth.Index)
		}
	}
	if errUsage != nil {
		status := http.StatusBadGateway
		if errors.As(errUsage, &httpErr) && (httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden) {
			status = http.StatusUnauthorized
		}
		c.JSON(status, gin.H{"error": fmt.Sprintf("failed to fetch quota: %v", errUsage)})
		return
	}
	c.JSON(http.StatusOK, kiroUsageToQuota(usage))
}

func (h *Handler) persistKiroRefresh(c *gin.Context, auth *coreauth.Auth, bundle *kiroauth.KiroTokenBundle) {
	if h.authManager == nil || bundle == nil {
		return
	}
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = map[string]any{}
	}
	if updated.Attributes == nil {
		updated.Attributes = map[string]string{}
	}
	updated.Metadata["access_token"] = bundle.AccessToken
	updated.Attributes["access_token"] = bundle.AccessToken
	if bundle.RefreshToken != "" {
		updated.Metadata["refresh_token"] = bundle.RefreshToken
		updated.Attributes["refresh_token"] = bundle.RefreshToken
	}
	if bundle.ProfileArn != "" {
		updated.Metadata["profile_arn"] = bundle.ProfileArn
		updated.Attributes["profile_arn"] = bundle.ProfileArn
	}
	if bundle.ExpiresAt != "" {
		updated.Metadata["expires_at"] = bundle.ExpiresAt
		updated.Metadata["expired"] = bundle.ExpiresAt
	}
	updated.LastRefreshedAt = time.Now().UTC()
	if _, errUpdate := h.authManager.UpdateRefreshedAuth(c.Request.Context(), auth, updated); errUpdate != nil {
		log.WithError(errUpdate).Warnf("kiro quota: failed to persist refreshed token for %s", auth.Index)
	}
}

func kiroEpochToRFC3339(v float64) string {
	if v <= 0 {
		return ""
	}
	sec := v
	if sec > 1e12 { // milliseconds
		sec = sec / 1000
	}
	return time.Unix(int64(sec), 0).UTC().Format(time.RFC3339)
}

func kiroUsageToQuota(u *kiroauth.UsageLimits) pluginapi.QuotaFetchResponse {
	resp := pluginapi.QuotaFetchResponse{}
	if u == nil {
		return resp
	}
	title := strings.TrimSpace(u.SubscriptionInfo.SubscriptionTitle)
	if title != "" || u.SubscriptionInfo.Type != "" {
		resp.Subscription = &pluginapi.QuotaSubscription{
			Plan:     title,
			TierName: title,
			TierID:   strings.TrimSpace(u.SubscriptionInfo.Type),
		}
	}

	buckets := make([]pluginapi.QuotaBucket, 0, len(u.UsageBreakdownList)+1)
	for _, b := range u.UsageBreakdownList {
		name := strings.TrimSpace(b.DisplayNamePlural)
		if name == "" {
			name = strings.TrimSpace(b.DisplayName)
		}
		if name == "" {
			name = strings.TrimSpace(b.ResourceType)
		}
		reset := kiroEpochToRFC3339(b.NextDateReset)
		if reset == "" {
			reset = kiroEpochToRFC3339(u.NextDateReset)
		}
		used, limit := b.Used(), b.Limit()
		key := strings.ToLower(strings.TrimSpace(b.ResourceType))
		if key == "" {
			key = strings.ToLower(name)
		}
		if limit > 0 {
			remaining := math.Max(0, math.Min(1, (limit-used)/limit))
			buckets = append(buckets, pluginapi.QuotaBucket{
				Window:            "monthly",
				RemainingFraction: remaining,
				ResetTime:         reset,
				Description:       fmt.Sprintf("%s (%.2f / %.0f)", name, used, limit),
			})
			resp.Summary = append(resp.Summary,
				pluginapi.QuotaMetric{Key: key + "_used", Label: name + " used", Value: math.Round(used*100) / 100, Format: "number"},
				pluginapi.QuotaMetric{Key: key + "_limit", Label: name + " limit", Value: limit, Format: "number"},
			)
		}
		if t := b.FreeTrialInfo; t != nil && strings.EqualFold(t.FreeTrialStatus, "ACTIVE") {
			tl, tu := t.UsageLimit, t.CurrentUsage
			if t.UsageLimitWithPrecision != nil {
				tl = *t.UsageLimitWithPrecision
			}
			if t.CurrentUsageWithPrecision != nil {
				tu = *t.CurrentUsageWithPrecision
			}
			if tl > 0 {
				buckets = append(buckets, pluginapi.QuotaBucket{
					Window:            "trial",
					RemainingFraction: math.Max(0, math.Min(1, (tl-tu)/tl)),
					ResetTime:         kiroEpochToRFC3339(t.FreeTrialExpiry),
					Description:       fmt.Sprintf("Free trial %s (%.2f / %.0f)", name, tu, tl),
				})
			}
		}
	}
	if len(buckets) > 0 {
		resp.Groups = []pluginapi.QuotaGroup{{DisplayName: "Kiro", Buckets: buckets}}
	}
	return resp
}
