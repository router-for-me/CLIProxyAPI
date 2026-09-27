package management

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type codexQuotaRecoveryResponse struct {
	AccountID string `json:"account_id"`
	RateLimit struct {
		Allowed      bool `json:"allowed"`
		LimitReached bool `json:"limit_reached"`
	} `json:"rate_limit"`
	ModelUsage map[string]struct {
		Available bool `json:"available"`
	} `json:"model_usage"`
}

func (h *Handler) reconcileCodexQuotaRecovery(ctx context.Context, auth *coreauth.Auth, req *http.Request, resp *http.Response, body []byte) {
	if h.authManager == nil || auth == nil || !strings.EqualFold(auth.Provider, "codex") ||
		auth.Disabled || auth.Status == coreauth.StatusDisabled || h.authManager.HomeEnabled() ||
		!isCodexUsageRequest(req) || resp.StatusCode != http.StatusOK || !isCodexUsageRequest(resp.Request) {
		return
	}
	accountID, _ := auth.Metadata["account_id"].(string)
	if accountID == "" {
		return
	}
	var usage codexQuotaRecoveryResponse
	if errDecode := json.Unmarshal(body, &usage); errDecode != nil || usage.AccountID != accountID ||
		!usage.RateLimit.Allowed || usage.RateLimit.LimitReached || !codexQuotaSnapshotRecovered(auth, usage) {
		return
	}
	if _, _, errReset := h.authManager.ResetQuotaIfUnchanged(ctx, auth); errReset != nil {
		log.WithError(errReset).Warn("failed to reconcile Codex quota recovery")
	}
}

func isCodexUsageRequest(req *http.Request) bool {
	return req != nil && req.URL != nil && req.Method == http.MethodGet && req.URL.Scheme == "https" &&
		strings.EqualFold(req.URL.Hostname(), "chatgpt.com") && req.URL.Path == "/backend-api/wham/usage" &&
		(req.Host == "" || strings.EqualFold(req.Host, req.URL.Host))
}

func codexQuotaSnapshotRecovered(auth *coreauth.Auth, usage codexQuotaRecoveryResponse) bool {
	if !auth.Quota.Exceeded || !isQuotaCooldownReason(auth.Quota.Reason) || !isCodexUsageLimitFailure(auth.LastError) {
		return false
	}
	confirmed := false
	for model, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		if state.Status == coreauth.StatusDisabled {
			return false
		}
		if !state.Unavailable && !state.Quota.Exceeded && state.LastError == nil &&
			state.Status != coreauth.StatusError && state.NextRetryAfter.IsZero() {
			continue
		}
		if !state.Quota.Exceeded || !isQuotaCooldownReason(state.Quota.Reason) {
			return false
		}
		// Credential-wide quota failures also gate siblings that have no failure
		// of their own. Their restrictions recover with the original quota failure.
		if state.Quota.Reason == "credential_quota" && state.LastError == nil {
			continue
		}
		if !isCodexUsageLimitFailure(state.LastError) || !usage.ModelUsage[model].Available {
			return false
		}
		confirmed = true
	}
	return confirmed
}

func isQuotaCooldownReason(reason string) bool {
	return reason == "quota" || reason == "credential_quota"
}

func isCodexUsageLimitFailure(err *coreauth.Error) bool {
	if err == nil || err.HTTPStatus != http.StatusTooManyRequests || err.Code == coreauth.ErrorCodeForceCooldown {
		return false
	}
	if err.Code == "usage_limit_reached" {
		return true
	}
	var failure struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	return json.Unmarshal([]byte(err.Message), &failure) == nil && failure.Error.Type == "usage_limit_reached"
}
