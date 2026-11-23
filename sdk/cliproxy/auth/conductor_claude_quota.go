package auth

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MoeForge/moego/json"
)

const claudeQuotaRecheckInterval = 15 * time.Minute

func canRecheckClaudeCredentialQuota(auth *Auth, now time.Time) bool {
	if auth == nil || auth.Provider != "claude" || auth.AuthKind() != AuthKindOAuth ||
		auth.Disabled || auth.Status == StatusDisabled || auth.RequiresSignIn || hasUnauthorizedAuthFailure(auth) {
		return false
	}
	if !auth.Quota.Exceeded || auth.Quota.Reason != "credential_quota" || !auth.Quota.NextRecoverAt.After(now) ||
		effectiveCredQuotaForceUntil(auth).After(now) || effectiveAuthForceUntil(auth).After(now) ||
		auth.quotaForceCooldownUntil.After(now) || auth.nonQuotaCooldownUntil.After(now) ||
		auth.credentialRetryAfter.After(now) || auth.nonAuthCredentialRetryAfter.After(now) {
		return false
	}
	if base := strings.TrimSpace(auth.Attributes["base_url"]); base != "" {
		upstream, err := url.Parse(base)
		if err != nil || upstream.Scheme != "https" || !strings.EqualFold(upstream.Host, "api.anthropic.com") {
			return false
		}
	}
	signals := auth.Quota.cooldownSignals
	return signals["Anthropic-Ratelimit-Unified-5h-Status"] == "rejected" || signals["Anthropic-Ratelimit-Unified-7d-Status"] == "rejected"
}

func isClaudeQuotaReportingModel(state *ModelState) bool {
	return state != nil && state.credentialQuotaPropagated && state.Quota.Reason == "quota"
}

func (m *Manager) runClaudeQuotaRechecks(ctx context.Context) {
	ticker := time.NewTicker(claudeQuotaRecheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.recheckClaudeCredentialQuotas(ctx)
		}
	}
}

func (m *Manager) recheckClaudeCredentialQuotas(ctx context.Context) {
	if m.HomeEnabled() {
		return
	}
	executor, ok := m.Executor("claude")
	if !ok {
		return
	}
	for _, auth := range m.List() {
		if ctx.Err() != nil {
			return
		}
		if !canRecheckClaudeCredentialQuota(auth, time.Now()) {
			continue
		}
		model := ""
		for candidate, state := range auth.ModelStates {
			if isClaudeQuotaReportingModel(state) && (model == "" || candidate < model) {
				model = candidate
			}
		}
		if model != "" && claudeUsageShowsHeadroom(m.WithAuthTransport(ctx, auth), executor, auth) {
			// Keep the cooldown unless a request on the reporting model completes.
			_, _ = m.verifyInference(ctx, auth.ID, model, auth)
		}
	}
}

func claudeUsageShowsHeadroom(ctx context.Context, executor ProviderExecutor, auth *Auth) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.anthropic.com/api/oauth/usage", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Anthropic-Beta", "oauth-2025-04-20")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := executor.HttpRequest(ctx, auth, req)
	if err != nil || resp == nil || resp.Body == nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	type usageWindow struct {
		Utilization *float64 `json:"utilization"`
	}
	var usage struct {
		FiveHour usageWindow `json:"five_hour"`
		SevenDay usageWindow `json:"seven_day"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&usage); err != nil {
		return false
	}
	for _, window := range []usageWindow{usage.FiveHour, usage.SevenDay} {
		if window.Utilization == nil || *window.Utilization < 0 || *window.Utilization >= 100 {
			return false
		}
	}
	return true
}
