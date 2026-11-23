package auth

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// refreshAccessError keeps an expired access token blocked without turning an
// uncertain refresh exchange into evidence of an upstream access-token rejection.
// wasTerminalUnauthorized describes the state before applying this refresh result.
func refreshAccessError(auth *Auth, message string, accessTokenRejected, wasTerminalUnauthorized bool) *Error {
	if auth != nil && strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") &&
		!accessTokenRejected && !wasTerminalUnauthorized &&
		(auth.RejectedAccessToken == "" || auth.RejectedAccessToken != authAccessToken(auth)) &&
		auth.RefreshError != nil && auth.RefreshError.Code == "refresh_outcome_unknown" {
		outcome := cloneError(auth.RefreshError)
		if outcome.HTTPStatus == http.StatusUnauthorized {
			// A diagnostic 401 must not turn this recovery state into access rejection.
			outcome.HTTPStatus = http.StatusServiceUnavailable
		}
		return outcome
	}
	return &Error{Code: "unauthorized", Message: message, HTTPStatus: http.StatusUnauthorized}
}

// codexRefreshMissingAccessForRequest keeps model-level availability from
// admitting a missing token while its grant still requires recovery.
func codexRefreshMissingAccessForRequest(ctx context.Context, auth *Auth, headers ...http.Header) bool {
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") || !auth.RequiresSignIn ||
		auth.RefreshError == nil || auth.RefreshError.Code != "refresh_outcome_unknown" {
		return false
	}
	return authAccessTokenForRequest(ctx, auth, headers...) == ""
}

// codexRefreshUnavailableForRequest lets a request use a healthy outer token
// while SIWC requires recovery. Ordinary cooldowns and model failures still apply.
func codexRefreshUnavailableForRequest(ctx context.Context, auth *Auth, now time.Time, headers ...http.Header) bool {
	if !auth.Unavailable || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") ||
		auth.LastError == nil || auth.LastError.Code != "refresh_outcome_unknown" || !auth.RequiresSignIn ||
		!IsCodexUseSIWC(auth) || IsCodexUseSIWCForRequest(ctx, auth, headers...) || auth.NextRetryAfter.After(now) ||
		auth.RejectedSIWCRefreshToken == "" || auth.RejectedSIWCRefreshToken != authSIWCTokenField(auth, "refresh_token") {
		return auth.Unavailable
	}
	if authOuterAccessToken(auth) == "" {
		return true
	}
	expiry, hasExpiry := auth.OuterExpirationTime()
	return hasExpiry && !expiry.After(now)
}
