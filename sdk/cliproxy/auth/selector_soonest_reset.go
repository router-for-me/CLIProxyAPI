package auth

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// SoonestResetSelector spends the credential whose weekly usage window resets soonest,
// until it cools down or one of its usage windows runs out, then moves to the next.
//
// Candidates are narrowed exactly like FillFirstSelector (highest available priority tier,
// cooldowns respected, websocket-capable Codex credentials preferred for websocket
// downstreams) and then ranked by the passive quota snapshot each credential carries in
// Quota.Signals (Claude anthropic-ratelimit-unified-* and Codex x-codex-* headers):
//
//  1. Credentials with a used-up window that has not reset yet sort last. They are still
//     picked when nothing else is left, so upstream can confirm the limit.
//  2. A credential whose weekly reset is unknown and that has not been probed since its
//     weekly window last rolled over gets one request (its probe) to learn the reset.
//     Quota snapshots live in memory only, so after a restart every credential is probed
//     once; a stated weekly reset that passes re-arms the probe once per rollover.
//  3. Credentials with a known weekly reset that is still ahead follow, earliest first.
//  4. Credentials whose probe taught nothing (still no weekly reset) come after them.
//
// The probe is reserved when the probing pick is made, so concurrent picks probe each
// credential once, and it is spent whatever the answer. Equal ranks keep the incoming
// order, which is the order FillFirstSelector picks from.
//
// The strategy name, signal reading and probe rules follow cliproxy-rs. Unlike cliproxy-rs,
// equal ranks are not rotated.
type SoonestResetSelector struct {
	mu sync.Mutex
	// probes records, per credential ID, that its probe for an unknown weekly reset was
	// spent, with the weekly-window rollover last probed for, per window.
	probes map[string]map[quotaWindowID]time.Time
}

// maxSoonestResetProbeEntries bounds the probe record. Records of removed credentials are
// dropped through InvalidateAuth; the bound only guards against unbounded growth, and
// clearing the record at worst sends one more probe per credential.
const maxSoonestResetProbeEntries = 4096

// Pick selects the available credential whose weekly quota window resets soonest.
func (s *SoonestResetSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	now := time.Now()
	available, err := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	return s.pick(available, now), nil
}

// InvalidateAuth forgets the probe record of a removed credential.
func (s *SoonestResetSelector) InvalidateAuth(authID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.probes, authID)
	s.mu.Unlock()
}

