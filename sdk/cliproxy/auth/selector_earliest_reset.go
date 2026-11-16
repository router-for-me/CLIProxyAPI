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

// DefaultQuotaThreshold is the used fraction at which a quota window stops
// accepting new sessions under the earliest-reset strategy.
const DefaultQuotaThreshold = 0.98

// Quota window kinds reported by QuotaWindows.
const (
	QuotaWindowFiveHour     = "five_hour"
	QuotaWindowWeekly       = "weekly"
	QuotaWindowWeeklyScoped = "weekly_scoped"
)

// Quota window sources reported by QuotaWindows.
const (
	QuotaSourceResponse = "response"
	QuotaSourceUsage    = "usage"
)

// QuotaWindow is one provider usage window for a credential.
type QuotaWindow struct {
	Kind string `json:"kind"`
	// Scope names the model family a scoped window applies to, such as "fable".
	Scope string `json:"scope,omitempty"`
	// Used is the used fraction, from 0 to 1.
	Used       float64       `json:"used"`
	ResetAt    time.Time     `json:"reset_at"`
	Period     time.Duration `json:"-"`
	Source     string        `json:"source"`
	ObservedAt time.Time     `json:"observed_at"`
}

// appliesTo reports whether the window limits requests for model.
func (w QuotaWindow) appliesTo(model string) bool {
	if w.Kind != QuotaWindowWeeklyScoped {
		return true
	}
	return w.Scope != "" && strings.Contains(strings.ToLower(model), w.Scope)
}

// current returns the window as of now. A window whose reset has passed is
// reported as unused and rolled forward by its period.
func (w QuotaWindow) current(now time.Time) QuotaWindow {
	if w.ResetAt.IsZero() || w.ResetAt.After(now) {
		return w
	}
	w.Used = 0
	if w.Period > 0 {
		for !w.ResetAt.After(now) {
			w.ResetAt = w.ResetAt.Add(w.Period)
		}
	} else {
		w.ResetAt = time.Time{}
	}
	return w
}

type usageReading struct {
	windows    []QuotaWindow
	observedAt time.Time
}

var usageReadings = struct {
	sync.RWMutex
	byAuth map[string]usageReading
}{byAuth: map[string]usageReading{}}

// RecordUsageReading stores windows read from a provider usage endpoint.
func RecordUsageReading(authID string, windows []QuotaWindow, observedAt time.Time) {
	usageReadings.Lock()
	defer usageReadings.Unlock()
	usageReadings.byAuth[authID] = usageReading{windows: windows, observedAt: observedAt}
}

// ForgetUsageReading drops the stored usage reading for a credential.
func ForgetUsageReading(authID string) {
	usageReadings.Lock()
	defer usageReadings.Unlock()
	delete(usageReadings.byAuth, authID)
}

// QuotaWindows returns the credential's quota windows as of now: a window
// whose reset has passed is reported as unused (see QuotaObservations).
func QuotaWindows(auth *Auth, now time.Time) []QuotaWindow {
	windows := QuotaObservations(auth)
	for i := range windows {
		windows[i] = windows[i].current(now)
	}
	return windows
}

// QuotaObservations merges the quota windows observed on the credential's
// latest response with its latest usage reading, keeping the newer value per
// window, sorted by kind then scope. Windows are returned as observed, even
// when their reset has passed.
func QuotaObservations(auth *Auth) []QuotaWindow {
	if auth == nil {
		return nil
	}
	merged := map[string]QuotaWindow{}
	add := func(w QuotaWindow) {
		key := w.Kind + "/" + w.Scope
		if existing, ok := merged[key]; ok && !w.ObservedAt.After(existing.ObservedAt) {
			return
		}
		merged[key] = w
	}
	for _, w := range windowsFromSignals(auth.Provider, auth.Quota.Signals, auth.Quota.ObservedAt) {
		add(w)
	}
	usageReadings.RLock()
	reading := usageReadings.byAuth[auth.ID]
	usageReadings.RUnlock()
	for _, w := range reading.windows {
		add(w)
	}
	windows := make([]QuotaWindow, 0, len(merged))
	for _, w := range merged {
		windows = append(windows, w)
	}
	sort.Slice(windows, func(i, j int) bool {
		if windows[i].Kind != windows[j].Kind {
			return windows[i].Kind < windows[j].Kind
		}
		return windows[i].Scope < windows[j].Scope
	})
	return windows
}

// QuotaObservedAt returns when the credential's quota was last observed from
// any source.
func QuotaObservedAt(auth *Auth) time.Time {
	if auth == nil {
		return time.Time{}
	}
	observed := time.Time{}
	if len(windowsFromSignals(auth.Provider, auth.Quota.Signals, auth.Quota.ObservedAt)) > 0 {
		observed = auth.Quota.ObservedAt
	}
	usageReadings.RLock()
	reading := usageReadings.byAuth[auth.ID]
	usageReadings.RUnlock()
	if reading.observedAt.After(observed) {
		observed = reading.observedAt
	}
	return observed
}

