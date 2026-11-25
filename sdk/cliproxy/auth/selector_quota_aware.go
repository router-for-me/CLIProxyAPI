package auth

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const (
	// quotaObservationMaxAge bounds how old a passive quota snapshot may be before
	// it is ignored. Other clients sharing the credential move its usage without
	// this proxy seeing it, so an old snapshot is not evidence of current headroom.
	quotaObservationMaxAge = 6 * time.Hour
	// quotaPaceMinHorizon keeps a window that is about to reset from claiming an
	// unbounded share of traffic.
	quotaPaceMinHorizon = time.Hour
	// quotaSessionTaperBelow is the short-window remaining fraction below which a
	// credential's share is reduced, so it is not driven into a hard 429.
	quotaSessionTaperBelow = 0.15
)

// QuotaAwareSelector distributes picks in proportion to each credential's
// sustainable long-window pace: the fraction of its long (weekly) window that is
// left, divided by the hours until that window resets. Quota that is not used
// before a reset is lost, so a credential with more weekly headroom, or whose
// weekly window resets sooner, receives a larger share. This keeps credentials
// running out of weekly quota at about the same time instead of exhausting some
// early and leaving the rest bottlenecked on their short (5h) windows.
//
// A credential whose short window is nearly exhausted is tapered toward zero.
//
// Weights come from the passive quota snapshot recorded from upstream response
// headers (Claude anthropic-ratelimit-unified-5h/7d-*, Codex x-codex-primary/
// secondary-*). Credentials without a usable snapshot receive the mean weight of
// those that have one, and with no snapshots at all selection is an even split.
//
// Selection is smooth weighted round-robin over the current weights, so the
// split is proportional and deterministic. Combined with session affinity this
// only decides where new or expired sessions go.
type QuotaAwareSelector struct {
	mu      sync.Mutex
	credits map[string]map[string]float64
	maxKeys int
	// nowFunc overrides the clock in tests.
	nowFunc func() time.Time
}

// quotaWindow is one rate-limit window as a remaining fraction and reset time.
type quotaWindow struct {
	remaining float64
	resetAt   time.Time
	length    time.Duration
}

// Pick selects the next credential using quota-pace weights.
func (s *QuotaAwareSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
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
	weights := quotaAwareWeights(available, now)
	key := provider + ":" + canonicalModelKey(model)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.credits == nil {
		s.credits = make(map[string]map[string]float64)
	}
	limit := s.maxKeys
	if limit <= 0 {
		limit = 4096
	}
	if _, ok := s.credits[key]; !ok && len(s.credits) >= limit {
		s.credits = make(map[string]map[string]float64)
	}
	credits := s.credits[key]
	if credits == nil {
		credits = make(map[string]float64, len(available))
		s.credits[key] = credits
	}
	if len(credits) > maxSmoothWeightedStateEntries {
		for authID := range credits {
			if _, ok := weights[authID]; !ok {
				delete(credits, authID)
			}
		}
	}

	var picked *Auth
	total := 0.0
	for _, auth := range available {
		weight := weights[auth.ID]
		credits[auth.ID] += weight
		total += weight
		if picked == nil || credits[auth.ID] > credits[picked.ID] {
			picked = auth
		}
	}
	credits[picked.ID] -= total
	selectorLogEntry(ctx).Debugf("quota-aware: picked auth=%s share=%.2f provider=%s model=%s candidates=%d", picked.ID, weights[picked.ID]/total, provider, model, len(available))
	return picked, nil
}

// quotaAwareWeights returns a positive weight for every candidate.
func quotaAwareWeights(auths []*Auth, now time.Time) map[string]float64 {
	weights := make(map[string]float64, len(auths))
	var unknown []string
	knownSum, knownCount := 0.0, 0
	for _, auth := range auths {
		weight, ok := quotaPaceWeight(auth, now)
		if !ok {
			unknown = append(unknown, auth.ID)
			continue
		}
		weights[auth.ID] = weight
		knownSum += weight
		knownCount++
	}
	if knownSum <= 0 {
		// Nothing usable to rank on (no snapshots, or every known credential reports
		// an exhausted window while still passing availability): split evenly.
		for _, auth := range auths {
			weights[auth.ID] = 1
		}
		return weights
	}
	mean := knownSum / float64(knownCount)
	for _, authID := range unknown {
		weights[authID] = mean
	}
	return weights
}

