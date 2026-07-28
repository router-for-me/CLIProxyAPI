package management

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// Quota exceeded toggles
func (h *Handler) GetSwitchProject(c *gin.Context) {
	c.JSON(200, gin.H{"switch-project": h.cfg.QuotaExceeded.SwitchProject})
}
func (h *Handler) PutSwitchProject(c *gin.Context) {
	h.updateBoolField(c, func(v bool) { h.cfg.QuotaExceeded.SwitchProject = v })
}

func (h *Handler) GetSwitchPreviewModel(c *gin.Context) {
	c.JSON(200, gin.H{"switch-preview-model": h.cfg.QuotaExceeded.SwitchPreviewModel})
}
func (h *Handler) PutSwitchPreviewModel(c *gin.Context) {
	h.updateBoolField(c, func(v bool) { h.cfg.QuotaExceeded.SwitchPreviewModel = v })
}

// ResetQuota clears quota/cooldown routing state for one auth index.
func (h *Handler) ResetQuota(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	var req struct {
		AuthIndex string `json:"auth_index"`
	}
	if errBindJSON := c.ShouldBindJSON(&req); errBindJSON != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	authIndex := strings.TrimSpace(req.AuthIndex)
	if authIndex == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
		return
	}

	auth := h.authByIndex(authIndex)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return
	}

	updated, models, errReset := h.authManager.ResetQuota(c.Request.Context(), auth.ID)
	if errReset != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to reset quota: %v", errReset)})
		return
	}
	if updated == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return
	}
	updated.EnsureIndex()

	c.JSON(http.StatusOK, gin.H{
		"status":     "ok",
		"auth_index": updated.Index,
		"models":     models,
	})
}

// GetOAuthQuota aggregates per-auth quota/availability state across every
// OAuth credential registered with the auth manager. It is the data source
// behind the "配额管理" view: a single endpoint that centralises, per account,
// whether its quota is currently exceeded (额度耗尽/冷却中), when it recovers,
// and the recent success/failure counters. Plugin-backed providers (e.g.
// codebuddy) appear here automatically once their executor surfaces a quota
// error (e.g. CodeBuddy business code 11221), because the host records the
// resulting cooldown on the same Auth.Quota / ModelState fields native
// providers use.
func (h *Handler) GetOAuthQuota(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	now := time.Now()
	// Optional case-insensitive provider filter, e.g. ?provider=codebuddy.
	providerFilter := strings.ToLower(strings.TrimSpace(c.Query("provider")))

	auths := h.authManager.List()
	accounts := make([]gin.H, 0, len(auths))
	summary := map[string]int{"total": 0, "available": 0, "quota_exceeded": 0, "unavailable": 0, "disabled": 0}

	for _, auth := range auths {
		if auth == nil {
			continue
		}
		auth.EnsureIndex()
		provider := strings.TrimSpace(auth.Provider)
		if providerFilter != "" && strings.ToLower(provider) != providerFilter {
			continue
		}

		entry := buildOAuthQuotaEntry(auth, now)
		accounts = append(accounts, entry)

		summary["total"]++
		switch {
		case auth.Disabled || auth.Status == coreauth.StatusDisabled:
			summary["disabled"]++
		case auth.Quota.Exceeded:
			summary["quota_exceeded"]++
		case auth.Unavailable:
			summary["unavailable"]++
		default:
			summary["available"]++
		}
	}

	sort.Slice(accounts, func(i, j int) bool {
		pi, _ := accounts[i]["provider"].(string)
		pj, _ := accounts[j]["provider"].(string)
		if !strings.EqualFold(pi, pj) {
			return strings.ToLower(pi) < strings.ToLower(pj)
		}
		li, _ := accounts[i]["label"].(string)
		lj, _ := accounts[j]["label"].(string)
		return strings.ToLower(li) < strings.ToLower(lj)
	})

	c.JSON(http.StatusOK, gin.H{
		"summary":  summary,
		"accounts": accounts,
	})
}