// windowsFromSignals parses the quota headers upstream records on every
// Claude and Codex response.
func windowsFromSignals(provider string, signals map[string]string, observedAt time.Time) []QuotaWindow {
	if len(signals) == 0 {
		return nil
	}
	get := func(name string) string {
		for key, value := range signals {
			if strings.EqualFold(key, name) {
				return strings.TrimSpace(value)
			}
		}
		return ""
	}
	var windows []QuotaWindow
	switch strings.ToLower(provider) {
	case "claude":
		for _, claim := range []struct {
			name, kind, scope string
			period            time.Duration
		}{
			{"5h", QuotaWindowFiveHour, "", 5 * time.Hour},
			{"7d", QuotaWindowWeekly, "", 7 * 24 * time.Hour},
			{"7d_oi", QuotaWindowWeeklyScoped, "fable", 7 * 24 * time.Hour},
		} {
			prefix := "Anthropic-Ratelimit-Unified-" + claim.name + "-"
			used, okUsed := parseFraction(get(prefix+"Utilization"), 1)
			reset, okReset := parseUnixTime(get(prefix + "Reset"))
			if !okUsed || !okReset {
				continue
			}
			windows = append(windows, QuotaWindow{Kind: claim.kind, Scope: claim.scope, Used: used, ResetAt: reset, Period: claim.period, Source: QuotaSourceResponse, ObservedAt: observedAt})
		}
	case "codex":
		for _, name := range []string{"primary", "secondary"} {
			prefix := "X-Codex-" + name + "-"
			used, okUsed := parseFraction(get(prefix+"Used-Percent"), 100)
			minutes, errMinutes := strconv.Atoi(get(prefix + "Window-Minutes"))
			if !okUsed || errMinutes != nil || minutes <= 0 {
				continue
			}
			reset, okReset := parseUnixTime(get(prefix + "Reset-At"))
			if !okReset {
				seconds, errSeconds := strconv.Atoi(get(prefix + "Reset-After-Seconds"))
				if errSeconds != nil || seconds < 0 || observedAt.IsZero() {
					continue
				}
				reset = observedAt.Add(time.Duration(seconds) * time.Second)
			}
			period := time.Duration(minutes) * time.Minute
			kind := QuotaWindowWeekly
			if period <= 5*time.Hour {
				kind = QuotaWindowFiveHour
			}
			windows = append(windows, QuotaWindow{Kind: kind, Used: used, ResetAt: reset, Period: period, Source: QuotaSourceResponse, ObservedAt: observedAt})
		}
	}
	return windows
}

func parseFraction(raw string, scale float64) (float64, bool) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, false
	}
	return math.Min(value/scale, 1), true
}

func parseUnixTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds > 0 {
		return time.Unix(seconds, 0), true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// EarliestResetSelector sends new sessions to the credential whose quota resets
// soonest, so capacity that is about to reset is used before it is lost.
//
// It ranks only the credentials upstream already considers available; cooldowns
// after real 429s stay with the conductor. Missing or stale quota data never
// removes a credential: it only ranks it after credentials with known capacity.
type EarliestResetSelector struct {
	// Threshold is the used fraction at which a window stops taking new sessions.
	Threshold float64
	// Now overrides the clock in tests.
	Now func() time.Time
}

type earliestResetRank struct {
	auth     *Auth
	known    bool
	eligible bool
	reset    time.Time
	capacity float64
}

// Pick implements Selector.
func (s *EarliestResetSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = ctx
	_ = opts
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	threshold := s.Threshold
	if threshold <= 0 || threshold > 1 {
		threshold = DefaultQuotaThreshold
	}
	ranks := make([]earliestResetRank, 0, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		ranks = append(ranks, rankForEarliestReset(auth, model, threshold, now))
	}
	if len(ranks) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	sort.SliceStable(ranks, func(i, j int) bool {
		a, b := ranks[i], ranks[j]
		if a.eligible != b.eligible {
			return a.eligible
		}
		if a.known != b.known {
			return a.known
		}
		if !a.reset.Equal(b.reset) {
			if a.reset.IsZero() || b.reset.IsZero() {
				return !a.reset.IsZero()
			}
			return a.reset.Before(b.reset)
		}
		if a.capacity != b.capacity {
			return a.capacity > b.capacity
		}
		return a.auth.ID < b.auth.ID
	})
	return ranks[0].auth, nil
}

func rankForEarliestReset(auth *Auth, model string, threshold float64, now time.Time) earliestResetRank {
	rank := earliestResetRank{auth: auth, eligible: true, capacity: 1}
	var weekly, scoped time.Time
	for _, w := range QuotaWindows(auth, now) {
		if !w.appliesTo(model) {
			continue
		}
		rank.known = true
		if w.Used >= threshold {
			rank.eligible = false
		}
		rank.capacity = math.Min(rank.capacity, 1-w.Used)
		switch w.Kind {
		case QuotaWindowWeekly:
			weekly = w.ResetAt
		case QuotaWindowWeeklyScoped:
			scoped = w.ResetAt
		}
	}
	// The scoped window is the one that limits a scoped model, so its reset
	// decides which capacity expires first.
	rank.reset = weekly
	if !scoped.IsZero() {
		rank.reset = scoped
	}
	return rank
}
