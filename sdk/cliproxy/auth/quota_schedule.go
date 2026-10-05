package auth

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// QuotaResetSchedule is an in-memory observation from a provider usage endpoint.
// It does not determine credential availability or release quota cooldowns.
type QuotaResetSchedule struct {
	WeeklyResetAt   time.Time `json:"weekly_reset_at"`
	FiveHourResetAt time.Time `json:"five_hour_reset_at"`
	ObservedAt      time.Time `json:"observed_at"`
}

func quotaScheduleIdentityMatches(a, b *Auth) bool {
	for _, key := range []string{"account_id", "email", "sub"} {
		left, _ := a.Metadata[key].(string)
		right, _ := b.Metadata[key].(string)
		if left != right {
			return false
		}
	}
	return true
}

// EffectiveQuotaResetSchedule uses the newest available provider observation.
// Header observations arrive on ordinary requests and require no usage probe.
func EffectiveQuotaResetSchedule(auth *Auth) QuotaResetSchedule {
	if auth == nil {
		return QuotaResetSchedule{}
	}
	schedule := auth.QuotaResetSchedule
	if !auth.Quota.ObservedAt.After(schedule.ObservedAt) {
		return schedule
	}
	headers := make(http.Header, len(auth.Quota.Signals))
	for name, value := range auth.Quota.Signals {
		headers.Set(name, value)
	}
	observed := quotaScheduleFromHeaders(auth.Provider, headers, auth.Quota.ObservedAt)
	if observed.WeeklyResetAt.IsZero() && observed.FiveHourResetAt.IsZero() {
		return schedule
	}
	return observed
}

func quotaScheduleFromHeaders(provider string, headers http.Header, observedAt time.Time) QuotaResetSchedule {
	schedule := QuotaResetSchedule{ObservedAt: observedAt}
	reset := func(name string) time.Time {
		seconds, err := strconv.ParseInt(strings.TrimSpace(headers.Get(name)), 10, 64)
		if err != nil || seconds <= 0 || seconds > 253402300799 {
			return time.Time{}
		}
		return time.Unix(seconds, 0).UTC()
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude":
		schedule.WeeklyResetAt = reset("Anthropic-Ratelimit-Unified-7d-Reset")
		schedule.FiveHourResetAt = reset("Anthropic-Ratelimit-Unified-5h-Reset")
	case "codex":
		for _, window := range []string{"primary", "secondary"} {
			prefix := "X-Codex-" + window
			minutes, err := strconv.ParseInt(strings.TrimSpace(headers.Get(prefix+"-Window-Minutes")), 10, 64)
			if err != nil || (minutes != 10080 && minutes != 300) {
				continue
			}
			at := reset(prefix + "-Reset-At")
			if at.IsZero() {
				seconds, errSeconds := strconv.ParseInt(strings.TrimSpace(headers.Get(prefix+"-Reset-After-Seconds")), 10, 64)
				if errSeconds == nil && seconds >= 0 && seconds <= 604800 && !observedAt.IsZero() {
					at = observedAt.Add(time.Duration(seconds) * time.Second)
				}
			}
			if minutes == 10080 {
				schedule.WeeklyResetAt = at
			} else {
				schedule.FiveHourResetAt = at
			}
		}
	}
	return schedule
}

// RecordQuotaResetScheduleIfUnchanged publishes usage reset times only while
// the credential inspected before the request remains current. Availability,
// cooldowns, tokens and persisted credential files are left untouched.
func (m *Manager) RecordQuotaResetScheduleIfUnchanged(ctx context.Context, expected *Auth, schedule QuotaResetSchedule) *Auth {
	if m == nil || expected == nil || expected.ID == "" || schedule.ObservedAt.IsZero() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	release, err := m.lockAuthMutationContext(ctx, expected.ID)
	if err != nil {
		return nil
	}
	defer release()
	m.mu.Lock()
	current := m.auths[expected.ID]
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if ctx.Err() != nil || (cfg != nil && cfg.Home.Enabled) || current == nil || current.Disabled || current.Status == StatusDisabled ||
		current.Provider != expected.Provider || current.RegistrationEpoch != expected.RegistrationEpoch || current.Generation != expected.Generation ||
		current.QuotaResetSchedule.ObservedAt.After(schedule.ObservedAt) {
		m.mu.Unlock()
		return nil
	}
	updated := current.Clone()
	updated.QuotaResetSchedule = schedule
	updated.Generation++
	updated.UpdatedAt = schedule.ObservedAt
	m.auths[updated.ID] = updated
	m.notifyAuthChangeLocked(updated.ID)
	snapshot := updated.Clone()
	m.mu.Unlock()
	if m.scheduler != nil {
		m.scheduler.upsertAuth(snapshot)
	}
	return snapshot
}
