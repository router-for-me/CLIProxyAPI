package auth

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// ResetAwareSelector prefers the available credential whose long-window quota
// resets soonest. Credentials currently limited by their five-hour window are
// excluded; priority and ID provide deterministic tie-breaking. The selector
// deliberately receives all available priority tiers so a low-priority
// credential with an earlier reset can be selected.
type ResetAwareSelector struct{}

// Pick selects an available credential by long-window reset time. The session
// affinity wrapper retains an established binding and invokes this selector
// only for a new or failed-over session.
func (s *ResetAwareSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	now := time.Now()
	available, err := getSelectorAvailableAuthsAcrossPriorities(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	filtered, err := resetAwareAvailableAuths(available, provider, model, now)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		leftReset, leftOK := authLongWindowReset(filtered[i], provider, now)
		rightReset, rightOK := authLongWindowReset(filtered[j], provider, now)
		if leftOK != rightOK {
			return leftOK
		}
		if leftOK && !leftReset.Equal(rightReset) {
			return leftReset.Before(rightReset)
		}
		leftPriority, rightPriority := authPriority(filtered[i]), authPriority(filtered[j])
		if leftPriority != rightPriority {
			return leftPriority > rightPriority
		}
		return filtered[i].ID < filtered[j].ID
	})
	return filtered[0], nil
}

func authSignal(auth *Auth, key string) string {
	if auth == nil || auth.Quota.Signals == nil {
		return ""
	}
	return strings.TrimSpace(auth.Quota.Signals[http.CanonicalHeaderKey(key)])
}

func authSignalTime(auth *Auth, key string) (time.Time, bool) {
	raw := authSignal(auth, key)
	if raw == "" {
		return time.Time{}, false
	}
	if seconds, err := strconv.ParseFloat(raw, 64); err == nil && seconds > 0 && !math.IsInf(seconds, 0) && !math.IsNaN(seconds) && seconds < 1e11 {
		whole := int64(seconds)
		return time.Unix(whole, int64((seconds-float64(whole))*1e9)), true
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, http.TimeFormat} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func authCodexWindowReset(auth *Auth, prefix string) (time.Time, bool) {
	if reset, ok := authSignalTime(auth, prefix+"Reset-At"); ok {
		return reset, true
	}
	seconds, err := strconv.ParseInt(authSignal(auth, prefix+"Reset-After-Seconds"), 10, 64)
	if err == nil && seconds >= 0 && seconds <= 31*24*60*60 && !auth.Quota.ObservedAt.IsZero() {
		return auth.Quota.ObservedAt.Add(time.Duration(seconds) * time.Second), true
	}
	return time.Time{}, false
}

func authLongWindowReset(auth *Auth, provider string, now time.Time) (time.Time, bool) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude", "anthropic":
		// Fable's seven-day window takes precedence over the shared window.
		for _, key := range []string{"Anthropic-Ratelimit-Unified-7d_oi-Reset", "Anthropic-Ratelimit-Unified-7d-Reset"} {
			if reset, ok := authSignalTime(auth, key); ok {
				return reset, reset.After(now)
			}
		}
	case "codex", "openai":
		// Codex can put the weekly window in primary (for example Pro Lite).
		for _, prefix := range []string{"X-Codex-Primary-", "X-Codex-Secondary-"} {
			minutes, _ := strconv.Atoi(authSignal(auth, prefix+"Window-Minutes"))
			if minutes == 10080 || (minutes == 0 && prefix == "X-Codex-Secondary-") {
				if reset, ok := authCodexWindowReset(auth, prefix); ok {
					return reset, reset.After(now)
				}
			}
		}
	}
	return time.Time{}, false
}

// resetAwareAvailableAuths is shared with affinity so a quota-limited bound
// credential is failed over before its cached binding can be reused. It never
// invents a next weekly deadline after a reset; a new provider observation must
// supply that deadline (Codex restarts its weekly window on reset).
func resetAwareAvailableAuths(auths []*Auth, provider, model string, now time.Time) ([]*Auth, error) {
	available := make([]*Auth, 0, len(auths))
	earliest := time.Time{}
	for _, auth := range auths {
		blocked, next := authObservedQuotaBlock(auth, provider, now)
		if !blocked {
			available = append(available, auth)
		} else if next.After(now) && (earliest.IsZero() || next.Before(earliest)) {
			earliest = next
		}
	}
	if len(available) != 0 {
		return available, nil
	}
	if !earliest.IsZero() {
		return nil, newModelCooldownError(model, provider, earliest.Sub(now))
	}
	return nil, &Error{Code: "auth_unavailable", Message: "all credentials are quota limited"}
}

func authObservedQuotaBlock(auth *Auth, provider string, now time.Time) (bool, time.Time) {
	if auth == nil {
		return true, time.Time{}
	}
	blocked := false
	latest := time.Time{}
	check := func(limited bool, reset time.Time, known bool) {
		if !limited || (known && !reset.After(now)) {
			return
		}
		blocked = true
		if reset.After(latest) {
			latest = reset
		}
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex", "openai":
		for _, prefix := range []string{"X-Codex-Primary-", "X-Codex-Secondary-"} {
			used, err := strconv.ParseFloat(authSignal(auth, prefix+"Used-Percent"), 64)
			reset, known := authCodexWindowReset(auth, prefix)
			limited := strings.EqualFold(authSignal(auth, prefix+"Limit-Reached"), "true") || (err == nil && used >= 100)
			check(limited, reset, known)
		}
	case "claude", "anthropic":
		for _, window := range []string{"5h", "7d", "7d_oi"} {
			prefix := "Anthropic-Ratelimit-Unified-" + window + "-"
			status := authSignal(auth, prefix+"Status")
			used, err := strconv.ParseFloat(authSignal(auth, prefix+"Utilization"), 64)
			reset, known := authSignalTime(auth, prefix+"Reset")
			check(strings.EqualFold(status, "rejected") || (status == "" && err == nil && used >= 1), reset, known)
		}
	}
	return blocked, latest
}