// buildOAuthQuotaEntry flattens one auth record's quota/availability state into
// a management-friendly view. It deliberately reuses the Auth.Quota /
// ModelState.Quota fields that the conductor already maintains on quota
// cooldown, so any provider (native or plugin) that trips a quota error shows
// up here without extra instrumentation.
func buildOAuthQuotaEntry(auth *coreauth.Auth, now time.Time) gin.H {
	provider := strings.TrimSpace(auth.Provider)
	label := strings.TrimSpace(auth.Label)
	if label == "" {
		label = auth.ID
	}

	// effective availability: an auth that is disabled or already past its
	// recovery time should not be reported as still cooling down.
	quotaExceeded := auth.Quota.Exceeded
	unavailable := auth.Unavailable
	var recoverInSeconds int64
	if !auth.Quota.NextRecoverAt.IsZero() && auth.Quota.NextRecoverAt.After(now) {
		recoverInSeconds = int64(time.Until(auth.Quota.NextRecoverAt).Seconds())
	} else if !auth.NextRetryAfter.IsZero() && auth.NextRetryAfter.After(now) {
		recoverInSeconds = int64(time.Until(auth.NextRetryAfter).Seconds())
	}
	// If the recovery moment already passed, the cooldown is over even if the
	// stale flags were not yet cleared by the scheduler.
	if recoverInSeconds == 0 && quotaExceeded && !auth.Quota.NextRecoverAt.IsZero() && !auth.Quota.NextRecoverAt.After(now) {
		quotaExceeded = false
	}

	status := "available"
	switch {
	case auth.Disabled || auth.Status == coreauth.StatusDisabled:
		status = "disabled"
	case quotaExceeded:
		status = "quota_exceeded"
	case unavailable:
		status = "unavailable"
	}

	entry := gin.H{
		"id":             auth.ID,
		"auth_index":     auth.Index,
		"provider":       provider,
		"label":          label,
		"status":         status,
		"status_message": auth.StatusMessage,
		"disabled":       auth.Disabled,
		"unavailable":    auth.Unavailable,
		"quota": gin.H{
			"exceeded":         auth.Quota.Exceeded,
			"reason":           auth.Quota.Reason,
			"next_recover_at":  auth.Quota.NextRecoverAt,
			"backoff_level":    auth.Quota.BackoffLevel,
			"recover_in_secs":  recoverInSeconds,
		},
		"success": auth.Success,
		"failed":  auth.Failed,
	}
	if email := authEmail(auth); email != "" {
		entry["email"] = email
	}
	if !auth.LastRefreshedAt.IsZero() {
		entry["last_refresh"] = auth.LastRefreshedAt
	}
	if !auth.NextRetryAfter.IsZero() {
		entry["next_retry_after"] = auth.NextRetryAfter
	}

	// Per-model quota states, so a provider that only cooled one model still
	// exposes which model is affected.
	if len(auth.ModelStates) > 0 {
		models := make([]gin.H, 0, len(auth.ModelStates))
		for name, ms := range auth.ModelStates {
			if ms == nil {
				continue
			}
			var modelRecoverIn int64
			if !ms.Quota.NextRecoverAt.IsZero() && ms.Quota.NextRecoverAt.After(now) {
				modelRecoverIn = int64(time.Until(ms.Quota.NextRecoverAt).Seconds())
			} else if !ms.NextRetryAfter.IsZero() && ms.NextRetryAfter.After(now) {
				modelRecoverIn = int64(time.Until(ms.NextRetryAfter).Seconds())
			}
			models = append(models, gin.H{
				"model":           name,
				"status":          ms.Status,
				"unavailable":     ms.Unavailable,
				"quota_exceeded":  ms.Quota.Exceeded,
				"reason":          ms.Quota.Reason,
				"next_recover_at": ms.Quota.NextRecoverAt,
				"recover_in_secs": modelRecoverIn,
			})
		}
		sort.Slice(models, func(i, j int) bool {
			mi, _ := models[i]["model"].(string)
			mj, _ := models[j]["model"].(string)
			return strings.ToLower(mi) < strings.ToLower(mj)
		})
		entry["models"] = models
	}
	return entry
}
