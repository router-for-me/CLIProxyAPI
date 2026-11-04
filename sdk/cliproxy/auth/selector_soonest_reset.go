package auth

import (
	"context"
	"strconv"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// SoonestResetSelector selects the available credential whose longest quota
// window resets soonest, so allowance that would otherwise expire unused at
// the next reset is spent first. Reset times come from the passive quota
// snapshot (Quota.Signals) recorded from upstream response headers.
//
// Credentials without an observed future reset (never used, or the observed
// window has already reset) come after credentials with one. Ties and
// unknowns keep fill-first order, so the strategy behaves like fill-first
// until reset times have been observed.
type SoonestResetSelector struct {
	nowFunc func() time.Time
}

// Pick selects the available auth with the soonest observed quota reset.
func (s *SoonestResetSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	now := time.Now()
	if s != nil && s.nowFunc != nil {
		now = s.nowFunc()
	}
	available, err := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)

	var picked *Auth
	var pickedReset time.Time
	for _, candidate := range available {
		reset, ok := quotaWindowResetAt(candidate, now)
		if !ok {
			continue
		}
		if picked == nil || reset.Before(pickedReset) {
			picked = candidate
			pickedReset = reset
		}
	}
	if picked == nil {
		return available[0], nil
	}
	return picked, nil
}

// quotaWindowResetAt reports the future reset time of the longest quota
// window in the auth's passive quota snapshot. Codex reports a primary and an
// optional secondary window; Claude reports unified 5h and 7d windows.
func quotaWindowResetAt(auth *Auth, now time.Time) (time.Time, bool) {
	if auth == nil || len(auth.Quota.Signals) == 0 {
		return time.Time{}, false
	}
	signals := auth.Quota.Signals

	var reset time.Time
	longest := int64(-1)
	for _, prefix := range []string{"X-Codex-Primary-", "X-Codex-Secondary-"} {
		windowReset, ok := codexWindowResetAt(signals, prefix, auth.Quota.ObservedAt)
		if !ok {
			continue
		}
		minutes, _ := strconv.ParseInt(strings.TrimSpace(signals[prefix+"Window-Minutes"]), 10, 64)
		if minutes > longest {
			longest = minutes
			reset = windowReset
		}
	}
	if reset.IsZero() {
		for _, key := range []string{"Anthropic-Ratelimit-Unified-7d-Reset", "Anthropic-Ratelimit-Unified-Reset"} {
			if parsed, ok := parseUnixSeconds(signals[key]); ok {
				reset = parsed
				break
			}
		}
	}
	if reset.IsZero() || !reset.After(now) {
		return time.Time{}, false
	}
	return reset, true
}

func codexWindowResetAt(signals map[string]string, prefix string, observedAt time.Time) (time.Time, bool) {
	if reset, ok := parseUnixSeconds(signals[prefix+"Reset-At"]); ok {
		return reset, true
	}
	if observedAt.IsZero() {
		return time.Time{}, false
	}
	after, err := strconv.ParseFloat(strings.TrimSpace(signals[prefix+"Reset-After-Seconds"]), 64)
	if err != nil || after < 0 {
		return time.Time{}, false
	}
	return observedAt.Add(time.Duration(after * float64(time.Second))), true
}

func parseUnixSeconds(raw string) (time.Time, bool) {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || seconds <= 0 {
		return time.Time{}, false
	}
	whole := int64(seconds)
	return time.Unix(whole, int64((seconds-float64(whole))*1e9)), true
}
