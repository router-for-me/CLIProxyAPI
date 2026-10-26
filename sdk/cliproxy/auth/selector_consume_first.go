package auth

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const (
	// consumeFirstSpentPercent marks a credential as spent once any observed window
	// is this full. Spent credentials sort after every other one but stay usable.
	consumeFirstSpentPercent = 95.0
	// consumeFirstUnstartedSlack treats an untouched window whose reset is still about
	// one whole window away as "not started": providers report that placeholder reset
	// and move it forward on every read, so it is not a real deadline.
	consumeFirstUnstartedSlack = 30 * time.Minute
)

// ConsumeFirstSelector spends the credential whose allowance resets soonest, so
// quota that is about to be lost is used before quota that will last.
//
// Credentials are ranked from the passive quota headers each one last returned
// (Claude anthropic-ratelimit-unified-*, Codex x-codex-primary/secondary-*):
//  1. not spent (every observed window below consumeFirstSpentPercent) before spent;
//  2. a known reset of the longest (weekly) window before an unknown one;
//  3. the soonest such reset;
//  4. the least used, then credential ID (fill-first order).
//
// A credential with no observation (another provider, a fresh restart, a window
// that has not started or already reset) has an unknown reset, so it ranks after
// every credential with a known reset and the remaining ties fall back to ID order.
type ConsumeFirstSelector struct{}

type consumeFirstRank struct {
	spent   bool
	resetAt time.Time
	used    float64
}

type quotaWindow struct {
	used    float64
	minutes float64
	resetAt time.Time
}

// Pick returns the available credential with the best consume-first rank.
func (s *ConsumeFirstSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	now := time.Now()
	available, err := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	best := available[0]
	bestRank := consumeFirstRankOf(best, now)
	for _, candidate := range available[1:] {
		if rank := consumeFirstRankOf(candidate, now); rank.before(bestRank) {
			best, bestRank = candidate, rank
		}
	}
	return best, nil
}

// ConsumeFirstStanding reports the figures consume-first ranks a credential on: the
// weekly reset it waits for (zero when unknown), its highest live used percent, and
// whether it is spent. Management views use it to show the router's own order.
func ConsumeFirstStanding(auth *Auth, now time.Time) (resetAt time.Time, usedPercent float64, spent bool) {
	if auth == nil {
		return time.Time{}, 0, false
	}
	rank := consumeFirstRankOf(auth, now)
	return rank.resetAt, rank.used, rank.spent
}

func (r consumeFirstRank) before(other consumeFirstRank) bool {
	if r.spent != other.spent {
		return !r.spent
	}
	if r.resetAt.IsZero() != other.resetAt.IsZero() {
		return !r.resetAt.IsZero()
	}
	if !r.resetAt.Equal(other.resetAt) {
		return r.resetAt.Before(other.resetAt)
	}
	return r.used < other.used
}

func consumeFirstRankOf(auth *Auth, now time.Time) consumeFirstRank {
	var rank consumeFirstRank
	var longest *quotaWindow
	for _, window := range quotaWindowsFromSignals(auth.Quota.Signals, auth.Quota.ObservedAt) {
		// Rank on the longest window even when it has expired: a short window must
		// never stand in for the weekly one, it only decides whether we are spent.
		if longest == nil || window.minutes > longest.minutes {
			longest = &window
		}
		if !window.resetAt.After(now) {
			// The window has reset since it was observed, so its figures are void.
			continue
		}
		if window.used >= consumeFirstSpentPercent {
			rank.spent = true
		}
		if window.used > rank.used {
			rank.used = window.used
		}
	}
	if longest == nil || !longest.resetAt.After(now) {
		return rank
	}
	notStarted := longest.used == 0 && longest.resetAt.Sub(now) >= time.Duration(longest.minutes)*time.Minute-consumeFirstUnstartedSlack
	if !notStarted {
		rank.resetAt = longest.resetAt
	}
	return rank
}

// quotaWindowsFromSignals reads the Claude and Codex usage windows from a quota
// observation snapshot. Used figures are percentages (0-100).
func quotaWindowsFromSignals(signals map[string]string, observedAt time.Time) []quotaWindow {
	if len(signals) == 0 {
		return nil
	}
	get := func(name string) (float64, bool) {
		value, errParse := strconv.ParseFloat(strings.TrimSpace(signals[http.CanonicalHeaderKey(name)]), 64)
		return value, errParse == nil
	}
	var windows []quotaWindow
	add := func(window quotaWindow) {
		if !window.resetAt.IsZero() {
			windows = append(windows, window)
		}
	}
	// Claude reports utilization as a 0-1 fraction and resets as Unix seconds.
	for _, claude := range []struct {
		key     string
		minutes float64
	}{{"5h", 300}, {"7d", 7 * 24 * 60}} {
		prefix := "anthropic-ratelimit-unified-" + claude.key + "-"
		utilization, _ := get(prefix + "utilization")
		reset, _ := get(prefix + "reset")
		add(quotaWindow{used: utilization * 100, minutes: claude.minutes, resetAt: unixTime(reset)})
	}
	// Codex reports used percent, window length, and either an absolute reset
	// (Unix seconds) or a reset relative to the observation.
	for _, codex := range []struct {
		prefix  string
		minutes float64
	}{{"x-codex-primary-", 300}, {"x-codex-secondary-", 7 * 24 * 60}} {
		used, _ := get(codex.prefix + "used-percent")
		minutes, hasMinutes := get(codex.prefix + "window-minutes")
		if !hasMinutes {
			minutes = codex.minutes
		}
		var resetAt time.Time
		if reset, ok := get(codex.prefix + "reset-at"); ok {
			resetAt = unixTime(reset)
		} else if after, okAfter := get(codex.prefix + "reset-after-seconds"); okAfter && !observedAt.IsZero() {
			resetAt = observedAt.Add(time.Duration(after * float64(time.Second)))
		}
		add(quotaWindow{used: used, minutes: minutes, resetAt: resetAt})
	}
	return windows
}

func unixTime(seconds float64) time.Time {
	if seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(seconds), 0)
}
