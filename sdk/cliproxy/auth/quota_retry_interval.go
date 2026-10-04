package auth

import (
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func quotaRetryInterval(cfg *internalconfig.Config) time.Duration {
	if cfg == nil || cfg.QuotaRetryIntervalSeconds <= 0 || cfg.QuotaRetryIntervalSeconds > int((1<<63-1)/time.Second) {
		return 0
	}
	interval := time.Duration(cfg.QuotaRetryIntervalSeconds) * time.Second
	if interval < minQuotaCooldownFloor {
		interval = minQuotaCooldownFloor
	}
	return interval
}

// boundQuotaRetryDeadline leaves independent failure deadlines intact. Inherited
// quota states have no LastError; a non-429 or forced failure remains authoritative.
func boundQuotaRetryDeadline(retry *time.Time, quota *QuotaState, lastError *Error, interval time.Duration, now time.Time) bool {
	if interval <= 0 || !quota.Exceeded || (quota.Reason != "quota" && quota.Reason != "credential_quota") || quota.NextRecoverAt.IsZero() {
		return false
	}
	if lastError != nil && (lastError.HTTPStatus != 429 || lastError.Code == ErrorCodeForceCooldown) {
		return false
	}
	ceiling := now.Add(interval)
	if !quota.NextRecoverAt.After(ceiling) {
		return false
	}
	// Compare before changing the quota deadline: equal timestamps are the same
	// quota restriction, while a longer retry timestamp may be an unrelated fault.
	if !retry.IsZero() && !retry.After(quota.NextRecoverAt) {
		*retry = ceiling
	}
	quota.NextRecoverAt = ceiling
	return true
}

func boundAuthQuotaRetries(auth *Auth, cfg *internalconfig.Config, now time.Time) bool {
	interval := quotaRetryInterval(cfg)
	if interval == 0 || auth == nil || auth.Disabled || auth.Status == StatusDisabled || hasUnauthorizedAuthFailure(auth) || quotaCooldownDisabledForAuthWithConfig(auth, cfg) {
		return false
	}
	changed := boundQuotaRetryDeadline(&auth.NextRetryAfter, &auth.Quota, auth.LastError, interval, now)
	for _, state := range auth.ModelStates {
		if state != nil && state.Status != StatusDisabled {
			changed = boundQuotaRetryDeadline(&state.NextRetryAfter, &state.Quota, state.LastError, interval, now) || changed
		}
	}
	return changed
}

func (m *Manager) publishQuotaRetrySnapshot(auth *Auth, now time.Time) {
	if m.scheduler != nil {
		m.scheduler.upsertAuth(auth)
	}
	models, epoch := registry.GetGlobalRegistry().GetModelsAndEpochForClient(auth.ID)
	projections := make([]registry.ClientModelProjection, 0, len(models))
	for _, model := range models {
		if model != nil {
			projections = append(projections, m.clientModelProjectionForAuth(auth, model.ID, now))
		}
	}
	registry.GetGlobalRegistry().ApplyClientModelProjections(auth.ID, epoch, auth.Generation, projections)
}
