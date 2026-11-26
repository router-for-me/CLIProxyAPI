package api

import (
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// oauthUsageWindowKeys maps Claude unified rate-limit window names (from the
// anthropic-ratelimit-unified-<window>-* headers) to the keys Anthropic's
// GET /api/oauth/usage response uses.
var oauthUsageWindowKeys = map[string]string{
	"5h":        "five_hour",
	"7d":        "seven_day",
	"7d_opus":   "seven_day_opus",
	"7d_sonnet": "seven_day_sonnet",
}

// handleOAuthUsage serves GET /api/oauth/usage, the endpoint Claude Code polls
// for subscription usage. It never calls Anthropic: the answer is built from
// the latest quota snapshot the proxy already recorded from upstream response
// headers, so client polls cost no upstream requests.
//
// The request carries no session identity, so it cannot be tied to the
// credential a client's session is bound to. It reports the Claude credential
// soonest-reset routing would bind a new session to; when no available
// credential has a known weekly reset, the available credential with the
// newest snapshot, and failing that the newest snapshot of any enabled Claude
// credential. Windows without data are null.
func (s *Server) handleOAuthUsage(c *gin.Context) {
	var auths []*coreauth.Auth
	if s.handlers != nil && s.handlers.AuthManager != nil {
		auths = s.handlers.AuthManager.List()
	}
	c.JSON(http.StatusOK, oauthUsagePayload(auths, time.Now()))
}

func oauthUsagePayload(auths []*coreauth.Auth, now time.Time) gin.H {
	payload := gin.H{}
	for _, key := range oauthUsageWindowKeys {
		payload[key] = nil
	}
	auth := oauthUsageAuth(auths, now)
	if auth == nil {
		return payload
	}
	for _, window := range oauthUsageWindows(auth) {
		key := oauthUsageWindowKeys[window.Name]
		item := gin.H{"utilization": nil, "resets_at": nil}
		switch {
		case !window.ResetAt.IsZero() && !window.ResetAt.After(now):
			// The window rolled over after the snapshot; the next one starts
			// with the next request, as with an idle window upstream.
			item["utilization"] = 0.0
		default:
			if window.UsedPercent != nil {
				item["utilization"] = math.Round(*window.UsedPercent*100) / 100
			}
			if !window.ResetAt.IsZero() {
				item["resets_at"] = window.ResetAt.UTC().Format(time.RFC3339)
			}
		}
		payload[key] = item
	}
	return payload
}

// oauthUsageAuth picks the Claude credential whose quota /api/oauth/usage
// reports. It returns nil when no enabled Claude credential has quota data.
func oauthUsageAuth(auths []*coreauth.Auth, now time.Time) *coreauth.Auth {
	claude := make([]*coreauth.Auth, 0, len(auths))
	for _, auth := range auths {
		if auth == nil || auth.Disabled || auth.Status == coreauth.StatusDisabled ||
			!strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
			continue
		}
		claude = append(claude, auth)
	}

	ranked, preferredID := coreauth.PreviewSoonestReset(claude, "claude", "", now)
	var newestAvailable, newestAny *coreauth.Auth
	for _, candidate := range ranked {
		auth := candidate.Auth
		if preferredID != "" && auth.ID == preferredID {
			return auth
		}
		if len(oauthUsageWindows(auth)) == 0 {
			continue
		}
		if candidate.Available && newerQuotaSnapshot(auth, newestAvailable) {
			newestAvailable = auth
		}
		if newerQuotaSnapshot(auth, newestAny) {
			newestAny = auth
		}
	}
	if newestAvailable != nil {
		return newestAvailable
	}
	return newestAny
}

// oauthUsageWindows returns the reported windows of auth that carry a
// utilization or reset time; status-only windows have nothing to show.
func oauthUsageWindows(auth *coreauth.Auth) []coreauth.QuotaWindow {
	var windows []coreauth.QuotaWindow
	for _, window := range coreauth.QuotaWindowsForAuth(auth) {
		if _, ok := oauthUsageWindowKeys[window.Name]; ok && (window.UsedPercent != nil || !window.ResetAt.IsZero()) {
			windows = append(windows, window)
		}
	}
	return windows
}

func newerQuotaSnapshot(auth, than *coreauth.Auth) bool {
	return than == nil || auth.Quota.ObservedAt.After(than.Quota.ObservedAt)
}
