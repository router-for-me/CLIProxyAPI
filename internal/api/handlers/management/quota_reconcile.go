package management

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const claudeUsageRecoveryURL = "https://api.anthropic.com/api/oauth/usage?cedar_ember=1&skip_spend=1"

type quotaUsageRetryError struct{ after string }

func (e *quotaUsageRetryError) Error() string { return "usage retry required" }

// ReconcileClaudeQuota is opt-in through the management API. Unlike the manual
// reset endpoint it requires fresh provider recovery evidence and an atomic
// unchanged-state check. It never redeems credits or calls a messages endpoint.
func (h *Handler) ReconcileClaudeQuota(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	var req struct {
		AuthIndex string `json:"auth_index"`
	}
	if c.ShouldBindJSON(&req) != nil || strings.TrimSpace(req.AuthIndex) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
		return
	}
	auth := h.authByIndex(strings.TrimSpace(req.AuthIndex))
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return
	}
	updated, models, err := h.authManager.ReconcileClaudeQuota(c.Request.Context(), auth.ID, h.claudeQuotaRecovered)
	if errors.Is(err, coreauth.ErrQuotaReconcileChanged) {
		c.JSON(http.StatusConflict, gin.H{"status": "state_changed"})
		return
	}
	if err != nil {
		// Never expose provider response bodies, tokens or raw transport errors.
		var retry *quotaUsageRetryError
		if errors.As(err, &retry) {
			c.Header("Retry-After", retry.after)
		}
		c.JSON(http.StatusBadGateway, gin.H{"status": "provider_check_failed"})
		return
	}
	if updated == nil {
		c.JSON(http.StatusOK, gin.H{"status": "unchanged"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "reconciled", "auth_index": updated.EnsureIndex(), "models": models})
}

func (h *Handler) claudeQuotaRecovered(ctx context.Context, auth *coreauth.Auth) (bool, error) {
	token, _ := auth.Metadata["access_token"].(string)
	if token == "" {
		return false, errors.New("missing access token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeUsageRecoveryURL, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache")
	cfg := h.configSnapshot()
	if cfg != nil && strings.TrimSpace(cfg.ClaudeHeaderDefaults.UserAgent) != "" {
		req.Header.Set("User-Agent", cfg.ClaudeHeaderDefaults.UserAgent)
	}
	client := h.claudeQuotaClient
	if client == nil {
		transportHandler := &Handler{cfg: cfg}
		httpClient := &http.Client{
			Transport:     transportHandler.apiCallTransport(auth, ""),
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		defer httpClient.CloseIdleConnections()
		client = httpClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, errors.New("usage request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	// Retry-After, stale/cache-served responses and any rejection fail closed.
	if after := strings.TrimSpace(resp.Header.Get("Retry-After")); after != "" {
		seconds, numberErr := strconv.ParseUint(after, 10, 63)
		date, dateErr := http.ParseTime(after)
		if (numberErr == nil && seconds > 0) || (dateErr == nil && date.After(time.Now())) {
			return false, &quotaUsageRetryError{after: after}
		}
		return false, errors.New("usage evidence unavailable")
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Retry-After") != "" ||
		(resp.Header.Get("Age") != "" && resp.Header.Get("Age") != "0") {
		return false, errors.New("usage evidence unavailable")
	}
	const maxBody = 512 * 1024
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return false, errors.New("invalid usage response")
	}
	return claudeUsageProvesRecovery(body), nil
}

// The aggregate reset-credit flag alone is NOT quota evidence: at_limit=false
// can coexist with seven_day.utilization=100. All exposed windows and limits
// must independently agree. Unknown schema is deliberately not recovery.
func claudeUsageProvesRecovery(body []byte) bool {
	var root map[string]any
	if json.Unmarshal(body, &root) != nil || root == nil {
		return false
	}
	cedar, ok := root["cedar_ember"].(map[string]any)
	if !ok || cedar["at_limit"] != false {
		return false
	}
	if v, exists := cedar["cooldown_until"]; !exists || v != nil {
		return false
	}
	exhausted, ok := cedar["exhausted"].([]any)
	if !ok || len(exhausted) != 0 {
		return false
	}
	for _, key := range []string{"five_hour", "seven_day"} {
		if _, exists := root[key]; !exists {
			return false
		}
	}
	windows := 0
	for key, value := range root {
		if value == nil {
			continue
		}
		switch key {
		case "cedar_ember", "limits", "member_dashboard_available", "seven_day_breakdown", "weekly_scoped_shares":
			continue
		}
		window, ok := value.(map[string]any)
		if !ok || !belowLimit(window["utilization"]) || window["locked_reason"] != nil {
			return false
		}
		windows++
	}
	limits, ok := root["limits"].([]any)
	if windows == 0 || !ok || len(limits) == 0 {
		return false
	}
	for _, raw := range limits {
		limit, ok := raw.(map[string]any)
		if !ok || limit["severity"] != "normal" || !belowLimit(limit["percent"]) {
			return false
		}
	}
	return true
}

func belowLimit(v any) bool {
	n, ok := v.(float64)
	return ok && !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n < 100
}
