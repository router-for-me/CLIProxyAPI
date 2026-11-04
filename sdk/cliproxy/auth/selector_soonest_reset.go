package auth

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// soonestResetProbeInterval bounds how often a credential without any quota
// observation is sent a request to learn its reset time.
const soonestResetProbeInterval = time.Hour

// SoonestResetSelector selects the available credential whose longest quota
// window resets soonest, so allowance that would otherwise expire unused at
// the next reset is spent first. Reset times come from the passive quota
// snapshot (Quota.Signals) recorded from upstream response headers.
//
// A Codex or Claude credential without a parseable reset yet (for example
// after a restart) is picked once per probe interval so its reset time is
// learned; otherwise the first observed credential would win every pick and
// the strategy would degrade to fill-first. Credentials whose observed window
// has already reset come after credentials with a future reset. Ties and
// unknowns keep fill-first order.
type SoonestResetSelector struct {
	nowFunc func() time.Time

	mu       sync.Mutex
	probedAt map[string]time.Time
}

// Pick selects the available auth with the soonest observed quota reset.
func (s *SoonestResetSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	now := time.Now()
	if s.nowFunc != nil {
		now = s.nowFunc()
	}
	available, err := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)

	if probe := s.nextProbe(available, now); probe != nil {
		return probe, nil
	}

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

// nextProbe returns the first credential without any parseable reset (no
// snapshot yet, or one that carried only partial quota headers) that has not
// been probed within soonestResetProbeInterval, and records the probe.
func (s *SoonestResetSelector) nextProbe(available []*Auth, now time.Time) *Auth {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, candidate := range available {
		if candidate == nil || !providerReportsQuotaReset(candidate.Provider) {
			continue
		}
		if _, known := quotaWindowReset(candidate); known {
			continue
		}
		if last, ok := s.probedAt[candidate.ID]; ok && now.Sub(last) < soonestResetProbeInterval {
			continue
		}
		if s.probedAt == nil {
			s.probedAt = make(map[string]time.Time)
		}
		s.probedAt[candidate.ID] = now
		return candidate
	}
	return nil
}

// providerReportsQuotaReset reports whether quotaWindowReset understands the
// provider's quota snapshot, so a probe request can teach its reset time.
func providerReportsQuotaReset(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex", "claude":
		return true
	default:
		return false
	}
}

// quotaWindowResetAt reports the future reset time of the longest quota
// window in the auth's passive quota snapshot.
func quotaWindowResetAt(auth *Auth, now time.Time) (time.Time, bool) {
	reset, ok := quotaWindowReset(auth)
	if !ok || !reset.After(now) {
		return time.Time{}, false
	}
	return reset, true
}

// quotaWindowReset reports the reset time of the longest quota window in the
// auth's passive quota snapshot, past or future. Only the current provider's
// keys are read, so a snapshot kept across an in-place provider change cannot
// leak the former provider's deadline. Codex reports a primary and an
// optional secondary window; Claude reports unified 5h and 7d windows.
func quotaWindowReset(auth *Auth) (time.Time, bool) {
	if auth == nil || len(auth.Quota.Signals) == 0 {
		return time.Time{}, false
	}
	signals := auth.Quota.Signals

	var reset time.Time
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "codex":
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
	case "claude":
		for _, key := range []string{"Anthropic-Ratelimit-Unified-7d-Reset", "Anthropic-Ratelimit-Unified-Reset"} {
			if parsed, ok := parseUnixSeconds(signals[key]); ok {
				reset = parsed
				break
			}
		}
	}
	return reset, !reset.IsZero()
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
