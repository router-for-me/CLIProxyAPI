package auth

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// weeklyWindowMinutes is the length of a weekly quota window. Codex reports
// window lengths in minutes; anything at least this long counts as weekly.
const weeklyWindowMinutes = 7 * 24 * 60

// ResetFirstSelector spends quota that is about to expire first. Among the
// available credentials in the best priority tier it picks the one whose
// weekly quota window resets soonest, so an account that resets tomorrow is
// drained before one that resets in four days.
//
// Reset times come from the passive quota signals each upstream response
// carries (Anthropic unified rate-limit headers, Codex x-codex-* headers and
// websocket rate-limit events). A credential with no observed reset is tried
// before the others so that it gets observed; after that it is ordered like
// the rest. Ties keep the deterministic fill-first order.
type ResetFirstSelector struct {
	// now is overridable in tests.
	now func() time.Time
}

// Pick selects the available credential whose weekly window resets soonest.
func (s *ResetFirstSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	now := time.Now()
	if s != nil && s.now != nil {
		now = s.now()
	}
	available, err := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	return SortByWeeklyReset(available, now)[0], nil
}

// SortByWeeklyReset returns a copy of auths ordered for reset-first routing:
// credentials without a known reset first, then by soonest weekly reset.
func SortByWeeklyReset(auths []*Auth, now time.Time) []*Auth {
	type ranked struct {
		auth  *Auth
		reset time.Time
		known bool
	}
	items := make([]ranked, len(auths))
	for i, a := range auths {
		reset, ok := WeeklyResetAt(a, now)
		items[i] = ranked{auth: a, reset: reset, known: ok}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].known != items[j].known {
			return !items[i].known
		}
		return items[i].reset.Before(items[j].reset)
	})
	out := make([]*Auth, len(items))
	for i, it := range items {
		out[i] = it.auth
	}
	return out
}

// WeeklyResetAt reports when the credential's weekly quota window next
// resets, as last observed from upstream. Resets already in the past are
// stale and reported as unknown.
func WeeklyResetAt(a *Auth, now time.Time) (time.Time, bool) {
	if a == nil || len(a.Quota.Signals) == 0 {
		return time.Time{}, false
	}
	var reset time.Time
	switch strings.ToLower(strings.TrimSpace(a.Provider)) {
	case "claude":
		reset = claudeWeeklyReset(a.Quota.Signals)
	case "codex":
		reset = codexWeeklyReset(a.Quota.Signals, a.Quota.ObservedAt)
	}
	if reset.IsZero() || !reset.After(now) {
		return time.Time{}, false
	}
	return reset, true
}

// claudeWeeklyReset reads the shared 7-day window reset. Model-scoped weekly
// windows (7d_<model>) share the same weekly boundary, so they only stand in
// when the shared header is missing.
func claudeWeeklyReset(signals map[string]string) time.Time {
	if t := unixSignal(signals, "Anthropic-Ratelimit-Unified-7d-Reset"); !t.IsZero() {
		return t
	}
	var earliest time.Time
	for key := range signals {
		lower := strings.ToLower(key)
		if !strings.HasPrefix(lower, "anthropic-ratelimit-unified-7d_") || !strings.HasSuffix(lower, "-reset") {
			continue
		}
		if t := unixSignal(signals, key); !t.IsZero() && (earliest.IsZero() || t.Before(earliest)) {
			earliest = t
		}
	}
	return earliest
}

// codexWeeklyReset picks the longest of the primary and secondary windows,
// which is the weekly one on every current plan.
func codexWeeklyReset(signals map[string]string, observedAt time.Time) time.Time {
	var best time.Time
	bestMinutes := -1
	for _, window := range []string{"Primary", "Secondary"} {
		prefix := "X-Codex-" + window + "-"
		minutes, errMinutes := strconv.Atoi(strings.TrimSpace(signals[prefix+"Window-Minutes"]))
		if errMinutes != nil {
			minutes = 0
			if window == "Secondary" {
				// Secondary is the weekly window when no length is reported.
				minutes = weeklyWindowMinutes
			}
		}
		reset := unixSignal(signals, prefix+"Reset-At")
		if reset.IsZero() && !observedAt.IsZero() {
			if secs, errSecs := strconv.ParseInt(strings.TrimSpace(signals[prefix+"Reset-After-Seconds"]), 10, 64); errSecs == nil && secs > 0 {
				reset = observedAt.Add(time.Duration(secs) * time.Second)
			}
		}
		if reset.IsZero() || minutes <= bestMinutes {
			continue
		}
		best, bestMinutes = reset, minutes
	}
	if bestMinutes < weeklyWindowMinutes {
		return time.Time{}
	}
	return best
}

// unixSignal parses a Unix-seconds signal value, ignoring key case.
func unixSignal(signals map[string]string, key string) time.Time {
	raw, ok := signals[key]
	if !ok {
		for k, v := range signals {
			if strings.EqualFold(k, key) {
				raw, ok = v, true
				break
			}
		}
	}
	if !ok {
		return time.Time{}
	}
	secs, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || secs <= 0 {
		return time.Time{}
	}
	return time.Unix(secs, 0)
}
