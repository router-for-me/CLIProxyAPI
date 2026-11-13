package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/MoeForge/moego/ojson"
	log "github.com/sirupsen/logrus"
)

const claudeQuotaCheckInterval = 5 * time.Minute
const claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"

// Reuse the auth refresh lifecycle so shutdown cancels outstanding quota reads.
func (m *Manager) watchClaudeQuota(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		m.reconcileClaudeQuota(ctx)
		timer.Reset(claudeQuotaCheckInterval)
	}
}

func claudeQuotaCandidate(a *Auth) bool {
	if a == nil || a.Provider != "claude" || a.AuthKind() == AuthKindAPIKey || a.Disabled || a.Status == StatusDisabled || hasUnauthorizedAuthFailure(a) || authMetadataString(a, "access_token") == "" {
		return false
	}
	if !a.Quota.Exceeded || a.Quota.Reason != "credential_quota" || !a.Quota.NextRecoverAt.After(time.Now()) {
		return false
	}
	if a.LastError != nil && a.LastError.HTTPStatus != http.StatusTooManyRequests {
		return false
	}
	// Usage does not establish recovery from unrelated model/transport failures.
	for _, s := range a.ModelStates {
		if s == nil {
			continue
		}
		if s.LastError != nil && s.LastError.HTTPStatus != http.StatusTooManyRequests {
			return false
		}
		if s.credentialQuotaShadow != nil {
			return false
		}
		if (s.Unavailable || s.Status == StatusError) && (!s.Quota.Exceeded || (s.Quota.Reason != "quota" && s.Quota.Reason != "credential_quota")) {
			return false
		}
	}
	return true
}

func (m *Manager) reconcileClaudeQuota(ctx context.Context) {
	if ctx.Err() != nil || m.HomeEnabled() {
		return
	}
	for _, a := range m.List() {
		if ctx.Err() != nil {
			return
		}
		if !claudeQuotaCandidate(a) {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeUsageURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("anthropic-beta", "oauth-2025-04-20")
		req.Header.Set("User-Agent", "CLIProxyAPI")
		resp, err := m.HttpRequest(ctx, a, req)
		if err != nil {
			continue
		}
		body, errRead := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		errClose := resp.Body.Close()
		// Redirects and incomplete responses cannot establish recovery.
		if len(body) > 1<<20 || errRead != nil || errClose != nil || resp.StatusCode != http.StatusOK || resp.Request == nil || resp.Request.Response != nil || resp.Request.URL.String() != claudeUsageURL || resp.Request.Header.Get("Authorization") != "Bearer "+authMetadataString(a, "access_token") {
			continue
		}
		if claudeUsageAvailable(body) {
			if errRestore := m.restoreClaudeQuota(ctx, a); errRestore != nil {
				log.WithError(errRestore).Warn("Failed to persist restored Claude quota")
			}
		}
	}
}

// Only a complete shared-window report with capacity in every present window
// can release an account-wide quota block. Extra usage credits are not evidence.
func claudeUsageAvailable(body []byte) bool {
	if !ojson.ValidBytes(body) {
		return false
	}
	root := ojson.ParseBytes(body)
	if !root.IsObject() {
		return false
	}
	five, week := root.Get("five_hour"), root.Get("seven_day")
	if !five.Exists() && five.Raw != "null" {
		return false
	}
	if !week.IsObject() {
		return false
	}
	available := true
	root.ForEach(func(k, v ojson.Result) bool {
		name := k.String()
		switch name {
		case "extra_usage", "spend", "member_dashboard_available", "seven_day_breakdown":
			return true
		case "weekly_scoped_shares":
			if v.IsArray() && len(v.Array()) > 0 && !root.Get("limits").IsArray() {
				available = false
				return false
			}
			if v.Raw != "null" && !v.IsArray() {
				available = false
				return false
			}
			return true
		case "limits":
			if v.Raw == "null" {
				return true
			}
			if !v.IsArray() {
				available = false
				return false
			}
			for _, limit := range v.Array() {
				percent := limit.Get("percent")
				if !limit.IsObject() || percent.Type != ojson.Number || percent.Float() < 0 || percent.Float() >= 100 {
					available = false
					return false
				}
			}
			return true
		}
		// Missing/null optional model windows carry no additional restriction.
		// Unknown populated fields must look like a validated quota window.
		if v.Raw == "null" {
			return true
		}
		locked := v.Get("locked_reason")
		if locked.Exists() && locked.Raw != "null" && locked.String() != "" {
			available = false
			return false
		}
		u := v.Get("utilization")
		if !v.IsObject() || u.Type != ojson.Number || u.Float() < 0 || u.Float() >= 100 {
			available = false
			return false
		}
		return true
	})
	return available
}

func (m *Manager) restoreClaudeQuota(ctx context.Context, snapshot *Auth) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	_, _, err := m.resetQuota(ctx, snapshot.ID, func(current *Auth) bool {
		return ctx.Err() == nil && !m.HomeEnabled() && current.RegistrationEpoch == snapshot.RegistrationEpoch && current.Generation == snapshot.Generation && claudeQuotaCandidate(current)
	})
	if err != nil {
		return fmt.Errorf("restore Claude quota: %w", err)
	}
	return nil
}
