package auth

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"
)

// Ranking classes for the soonest-reset strategy, best first.
const (
	// soonestResetClassProbe is a subscription whose weekly reset is not known yet and has no
	// probe pending: no snapshot since start reported usage windows, or its weekly window
	// rolled over. One request usually learns the reset, because every response that
	// carries quota headers replaces the quota snapshot.
	soonestResetClassProbe = iota
	// soonestResetClassKnown is a credential whose weekly window reset is known and ahead.
	soonestResetClassKnown
	// soonestResetClassSubscription is a subscription without a known weekly reset: its probe
	// is pending, or its plan reports usage windows but no weekly one.
	soonestResetClassSubscription
	// soonestResetClassOther is a credential with no weekly reset to learn: API keys and
	// providers without quota observation.
	soonestResetClassOther
)

const (
	quotaWindowWeek = 7 * 24 * time.Hour
	quotaWindowDay  = 24 * time.Hour
	// quotaWindowFallback bounds a used-up window that reports neither reset nor length.
	quotaWindowFallback = 5 * time.Hour
	// maxQuotaSignalSeconds bounds numeric signals (about 31 years in seconds).
	maxQuotaSignalSeconds = 1e9
	// soonestResetProbeHold is how long a probe pick reserves a credential: other picks rank
	// it as a pending subscription meanwhile, so concurrent requests do not pile onto it
	// before its response (for streams, the end of the stream) records the reset.
	soonestResetProbeHold = 5 * time.Minute
)

// soonestResetFacts is what a credential's quota snapshot says, independent of the clock.
type soonestResetFacts struct {
	// subscription reports that a request can teach the weekly reset: Claude and Codex
	// subscription responses carry it in quota headers.
	subscription bool
	// learnable reports that the snapshot does not settle the weekly reset: nothing was
	// observed, no usage window was reported, or the weekly window came without a reset.
	learnable bool
	// windowed reports that the snapshot reported at least one usage window.
	windowed bool
	// weeklyReset is the reported weekly window reset; zero when unknown.
	weeklyReset time.Time
	// exhaustedUntil is when the latest used-up usage window ends; zero when none is used up.
	exhaustedUntil time.Time
}

// soonestResetRank orders credentials for the soonest-reset strategy.
type soonestResetRank struct {
	// exhausted reports that an observed usage window is used up and has not reset yet.
	exhausted   bool
	class       int
	weeklyReset time.Time
}

// soonestResetFactsFor parses the credential-level quota snapshot, which holds the latest
// observation from any model.
func soonestResetFactsFor(auth *Auth) soonestResetFacts {
	if auth == nil {
		return soonestResetFacts{}
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	observedAt := auth.Quota.ObservedAt
	weekly, hasWeekly, windows := quotaWindowsFor(provider, auth.Quota)
	facts := soonestResetFacts{subscription: soonestResetSubscription(provider, auth)}
	if hasWeekly {
		facts.weeklyReset = weekly.reset
		windows = append(windows, weekly)
	}
	for _, window := range windows {
		if until := window.exhaustedUntil(observedAt); until.After(facts.exhaustedUntil) {
			facts.exhaustedUntil = until
		}
	}
	if len(windows) == 0 && provider == "claude" {
		facts.exhaustedUntil = claudeUnifiedRejectionEnd(auth.Quota.Signals, observedAt)
	}
	facts.windowed = len(windows) > 0
	facts.learnable = facts.subscription &&
		(observedAt.IsZero() || !facts.windowed || (hasWeekly && weekly.reset.IsZero()))
	return facts
}

// carry returns f, or previous when f reports no usage window but previous does. Such a
// snapshot says nothing about the windows: Claude's request-scoped fast-mode credit refusal,
// for example, carries unified quota headers without any window, and must not make a
// credential whose weekly reset is known rank as unknown. A rejection without windows still
// keeps the credential used up until it ends.
func (f soonestResetFacts) carry(previous soonestResetFacts) soonestResetFacts {
	if f.windowed || !previous.windowed || f.subscription != previous.subscription {
		return f
	}
	if f.exhaustedUntil.After(previous.exhaustedUntil) {
		previous.exhaustedUntil = f.exhaustedUntil
	}
	return previous
}

// rank ranks the facts at now. probePending reports a recent probe pick of the credential.
func (f soonestResetFacts) rank(now time.Time, probePending bool) soonestResetRank {
	rank := soonestResetRank{exhausted: f.exhaustedUntil.After(now), class: soonestResetClassOther}
	switch {
	case f.weeklyReset.After(now):
		rank.class = soonestResetClassKnown
		rank.weeklyReset = f.weeklyReset
	case !f.subscription:
		// Nothing to learn: API keys and providers without quota observation.
	case (f.learnable || !f.weeklyReset.IsZero()) && !probePending:
		// A weekly reset that passed means the window rolled over since it was observed.
		rank.class = soonestResetClassProbe
	default:
		rank.class = soonestResetClassSubscription
	}
	return rank
}

// less reports whether r ranks strictly before other: usable before used-up credentials,
// then by class, then known weekly resets earliest first.
func (r soonestResetRank) less(other soonestResetRank) bool {
	if r.exhausted != other.exhausted {
		return !r.exhausted
	}
	if r.class != other.class {
		return r.class < other.class
	}
	if r.class == soonestResetClassKnown && !r.weeklyReset.Equal(other.weeklyReset) {
		return r.weeklyReset.Before(other.weeklyReset)
	}
	return false
}

// soonestResetPrefers reports whether a candidate ranked rank with ID id should replace the
// current best. Equal ranks resolve to the lowest ID, matching fill-first.
func soonestResetPrefers(rank soonestResetRank, id string, best soonestResetRank, bestID string) bool {
	if rank.less(best) {
		return true
	}
	return !best.less(rank) && id < bestID
}

// soonestResetProbes records when credentials were last picked to learn their weekly reset.
type soonestResetProbes map[string]time.Time

type soonestResetProbeSuppressedKey struct{}

// withoutSoonestResetProbe marks picks whose results never record quota observations, such as
// token counts and SelectAuth callers, so they do not spend a soonest-reset probe.
func withoutSoonestResetProbe(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, soonestResetProbeSuppressedKey{}, true)
}

