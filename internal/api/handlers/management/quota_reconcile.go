package management

import (
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const claudeUsageRecoveryURL = "https://api.anthropic.com/api/oauth/usage?cedar_ember=1&skip_spend=1"

type quotaUsageRetryError struct{ after string }

func (e *quotaUsageRetryError) Error() string { return "usage retry required" }

// ReconcileQuota is opt-in through the management API. Unlike the manual
// reset endpoint it requires fresh provider recovery evidence and an atomic
// unchanged-state check. It never redeems credits or calls a messages endpoint.
func (h *Handler) ReconcileQuota(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	if h.quotaReconcileHomeEnabled() {
		c.JSON(http.StatusConflict, gin.H{"status": "unsupported_mode"})
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
		c.JSON(http.StatusOK, gin.H{"status": "credential_missing"})
		return
	}
	verify := h.quotaRecoveryAdapter(auth.Provider)
	if verify == nil {
		c.JSON(http.StatusOK, gin.H{"status": "unsupported_provider"})
		return
	}
	token, _ := auth.Metadata["access_token"].(string)
	if _, knownExpiry := auth.AccessTokenExpirationTime(); token == "" || !knownExpiry || (auth.Provider == "codex" && coreauth.IsCodexUseSIWC(auth)) {
		c.JSON(http.StatusOK, gin.H{"status": "unsupported_credential"})
		return
	}
	updated, models, err := h.authManager.ReconcileQuota(c.Request.Context(), auth.ID, verify)
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

// fetchQuotaRecovery uses only requests constructed by audited provider adapters.
// No user-controlled endpoint or callback URL is accepted by the API.
func (h *Handler) fetchQuotaRecovery(req *http.Request, auth *coreauth.Auth) ([]byte, error) {
	client := h.quotaRecoveryClient
	if client == nil {
		httpClient, errClient := h.quotaRecoveryHTTPClient(req.Context(), auth)
		if errClient != nil {
			return nil, errClient
		}
		defer httpClient.CloseIdleConnections()
		client = httpClient
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, errors.New("usage request failed")
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Warn("quota reconciliation: response close failed")
		}
	}()
	if after := strings.TrimSpace(resp.Header.Get("Retry-After")); after != "" {
		seconds, errNumber := strconv.ParseUint(after, 10, 63)
		date, errDate := http.ParseTime(after)
		if (errNumber == nil && seconds > 0) || (errDate == nil && date.After(time.Now())) {
			return nil, &quotaUsageRetryError{after: after}
		}
		return nil, errors.New("usage evidence unavailable")
	}
	if resp.StatusCode != http.StatusOK || (resp.Header.Get("Age") != "" && resp.Header.Get("Age") != "0") {
		return nil, errors.New("usage evidence unavailable")
	}
	maxBody := 512 << 10
	if auth.Provider == "antigravity" && req.URL.String() == sdkauth.AntigravityModelBaseURLDaily+sdkauth.AntigravityModelsPath {
		maxBody = sdkauth.AntigravityMaxCatalogBytes
	}
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, int64(maxBody)+1))
	if errRead != nil || len(body) > maxBody {
		return nil, errors.New("invalid usage response")
	}
	return body, nil
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
	if !ok || !onlyKeys(cedar, "eligible", "ineligible_reason", "at_limit", "exhausted", "grants",
		"next_grant_id", "weekly_resets_at", "cooldown_until", "event_props", "paused_held") || cedar["at_limit"] != false {
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
		if !knownClaudeWindow(key) {
			return false
		}
		window, ok := value.(map[string]any)
		if !ok || !onlyKeys(window, "utilization", "resets_at", "limit_dollars", "used_dollars", "remaining_dollars", "locked_reason") || !belowLimit(window["utilization"]) || window["locked_reason"] != nil {
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
		if !ok || !onlyKeys(limit, "kind", "group", "percent", "severity", "resets_at", "scope", "is_active") ||
			limit["severity"] != "normal" || !belowLimit(limit["percent"]) {
			return false
		}
	}
	return true
}

func belowLimit(v any) bool {
	n, ok := v.(float64)
	return ok && !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n < 100
}

func knownClaudeWindow(key string) bool {
	switch key {
	case "five_hour", "seven_day", "seven_day_oauth_apps", "seven_day_opus", "seven_day_sonnet", "seven_day_cowork", "seven_day_omelette":
		return true
	default:
		return false
	}
}

func onlyKeys(value map[string]any, allowed ...string) bool {
	for key := range value {
		found := false
		for _, known := range allowed {
			if key == known {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
