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
// It will automatically refresh the token if expired before querying quota.
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

	// Check if token is expired and refresh if needed
	tokenData = h.ensureKiroTokenFresh(auth, tokenData)
	if tokenData == nil || tokenData.AccessToken == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "kiro token refresh failed"})
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
		// If quota check fails with auth error, try refreshing token and retry once
		if strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "invalid") {
			log.Debug("kiro quota check failed with auth error, attempting token refresh and retry")
			refreshedToken := h.forceRefreshKiroToken(auth, tokenData)
			if refreshedToken != nil && refreshedToken.AccessToken != "" {
				usage, err = checker.CheckUsage(ctx, refreshedToken)
			}
		}
		if err != nil {
			log.WithError(err).Debug("kiro quota request failed")
			c.JSON(http.StatusBadGateway, gin.H{"error": "kiro quota request failed: " + err.Error()})
			return
		}
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

// ensureKiroTokenFresh checks if the token is expired and refreshes it if needed.
func (h *Handler) ensureKiroTokenFresh(auth *coreauth.Auth, tokenData *kiro.KiroTokenData) *kiro.KiroTokenData {
	if tokenData == nil {
		return nil
	}

	// Check expiry from metadata
	if tokenData.ExpiresAt != "" {
		expiresAt, err := time.Parse(time.RFC3339, tokenData.ExpiresAt)
		if err == nil && time.Now().After(expiresAt.Add(-2*time.Minute)) {
			// Token expired or about to expire, refresh it
			log.Debugf("kiro token expired (expires_at=%s), refreshing before quota check", tokenData.ExpiresAt)
			refreshed := h.forceRefreshKiroToken(auth, tokenData)
			if refreshed != nil {
				return refreshed
			}
		}
	}

	return tokenData
}

// forceRefreshKiroToken refreshes the Kiro token using SSO OIDC and updates auth metadata in memory.
func (h *Handler) forceRefreshKiroToken(auth *coreauth.Auth, tokenData *kiro.KiroTokenData) *kiro.KiroTokenData {
	if tokenData.RefreshToken == "" {
		log.Debug("kiro: cannot refresh token - no refresh_token available")
		return nil
	}

	ssoClient := kiro.NewSSOOIDCClient(h.cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var newToken *kiro.KiroTokenData
	var err error

	authMethod := strings.ToLower(tokenData.AuthMethod)
	region := tokenData.Region

	switch authMethod {
	case "idc":
		newToken, err = ssoClient.RefreshTokenWithRegion(
			ctx,
			tokenData.ClientID,
			tokenData.ClientSecret,
			tokenData.RefreshToken,
			region,
			tokenData.StartURL,
		)
	case "builder-id":
		newToken, err = ssoClient.RefreshToken(
			ctx,
			tokenData.ClientID,
			tokenData.ClientSecret,
			tokenData.RefreshToken,
		)
	default:
		// Social auth (Google) - use Kiro social auth service endpoint
		socialClient := kiro.NewSocialAuthClient(h.cfg)
		newToken, err = socialClient.RefreshSocialToken(ctx, tokenData.RefreshToken)
	}

	if err != nil {
		log.WithError(err).Debug("kiro: token refresh failed during quota check")
		return nil
	}

	// Update auth metadata in memory so subsequent calls use the new token
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]interface{})
	}
	auth.Metadata["access_token"] = newToken.AccessToken
	if newToken.RefreshToken != "" {
		auth.Metadata["refresh_token"] = newToken.RefreshToken
	}
	if newToken.ExpiresAt != "" {
		auth.Metadata["expires_at"] = newToken.ExpiresAt
	}

	log.Debugf("kiro: token refreshed for quota check, new expires_at=%s", newToken.ExpiresAt)
	return newToken
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
	expiresAt, _ := auth.Metadata["expires_at"].(string)
	authMethod, _ := auth.Metadata["auth_method"].(string)

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
		ExpiresAt:    expiresAt,
		AuthMethod:   authMethod,
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