// pick returns the candidate with the lowest soonest-reset rank at now, reserving its probe
// when the pick is one. Ties keep the first candidate in slice order.
func (s *SoonestResetSelector) pick(auths []*Auth, now time.Time) *Auth {
	// Snapshots are read before taking the lock; only the probe record is shared state.
	windows := make([]quotaWindows, len(auths))
	for i, candidate := range auths {
		if candidate != nil {
			windows[i] = observedQuotaWindows(candidate.Provider, candidate.Quota.Signals, candidate.Quota.ObservedAt, now)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	picked := -1
	var pickedRank soonestResetRank
	for i, candidate := range auths {
		if candidate == nil {
			continue
		}
		record, hasRecord := s.probes[candidate.ID]
		rank := windows[i].rank(soonestResetProbed(record, hasRecord, windows[i].rolledOver))
		if picked < 0 || rank.less(pickedRank) {
			picked = i
			pickedRank = rank
		}
	}
	if picked < 0 {
		return nil
	}
	if pickedRank.class == soonestResetClassProbe {
		s.reserveProbeLocked(auths[picked].ID, windows[picked].rolledOver)
	}
	return auths[picked]
}

// reserveProbeLocked spends a credential's probe. The next pick ranks it by what the
// answer reports, if anything. Must be called with s.mu held.
func (s *SoonestResetSelector) reserveProbeLocked(authID string, rolledOver *quotaRollover) {
	markers, ok := s.probes[authID]
	if !ok {
		if s.probes == nil || len(s.probes) >= maxSoonestResetProbeEntries {
			s.probes = make(map[string]map[quotaWindowID]time.Time)
		}
		markers = make(map[quotaWindowID]time.Time)
		s.probes[authID] = markers
	}
	if rolledOver != nil {
		markers[rolledOver.window] = rolledOver.at
	}
}

// soonestResetProbed reports whether a credential was already probed for what it reports
// now. A probe holds until its weekly window rolls over after it: a stated reset of that
// same window that passed more than the window's gap after the rollover last probed for it.
func soonestResetProbed(record map[quotaWindowID]time.Time, hasRecord bool, rolledOver *quotaRollover) bool {
	if !hasRecord {
		return false
	}
	if rolledOver == nil {
		return true
	}
	probedAt, ok := record[rolledOver.window]
	return ok && !rolledOver.at.After(probedAt.Add(rolledOver.gap))
}

// soonestResetClass orders credentials that are not exhausted.
type soonestResetClass int

const (
	// soonestResetClassProbe: the weekly reset is unknown and the credential has not been
	// probed since its weekly window last rolled over.
	soonestResetClassProbe soonestResetClass = iota
	// soonestResetClassKnown: the weekly reset is known and still ahead.
	soonestResetClassKnown
	// soonestResetClassUnknown: the weekly reset is unknown and the probe taught nothing.
	soonestResetClassUnknown
)

// soonestResetRank orders credentials for SoonestResetSelector.
type soonestResetRank struct {
	// exhausted reports that a usage window is used up and has not reset yet.
	exhausted bool
	class     soonestResetClass
	// weeklyReset is when the weekly window resets; zero when unknown or already passed.
	weeklyReset time.Time
}

func (r soonestResetRank) less(other soonestResetRank) bool {
	if r.exhausted != other.exhausted {
		return !r.exhausted
	}
	if r.class != other.class {
		return r.class < other.class
	}
	return r.class == soonestResetClassKnown && r.weeklyReset.Before(other.weeklyReset)
}

// rank orders a credential by its windows and whether its probe was already spent.
func (w quotaWindows) rank(probed bool) soonestResetRank {
	rank := soonestResetRank{exhausted: w.exhausted, weeklyReset: w.weeklyReset}
	switch {
	case !w.weeklyReset.IsZero():
		rank.class = soonestResetClassKnown
	case probed:
		rank.class = soonestResetClassUnknown
	default:
		rank.class = soonestResetClassProbe
	}
	return rank
}

// quotaWindowID identifies a usage window. Probe markers are kept per window, so a rollover
// of one window is never compared with a probe made for another.
type quotaWindowID int

const (
	quotaWindowClaude7d quotaWindowID = iota + 1
	quotaWindowClaude5h
	quotaWindowCodexPrimary
	quotaWindowCodexSecondary
)

// quotaRollover is a weekly window whose stated reset has passed.
type quotaRollover struct {
	window quotaWindowID
	// at is the stated reset that passed.
	at time.Time
	// gap is how far apart two rollovers of this window must be to count as different
	// ones: half its length (half of fallbackQuotaWindow when unknown), which absorbs the
	// rounding of relative resets around a boundary.
	gap time.Duration
}

// quotaWindows is what a passive quota snapshot says about a credential's usage windows.
// The zero value means nothing is known.
type quotaWindows struct {
	// weeklyReset is when the long (weekly) window resets, while that is still ahead of now.
	weeklyReset time.Time
	// rolledOver is the long window's stated reset once it has passed: the window rolled
	// over, so what was learned about it is stale and the credential may be probed again.
	// Only a reset upstream stated (an absolute time, or a delay above zero) identifies a
	// rollover; a zero delay would make every answer's observation time a new one.
	rolledOver *quotaRollover
	// exhausted reports that a window (Claude 5-hour or 7-day, Codex primary or secondary)
	// is used up and has not reset yet, so upstream would refuse until it does.
	exhausted bool
}

// fallbackQuotaWindow is how long a used-up window without a usable reset or length is
// assumed to hold.
const fallbackQuotaWindow = 5 * time.Hour

// usageWindow is one usage window read from quota signals; each part may be missing.
type usageWindow struct {
	id     quotaWindowID
	usedUp bool
	// reset is when the window resets; zero when unknown.
	reset time.Time
	// statedReset is reset when it can identify a rollover (not synthesized from a zero
	// delay); zero otherwise.
	statedReset time.Time
	// length is the window length; zero when unknown.
	length time.Duration
}

// exhaustedUntil reports until when a used-up window holds: its reset, else its length
// (or fallbackQuotaWindow) after the observation.
func (w usageWindow) exhaustedUntil(observedAt time.Time) (time.Time, bool) {
	if !w.usedUp {
		return time.Time{}, false
	}
	if !w.reset.IsZero() {
		return w.reset, true
	}
	if w.length > 0 {
		return observedAt.Add(w.length), true
	}
	return observedAt.Add(fallbackQuotaWindow), true
}

// observedQuotaWindows reads a quota snapshot (header name to value, observed at observedAt)
// for provider. Only "claude" and "codex" snapshots carry usage windows; any other provider,
// an empty snapshot, or a snapshot without an observation time knows nothing.
func observedQuotaWindows(provider string, signals map[string]string, observedAt, now time.Time) quotaWindows {
	if len(signals) == 0 || observedAt.IsZero() {
		return quotaWindows{}
	}
	get := func(name string) string {
		return quotaSignalValue(signals, name)
	}
	number := func(name string) (float64, bool) {
		return parseQuotaSignalNumber(get(name))
	}

	provider = strings.ToLower(strings.TrimSpace(provider))
	var long, short *usageWindow
	switch provider {
	case "claude":
		window := func(id quotaWindowID, label string, length time.Duration) *usageWindow {
			prefix := "Anthropic-Ratelimit-Unified-" + label + "-"
			utilization, hasUtilization := number(prefix + "Utilization")
			reset, _ := parseQuotaResetTime(get(prefix + "Reset"))
			return &usageWindow{
				id: id,
				usedUp: strings.EqualFold(get(prefix+"Status"), "rejected") ||
					(hasUtilization && utilization >= 1),
				reset:       reset,
				statedReset: reset,
				length:      length,
			}
		}
		long = window(quotaWindowClaude7d, "7d", 7*24*time.Hour)
		short = window(quotaWindowClaude5h, "5h", 5*time.Hour)
	case "codex":
		// Usage, reset and length are read independently; a window exists when any of
		// them was reported.
		window := func(id quotaWindowID, label string) *usageWindow {
			prefix := "X-Codex-" + label + "-"
			used, hasUsed := number(prefix + "Used-Percent")
			reset, _ := parseQuotaResetTime(get(prefix + "Reset-At"))
			statedReset := reset
			if reset.IsZero() {
				if after, ok := number(prefix + "Reset-After-Seconds"); ok && after >= 0 {
					if delay, okDelay := quotaSecondsDuration(after); okDelay {
						reset = observedAt.Add(delay)
						// A zero delay still ends a used-up window now, but its time is
						// just the observation's, so it names no rollover.
						if after > 0 {
							statedReset = reset
						}
					}
				}
			}
			var length time.Duration
			if minutes, ok := number(prefix + "Window-Minutes"); ok {
				length, _ = quotaWindowLength(minutes)
			}
			if !hasUsed && reset.IsZero() && length <= 0 {
				return nil
			}
			return &usageWindow{id: id, usedUp: hasUsed && used >= 100, reset: reset, statedReset: statedReset, length: length}
		}
		primary, secondary := window(quotaWindowCodexPrimary, "Primary"), window(quotaWindowCodexSecondary, "Secondary")
		const day = 24 * time.Hour
		switch {
		// The longer window is the weekly one, whichever header carries it; without
		// lengths, secondary is weekly.
		case primary != nil && secondary != nil:
			if primary.length > 0 && secondary.length > 0 && primary.length > secondary.length {
				long, short = primary, secondary
			} else {
				long, short = secondary, primary
			}
		case primary != nil:
			if primary.length >= day {
				long = primary
			} else {
				short = primary
			}
		case secondary != nil:
			if secondary.length <= 0 || secondary.length >= day {
				long = secondary
			} else {
				short = secondary
			}
		}
	default:
		return quotaWindows{}
	}

	var exhaustedUntil time.Time
	var soonestReset time.Time
	for _, window := range []*usageWindow{long, short} {
		if window == nil {
			continue
		}
		if until, ok := window.exhaustedUntil(observedAt); ok && until.After(exhaustedUntil) {
			exhaustedUntil = until
		}
		if !window.reset.IsZero() && (soonestReset.IsZero() || window.reset.Before(soonestReset)) {
			soonestReset = window.reset
		}
	}
	// X-Codex-Limit-Reached lasts as long as the used-up windows do; when none is used
	// up, until the soonest reset any window reports; with no reset at all, five hours
	// from the observation.
	if provider == "codex" && strings.EqualFold(get("X-Codex-Limit-Reached"), "true") && exhaustedUntil.IsZero() {
		if !soonestReset.IsZero() {
			exhaustedUntil = soonestReset
		} else {
			exhaustedUntil = observedAt.Add(fallbackQuotaWindow)
		}
	}

	var windows quotaWindows
	windows.exhausted = exhaustedUntil.After(now)
	if long != nil {
		if long.reset.After(now) {
			windows.weeklyReset = long.reset
		}
		if !long.statedReset.IsZero() && !long.statedReset.After(now) {
			gap := long.length
			if gap <= 0 {
				gap = fallbackQuotaWindow
			}
			windows.rolledOver = &quotaRollover{window: long.id, at: long.statedReset, gap: gap / 2}
		}
	}
	return windows
}

// quotaSignalValue returns the trimmed value of a quota signal, matching the header name
// case-insensitively. Signals are stored under canonical header keys, so the canonical
// lookup normally succeeds without scanning.
func quotaSignalValue(signals map[string]string, name string) string {
	if value, ok := signals[http.CanonicalHeaderKey(name)]; ok {
		return strings.TrimSpace(value)
	}
	for key, value := range signals {
		if strings.EqualFold(strings.TrimSpace(key), name) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// parseQuotaSignalNumber parses a finite decimal quota signal value.
func parseQuotaSignalNumber(raw string) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

// parseQuotaResetTime parses a reset signal: epoch seconds (fractions allowed, positive
// and finite) or an RFC 3339 time.
func parseQuotaResetTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if seconds, err := strconv.ParseFloat(raw, 64); err == nil {
		if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds >= math.MaxInt64 {
			return time.Time{}, false
		}
		whole, fraction := math.Modf(seconds)
		return time.Unix(int64(whole), int64(fraction*float64(time.Second))), true
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

// quotaSecondsDuration converts a non-negative finite number of seconds to a Duration when
// it is representable.
func quotaSecondsDuration(seconds float64) (time.Duration, bool) {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
		return 0, false
	}
	nanos := seconds * float64(time.Second)
	if nanos >= math.MaxInt64 {
		return 0, false
	}
	return time.Duration(nanos), true
}

// quotaWindowLength converts a window length in minutes to a Duration when it is positive
// and representable; otherwise the missing-length policy applies.
func quotaWindowLength(minutes float64) (time.Duration, bool) {
	if math.IsNaN(minutes) || math.IsInf(minutes, 0) || minutes <= 0 {
		return 0, false
	}
	length, ok := quotaSecondsDuration(minutes * 60)
	if !ok || length <= 0 {
		return 0, false
	}
	return length, true
}
