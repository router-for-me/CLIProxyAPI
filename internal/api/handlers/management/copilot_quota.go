package management

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/copilot"
)

// GetCopilotQuota returns Copilot premium request quota for all authenticated accounts.
// Query param: ?force=true to bypass cache and fetch fresh data.
func (h *Handler) GetCopilotQuota(c *gin.Context) {
	if h.copilotService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Copilot quota service not initialized"})
		return
	}

	force := c.Query("force") == "true"
	ctx := c.Request.Context()

	var mgmtResp *copilot.ManagementResponse
	var err error
	if force {
		mgmtResp, err = h.copilotService.RefreshAll(ctx)
	} else {
		mgmtResp, err = h.copilotService.GetAllQuotas(ctx)
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if mgmtResp == nil {
		mgmtResp = &copilot.ManagementResponse{Accounts: []copilot.AccountQuota{}}
	}

	if len(mgmtResp.Accounts) == 0 {
		mgmtResp.Message = "No Copilot quota accounts configured. Use CLI --copilot-quota-login or POST /v0/management/copilot-quota/auth to add accounts."
	}

	c.JSON(http.StatusOK, mgmtResp)
}

// PostCopilotQuotaAuth initiates a GitHub Device Code OAuth flow.
// Returns the device code and verification URI for the user to complete in a browser.
func (h *Handler) PostCopilotQuotaAuth(c *gin.Context) {
	if h.copilotService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Copilot quota service not initialized"})
		return
	}

	ctx := c.Request.Context()
	deviceResp, err := h.copilotService.StartDeviceFlow(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"user_code":        deviceResp.UserCode,
		"verification_uri": deviceResp.VerificationURI,
		"expires_in":       deviceResp.ExpiresIn,
		"interval":         deviceResp.Interval,
		"device_code":      deviceResp.DeviceCode,
	})
}

// PostCopilotQuotaAuthPoll polls for the result of a pending Device Code authorization.
func (h *Handler) PostCopilotQuotaAuthPoll(c *gin.Context) {
	if h.copilotService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Copilot quota service not initialized"})
		return
	}

	var body struct {
		DeviceCode string `json:"device_code"`
		Interval   int    `json:"interval"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.DeviceCode == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "device_code is required"})
		return
	}

	interval := time.Duration(body.Interval) * time.Second

	ctx := c.Request.Context()
	token, err := h.copilotService.CompleteDeviceFlow(ctx, body.DeviceCode, interval)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"status": "error", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "complete",
		"email":  token.Email,
	})
}

// DeleteCopilotQuotaAuth removes a GitHub account from Copilot quota tracking.
func (h *Handler) DeleteCopilotQuotaAuth(c *gin.Context) {
	if h.copilotService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Copilot quota service not initialized"})
		return
	}

	email := c.Param("email")
	if email == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email parameter is required"})
		return
	}

	accounts := h.copilotService.ListAccounts()
	found := false
	for _, acc := range accounts {
		if acc == email {
			found = true
			break
		}
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
		return
	}

	if err := h.copilotService.RemoveToken(email); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Account removed",
		"email":   email,
	})
}

// GetCopilotQuotaAccounts returns the list of authenticated GitHub accounts.
func (h *Handler) GetCopilotQuotaAccounts(c *gin.Context) {
	if h.copilotService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Copilot quota service not initialized"})
		return
	}

	accounts := h.copilotService.ListAccounts()
	if accounts == nil {
		accounts = []string{}
	}

	c.JSON(http.StatusOK, gin.H{"accounts": accounts})
}
