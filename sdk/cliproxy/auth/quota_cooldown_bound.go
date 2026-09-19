package auth

import (
	"strings"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

const (
	// defaultMaxTrustedQuotaCooldown limits how far ahead an upstream supplied
	// quota reset deadline is trusted locally. Upstream deadlines are advisory:
	// an account can be usable again well before the advertised time, after a
	// manual quota reset or a plan change. Recording the full deadline makes the
	// credential unselectable until it passes, so the proxy never observes the
	// recovery and an operator has to clear the state by hand.
	defaultMaxTrustedQuotaCooldown = time.Hour

	// boundedQuotaMaxLevel caps the escalation exponent so the shift below
	// cannot overflow a time.Duration.
	boundedQuotaMaxLevel = 16
)

// boundedQuotaCooldown limits an upstream supplied quota cooldown so a credential
// that recovers early is reconsidered by the next real request instead of waiting
// out the advertised deadline.
//
// A hint no longer than maximum is trusted unchanged. A longer one is shortened to
// maximum, doubling on each window that expires without the credential recovering,
// and never exceeding what upstream actually advertised. Passing a non positive
// maximum disables the bound and restores the previous behaviour of trusting the
// hint verbatim.
//
// Escalation happens at most once per window: a still open NextRecoverAt means
// another caller already recorded this failure, which keeps concurrent failures
// from compounding the wait. This mirrors quotaCooldownAfterFailure.
func boundedQuotaCooldown(hinted time.Duration, quota QuotaState, now time.Time, maximum time.Duration) (time.Duration, int) {
	level := quota.BackoffLevel
	if level < 0 {
		level = 0
	}
	if level > boundedQuotaMaxLevel {
		level = boundedQuotaMaxLevel
	}
	if maximum <= 0 || hinted <= maximum {
		return hinted, level
	}
	// Escalate only when a real previous window has already elapsed. Keying this
	// off Quota.Exceeded would be wrong: one caller sets that flag before the
	// deadline is recomputed, which would double the very first window.
	if !quota.NextRecoverAt.IsZero() && !quota.NextRecoverAt.After(now) && level < boundedQuotaMaxLevel {
		level++
	}
	bounded := maximum << level
	if bounded <= 0 || bounded > hinted {
		bounded = hinted
	}
	return bounded, level
}

// maxTrustedQuotaCooldown resolves the configured bound for upstream supplied
// quota deadlines. An unset value uses defaultMaxTrustedQuotaCooldown; an
// explicit "0" disables the bound; an unparseable value falls back to the
// default rather than silently disabling the bound.
func (m *Manager) maxTrustedQuotaCooldown() time.Duration {
	if m == nil {
		return defaultMaxTrustedQuotaCooldown
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	return maxTrustedQuotaCooldownFromConfig(cfg)
}

func maxTrustedQuotaCooldownFromConfig(cfg *internalconfig.Config) time.Duration {
	if cfg == nil {
		return defaultMaxTrustedQuotaCooldown
	}
	raw := strings.TrimSpace(cfg.QuotaExceeded.MaxTrustedCooldown)
	if raw == "" {
		return defaultMaxTrustedQuotaCooldown
	}
	parsed, errParse := time.ParseDuration(raw)
	if errParse != nil {
		return defaultMaxTrustedQuotaCooldown
	}
	if parsed <= 0 {
		return 0
	}
	if parsed < minQuotaCooldownFloor {
		return minQuotaCooldownFloor
	}
	return parsed
}
