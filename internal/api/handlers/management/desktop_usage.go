package management

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/claudeusage"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexusage"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/lifecycle"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/resetcredits"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// requestServerShutdown stops the process; tests replace it because the real
// trigger is process-wide and fires once.
var requestServerShutdown = lifecycle.RequestShutdown

// desktopCredentialFields are the credential-list fields the desktop usage
// view reuses, so both endpoints describe a credential identically.
var desktopCredentialFields = []string{
	"id", "auth_index", "name", "provider", "label", "email", "note",
	"disabled", "status", "status_message", "priority",
}

type desktopUsageWindow struct {
	Key         string     `json:"key"`
	Label       string     `json:"label"`
	UsedPercent *float64   `json:"used_percent"`
	ResetsAt    *time.Time `json:"resets_at"`
	Status      *string    `json:"status"`
	// DurationSeconds is the window length; null when the provider does not say.
	DurationSeconds *int64 `json:"duration_seconds"`
	// Scoped marks model-, feature- or spend-specific windows; only unscoped ones drive routing.
	Scoped bool `json:"scoped"`
	// ResetCadence is "fixed" or "on_use"; null when the provider does not say.
	ResetCadence *string `json:"reset_cadence"`
}

type desktopUsageRefreshRequest struct {
	AuthIndex string `json:"auth_index"`
}

// GetUsageWindows lists every credential with its normalized usage windows and
// the credential the routing strategy would pick for a new conversation.
func (h *Handler) GetUsageWindows(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	c.JSON(http.StatusOK, h.usageWindowsPayload(time.Now()))
}

// RefreshUsageWindows looks up Claude usage now, for one credential when
// auth_index is given or for every eligible credential otherwise.
func (h *Handler) RefreshUsageWindows(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	var body desktopUsageRefreshRequest
	if errBind := c.ShouldBindJSON(&body); errBind != nil && !errors.Is(errBind, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	cfg := func() *config.Config { return h.configSnapshot() }
	poller := claudeusage.NewPoller(h.authManager, cfg)
	tracker := codexusage.NewTracker(h.authManager, cfg)
	failures := map[string]string{}
	if authIndex := strings.TrimSpace(body.AuthIndex); authIndex != "" {
		auth := h.authByIndex(authIndex)
		if auth == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
			return
		}
		var errRefresh error
		switch {
		case claudeusage.Eligible(auth):
			errRefresh = poller.Refresh(c.Request.Context(), auth)
		case codexusage.Eligible(auth):
			errRefresh = tracker.Refresh(c.Request.Context(), auth)
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "usage lookup supports enabled Claude and Codex OAuth credentials only"})
			return
		}
		if errRefresh != nil {
			failures[auth.ID] = errRefresh.Error()
		}
	} else {
		for id, errRefresh := range poller.RefreshAll(c.Request.Context()) {
			failures[id] = errRefresh.Error()
		}
		for id, errRefresh := range tracker.RefreshAll(c.Request.Context()) {
			failures[id] = errRefresh.Error()
		}
	}
	payload := h.usageWindowsPayload(time.Now())
	if len(failures) > 0 {
		payload["errors"] = failures
	}
	c.JSON(http.StatusOK, payload)
}

