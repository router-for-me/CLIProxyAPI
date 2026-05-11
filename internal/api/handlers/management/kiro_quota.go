package management

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// GetKiroQuota fetches Kiro (AWS CodeWhisperer) usage quota information.
//
// Endpoint:
//
//	GET /v0/management/kiro-quota
//
// Query Parameters (optional):
//   - auth_index: The credential "auth_index" from GET /v0/management/auth-files.
//     If omitted, uses the first available Kiro credential.
//
// Response:
//
//	Returns the UsageQuotaResponse with usage breakdown and subscription info.
//
// Example:
//
//	curl -sS -X GET "http://127.0.0.1:8317/v0/management/kiro-quota?auth_index=<AUTH_INDEX>" \
//	  -H "Authorization: Bearer <MANAGEMENT_KEY>"
func (h *Handler) GetKiroQuota(c *gin.Context) {
	authIndex := strings.TrimSpace(c.Query("auth_index"))
	if authIndex == "" {
		authIndex = strings.TrimSpace(c.Query("authIndex"))
	}
	if authIndex == "" {
		authIndex = strings.TrimSpace(c.Query("AuthIndex"))
	}

	auth := h.findKiroAuth(authIndex)
	if auth == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no kiro credential found"})
		return
	}

	// Extract token data from auth metadata
	tokenData := extractKiroTokenData(auth)
	if tokenData == nil || tokenData.AccessToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kiro access token not available (token may need refresh)"})
		return
	}

	// Create usage checker with proxy-aware HTTP client
	checker := kiro.NewUsageCheckerWithClient(
		util.SetProxy(&h.cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second}),
	)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	usage, err := checker.CheckUsage(ctx, tokenData)
	if err != nil {
		log.WithError(err).Debug("kiro quota request failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "kiro quota request failed: " + err.Error()})
		return
	}

	// Build enriched response
	response := gin.H{
		"usage":        usage,
		"quota_status": buildKiroQuotaStatus(usage),
		"auth_index":   auth.Index,
		"auth_name":    auth.FileName,
	}

	c.JSON(http.StatusOK, response)
}

// findKiroAuth locates a Kiro credential by auth_index or returns the first available one.
func (h *Handler) findKiroAuth(authIndex string) *coreauth.Auth {
	if h == nil || h.authManager == nil {
		return nil
	}

	auths := h.authManager.List()
	var firstKiro *coreauth.Auth

	for _, auth := range auths {
		if auth == nil {
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		if provider != "kiro" {
			continue
		}
		if auth.Disabled {
			continue
		}
		if firstKiro == nil {
			firstKiro = auth
		}
		if authIndex != "" {
			auth.EnsureIndex()
			if auth.Index == authIndex {
				return auth
			}
		}
	}

	if authIndex == "" {
		return firstKiro
	}
	return nil
}

// extractKiroTokenData extracts KiroTokenData from a coreauth.Auth's Metadata.
func extractKiroTokenData(auth *coreauth.Auth) *kiro.KiroTokenData {
	if auth == nil || auth.Metadata == nil {
		return nil
	}

	accessToken, _ := auth.Metadata["access_token"].(string)
	refreshToken, _ := auth.Metadata["refresh_token"].(string)
	profileArn, _ := auth.Metadata["profile_arn"].(string)
	clientID, _ := auth.Metadata["client_id"].(string)
	clientSecret, _ := auth.Metadata["client_secret"].(string)
	region, _ := auth.Metadata["region"].(string)
	startURL, _ := auth.Metadata["start_url"].(string)

	if accessToken == "" {
		return nil
	}

	return &kiro.KiroTokenData{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ProfileArn:   profileArn,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Region:       region,
		StartURL:     startURL,
	}
}

// buildKiroQuotaStatus builds a summary status from the usage response.
func buildKiroQuotaStatus(usage *kiro.UsageQuotaResponse) gin.H {
	if usage == nil {
		return gin.H{"exhausted": true, "remaining": 0}
	}

	remaining := kiro.GetRemainingQuota(usage)
	exhausted := kiro.IsQuotaExhausted(usage)
	percentage := kiro.GetUsagePercentage(usage)

	status := gin.H{
		"exhausted":        exhausted,
		"remaining":        remaining,
		"usage_percentage": percentage,
	}

	if usage.NextDateReset > 0 {
		status["next_reset"] = time.Unix(int64(usage.NextDateReset/1000), 0)
	}

	if usage.SubscriptionInfo != nil {
		status["subscription"] = usage.SubscriptionInfo
	}

	return status
}