// soonestResetProbeAllowed reports whether a pick made with ctx may record a probe.
func soonestResetProbeAllowed(ctx context.Context) bool {
	if ctx == nil {
		return true
	}
	suppressed, _ := ctx.Value(soonestResetProbeSuppressedKey{}).(bool)
	return !suppressed
}

// pending reports whether a probe pick of id is recent enough to still await its response.
func (p soonestResetProbes) pending(id string, now time.Time) bool {
	pickedAt, ok := p[id]
	return ok && now.Sub(pickedAt) < soonestResetProbeHold
}

// record notes a probe pick of id, dropping expired entries so the map stays bounded by the
// number of credentials probed within one hold.
func (p *soonestResetProbes) record(id string, now time.Time) {
	if *p == nil {
		*p = make(soonestResetProbes)
	}
	for key, pickedAt := range *p {
		if now.Sub(pickedAt) >= soonestResetProbeHold {
			delete(*p, key)
		}
	}
	(*p)[id] = now
}

type soonestResetFactsKey struct{}

// withSoonestResetFacts hands the soonest-reset selector facts by auth ID that were recorded
// as results arrived.
func withSoonestResetFacts(ctx context.Context, facts map[string]soonestResetFacts) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, soonestResetFactsKey{}, facts)
}

func soonestResetFactsFromContext(ctx context.Context) map[string]soonestResetFacts {
	if ctx == nil {
		return nil
	}
	facts, _ := ctx.Value(soonestResetFactsKey{}).(map[string]soonestResetFacts)
	return facts
}

// selectorUsesSoonestReset reports whether selector picks with SoonestResetSelector, alone
// or as the session affinity fallback.
func selectorUsesSoonestReset(selector Selector) bool {
	switch s := selector.(type) {
	case *SoonestResetSelector:
		return true
	case *SessionAffinitySelector:
		_, ok := s.fallback.(*SoonestResetSelector)
		return ok
	default:
		return false
	}
}

// withSoonestResetFactsForSelector hands a soonest-reset selector the scheduler's facts for
// auths. The scheduler records them as results arrive and carries them across snapshots
// without usage windows; the selector sees snapshots only when it picks, and session
// affinity serves most requests without a pick.
func (m *Manager) withSoonestResetFactsForSelector(ctx context.Context, selector Selector, auths []*Auth) context.Context {
	if m == nil || m.scheduler == nil || !selectorUsesSoonestReset(selector) {
		return ctx
	}
	ids := make([]string, 0, len(auths))
	for _, auth := range auths {
		if auth != nil {
			ids = append(ids, auth.ID)
		}
	}
	return withSoonestResetFacts(ctx, m.scheduler.soonestResetFacts(ids))
}

// pickSoonestResetAuth returns the auth whose weekly usage window resets soonest, ranking an
// auth by its facts in known when present, else by its own snapshot. Pending probes in
// probes, when given, are respected; a pick that probes an unknown reset is recorded there
// when recordProbe is set.
func pickSoonestResetAuth(auths []*Auth, now time.Time, known map[string]soonestResetFacts, probes *soonestResetProbes, recordProbe bool) *Auth {
	var pending soonestResetProbes
	if probes != nil {
		pending = *probes
	}
	var best *Auth
	var bestRank soonestResetRank
	for _, candidate := range auths {
		if candidate == nil {
			continue
		}
		facts, ok := known[candidate.ID]
		if !ok {
			facts = soonestResetFactsFor(candidate)
		}
		rank := facts.rank(now, pending.pending(candidate.ID, now))
		if best == nil || soonestResetPrefers(rank, candidate.ID, bestRank, best.ID) {
			best = candidate
			bestRank = rank
		}
	}
	if best != nil && probes != nil && recordProbe && bestRank.class == soonestResetClassProbe {
		probes.record(best.ID, now)
	}
	return best
}