// ShutdownServer stops the proxy gracefully. Only callers on this machine may
// use it, even when remote management is enabled.
func (h *Handler) ShutdownServer(c *gin.Context) {
	if !isLoopbackRequest(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "shutdown is only allowed from localhost"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": "shutting down"})
	// Graceful HTTP shutdown waits for this response to finish.
	requestServerShutdown()
}

// isLoopbackRequest requires both the TCP peer and the resolved client IP to be
// loopback, so a trusted reverse proxy cannot forward remote callers.
func isLoopbackRequest(c *gin.Context) bool {
	host, _, errSplit := net.SplitHostPort(strings.TrimSpace(c.Request.RemoteAddr))
	if errSplit != nil {
		host = strings.TrimSpace(c.Request.RemoteAddr)
	}
	peer := net.ParseIP(host)
	client := net.ParseIP(c.ClientIP())
	return peer != nil && peer.IsLoopback() && client != nil && client.IsLoopback()
}

func (h *Handler) usageWindowsPayload(now time.Time) gin.H {
	strategy := ""
	preferredKind := ""
	if cfg := h.configSnapshot(); cfg != nil {
		preferredKind = coreauth.NormalizeAuthKind(cfg.Routing.PreferAuthKind)
		strategy = strings.TrimSpace(cfg.Routing.Strategy)
		if normalized, ok := normalizeRoutingStrategy(strategy); ok {
			strategy = normalized
		}
	}
	auths := h.authManager.List()
	byProvider := map[string][]*coreauth.Auth{}
	for _, auth := range auths {
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		byProvider[provider] = append(byProvider[provider], auth)
	}
	preferred := h.authManager.PreferredAuths()
	nextPick := map[string]string{}
	for provider, group := range byProvider {
		if pick := preferredNextPick(preferred[provider], group, now); pick != "" {
			nextPick[provider] = pick
			continue
		}
		if preferredKind != "" {
			var preferredGroup []*coreauth.Auth
			for _, auth := range group {
				if auth.AuthKind() == preferredKind {
					preferredGroup = append(preferredGroup, auth)
				}
			}
			if pick := coreauth.PreviewNextPick(strategy, preferredGroup, now); pick != "" {
				nextPick[provider] = pick
				continue
			}
		}
		nextPick[provider] = coreauth.PreviewNextPick(strategy, group, now)
	}
	credentials := make([]gin.H, 0, len(auths))
	for _, auth := range auths {
		entry := h.desktopCredentialEntry(auth, now)
		if entry == nil {
			continue
		}
		credential := gin.H{}
		for _, key := range desktopCredentialFields {
			if value, ok := entry[key]; ok {
				credential[key] = value
			}
		}
		if apiKey, ok := entry["api_key"]; ok {
			credential["api_key"] = apiKey
		}
		credential["cooldown_until"] = nil
		if until, ok := coreauth.CredentialCooldownUntil(auth, now); ok {
			credential["cooldown_until"] = until.UTC()
		}
		credential["observed_at"] = nil
		if observedAt := latestQuotaSnapshot(auth.Quota); !observedAt.IsZero() {
			credential["observed_at"] = observedAt.UTC()
		}
		pick := nextPick[strings.ToLower(strings.TrimSpace(auth.Provider))]
		credential["next_pick"] = pick != "" && pick == auth.ID
		credential["weight"] = coreauth.CredentialWeight(auth)
		credential["plan"] = desktopPlan(auth)
		credential["reset_credits"] = desktopResetCredits(auth)
		credential["preferred"] = preferred[strings.ToLower(strings.TrimSpace(auth.Provider))] == auth.ID
		credential["windows"] = desktopWindows(auth, now)
		credentials = append(credentials, credential)
	}
	return gin.H{"strategy": strategy, "credentials": credentials}
}

// Config-backed keys have no login file. Publish only display fields, never their raw account value.
func (h *Handler) desktopCredentialEntry(auth *coreauth.Auth, now time.Time) gin.H {
	kind, key := auth.AccountInfo()
	if !strings.EqualFold(strings.TrimSpace(kind), "api_key") {
		return h.buildAuthFileEntry(auth, authFileQuotaContext{v8: true})
	}
	auth.EnsureIndex()
	_, status, message, _ := reconcileAuthFileCooldownState(auth, now)
	masked := "••••"
	if len(key) >= 12 {
		masked = key[:4] + "…" + key[len(key)-4:]
	}
	if key != "" {
		message = strings.ReplaceAll(message, key, masked)
	}
	priority, _ := strconv.Atoi(auth.Attributes["priority"])
	return gin.H{
		"id": auth.ID, "auth_index": auth.Index, "name": auth.ID,
		"provider": auth.Provider, "label": auth.Label,
		"disabled": auth.Disabled, "status": status, "status_message": message,
		"priority": priority,
		"api_key":  gin.H{"masked": masked, "success": auth.Success, "failed": auth.Failed},
	}
}

// desktopPlan is the credential's subscription plan: Claude's from the profile lookup, Codex's
// from its login token. Nil when unknown. Codex plans publish no usage multiple.
func desktopPlan(auth *coreauth.Auth) gin.H {
	if plan, ok := claudeusage.PlanOf(auth); ok {
		var multiplier *float64
		if plan.Multiplier > 0 {
			multiplier = &plan.Multiplier
		}
		return gin.H{"tier": plan.Tier, "type": plan.Type, "label": plan.Label, "multiplier": multiplier}
	}
	if strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		if planType := strings.TrimSpace(auth.Attributes["plan_type"]); planType != "" {
			return gin.H{"tier": planType, "type": planType, "label": claudeusage.TypeLabel(planType), "multiplier": nil}
		}
	}
	return nil
}

// desktopResetCredits is a Codex or Claude account's rate-limit reset credits; nil when never reported.
func desktopResetCredits(auth *coreauth.Auth) gin.H {
	credits, ok := resetcredits.Of(auth)
	if !ok {
		return nil
	}
	expiries := make([]time.Time, len(credits.Expiries))
	for i, at := range credits.Expiries {
		expiries[i] = at.UTC()
	}
	payload := gin.H{"available": credits.Available, "applicable": credits.Applicable, "checked_at": credits.CheckedAt.UTC(), "expiries": expiries}
	if credits.Grants != nil {
		payload["grants"] = credits.Grants
	}
	return payload
}

// latestQuotaSnapshot is when the newer of the response and lookup snapshots was taken.
func latestQuotaSnapshot(quota coreauth.QuotaState) time.Time {
	if quota.LookedUpAt.After(quota.ObservedAt) {
		return quota.LookedUpAt
	}
	return quota.ObservedAt
}

func desktopWindows(auth *coreauth.Auth, now time.Time) []desktopUsageWindow {
	windows := coreauth.QuotaWindows(auth, now)
	out := make([]desktopUsageWindow, 0, len(windows))
	for _, window := range windows {
		item := desktopUsageWindow{Key: window.Key, Label: window.Label, UsedPercent: window.UsedPercent, Scoped: window.Scoped}
		if window.Duration > 0 {
			seconds := int64(window.Duration / time.Second)
			item.DurationSeconds = &seconds
		}
		if !window.ResetsAt.IsZero() {
			resetsAt := window.ResetsAt.UTC()
			item.ResetsAt = &resetsAt
		}
		if window.Status != "" {
			status := window.Status
			item.Status = &status
		}
		if window.Cadence != coreauth.ResetCadenceUnknown {
			cadence := string(window.Cadence)
			item.ResetCadence = &cadence
		}
		out = append(out, item)
	}
	return out
}
