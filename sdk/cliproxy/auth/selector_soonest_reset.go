package auth

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// weeklyWindowMinutes is the length of the weekly quota window advertised by Codex.
const weeklyWindowMinutes = 7 * 24 * 60

// SoonestResetSelector prefers the credential whose weekly (7-day) quota window resets
// soonest, so quota that would otherwise expire unused at the reset is consumed first.
// Once that credential is exhausted it enters cooldown and the next-soonest one is used.
//
// Reset times come from the passive quota snapshot (Quota.Signals) captured from upstream
// response headers. Credentials without a known future reset, either never observed or
// with a reset that already passed, are picked first so a single request can learn their
// current window. Ties fall back to ID order, which makes providers without quota
// signals behave like fill-first.
type SoonestResetSelector struct{}

// Pick selects the available auth whose weekly quota window resets soonest.
func (s *SoonestResetSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	now := time.Now()
	available, err := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	var picked *Auth
	var pickedReset time.Time
	for _, auth := range available {
		resetAt := weeklyQuotaResetAt(auth, now)
		if picked == nil || resetAt.Before(pickedReset) {
			picked = auth
			pickedReset = resetAt
		}
	}
	if picked == nil {
		return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
	}
	return picked, nil
}

// weeklyQuotaResetAt returns the observed weekly quota reset time for auth, or the zero
// time when no future reset is known.
func weeklyQuotaResetAt(auth *Auth, now time.Time) time.Time {
	if auth == nil || len(auth.Quota.Signals) == 0 {
		return time.Time{}
	}
	signals := auth.Quota.Signals
	signal := func(name string) string {
		return strings.TrimSpace(signals[http.CanonicalHeaderKey(name)])
	}
	var resetAt time.Time
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "claude":
		resetAt = parseQuotaResetAt(signal("Anthropic-Ratelimit-Unified-7d-Reset"), "", auth.Quota.ObservedAt)
	case "codex":
		// Codex reports the weekly limit as the secondary window. Accounts without a
		// secondary window may expose the weekly limit as the primary one instead.
		resetAt = parseQuotaResetAt(signal("X-Codex-Secondary-Reset-At"), signal("X-Codex-Secondary-Reset-After-Seconds"), auth.Quota.ObservedAt)
		if resetAt.IsZero() {
			if minutes, errParse := strconv.ParseInt(signal("X-Codex-Primary-Window-Minutes"), 10, 64); errParse == nil && minutes >= weeklyWindowMinutes {
				resetAt = parseQuotaResetAt(signal("X-Codex-Primary-Reset-At"), signal("X-Codex-Primary-Reset-After-Seconds"), auth.Quota.ObservedAt)
			}
		}
	}
	if !resetAt.After(now) {
		return time.Time{}
	}
	return resetAt
}

// parseQuotaResetAt resolves a reset time from an absolute unix timestamp or, failing
// that, from a relative seconds value anchored at the observation time.
func parseQuotaResetAt(resetAt, resetAfterSeconds string, observedAt time.Time) time.Time {
	if unix, errParse := strconv.ParseFloat(resetAt, 64); errParse == nil && unix > 0 {
		return time.Unix(int64(unix), 0)
	}
	if parsed, errParse := time.Parse(time.RFC3339, resetAt); errParse == nil {
		return parsed
	}
	if observedAt.IsZero() {
		return time.Time{}
	}
	if seconds, errParse := strconv.ParseFloat(resetAfterSeconds, 64); errParse == nil && seconds >= 0 {
		return observedAt.Add(time.Duration(seconds * float64(time.Second)))
	}
	return time.Time{}
}
