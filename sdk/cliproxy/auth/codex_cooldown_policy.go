package auth

import (
	"strings"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

const (
	legacyUnauthorizedCooldown = 30 * time.Minute
	adaptiveCooldownMax        = 5 * time.Minute
	defaultCooldownJitter      = 20
	quotaProbeNearThreshold    = 2 * time.Hour
	quotaProbeFarInterval      = 30 * time.Minute
	quotaProbeNearInterval     = 5 * time.Minute
)

var defaultAdaptiveBackoff = [...]time.Duration{
	15 * time.Second,
	30 * time.Second,
	time.Minute,
	2 * time.Minute,
	5 * time.Minute,
}

type codexCooldownPolicy struct {
	adaptive          bool
	unauthorized      time.Duration
	transientBackoff  []time.Duration
	jitterPercent     int
	quotaProbeEnabled bool
}

func (m *Manager) codexCooldownPolicyForAuth(auth *Auth) codexCooldownPolicy {
	policy := codexCooldownPolicy{unauthorized: legacyUnauthorizedCooldown}
	if m == nil || auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return policy
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil || !cfg.Codex.QuotaCooldown.Enabled {
		return policy
	}
	configured := cfg.Codex.QuotaCooldown
	policy.adaptive = true
	policy.quotaProbeEnabled = true
	if configured.UnauthorizedCooldownSeconds > 0 {
		policy.unauthorized = time.Duration(configured.UnauthorizedCooldownSeconds) * time.Second
	}
	policy.transientBackoff = normalizeAdaptiveBackoff(configured.TransientBackoffSeconds)
	policy.jitterPercent = configured.JitterPercent
	if policy.jitterPercent <= 0 {
		policy.jitterPercent = defaultCooldownJitter
	}
	if policy.jitterPercent > 100 {
		policy.jitterPercent = 100
	}
	return policy
}

func normalizeAdaptiveBackoff(configured []int) []time.Duration {
	if len(configured) == 0 {
		out := make([]time.Duration, len(defaultAdaptiveBackoff))
		copy(out, defaultAdaptiveBackoff[:])
		return out
	}
	out := make([]time.Duration, 0, len(configured))
	for _, seconds := range configured {
		if seconds <= 0 {
			continue
		}
		cooldown := adaptiveCooldownMax
		if seconds < int(adaptiveCooldownMax/time.Second) {
			cooldown = time.Duration(seconds) * time.Second
		}
		out = append(out, cooldown)
	}
	if len(out) == 0 {
		return normalizeAdaptiveBackoff(nil)
	}
	return out
}

func isCodexUsageLimitResult(auth *Auth, resultErr *Error) bool {
	if auth == nil || resultErr == nil || resultErr.StatusCode() != 429 {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return false
	}
	return strings.Contains(strings.ToLower(resultErr.Code+" "+resultErr.Message), "usage_limit_reached")
}

func jitterAdaptiveCooldown(base time.Duration, jitterPercent int, randomUnit float64) time.Duration {
	if base <= 0 {
		return 0
	}
	if base > adaptiveCooldownMax {
		base = adaptiveCooldownMax
	}
	if jitterPercent <= 0 {
		return base
	}
	if randomUnit < 0 {
		randomUnit = 0
	} else if randomUnit > 1 {
		randomUnit = 1
	}
	spread := float64(jitterPercent) / 100
	factor := 1 - spread + 2*spread*randomUnit
	cooldown := time.Duration(float64(base) * factor)
	if cooldown > adaptiveCooldownMax {
		return adaptiveCooldownMax
	}
	if cooldown < time.Second {
		return time.Second
	}
	return cooldown
}

func nextCodexQuotaProbeAt(now, resetAt time.Time) time.Time {
	if resetAt.IsZero() {
		return now
	}
	remaining := resetAt.Sub(now)
	if remaining <= 0 {
		return now
	}
	interval := quotaProbeFarInterval
	if remaining <= quotaProbeNearThreshold {
		interval = quotaProbeNearInterval
	}
	next := now.Add(interval)
	if next.After(resetAt) {
		return resetAt
	}
	return next
}

func (m *Manager) normalizeRestoredCodexCooldownRecord(auth *Auth, record CooldownStateRecord, now time.Time) CooldownStateRecord {
	policy := m.codexCooldownPolicyForAuth(auth)
	if !policy.adaptive || record.LastError == nil || record.LastError.StatusCode() != 429 || record.Quota.Reason != "quota" {
		return record
	}
	if isCodexUsageLimitResult(auth, record.LastError) {
		record.Quota.Reason = "usage_limit_reached"
		record.Reason = "usage_limit_reached"
		if record.Quota.NextRecoverAt.IsZero() {
			record.Quota.NextRecoverAt = record.NextRetryAfter
		}
		record.Quota.NextProbeAt = nextCodexQuotaProbeAt(now, record.Quota.NextRecoverAt)
		return record
	}
	next, level := quotaCooldownAfterFailure(QuotaState{}, now, policy, 0.5)
	record.NextRetryAfter = next
	record.Quota = QuotaState{
		Exceeded:      true,
		Reason:        "rate_limit",
		NextRecoverAt: next,
		BackoffLevel:  level,
	}
	record.Reason = "rate_limit"
	return record
}

func (m *Manager) reconcileCodexQuotaCooldownConfig() bool {
	if m == nil {
		return false
	}
	now := time.Now()
	changed := false
	snapshots := make([]*Auth, 0)
	m.mu.Lock()
	for _, auth := range m.auths {
		if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") || m.cooldownDisabledForAuth(auth) {
			continue
		}
		policy := m.codexCooldownPolicyForAuth(auth)
		authChanged := reconcileCodexQuotaState(auth, &auth.Quota, &auth.NextRetryAfter, &auth.Unavailable, &auth.Status, &auth.StatusMessage, &auth.LastError, policy, now)
		modelChanged := false
		for _, state := range auth.ModelStates {
			if state == nil {
				continue
			}
			if reconcileCodexQuotaState(auth, &state.Quota, &state.NextRetryAfter, &state.Unavailable, &state.Status, &state.StatusMessage, &state.LastError, policy, now) {
				state.UpdatedAt = now
				modelChanged = true
			}
		}
		if modelChanged && !auth.Quota.Exceeded {
			updateAggregatedAvailability(auth, now)
		}
		if authChanged || modelChanged {
			auth.UpdatedAt = now
			snapshots = append(snapshots, auth.Clone())
			changed = true
		}
	}
	m.mu.Unlock()
	if m.scheduler != nil {
		for _, snapshot := range snapshots {
			m.scheduler.upsertAuth(snapshot)
		}
	}
	if changed {
		m.signalCodexQuotaProbe()
	}
	return changed
}