// soonestResetSubscription reports whether a request can teach the credential's weekly reset,
// mirroring how the executors decide that a credential bills a subscription.
func soonestResetSubscription(provider string, auth *Auth) bool {
	switch provider {
	case "claude":
		apiKey := strings.TrimSpace(auth.Attributes[AttributeAPIKey])
		if strings.Contains(apiKey, "sk-ant-oat") {
			// Claude setup tokens configured as API keys are sent as subscription OAuth.
			return true
		}
		return apiKey == "" && auth.AuthKind() != AuthKindAPIKey
	case "codex":
		return auth.AuthKind() != AuthKindAPIKey
	default:
		return false
	}
}

// quotaWindow is one usage window read from quota signals; each part may be missing.
type quotaWindow struct {
	usedUp bool
	reset  time.Time
	length time.Duration
}

// exhaustedUntil reports until when a used-up window holds: its reset, else its length (or
// quotaWindowFallback) after the observation. It is zero when the window is not used up.
func (w quotaWindow) exhaustedUntil(observedAt time.Time) time.Time {
	if !w.usedUp {
		return time.Time{}
	}
	if !w.reset.IsZero() {
		return w.reset
	}
	if observedAt.IsZero() {
		return time.Time{}
	}
	length := w.length
	if length <= 0 {
		length = quotaWindowFallback
	}
	return observedAt.Add(length)
}

// quotaWindowsFor reads the weekly window, when reported, plus the other usage windows from a
// quota snapshot. Unknown providers report nothing.
func quotaWindowsFor(provider string, quota QuotaState) (weekly quotaWindow, hasWeekly bool, others []quotaWindow) {
	if len(quota.Signals) == 0 {
		return quotaWindow{}, false, nil
	}
	switch provider {
	case "claude":
		return claudeQuotaWindows(quota.Signals)
	case "codex":
		return codexQuotaWindows(quota)
	case "devin":
		return devinQuotaWindows(quota.Signals)
	default:
		return quotaWindow{}, false, nil
	}
}

// claudeQuotaWindows reads the shared 7-day and 5-hour unified windows. Keys are canonical
// header names because collectQuotaSignals canonicalizes them.
func claudeQuotaWindows(signals map[string]string) (quotaWindow, bool, []quotaWindow) {
	read := func(label string, length time.Duration) (quotaWindow, bool) {
		prefix := "Anthropic-Ratelimit-Unified-" + label + "-"
		status := strings.TrimSpace(signals[prefix+"Status"])
		utilization, hasUtilization := quotaSignalNumber(signals, prefix+"Utilization")
		reset, hasReset := parseQuotaResetTime(signals[prefix+"Reset"])
		if status == "" && !hasUtilization && !hasReset {
			return quotaWindow{}, false
		}
		return quotaWindow{
			usedUp: strings.EqualFold(status, "rejected") || (hasUtilization && utilization >= 1),
			reset:  reset,
			length: length,
		}, true
	}
	weekly, hasWeekly := read("7d", quotaWindowWeek)
	var others []quotaWindow
	if short, ok := read("5h", 5*time.Hour); ok {
		others = append(others, short)
	}
	return weekly, hasWeekly, others
}

// claudeUnifiedRejectionEnd reports until when a unified rejection that names no usage window
// holds: the unified reset, else the representative claim's window length (or
// quotaWindowFallback) after the observation. Overage rejections say nothing about the
// subscription windows, so they, like snapshots without a rejection, report zero.
func claudeUnifiedRejectionEnd(signals map[string]string, observedAt time.Time) time.Time {
	if !strings.EqualFold(strings.TrimSpace(signals["Anthropic-Ratelimit-Unified-Status"]), "rejected") {
		return time.Time{}
	}
	claim := strings.ToLower(strings.TrimSpace(signals["Anthropic-Ratelimit-Unified-Representative-Claim"]))
	if strings.Contains(claim, "overage") {
		return time.Time{}
	}
	window := quotaWindow{usedUp: true}
	window.reset, _ = parseQuotaResetTime(signals["Anthropic-Ratelimit-Unified-Reset"])
	switch claim {
	case "five_hour":
		window.length = 5 * time.Hour
	case "seven_day":
		window.length = quotaWindowWeek
	}
	return window.exhaustedUntil(observedAt)
}

