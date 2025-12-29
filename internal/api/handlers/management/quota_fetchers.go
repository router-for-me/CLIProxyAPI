package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/quota"
)

// GetAllQuotas returns quota for all connected accounts.
// GET /v0/management/quotas
func (h *Handler) GetAllQuotas(c *gin.Context) {
	if h.quotaManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "quota manager not initialized"})
		return
	}

	ctx := c.Request.Context()
	quotas, err := h.quotaManager.FetchAllQuotas(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, quotas)
}

// GetProviderQuotas returns quota for a specific provider.
// GET /v0/management/quotas/:provider
func (h *Handler) GetProviderQuotas(c *gin.Context) {
	if h.quotaManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "quota manager not initialized"})
		return
	}

	provider := c.Param("provider")
	if provider == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider is required"})
		return
	}

	ctx := c.Request.Context()
	quotas, err := h.quotaManager.FetchProviderQuotas(ctx, provider)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, quotas)
}

// GetAccountQuota returns quota for a specific account.
// GET /v0/management/quotas/:provider/:account
func (h *Handler) GetAccountQuota(c *gin.Context) {
	if h.quotaManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "quota manager not initialized"})
		return
	}

	provider := c.Param("provider")
	account := c.Param("account")
	if provider == "" || account == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider and account are required"})
		return
	}

	ctx := c.Request.Context()
	quotaResp, err := h.quotaManager.FetchAccountQuota(ctx, provider, account)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, quotaResp)
}

// RefreshQuotas forces a quota refresh for all or specific providers.
// POST /v0/management/quotas/refresh
func (h *Handler) RefreshQuotas(c *gin.Context) {
	if h.quotaManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "quota manager not initialized"})
		return
	}

	var req quota.RefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Allow empty body - refresh all
		req.Providers = nil
	}

	ctx := c.Request.Context()
	quotas, err := h.quotaManager.RefreshQuotas(ctx, req.Providers)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, quotas)
}

// GetSubscriptionInfo returns subscription/tier info for Antigravity accounts.
// GET /v0/management/subscription-info
func (h *Handler) GetSubscriptionInfo(c *gin.Context) {
	if h.quotaManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "quota manager not initialized"})
		return
	}

	ctx := c.Request.Context()
	info, err := h.quotaManager.GetSubscriptionInfo(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, info)
}