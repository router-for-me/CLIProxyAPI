package management

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// kiroQuotaCache stores cached quota info for Kiro auth entries.
var (
	kiroQuotaMu    sync.RWMutex
	kiroQuotaStore = make(map[string]gin.H) // keyed by auth ID
)

// getKiroQuotaCached returns cached quota info for a Kiro auth entry.
func (h *Handler) getKiroQuotaCached(auth *coreauth.Auth) gin.H {
	if auth == nil {
		return nil
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if provider != "kiro" {
		return nil
	}

	kiroQuotaMu.RLock()
	result, ok := kiroQuotaStore[auth.ID]
	kiroQuotaMu.RUnlock()

	if ok {
		return result
	}

	// If not cached, try to fetch synchronously (first time only)
	return h.fetchAndCacheKiroQuota(auth)
}

// fetchAndCacheKiroQuota fetches quota for a single Kiro auth and caches it.
func (h *Handler) fetchAndCacheKiroQuota(auth *coreauth.Auth) gin.H {
	if auth == nil || auth.Metadata == nil {
		return nil
	}

	tokenData := extractKiroTokenData(auth)
	if tokenData == nil || tokenData.AccessToken == "" {
		return nil
	}

	checker := kiro.NewUsageCheckerWithClient(
		util.SetProxy(&h.cfg.SDKConfig, &http.Client{Timeout: 15 * time.Second}),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	usage, err := checker.CheckUsage(ctx, tokenData)
	if err != nil {
		log.WithError(err).Debugf("kiro quota fetch failed for %s", auth.ID)
		return nil
	}

	result := buildKiroQuotaEntry(usage)

	kiroQuotaMu.Lock()
	kiroQuotaStore[auth.ID] = result
	kiroQuotaMu.Unlock()

	return result
}

// buildKiroQuotaEntry builds the quota info map from a usage response.
func buildKiroQuotaEntry(usage *kiro.UsageQuotaResponse) gin.H {
	if usage == nil || len(usage.UsageBreakdownList) == 0 {
		return nil
	}

	bd := usage.UsageBreakdownList[0]
	result := gin.H{
		"resource_type":    bd.ResourceType,
		"used":             bd.CurrentUsageWithPrecision,
		"limit":            bd.UsageLimitWithPrecision,
		"remaining":        bd.UsageLimitWithPrecision - bd.CurrentUsageWithPrecision,
		"usage_percentage": kiro.GetUsagePercentage(usage),
		"exhausted":        kiro.IsQuotaExhausted(usage),
	}

	if usage.SubscriptionInfo != nil {
		result["plan"] = usage.SubscriptionInfo.SubscriptionTitle
	}

	if usage.NextDateReset > 0 {
		result["next_reset"] = time.Unix(int64(usage.NextDateReset/1000), 0)
	}

	return result
}

// StartKiroQuotaRefresher starts a background goroutine that periodically
// refreshes Kiro quota info for all active Kiro auth entries.
func (h *Handler) StartKiroQuotaRefresher() {
	go func() {
		// Initial delay to let the service start up
		time.Sleep(10 * time.Second)
		h.refreshAllKiroQuotas()

		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			h.refreshAllKiroQuotas()
		}
	}()
}

// refreshAllKiroQuotas fetches quota for all active Kiro auth entries.
func (h *Handler) refreshAllKiroQuotas() {
	if h == nil || h.authManager == nil {
		return
	}

	auths := h.authManager.List()
	for _, auth := range auths {
		if auth == nil || auth.Disabled {
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		if provider != "kiro" {
			continue
		}
		h.fetchAndCacheKiroQuota(auth)
		// Small delay between requests to avoid rate limiting
		time.Sleep(2 * time.Second)
	}
}
