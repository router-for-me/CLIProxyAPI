package management

import (
	"bytes"
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	kimiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kimi"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const codexUsageRecoveryURL = "https://chatgpt.com/backend-api/wham/usage"
const antigravityQuotaRecoveryURL = "https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary"

type quotaRecoveryAdapter func(context.Context, *coreauth.Auth) (bool, error)

// This registry is a capability boundary, not a list of providers the worker
// should ignore. Unsupported providers are reported explicitly and never reset.
func (h *Handler) quotaRecoveryAdapter(provider string) quotaRecoveryAdapter {
	switch provider {
	case "claude":
		return h.claudeQuotaRecovered
	case "codex":
		return h.codexQuotaRecovered
	case "antigravity":
		return h.antigravityQuotaRecovered
	case "kimi":
		return h.kimiQuotaRecovered
	default:
		return nil
	}
}

func (h *Handler) quotaReconcileHomeEnabled() bool {
	cfg := h.configSnapshot()
	return (cfg != nil && cfg.Home.Enabled) || (h.authManager != nil && h.authManager.HomeEnabled())
}

// QuotaReconcileCapabilities is independent of account existence or cooldowns.
// Home owns its own scheduling state; local mutation must not masquerade as a
// Home repair. Display-only plugin quota buckets are not recovery attestations.
func (h *Handler) QuotaReconcileCapabilities(c *gin.Context) {
	mode := "local"
	if h.quotaReconcileHomeEnabled() {
		mode = "home_unsupported"
	}
	c.JSON(http.StatusOK, gin.H{
		"version": 1, "mode": mode,
		"providers":          []string{"claude", "codex", "antigravity", "kimi"},
		"credential_kind":    "oauth_with_known_expiry",
		"unsupported_policy": "preserve_cooldown",
		"unsupported": gin.H{
			"xai":        "billing or inference health is not authoritative rate-limit recovery",
			"gemini":     "API-key quota recovery evidence unavailable",
			"gemini-cli": "no reviewed complete recovery adapter",
			"other":      "no reviewed complete recovery adapter",
		},
	})
}

func (h *Handler) quotaRecoveryHTTPClient(ctx context.Context, auth *coreauth.Auth) (*http.Client, error) {
	cfg := h.configSnapshot()
	if cfg == nil {
		cfg = &config.Config{}
	}
	proxy := cfg.ProxyURL
	if strings.TrimSpace(auth.ProxyURL) != "" {
		proxy = auth.ProxyURL
	}
	var client *http.Client
	switch auth.Provider {
	case "claude":
		sdkCfg := cfg.SDKConfig
		sdkCfg.ProxyURL = proxy
		client = claudeauth.NewAnthropicHttpClient(&sdkCfg)
	case "codex":
		client = helps.NewUtlsHTTPClient(ctx, cfg, auth, 0)
	case "antigravity":
		var errClient error
		client, errClient = sdkauth.NewAntigravityProbeClient(proxy)
		if errClient != nil {
			return nil, errors.New("quota transport unavailable")
		}
	default:
		transportHandler := &Handler{cfg: cfg}
		client = &http.Client{Transport: transportHandler.apiCallTransport(auth, "")}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client, nil
}

func recoveryRequest(ctx context.Context, auth *coreauth.Auth, method, endpoint string, body []byte) (*http.Request, error) {
	token, _ := auth.Metadata["access_token"].(string)
	if token == "" {
		return nil, errors.New("access token unavailable")
	}
	var req *http.Request
	var errRequest error
	if body == nil {
		req, errRequest = http.NewRequestWithContext(ctx, method, endpoint, nil)
	} else {
		req, errRequest = http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	}
	if errRequest != nil {
		return nil, errRequest
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (h *Handler) claudeQuotaRecovered(ctx context.Context, auth *coreauth.Auth) (bool, error) {
	req, errRequest := recoveryRequest(ctx, auth, http.MethodGet, claudeUsageRecoveryURL, nil)
	if errRequest != nil {
		return false, errRequest
	}
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	if cfg := h.configSnapshot(); cfg != nil && cfg.ClaudeHeaderDefaults.UserAgent != "" {
		req.Header.Set("User-Agent", cfg.ClaudeHeaderDefaults.UserAgent)
	}
	body, errFetch := h.fetchQuotaRecovery(req, auth)
	return errFetch == nil && claudeUsageProvesRecovery(body), errFetch
}

func (h *Handler) codexQuotaRecovered(ctx context.Context, auth *coreauth.Auth) (bool, error) {
	accountID, _ := auth.Metadata["account_id"].(string)
	if strings.TrimSpace(accountID) == "" {
		return false, nil // Never query an implicit/default workspace.
	}
	req, errRequest := recoveryRequest(ctx, auth, http.MethodGet, codexUsageRecoveryURL, nil)
	if errRequest != nil {
		return false, errRequest
	}
	req.Header.Set("ChatGPT-Account-Id", accountID)
	if cfg := h.configSnapshot(); cfg != nil && cfg.CodexHeaderDefaults.UserAgent != "" {
		req.Header.Set("User-Agent", cfg.CodexHeaderDefaults.UserAgent)
	}
	body, errFetch := h.fetchQuotaRecovery(req, auth)
	if errFetch == nil {
		var identity map[string]any
		if json.Unmarshal(body, &identity) != nil {
			return false, nil
		}
		if returned, exists := identity["account_id"]; exists && returned != accountID {
			return false, nil
		}
	}
	return errFetch == nil && codexUsageProvesRecovery(body), errFetch
}

func codexUsageProvesRecovery(body []byte) bool {
	var root map[string]any
	if json.Unmarshal(body, &root) != nil || !onlyKeys(root, "rate_limit", "additional_rate_limits",
		"code_review_rate_limit", "plan_type", "credits", "rate_limit_reset_credits", "account_id", "user_id", "email") {
		return false
	}
	if !codexRateLimitRecovered(root["rate_limit"]) {
		return false
	}
	if _, exists := root["code_review_rate_limit"]; !exists {
		return false
	}
	// Absence is not evidence of zero additional/model-specific windows.
	additional, ok := root["additional_rate_limits"].([]any)
	if !ok {
		return false
	}
	for _, item := range additional {
		entry, ok := item.(map[string]any)
		if !ok || !onlyKeys(entry, "limit_name", "metered_feature", "rate_limit") ||
			!codexRateLimitRecovered(entry["rate_limit"]) {
			return false
		}
	}
	if review := root["code_review_rate_limit"]; review != nil && !codexRateLimitRecovered(review) {
		return false
	}
	// Credits and reset-grant balances are never substitutes for allowance.
	return true
}

func codexRateLimitRecovered(value any) bool {
	limit, ok := value.(map[string]any)
	if !ok || !onlyKeys(limit, "allowed", "limit_reached", "primary_window", "secondary_window") ||
		limit["allowed"] != true || limit["limit_reached"] != false {
		return false
	}
	windows := 0
	for _, key := range []string{"primary_window", "secondary_window"} {
		raw, exists := limit[key]
		if !exists {
			return false
		}
		if raw == nil {
			continue
		}
		window, ok := raw.(map[string]any)
		if !ok || !onlyKeys(window, "used_percent", "limit_window_seconds", "reset_after_seconds", "reset_at") ||
			!belowLimit(window["used_percent"]) {
			return false
		}
		seconds, ok := window["limit_window_seconds"].(float64)
		if !ok || seconds <= 0 {
			return false
		}
		windows++
	}
	return windows > 0
}

func (h *Handler) antigravityQuotaRecovered(ctx context.Context, auth *coreauth.Auth) (bool, error) {
	project, _ := auth.Metadata["project_id"].(string)
	if strings.TrimSpace(project) == "" {
		return false, nil
	}
	doc := ojson.NewDoc([]byte("{}"))
	doc.Set("project", project)
	for _, endpoint := range []string{antigravityQuotaRecoveryURL, sdkauth.AntigravityModelBaseURLDaily + sdkauth.AntigravityModelsPath} {
		req, errRequest := recoveryRequest(ctx, auth, http.MethodPost, endpoint, doc.Bytes())
		if errRequest != nil {
			return false, errRequest
		}
		req.Header.Set("User-Agent", sdkauth.AntigravityUserAgent())
		body, errFetch := h.fetchQuotaRecovery(req, auth)
		if errFetch != nil {
			return false, errFetch // No endpoint/account fallback after rejection.
		}
		if endpoint == antigravityQuotaRecoveryURL {
			if !antigravitySummaryRecovered(body) {
				return false, nil
			}
		} else if !antigravityModelsRecovered(body, auth) {
			return false, nil
		}
	}
	return true, nil
}

func antigravitySummaryRecovered(body []byte) bool {
	var root map[string]any
	if json.Unmarshal(body, &root) != nil || !onlyKeys(root, "groups") {
		return false
	}
	groups, ok := root["groups"].([]any)
	if !ok || len(groups) == 0 {
		return false
	}
	for _, item := range groups {
		group, ok := item.(map[string]any)
		if !ok || !onlyKeys(group, "displayName", "buckets") {
			return false
		}
		buckets, ok := group["buckets"].([]any)
		if !ok || len(buckets) == 0 {
			return false
		}
		for _, raw := range buckets {
			bucket, ok := raw.(map[string]any)
			if !ok || !onlyKeys(bucket, "bucketId", "displayName", "window", "remainingFraction", "resetTime", "description") ||
				!positiveFraction(bucket["remainingFraction"]) {
				return false
			}
		}
	}
	return true
}

func antigravityModelsRecovered(body []byte, auth *coreauth.Auth) bool {
	var root map[string]any
	if json.Unmarshal(body, &root) != nil || !onlyKeys(root, "models", "defaultModelId", "webSearchModelIds") {
		return false
	}
	models, ok := root["models"].(map[string]any)
	if !ok {
		return false
	}
	checked := 0
	for modelID, state := range auth.ModelStates {
		if state == nil || !state.Quota.Exceeded || !state.Quota.NextRecoverAt.After(time.Now()) {
			continue
		}
		// Exact upstream ID only. Aliases, missing models and "-disabled" entries
		// are not evidence of the pinned model becoming eligible.
		model, ok := models[modelID].(map[string]any)
		if !ok || !onlyKeys(model, "displayName", "modelId", "model", "quotaInfo", "disabled",
			"supportsImages", "supportsThinking", "thinkingBudget", "maxTokens") ||
			strings.Contains(modelID, "-disabled") || model["disabled"] == true {
			return false
		}
		info, ok := model["quotaInfo"].(map[string]any)
		if !ok || !onlyKeys(info, "remainingFraction", "resetTime") || !positiveFraction(info["remainingFraction"]) {
			return false
		}
		checked++
	}
	return checked > 0
}

func positiveFraction(value any) bool {
	n, ok := value.(float64)
	return ok && n > 0 && n <= 1
}

func (h *Handler) kimiQuotaRecovered(ctx context.Context, auth *coreauth.Auth) (bool, error) {
	endpoint := kimiauth.KimiAPIBaseURL + "/v1/usages"
	if kimiauth.IsKimiAIDomain(kimiauth.ResolveKimiDomainFromAuth(auth)) {
		endpoint = kimiauth.KimiAIAPIBaseURL + "/v1/usages"
	}
	req, errRequest := recoveryRequest(ctx, auth, http.MethodGet, endpoint, nil)
	if errRequest != nil {
		return false, errRequest
	}
	body, errFetch := h.fetchQuotaRecovery(req, auth)
	return errFetch == nil && kimiUsageProvesRecovery(body), errFetch
}

func kimiUsageProvesRecovery(body []byte) bool {
	var root map[string]any
	if json.Unmarshal(body, &root) != nil || !onlyKeys(root, "usage", "limits", "usages") {
		return false
	}
	checked := 0
	if usage := root["usage"]; usage != nil {
		if !kimiDetailRecovered(usage) {
			return false
		}
		checked++
	}
	limits, ok := root["limits"].([]any)
	if !ok {
		return false
	}
	for _, raw := range limits {
		limit, ok := raw.(map[string]any)
		if !ok || !onlyKeys(limit, "name", "title", "scope", "detail", "window") || !kimiDetailRecovered(limit["detail"]) {
			return false
		}
		checked++
	}
	if raw := root["usages"]; raw != nil {
		usages, ok := raw.(map[string]any)
		if !ok || !onlyKeys(usages, "limit_month_total") {
			return false
		}
		monthly, ok := usages["limit_month_total"].(map[string]any)
		if !ok || !onlyKeys(monthly, "used_ratio", "reset_time") {
			return false
		}
		ratio, ok := kimiQuotaNumber(monthly["used_ratio"])
		if !ok || ratio < 0 || ratio >= 1 {
			return false
		}
		checked++
	}
	return checked > 0
}

func kimiDetailRecovered(raw any) bool {
	detail, ok := raw.(map[string]any)
	if !ok || !onlyKeys(detail, "used", "limit", "remaining", "name", "title", "resetAt", "reset_at", "resetTime", "reset_time", "resetIn", "reset_in", "ttl") {
		return false
	}
	used, usedOK := kimiQuotaNumber(detail["used"])
	limit, limitOK := kimiQuotaNumber(detail["limit"])
	remaining, remainingOK := kimiQuotaNumber(detail["remaining"])
	return usedOK && limitOK && remainingOK && used >= 0 && limit > 0 && used < limit &&
		remaining > 0 && remaining <= limit && used+remaining <= limit
}

// Kimi's published counter schema permits JSON numbers or numeric strings.
func kimiQuotaNumber(value any) (float64, bool) {
	n, ok := value.(float64)
	if text, isString := value.(string); isString {
		var errNumber error
		n, errNumber = strconv.ParseFloat(text, 64)
		ok = errNumber == nil
	}
	return n, ok && !math.IsNaN(n) && !math.IsInf(n, 0)
}
