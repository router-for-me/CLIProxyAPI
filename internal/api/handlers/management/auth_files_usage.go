package management

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type authUsageWindow struct {
	Window          string     `json:"window"`
	Status          string     `json:"status"`
	Used            *float64   `json:"used,omitempty"`
	Limit           *float64   `json:"limit,omitempty"`
	ResetAt         *time.Time `json:"reset_at,omitempty"`
	RetryAfter      *time.Time `json:"retry_after,omitempty"`
	FirstExceededAt *time.Time `json:"first_exceeded_at,omitempty"`
	Source          string     `json:"source"`
	Confidence      string     `json:"confidence"`
	Message         string     `json:"message,omitempty"`
	Model           string     `json:"model,omitempty"`
}

// GetAuthFileUsage returns per-account quota/usage signals. It exposes only
// provider/router-reported values. Missing provider usage/limit/reset data is
// represented as unknown rather than estimated.
func (h *Handler) GetAuthFileUsage(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(503, gin.H{"error": "core auth manager unavailable"})
		return
	}
	nameFilter := strings.TrimSpace(c.Query("name"))
	authIndexFilter := strings.TrimSpace(c.Query("auth_index"))
	now := time.Now()
	out := make([]gin.H, 0)
	all := h.authManager.List()
	selected := make([]*coreauth.Auth, 0, len(all))
	for _, auth := range all {
		if matchesAuthFileLookup(auth, nameFilter, authIndexFilter) {
			selected = append(selected, auth)
		}
	}
	prewarmProviderUsage(selected)
	for _, auth := range selected {
		entry := h.buildAuthFileEntry(auth)
		if entry == nil {
			continue
		}
		entry["quota_windows"] = buildAuthUsageWindows(auth)
		entry["usage_source"] = "router/account-state"
		entry["usage_generated_at"] = now.UTC()
		entry["usage_note"] = "Only retry_after/reset_at fields present in provider/router state are exact. Unknown windows mean the provider did not expose per-account usage, limit, or reset data to CLIProxy."
		out = append(out, entry)
	}
	c.JSON(200, gin.H{"files": out})
}

func buildAuthUsageWindows(auth *coreauth.Auth) []authUsageWindow {
	if auth == nil {
		return nil
	}
	windows := make([]authUsageWindow, 0, 4)
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	msg := strings.TrimSpace(auth.StatusMessage)
	lower := strings.ToLower(msg)

	if !auth.NextRetryAfter.IsZero() {
		retry := auth.NextRetryAfter.UTC()
		windows = append(windows, authUsageWindow{
			Window:     "router_retry",
			Status:     "cooling",
			RetryAfter: &retry,
			ResetAt:    &retry,
			Source:     "router_next_retry_after",
			Confidence: "exact",
			Message:    msg,
		})
	}
	if auth.Quota.Exceeded || !auth.Quota.FirstExceededAt.IsZero() || !auth.Quota.NextRecoverAt.IsZero() || strings.TrimSpace(auth.Quota.Reason) != "" {
		w := authUsageWindow{Window: "account_quota", Status: "cooling", Source: "router_quota_state", Confidence: "router-state", Message: strings.TrimSpace(auth.Quota.Reason)}
		if w.Message == "" {
			w.Message = msg
		}
		if !auth.Quota.NextRecoverAt.IsZero() {
			t := auth.Quota.NextRecoverAt.UTC()
			w.ResetAt = &t
			w.RetryAfter = &t
		}
		if !auth.Quota.FirstExceededAt.IsZero() {
			t := auth.Quota.FirstExceededAt.UTC()
			w.FirstExceededAt = &t
		}
		windows = append(windows, w)
	}
	for model, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		if state.Unavailable || state.Quota.Exceeded || !state.NextRetryAfter.IsZero() || strings.TrimSpace(state.StatusMessage) != "" {
			w := authUsageWindow{Window: "model_quota", Model: model, Status: string(state.Status), Source: "router_model_state", Confidence: "router-state", Message: strings.TrimSpace(state.StatusMessage)}
			if !state.NextRetryAfter.IsZero() {
				t := state.NextRetryAfter.UTC()
				w.RetryAfter = &t
				w.ResetAt = &t
				w.Confidence = "exact"
			}
			if !state.Quota.NextRecoverAt.IsZero() {
				t := state.Quota.NextRecoverAt.UTC()
				w.ResetAt = &t
			}
			if !state.Quota.FirstExceededAt.IsZero() {
				t := state.Quota.FirstExceededAt.UTC()
				w.FirstExceededAt = &t
			}
			if w.Message == "" {
				w.Message = strings.TrimSpace(state.Quota.Reason)
			}
			windows = append(windows, w)
		}
	}

	if provider == "xai" {
		if strings.Contains(lower, "rolling 24") || strings.Contains(lower, "usage balance") || strings.Contains(lower, "grok build") || strings.Contains(lower, "free-usage") || strings.Contains(lower, "included free usage") {
			windows = append(windows, authUsageWindow{Window: "24h", Status: "cooling", Source: "provider_error_message", Confidence: "window-only", Message: msg})
		}
	}
	windows = append(windows, providerUsageWindows(auth)...)

	if len(windows) == 0 {
		windows = append(windows, authUsageWindow{Window: "account", Status: "active", Source: "router_account_state", Confidence: "exact"})
	}
	return windows
}