// codexQuotaWindows reads the main primary and secondary windows. The weekly window is
// whichever is longer, since plans differ in which header carries it; a window without a
// length loses only to one at least a day long, so without lengths secondary is weekly.
// Additional-limit namespaces (X-Codex-<name>-...) are ignored.
func codexQuotaWindows(quota QuotaState) (quotaWindow, bool, []quotaWindow) {
	read := func(label string) (quotaWindow, bool) {
		prefix := "X-Codex-" + label + "-"
		used, hasUsed := quotaSignalNumber(quota.Signals, prefix+"Used-Percent")
		reset, hasReset := parseQuotaResetTime(quota.Signals[prefix+"Reset-At"])
		if !hasReset {
			if after, ok := quotaSignalNumber(quota.Signals, prefix+"Reset-After-Seconds"); ok && after >= 0 && !quota.ObservedAt.IsZero() {
				// A zero delay ends the window at the observation but states no real reset.
				reset = quota.ObservedAt.Add(time.Duration(after * float64(time.Second)))
				hasReset = after > 0
			}
		}
		var length time.Duration
		if minutes, ok := quotaSignalNumber(quota.Signals, prefix+"Window-Minutes"); ok && minutes > 0 && minutes*60 <= maxQuotaSignalSeconds {
			length = time.Duration(minutes * float64(time.Minute))
		}
		// A zero-filled window (no length, no reset, nothing used) is a placeholder for a
		// window the plan does not have.
		if !hasReset && length <= 0 && (!hasUsed || used <= 0) {
			return quotaWindow{}, false
		}
		return quotaWindow{usedUp: hasUsed && used >= 100, reset: reset, length: length}, true
	}
	primary, hasPrimary := read("Primary")
	secondary, hasSecondary := read("Secondary")
	var weekly quotaWindow
	var others []quotaWindow
	switch {
	case hasPrimary && hasSecondary:
		weekly, others = secondary, []quotaWindow{primary}
		if primary.length > secondary.length && (secondary.length > 0 || primary.length >= quotaWindowDay) {
			weekly, others = primary, []quotaWindow{secondary}
		}
	case hasPrimary:
		weekly = primary
	case hasSecondary:
		weekly = secondary
	default:
		return quotaWindow{}, false, nil
	}
	// A known length must be at least a day; without a length, a lone primary is the short
	// window and anything else counts as weekly.
	if (weekly.length > 0 && weekly.length < quotaWindowDay) || (weekly.length <= 0 && hasPrimary && !hasSecondary) {
		return quotaWindow{}, false, append(others, weekly)
	}
	return weekly, true, others
}

// devinQuotaWindows reads the weekly and daily windows that Devin status refreshes poll.
func devinQuotaWindows(signals map[string]string) (quotaWindow, bool, []quotaWindow) {
	read := func(period string, length time.Duration) (quotaWindow, bool) {
		remaining, hasRemaining := quotaSignalNumber(signals, period+"_quota_remaining_percent")
		reset, hasReset := parseQuotaResetTime(signals[period+"_quota_reset_at"])
		if !hasRemaining && !hasReset {
			return quotaWindow{}, false
		}
		return quotaWindow{usedUp: hasRemaining && remaining <= 0, reset: reset, length: length}, true
	}
	weekly, hasWeekly := read("weekly", quotaWindowWeek)
	var others []quotaWindow
	if daily, ok := read("daily", quotaWindowDay); ok {
		others = append(others, daily)
	}
	return weekly, hasWeekly, others
}

// quotaSignalNumber parses a finite numeric signal, tolerating a trailing percent sign.
// Values beyond maxQuotaSignalSeconds are rejected so durations derived from them cannot
// overflow.
func quotaSignalNumber(signals map[string]string, key string) (float64, bool) {
	raw := strings.TrimSuffix(strings.TrimSpace(signals[key]), "%")
	if raw == "" {
		return 0, false
	}
	value, errParse := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if errParse != nil || math.IsNaN(value) || math.Abs(value) > maxQuotaSignalSeconds {
		return 0, false
	}
	return value, true
}

// parseQuotaResetTime parses a reset signal given as epoch seconds (fractions allowed) or
// as an RFC 3339 time.
func parseQuotaResetTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if seconds, errParse := strconv.ParseFloat(raw, 64); errParse == nil {
		if math.IsNaN(seconds) || seconds <= 0 || seconds > 1e12 {
			return time.Time{}, false
		}
		whole := int64(seconds)
		return time.Unix(whole, int64((seconds-float64(whole))*float64(time.Second))), true
	}
	if parsed, errParse := time.Parse(time.RFC3339, raw); errParse == nil {
		return parsed, true
	}
	return time.Time{}, false
}
