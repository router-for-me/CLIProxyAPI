package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// ResetQuotaIfUnchanged recovers a local quota cooldown only while the inspected
// credential snapshot is still current. The caller must first confirm provider
// capacity for every affected quota window and model.
func (m *Manager) ResetQuotaIfUnchanged(ctx context.Context, expected *Auth) (*Auth, []string, error) {
	if expected == nil || strings.TrimSpace(expected.ID) == "" || strings.TrimSpace(expected.Provider) == "" {
		return nil, nil, nil
	}
	return m.resetQuota(ctx, expected.ID, expected)
}

// QuotaRecoveryCandidate reports whether all restrictions on a snapshot are
// attributable to quota. It neither confirms provider capacity nor mutates auth.
func QuotaRecoveryCandidate(auth *Auth) bool {
	if auth == nil || auth.Disabled || strings.TrimSpace(auth.ID) == "" || strings.TrimSpace(auth.Provider) == "" {
		return false
	}
	if !quotaRecoverySIWCAccountMatches(auth) {
		return false
	}
	known, safe := quotaRecoveryState(auth.Provider, auth.Status, auth.StatusMessage, auth.Unavailable, auth.NextRetryAfter, auth.Quota, auth.LastError)
	if !safe {
		return false
	}
	now := time.Now()
	if auth.nonQuotaCooldownUntil.After(now) || effectiveAuthForceUntil(auth).After(now) || effectiveCredQuotaForceUntil(auth).After(now) {
		return false
	}
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		// A subscription refresh cannot release an independent model failure
		// hidden beneath a later credential-wide quota restriction.
		if isModelStateActiveCooldown(state.credentialQuotaShadow, now) {
			return false
		}
		if state.nonQuotaCooldownUntil.After(now) || effectiveModelForceUntil(state).After(now) {
			return false
		}
		message, lastError := state.StatusMessage, state.LastError
		// A propagated-only layer owns the current quota restriction. Its retained
		// sibling diagnostic describes an expired failure, not this active layer.
		if state.credentialQuotaPropagated && !isModelStateActiveCooldown(state.credentialQuotaShadow, now) && state.Quota.Exceeded && state.Quota.Reason == "credential_quota" {
			message, lastError = "credential_quota", nil
		}
		modelKnown, modelSafe := quotaRecoveryState(auth.Provider, state.Status, message, state.Unavailable, state.NextRetryAfter, state.Quota, lastError)
		if !modelSafe {
			return false
		}
		known = known || modelKnown
	}
	return known
}

func quotaRecoveryState(provider string, status Status, message string, unavailable bool, retry time.Time, quota QuotaState, lastError *Error) (bool, bool) {
	if status != "" && status != StatusActive && status != StatusError {
		return false, false
	}
	reason := strings.TrimSpace(quota.Reason)
	if reason != "" && reason != "quota" && reason != "credential_quota" {
		return false, false
	}
	known := quota.Exceeded && (reason == "quota" || reason == "credential_quota")
	if (quota.Exceeded || reason != "") && !known {
		return false, false
	}
	if lastError != nil && (lastError.Code == ErrorCodeForceCooldown || statusCodeFromResult(lastError) != http.StatusTooManyRequests || isInvalidGrantResultError(lastError) || isCloudflareChallengeResultError(lastError)) {
		return false, false
	}
	if known && strings.EqualFold(provider, "codex") && lastError != nil && !quotaCodexSubscriptionError(lastError) {
		return false, false
	}
	if known && strings.EqualFold(provider, "claude") && lastError != nil && quotaClaudePerMinuteError(lastError) {
		return false, false
	}
	if !retry.IsZero() && (!known || quota.NextRecoverAt.IsZero() || retry.After(quota.NextRecoverAt)) {
		return false, false
	}
	if (!quota.NextRecoverAt.IsZero() || quota.BackoffLevel != 0 || unavailable || status == StatusError || lastError != nil) && !known {
		return false, false
	}
	// Nil errors occur on restored or credential-quota propagated states. Do not
	// infer quota ownership from an unrelated diagnostic on those states.
	if lastError == nil {
		switch strings.ToLower(strings.TrimSpace(message)) {
		case "", "quota", "credential_quota", "quota exhausted":
		default:
			return false, false
		}
	}
	return known, true
}

// quotaResetMatchesLocked requires m.mu and the credential mutation gate.
func (m *Manager) quotaResetMatchesLocked(auth, expected *Auth) bool {
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	return auth != nil && !(cfg != nil && cfg.Home.Enabled) && auth.ID == expected.ID && auth.Provider == expected.Provider &&
		auth.RegistrationEpoch == expected.RegistrationEpoch && auth.Generation == expected.Generation &&
		QuotaRecoveryCandidate(expected) && QuotaRecoveryCandidate(auth) &&
		m.quotaRecoveryModelHeadersMatchLocked(expected) && m.quotaRecoveryModelHeadersMatchLocked(auth)
}

// A store may import newer credentials from disk into the staged snapshot.
// Recovery evidence still belongs to the identity inspected before that save.
func quotaRecoverySavedIdentityMatches(saved, expected *Auth) bool {
	if saved.ID != expected.ID || saved.Provider != expected.Provider || CredentialsChanged(saved, expected) {
		return false
	}
	for _, key := range []string{"account_id", "email", "sub", "account_uuid", "organization_uuid"} {
		actual, _ := saved.Metadata[key].(string)
		inspected, _ := expected.Metadata[key].(string)
		if actual != inspected {
			return false
		}
	}
	return quotaRecoverySIWCAccountMatches(saved)
}

// Codex uses 429 for subscription limits and independent request/capacity limits.
// Only the subscription failure is established by the plan usage observation.
func quotaCodexSubscriptionError(err *Error) bool {
	if err.Code == ErrorCodeCredentialQuota {
		return true
	}
	for _, path := range []string{"error.type", "type", "body.error.type", "response.error.type"} {
		if ojson.Get(err.Message, path).String() == "usage_limit_reached" {
			return true
		}
	}
	return false
}

// Claude's explicit per-minute throttles are independent of subscription windows.
func quotaClaudePerMinuteError(err *Error) bool {
	var envelope struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(err.Message), &envelope) != nil || envelope.Error.Type != "rate_limit_error" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(envelope.Error.Message)) {
	case "this request would exceed the requests per minute rate limit. please try again later.",
		"this request would exceed the input tokens per minute rate limit. please try again later.",
		"this request would exceed the output tokens per minute rate limit. please try again later.":
		return true
	default:
		return false
	}
}