// quotaPaceWeight computes the pace weight for one credential. ok is false when
// the credential has no recent, parseable quota snapshot.
func quotaPaceWeight(auth *Auth, now time.Time) (float64, bool) {
	short, long, ok := quotaWindowsFromSignals(auth, now)
	if !ok {
		return 0, false
	}
	horizon := long.resetAt.Sub(now)
	if horizon < quotaPaceMinHorizon {
		horizon = quotaPaceMinHorizon
	}
	weight := long.remaining / horizon.Hours()
	if short != nil {
		if short.remaining <= 0 {
			return 0, true
		}
		if short.remaining < quotaSessionTaperBelow {
			weight *= short.remaining / quotaSessionTaperBelow
		}
	}
	return weight, true
}

// quotaWindowsFromSignals extracts the short and long windows from a passive
// quota snapshot. short is nil when only one window is reported, in which case
// that window is returned as long.
func quotaWindowsFromSignals(auth *Auth, now time.Time) (short, long *quotaWindow, ok bool) {
	if auth == nil || len(auth.Quota.Signals) == 0 || auth.Quota.ObservedAt.IsZero() {
		return nil, nil, false
	}
	observedAt := auth.Quota.ObservedAt
	if now.Sub(observedAt) > quotaObservationMaxAge {
		return nil, nil, false
	}
	signals := auth.Quota.Signals
	windows := make([]quotaWindow, 0, 2)
	if window, okWindow := claudeQuotaWindow(signals, "Anthropic-Ratelimit-Unified-5h-", 5*time.Hour); okWindow {
		windows = append(windows, window)
	}
	if window, okWindow := claudeQuotaWindow(signals, "Anthropic-Ratelimit-Unified-7d-", 7*24*time.Hour); okWindow {
		windows = append(windows, window)
	}
	for _, prefix := range []string{"X-Codex-Primary-", "X-Codex-Secondary-"} {
		if window, okWindow := codexQuotaWindow(signals, prefix, observedAt); okWindow {
			windows = append(windows, window)
		}
	}
	if len(windows) == 0 {
		return nil, nil, false
	}
	for i := range windows {
		// A window whose reset has passed since the snapshot is fresh again.
		if !windows[i].resetAt.After(now) {
			windows[i].remaining = 1
			windows[i].resetAt = now.Add(windows[i].length)
		}
	}
	sort.Slice(windows, func(i, j int) bool { return windows[i].length < windows[j].length })
	long = &windows[len(windows)-1]
	if len(windows) > 1 {
		short = &windows[0]
	}
	return short, long, true
}

// claudeQuotaWindow parses anthropic-ratelimit-unified-<window>-utilization
// (fraction used, 0..1) and -reset (unix seconds).
func claudeQuotaWindow(signals map[string]string, prefix string, length time.Duration) (quotaWindow, bool) {
	utilization, okUtil := parseQuotaFloat(signals[prefix+"Utilization"])
	resetUnix, okReset := parseQuotaFloat(signals[prefix+"Reset"])
	if !okUtil || !okReset || resetUnix <= 0 {
		return quotaWindow{}, false
	}
	return quotaWindow{
		remaining: clampUnit(1 - utilization),
		resetAt:   time.Unix(int64(resetUnix), 0),
		length:    length,
	}, true
}

// codexQuotaWindow parses x-codex-<primary|secondary>-used-percent (0..100),
// -window-minutes, and -reset-at (unix seconds) or -reset-after-seconds
// (relative to the snapshot time).
func codexQuotaWindow(signals map[string]string, prefix string, observedAt time.Time) (quotaWindow, bool) {
	usedPercent, okUsed := parseQuotaFloat(signals[prefix+"Used-Percent"])
	minutes, okMinutes := parseQuotaFloat(signals[prefix+"Window-Minutes"])
	if !okUsed || !okMinutes || minutes <= 0 {
		return quotaWindow{}, false
	}
	var resetAt time.Time
	if resetUnix, okAt := parseQuotaFloat(signals[prefix+"Reset-At"]); okAt && resetUnix > 0 {
		resetAt = time.Unix(int64(resetUnix), 0)
	} else if after, okAfter := parseQuotaFloat(signals[prefix+"Reset-After-Seconds"]); okAfter && after >= 0 {
		resetAt = observedAt.Add(time.Duration(after * float64(time.Second)))
	} else {
		return quotaWindow{}, false
	}
	return quotaWindow{
		remaining: clampUnit(1 - usedPercent/100),
		resetAt:   resetAt,
		length:    time.Duration(minutes * float64(time.Minute)),
	}, true
}

func parseQuotaFloat(raw string) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	value, errParse := strconv.ParseFloat(raw, 64)
	if errParse != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

func clampUnit(value float64) float64 {
	return math.Max(0, math.Min(1, value))
}
