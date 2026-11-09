package management

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func bankedResetError(c *gin.Context, err error) {
	status := http.StatusBadGateway
	if errors.Is(err, coreauth.ErrBankedResetDisabled) {
		status = http.StatusForbidden
	}
	if errors.Is(err, coreauth.ErrNoBankedReset) {
		status = http.StatusConflict
	}
	if errors.Is(err, coreauth.ErrBankedResetBackoff) {
		status = http.StatusTooManyRequests
	}
	c.JSON(status, gin.H{"error": err.Error()})
}

func (h *Handler) ListBankedResets(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(503, gin.H{"error": "auth manager unavailable"})
		return
	}
	a := h.authByIndex(strings.TrimSpace(c.Query("auth_index")))
	if a == nil {
		c.JSON(404, gin.H{"error": "auth not found"})
		return
	}
	status, err := h.authManager.ListBankedResets(c.Request.Context(), a.ID)
	if err != nil {
		bankedResetError(c, err)
		return
	}
	c.JSON(200, status)
}

func (h *Handler) RedeemBankedReset(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(503, gin.H{"error": "auth manager unavailable"})
		return
	}
	var req struct {
		AuthIndex string `json:"auth_index"`
		CreditID  string `json:"credit_id"`
	}
	if c.ShouldBindJSON(&req) != nil || strings.TrimSpace(req.AuthIndex) == "" {
		c.JSON(400, gin.H{"error": "auth_index is required"})
		return
	}
	a := h.authByIndex(strings.TrimSpace(req.AuthIndex))
	if a == nil {
		c.JSON(404, gin.H{"error": "auth not found"})
		return
	}
	result, err := h.authManager.RedeemBankedReset(c.Request.Context(), a.ID, req.CreditID)
	if err != nil {
		bankedResetError(c, err)
		return
	}
	c.JSON(200, result)
}

func (h *Handler) CheckBankedResets(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(503, gin.H{"error": "auth manager unavailable"})
		return
	}
	h.authManager.CheckBankedResets(c.Request.Context(), nil)
	c.JSON(200, gin.H{"status": "ok"})
}

// Existing Management Center reset buttons use api-call. In managed modes,
// send those mutations through the same account lock and durable intent as auto
// use. Leave the off-mode contract untouched. No v0 routes are added.
func (h *Handler) handleBankedResetAPICall(c *gin.Context, a *coreauth.Auth, method string, target *url.URL, data string) bool {
	if !c.GetBool(ConfigV8ContextKey) {
		return false
	}
	if method != http.MethodPost || a == nil || h.authManager == nil || h.cfg == nil {
		return false
	}
	mode := h.cfg.QuotaExceeded.BankedReset.Mode
	if mode != "auto" && mode != "manual" {
		return false
	}
	codex := a.Provider == "codex" && target.Scheme == "https" && target.Host == "chatgpt.com" && target.Path == "/backend-api/wham/rate-limit-reset-credits/consume"
	claude := a.Provider == "claude" && target.Scheme == "https" && target.Host == "api.anthropic.com" && strings.HasPrefix(target.Path, "/api/organizations/") && strings.HasSuffix(target.Path, "/reset_rate_limits")
	if !codex && !claude {
		return false
	}
	var req struct {
		CreditID string `json:"credit_id"`
		GrantID  string `json:"grant_id"`
		Program  string `json:"program"`
	}
	if json.Unmarshal([]byte(data), &req) != nil || claude && req.Program != "cedar_ember" {
		c.JSON(400, gin.H{"error": "invalid reset request"})
		return true
	}
	creditID := req.CreditID
	if claude {
		creditID = req.GrantID
	}
	result, err := h.authManager.RedeemBankedReset(c.Request.Context(), a.ID, creditID)
	if err != nil {
		bankedResetError(c, err)
		return true
	}
	body := gin.H{"code": result.Code, "result": result.Code}
	encoded, _ := json.Marshal(body)
	c.JSON(200, apiCallResponse{StatusCode: 200, Header: map[string][]string{}, Body: string(encoded)})
	return true
}