func reconcileCodexQuotaState(auth *Auth, quota *QuotaState, nextRetry *time.Time, unavailable *bool, status *Status, statusMessage *string, lastError **Error, policy codexCooldownPolicy, now time.Time) bool {
	if quota == nil || nextRetry == nil || unavailable == nil || status == nil || statusMessage == nil || lastError == nil || *lastError == nil || (*lastError).StatusCode() != 429 || !quota.Exceeded {
		return false
	}
	if policy.adaptive {
		if quota.Reason != "quota" {
			return false
		}
		if isCodexUsageLimitResult(auth, *lastError) {
			quota.Reason = "usage_limit_reached"
			if quota.NextRecoverAt.IsZero() {
				quota.NextRecoverAt = *nextRetry
			}
			quota.NextProbeAt = nextCodexQuotaProbeAt(now, quota.NextRecoverAt)
			return true
		}
		next, level := quotaCooldownAfterFailure(QuotaState{}, now, policy, 0.5)
		*quota = QuotaState{Exceeded: true, Reason: "rate_limit", NextRecoverAt: next, BackoffLevel: level}
		*nextRetry = next
		return true
	}
	if quota.Reason != "usage_limit_reached" {
		return false
	}
	quota.NextProbeAt = time.Time{}
	quota.Reason = "quota"
	if quota.NextRecoverAt.After(now) {
		*nextRetry = quota.NextRecoverAt
		*unavailable = true
		return true
	}
	*quota = QuotaState{}
	*nextRetry = time.Time{}
	*unavailable = false
	*status = StatusActive
	*statusMessage = ""
	*lastError = nil
	return true
}
