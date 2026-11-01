package auth

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// soonestResetTestNow precedes every reset in the production Claude snapshot below.
var soonestResetTestNow = time.Unix(1793400000, 0)

func soonestResetEpoch(t time.Time) string {
	return strconv.FormatInt(t.Unix(), 10)
}

// claudeProductionQuotaSignals is an anonymized snapshot of the unified rate-limit headers
// a Claude subscription account returns. Overage-Status is "rejected" on every account and
// must not be read as a used-up window.
func claudeProductionQuotaSignals(weeklyReset, weeklyUtilization string) map[string]string {
	return map[string]string{
		"Anthropic-Ratelimit-Unified-7d-Reset":                weeklyReset,
		"Anthropic-Ratelimit-Unified-7d-Status":               "allowed_warning",
		"Anthropic-Ratelimit-Unified-7d-Surpassed-Threshold":  "0.75",
		"Anthropic-Ratelimit-Unified-7d-Utilization":          weeklyUtilization,
		"Anthropic-Ratelimit-Unified-Fallback-Percentage":     "0.5",
		"Anthropic-Ratelimit-Unified-Overage-Disabled-Reason": "org_level_disabled",
		"Anthropic-Ratelimit-Unified-Overage-Status":          "rejected",
		"Anthropic-Ratelimit-Unified-Representative-Claim":    "seven_day",
		"Anthropic-Ratelimit-Unified-Reset":                   weeklyReset,
		"Anthropic-Ratelimit-Unified-Status":                  "allowed_warning",
		"Anthropic-Ratelimit-Unified-Upgrade-Paths":           "overage",
	}
}

func withQuotaSignals(signals map[string]string, extra map[string]string) map[string]string {
	merged := make(map[string]string, len(signals)+len(extra))
	for key, value := range signals {
		merged[key] = value
	}
	for key, value := range extra {
		merged[key] = value
	}
	return merged
}

func soonestResetAuth(id, provider string, observedAt time.Time, signals map[string]string) *Auth {
	return &Auth{ID: id, Provider: provider, Quota: QuotaState{ObservedAt: observedAt, Signals: signals}}
}

// soonestResetOrder returns the IDs in the order successive picks would drain them.
func soonestResetOrder(selector *SoonestResetSelector, auths []*Auth, now time.Time) []string {
	remaining := append([]*Auth(nil), auths...)
	order := make([]string, 0, len(auths))
	for len(remaining) > 0 {
		picked := selector.pick(remaining, now)
		order = append(order, picked.ID)
		for i, candidate := range remaining {
			if candidate == picked {
				remaining = append(remaining[:i], remaining[i+1:]...)
				break
			}
		}
	}
	return order
}

// probedSoonestResetSelector returns a selector whose probe for every listed credential was
// already spent, before any rollover.
func probedSoonestResetSelector(auths []*Auth) *SoonestResetSelector {
	selector := &SoonestResetSelector{probes: make(map[string]map[quotaWindowID]time.Time)}
	for _, auth := range auths {
		selector.probes[auth.ID] = map[quotaWindowID]time.Time{}
	}
	return selector
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func TestPickSoonestResetAuth_Order(t *testing.T) {
	t.Parallel()

	now := soonestResetTestNow
	observed := now.Add(-time.Minute)
	in := func(d time.Duration) string { return soonestResetEpoch(now.Add(d)) }
	claude := func(id string, signals map[string]string) *Auth {
		return soonestResetAuth(id, "claude", observed, signals)
	}
	weekly := func(id string, reset string, extra map[string]string) *Auth {
		return claude(id, withQuotaSignals(map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": reset}, extra))
	}

	// Unless a case is fresh, every probe was already spent, so the cases isolate how the
	// snapshot ranks; the fresh cases show where unprobed credentials go.
	tests := []struct {
		name  string
		fresh bool
		auths []*Auth
		want  []string
	}{
		{
			name: "production snapshot: earliest weekly reset first regardless of utilization",
			auths: []*Auth{
				claude("a", claudeProductionQuotaSignals("1793664000", "0.93")),
				claude("b", claudeProductionQuotaSignals("1793577600", "0.96")),
				claude("c", claudeProductionQuotaSignals("1793530800", "0.95")),
				claude("d", claudeProductionQuotaSignals("1793552400", "0.97")),
			},
			want: []string{"c", "d", "b", "a"},
		},
		{
			name: "weekly status rejected sorts last",
			auths: []*Auth{
				weekly("a", in(24*time.Hour), map[string]string{"Anthropic-Ratelimit-Unified-7d-Status": "Rejected"}),
				weekly("b", in(48*time.Hour), nil),
			},
			want: []string{"b", "a"},
		},
		{
			name: "weekly utilization at one sorts last",
			auths: []*Auth{
				weekly("a", in(24*time.Hour), map[string]string{"Anthropic-Ratelimit-Unified-7d-Utilization": "1.0"}),
				weekly("b", in(48*time.Hour), map[string]string{"Anthropic-Ratelimit-Unified-7d-Utilization": "0.99"}),
			},
			want: []string{"b", "a"},
		},
		{
			name: "five-hour status rejected sorts last",
			auths: []*Auth{
				weekly("a", in(24*time.Hour), map[string]string{
					"Anthropic-Ratelimit-Unified-5h-Status": "rejected",
					"Anthropic-Ratelimit-Unified-5h-Reset":  in(2 * time.Hour),
				}),
				weekly("b", in(48*time.Hour), nil),
			},
			want: []string{"b", "a"},
		},
		{
			name: "five-hour utilization above one sorts last",
			auths: []*Auth{
				weekly("a", in(24*time.Hour), map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "1.02"}),
				weekly("b", in(48*time.Hour), nil),
			},
			want: []string{"b", "a"},
		},
		{
			name: "used-up window whose reset passed is usable again",
			auths: []*Auth{
				weekly("a", in(48*time.Hour), nil),
				weekly("b", in(24*time.Hour), map[string]string{
					"Anthropic-Ratelimit-Unified-5h-Status": "rejected",
					"Anthropic-Ratelimit-Unified-5h-Reset":  in(-time.Second),
				}),
			},
			want: []string{"b", "a"},
		},
		{
			name: "weekly reset that passed re-arms the probe",
			auths: []*Auth{
				weekly("a", in(-time.Minute), nil),
				weekly("b", in(24*time.Hour), map[string]string{"Anthropic-Ratelimit-Unified-7d-Status": "rejected"}),
				weekly("c", in(72*time.Hour), nil),
			},
			want: []string{"a", "c", "b"},
		},
		{
			name: "probed credential without snapshot ranks after known and before exhausted",
			auths: []*Auth{
				{ID: "a", Provider: "claude"},
				weekly("b", in(time.Hour), map[string]string{"Anthropic-Ratelimit-Unified-7d-Utilization": "1"}),
				weekly("c", in(72*time.Hour), nil),
			},
			want: []string{"c", "a", "b"},
		},
		{
			name: "unparseable resets are unknown",
			auths: []*Auth{
				weekly("a", "soon", nil),
				weekly("b", "0", nil),
				weekly("c", "-1793530800", nil),
				weekly("d", "NaN", nil),
				weekly("e", "+Inf", nil),
				weekly("f", "2026-13-01T00:00:00Z", nil),
				weekly("g", in(96*time.Hour), nil),
			},
			want: []string{"g", "a", "b", "c", "d", "e", "f"},
		},
		{
			name: "unparseable utilization and status are not exhausted",
			auths: []*Auth{
				weekly("a", in(24*time.Hour), map[string]string{
					"Anthropic-Ratelimit-Unified-7d-Utilization": "full",
					"Anthropic-Ratelimit-Unified-5h-Status":      "rejected-ish",
				}),
				weekly("b", in(48*time.Hour), nil),
			},
			want: []string{"a", "b"},
		},
		{
			name: "RFC 3339 and fractional epoch resets compare with whole epoch seconds",
			auths: []*Auth{
				weekly("a", in(3*time.Hour), nil),
				weekly("b", now.Add(2*time.Hour).UTC().Format(time.RFC3339), nil),
				weekly("c", now.Add(time.Hour).Format("2006-01-02T15:04:05.999-07:00"), nil),
				weekly("d", strconv.FormatFloat(float64(now.Add(90*time.Minute).Unix())+0.5, 'f', 1, 64), nil),
			},
			want: []string{"c", "d", "b", "a"},
		},
		{
			name: "equal ranks keep incoming order",
			auths: []*Auth{
				weekly("z", in(24*time.Hour), nil),
				{ID: "y", Provider: "claude"},
				weekly("x", in(24*time.Hour), nil),
				{ID: "w", Provider: "claude"},
			},
			want: []string{"z", "x", "y", "w"},
		},
		{
			name: "overage status rejected alone is not exhausted",
			auths: []*Auth{
				weekly("a", in(24*time.Hour), map[string]string{"Anthropic-Ratelimit-Unified-Overage-Status": "rejected"}),
				weekly("b", in(48*time.Hour), nil),
			},
			want: []string{"a", "b"},
		},
		{
			name: "header names are matched case-insensitively",
			auths: []*Auth{
				claude("a", map[string]string{"anthropic-ratelimit-unified-7d-reset": in(48 * time.Hour)}),
				claude("b", map[string]string{"ANTHROPIC-RATELIMIT-UNIFIED-7D-RESET": " " + in(24*time.Hour) + " "}),
				claude("c", map[string]string{
					"Anthropic-Ratelimit-Unified-7d-Reset":  in(12 * time.Hour),
					"anthropic-ratelimit-unified-7d-status": "REJECTED",
				}),
			},
			want: []string{"b", "a", "c"},
		},
		{
			name: "mixed providers rank by each credential's own provider",
			auths: []*Auth{
				soonestResetAuth("a-gemini", "gemini", observed, map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": in(time.Hour)}),
				soonestResetAuth("b-codex", "codex", observed, map[string]string{
					"X-Codex-Secondary-Window-Minutes": "10080",
					"X-Codex-Secondary-Reset-At":       in(36 * time.Hour),
				}),
				weekly("c-claude", in(48*time.Hour), nil),
				soonestResetAuth("d-codex", "Codex", observed, map[string]string{
					"X-Codex-Secondary-Window-Minutes": "10080",
					"X-Codex-Secondary-Reset-At":       in(12 * time.Hour),
				}),
				{ID: "e-api-key", Provider: "openai-compatibility"},
			},
			want: []string{"d-codex", "b-codex", "c-claude", "a-gemini", "e-api-key"},
		},
		{
			name: "providers without quota windows are unknown",
			auths: []*Auth{
				soonestResetAuth("a-devin", "devin", observed, map[string]string{"Retry-After": "30", "X-Codex-Limit-Reached": "true"}),
				soonestResetAuth("b-claude-signals-on-codex", "codex", observed, map[string]string{"Anthropic-Ratelimit-Unified-7d-Status": "rejected"}),
				weekly("c", in(24*time.Hour), nil),
			},
			want: []string{"c", "a-devin", "b-claude-signals-on-codex"},
		},
		{
			name: "snapshot without observation time is unknown",
			auths: []*Auth{
				soonestResetAuth("a", "claude", time.Time{}, map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": in(time.Hour)}),
				weekly("b", in(24*time.Hour), nil),
			},
			want: []string{"b", "a"},
		},
	}

	tests = append(tests, []struct {
		name  string
		fresh bool
		auths []*Auth
		want  []string
	}{
		{
			name:  "unprobed unknown credentials come first in incoming order",
			fresh: true,
			auths: []*Auth{
				weekly("a", in(24*time.Hour), nil),
				{ID: "b", Provider: "claude"},
				weekly("c", in(12*time.Hour), nil),
				soonestResetAuth("d", "claude", observed, map[string]string{"Anthropic-Ratelimit-Unified-5h-Status": "allowed"}),
				weekly("e", in(time.Hour), map[string]string{"Anthropic-Ratelimit-Unified-7d-Utilization": "1"}),
			},
			want: []string{"b", "d", "c", "a", "e"},
		},
		{
			name:  "exhausted sorts last even when unknown and unprobed",
			fresh: true,
			auths: []*Auth{
				soonestResetAuth("a", "claude", observed, map[string]string{"Anthropic-Ratelimit-Unified-5h-Status": "rejected"}),
				soonestResetAuth("b", "codex", observed, map[string]string{"X-Codex-Limit-Reached": "true"}),
				weekly("c", in(24*time.Hour), nil),
				{ID: "d", Provider: "claude"},
			},
			want: []string{"d", "c", "a", "b"},
		},
	}...)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			selector := &SoonestResetSelector{}
			if !tt.fresh {
				selector = probedSoonestResetSelector(tt.auths)
			}
			if got := soonestResetOrder(selector, tt.auths, now); !equalStrings(got, tt.want) {
				t.Fatalf("order = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestObservedQuotaWindows(t *testing.T) {
	t.Parallel()

	now := soonestResetTestNow
	day := 24 * time.Hour
	at := func(d time.Duration) time.Time { return now.Add(d) }
	in := func(d time.Duration) string { return soonestResetEpoch(now.Add(d)) }

	tests := []struct {
		name          string
		provider      string
		signals       map[string]string
		observedAt    time.Time
		now           time.Time
		wantExhausted bool
		wantWeekly    time.Time
	}{
		{
			name:       "claude weekly reset ahead",
			provider:   "claude",
			signals:    claudeProductionQuotaSignals("1793530800", "0.95"),
			observedAt: at(-time.Minute),
			now:        now,
			wantWeekly: time.Unix(1793530800, 0),
		},
		{
			name:       "claude weekly reset passed rolls over to unknown",
			provider:   "claude",
			signals:    map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": in(-time.Minute)},
			observedAt: at(-time.Hour),
			now:        now,
		},
		{
			name:          "claude five-hour used up until its reset",
			provider:      "claude",
			signals:       map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "1.0", "Anthropic-Ratelimit-Unified-5h-Reset": in(10 * time.Minute)},
			observedAt:    now,
			now:           at(10*time.Minute - time.Second),
			wantExhausted: true,
		},
		{
			name:       "claude five-hour used up ends at its reset",
			provider:   "claude",
			signals:    map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "1.0", "Anthropic-Ratelimit-Unified-5h-Reset": in(10 * time.Minute)},
			observedAt: now,
			now:        at(10*time.Minute + time.Second),
		},
		{
			name:          "claude five-hour rejected without reset holds five hours",
			provider:      "claude",
			signals:       map[string]string{"Anthropic-Ratelimit-Unified-5h-Status": "rejected"},
			observedAt:    now,
			now:           at(5*time.Hour - time.Second),
			wantExhausted: true,
		},
		{
			name:       "claude five-hour rejected without reset ends after five hours",
			provider:   "claude",
			signals:    map[string]string{"Anthropic-Ratelimit-Unified-5h-Status": "rejected"},
			observedAt: now,
			now:        at(5*time.Hour + time.Second),
		},
		{
			name:          "claude weekly rejected with unparseable reset holds seven days",
			provider:      "claude",
			signals:       map[string]string{"Anthropic-Ratelimit-Unified-7d-Status": "rejected", "Anthropic-Ratelimit-Unified-7d-Reset": "later"},
			observedAt:    now,
			now:           at(7*day - time.Second),
			wantExhausted: true,
		},
		{
			name:       "claude weekly rejected without reset ends after seven days",
			provider:   "claude",
			signals:    map[string]string{"Anthropic-Ratelimit-Unified-7d-Status": "rejected"},
			observedAt: now,
			now:        at(7*day + time.Second),
		},
		{
			name:       "claude overage rejected is not a usage window",
			provider:   "claude",
			signals:    map[string]string{"Anthropic-Ratelimit-Unified-Overage-Status": "rejected", "Anthropic-Ratelimit-Unified-Status": "allowed"},
			observedAt: now,
			now:        now,
		},
		{
			name:       "claude ignores codex limit reached",
			provider:   "claude",
			signals:    map[string]string{"X-Codex-Limit-Reached": "true"},
			observedAt: now,
			now:        now,
		},
		{
			name:     "codex used-up five-hour primary with weekly secondary",
			provider: "codex",
			signals: map[string]string{
				"X-Codex-Primary-Used-Percent":        "100",
				"X-Codex-Primary-Window-Minutes":      "300",
				"X-Codex-Primary-Reset-After-Seconds": "3600",
				"X-Codex-Secondary-Used-Percent":      "40",
				"X-Codex-Secondary-Window-Minutes":    "10080",
				"X-Codex-Secondary-Reset-At":          in(2 * day),
			},
			observedAt:    at(-100 * time.Second),
			now:           now,
			wantExhausted: true,
			wantWeekly:    at(2 * day),
		},
		{
			name:     "codex reset-after counts from the observation",
			provider: "codex",
			signals: map[string]string{
				"X-Codex-Primary-Used-Percent":        "100",
				"X-Codex-Primary-Window-Minutes":      "300",
				"X-Codex-Primary-Reset-After-Seconds": "3600",
				"X-Codex-Secondary-Used-Percent":      "40",
				"X-Codex-Secondary-Window-Minutes":    "10080",
				"X-Codex-Secondary-Reset-At":          in(2 * day),
			},
			observedAt: at(-100 * time.Second),
			now:        at(3501 * time.Second),
			wantWeekly: at(2 * day),
		},
		{
			name:     "codex weekly window carried by primary",
			provider: "codex",
			signals: map[string]string{
				"X-Codex-Primary-Used-Percent":          "51",
				"X-Codex-Primary-Window-Minutes":        "10080",
				"X-Codex-Primary-Reset-At":              in(3 * day),
				"X-Codex-Secondary-Used-Percent":        "100",
				"X-Codex-Secondary-Window-Minutes":      "300",
				"X-Codex-Secondary-Reset-After-Seconds": "600",
			},
			observedAt:    now,
			now:           now,
			wantExhausted: true,
			wantWeekly:    at(3 * day),
		},
		{
			name:     "codex without lengths treats secondary as weekly",
			provider: "codex",
			signals: map[string]string{
				"X-Codex-Primary-Reset-At":   in(time.Hour),
				"X-Codex-Secondary-Reset-At": in(4 * day),
			},
			observedAt: now,
			now:        now,
			wantWeekly: at(4 * day),
		},
		{
			name:     "codex primary-only weekly window",
			provider: "codex",
			signals: map[string]string{
				"X-Codex-Primary-Window-Minutes":        "10080",
				"X-Codex-Primary-Reset-After-Seconds":   "86400",
				"X-Codex-Secondary-Reset-After-Seconds": "-1",
			},
			observedAt: now,
			now:        now,
			wantWeekly: at(day),
		},
		{
			name:     "codex primary-only short window has no weekly reset",
			provider: "codex",
			signals: map[string]string{
				"X-Codex-Primary-Window-Minutes": "300",
				"X-Codex-Primary-Reset-At":       in(time.Hour),
			},
			observedAt: now,
			now:        now,
		},
		{
			name:     "codex primary-only window without length is short",
			provider: "codex",
			signals: map[string]string{
				"X-Codex-Primary-Reset-At": in(time.Hour),
			},
			observedAt: now,
			now:        now,
		},
		{
			name:     "codex secondary-only window without length is weekly",
			provider: "codex",
			signals: map[string]string{
				"X-Codex-Secondary-Reset-At": in(2 * day),
			},
			observedAt: now,
			now:        now,
			wantWeekly: at(2 * day),
		},
		{
			name:     "codex secondary-only short window is not weekly",
			provider: "codex",
			signals: map[string]string{
				"X-Codex-Secondary-Window-Minutes": "60",
				"X-Codex-Secondary-Reset-At":       in(time.Hour),
			},
			observedAt: now,
			now:        now,
		},
		{
			name:     "codex used-up window without reset holds its length",
			provider: "codex",
			signals: map[string]string{
				"X-Codex-Primary-Used-Percent":   "100.0",
				"X-Codex-Primary-Window-Minutes": "60",
			},
			observedAt:    now,
			now:           at(59 * time.Minute),
			wantExhausted: true,
		},
		{
			name:     "codex used percent below one hundred is not exhausted",
			provider: "codex",
			signals: map[string]string{
				"X-Codex-Primary-Used-Percent": "99.9",
			},
			observedAt: now,
			now:        now,
		},
		{
			name:          "codex limit reached without windows holds five hours",
			provider:      "codex",
			signals:       map[string]string{"X-Codex-Limit-Reached": "True"},
			observedAt:    now,
			now:           at(5*time.Hour - time.Second),
			wantExhausted: true,
		},
		{
			name:       "codex limit reached without windows ends after five hours",
			provider:   "codex",
			signals:    map[string]string{"X-Codex-Limit-Reached": "true"},
			observedAt: now,
			now:        at(5*time.Hour + time.Second),
		},
		{
			name:     "codex limit reached lasts until the soonest reported reset",
			provider: "codex",
			signals: map[string]string{
				"X-Codex-Limit-Reached":               "true",
				"X-Codex-Primary-Used-Percent":        "80",
				"X-Codex-Primary-Window-Minutes":      "300",
				"X-Codex-Primary-Reset-After-Seconds": "1800",
				"X-Codex-Secondary-Window-Minutes":    "10080",
				"X-Codex-Secondary-Reset-At":          in(2 * day),
			},
			observedAt:    now,
			now:           at(29 * time.Minute),
			wantExhausted: true,
			wantWeekly:    at(2 * day),
		},
		{
			name:     "codex limit reached ends at the soonest reported reset",
			provider: "codex",
			signals: map[string]string{
				"X-Codex-Limit-Reached":               "true",
				"X-Codex-Primary-Used-Percent":        "80",
				"X-Codex-Primary-Window-Minutes":      "300",
				"X-Codex-Primary-Reset-After-Seconds": "1800",
				"X-Codex-Secondary-Window-Minutes":    "10080",
				"X-Codex-Secondary-Reset-At":          in(2 * day),
			},
			observedAt: now,
			now:        at(31 * time.Minute),
			wantWeekly: at(2 * day),
		},
		{
			name:     "codex limit reached false is ignored",
			provider: "codex",
			signals: map[string]string{
				"X-Codex-Limit-Reached": "false",
			},
			observedAt: now,
			now:        now,
		},
		{
			name:       "other providers know nothing",
			provider:   "gemini",
			signals:    map[string]string{"X-Codex-Limit-Reached": "true", "Anthropic-Ratelimit-Unified-7d-Status": "rejected"},
			observedAt: now,
			now:        now,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := observedQuotaWindows(tt.provider, tt.signals, tt.observedAt, tt.now)
			if got.exhausted != tt.wantExhausted || !got.weeklyReset.Equal(tt.wantWeekly) {
				t.Fatalf("observedQuotaWindows() = {exhausted:%v weeklyReset:%v}, want {exhausted:%v weeklyReset:%v}",
					got.exhausted, got.weeklyReset, tt.wantExhausted, tt.wantWeekly)
			}
		})
	}
}

func TestParseQuotaResetTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw    string
		want   time.Time
		wantOK bool
	}{
		{raw: "1793530800", want: time.Unix(1793530800, 0), wantOK: true},
		{raw: " 1793530800.25 ", want: time.Unix(1793530800, 250000000), wantOK: true},
		{raw: "2026-11-02T08:00:00Z", want: time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC), wantOK: true},
		{raw: "2026-11-02T10:00:00.5+02:00", want: time.Date(2026, 11, 2, 8, 0, 0, 500000000, time.UTC), wantOK: true},
		{raw: ""},
		{raw: "0"},
		{raw: "-1"},
		{raw: "NaN"},
		{raw: "Inf"},
		{raw: "1e400"},
		{raw: "1e19"},
		{raw: "tomorrow"},
		{raw: "2026-11-02"},
	}
	for _, tt := range tests {
		got, ok := parseQuotaResetTime(tt.raw)
		if ok != tt.wantOK || !got.Equal(tt.want) {
			t.Errorf("parseQuotaResetTime(%q) = %v, %v; want %v, %v", tt.raw, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestSoonestResetSelectorPick(t *testing.T) {
	t.Parallel()

	const model = "claude-soonest-reset"
	in := func(d time.Duration) string { return soonestResetEpoch(time.Now().Add(d)) }
	weekly := func(id string, priority string, reset string, extra map[string]string) *Auth {
		auth := soonestResetAuth(id, "claude", time.Now().Add(-time.Minute),
			withQuotaSignals(map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": reset}, extra))
		if priority != "" {
			auth.Attributes = map[string]string{"priority": priority}
		}
		return auth
	}
	cooled := func(auth *Auth) *Auth {
		next := time.Now().Add(30 * time.Minute)
		auth.ModelStates = map[string]*ModelState{
			model: {Status: StatusActive, Unavailable: true, NextRetryAfter: next, Quota: QuotaState{Exceeded: true, NextRecoverAt: next}},
		}
		return auth
	}

	tests := []struct {
		name     string
		provider string
		auths    []*Auth
		want     []string
	}{
		{
			name:     "earliest weekly reset wins",
			provider: "claude",
			auths: []*Auth{
				weekly("a", "", in(72*time.Hour), nil),
				weekly("b", "", in(24*time.Hour), nil),
				weekly("c", "", in(48*time.Hour), nil),
			},
			want: []string{"b", "b"},
		},
		{
			name:     "unknown credentials are probed once each in ID order, then fill first",
			provider: "claude",
			auths: []*Auth{
				{ID: "c", Provider: "claude"},
				{ID: "a", Provider: "claude"},
				{ID: "b", Provider: "claude"},
			},
			want: []string{"a", "b", "c", "a", "a"},
		},
		{
			name:     "higher priority tier wins over an earlier reset in a lower tier",
			provider: "claude",
			auths: []*Auth{
				weekly("high-late", "10", in(96*time.Hour), nil),
				weekly("low-early", "0", in(time.Hour), nil),
			},
			want: []string{"high-late", "high-late"},
		},
		{
			name:     "exhausted credential in the higher tier still beats a lower tier",
			provider: "claude",
			auths: []*Auth{
				weekly("high-exhausted", "10", in(96*time.Hour), map[string]string{"Anthropic-Ratelimit-Unified-7d-Status": "rejected"}),
				weekly("low-early", "0", in(time.Hour), nil),
			},
			want: []string{"high-exhausted", "high-exhausted"},
		},
		{
			name:     "cooled-down credential is excluded",
			provider: "claude",
			auths: []*Auth{
				cooled(weekly("a", "", in(time.Hour), nil)),
				weekly("b", "", in(48*time.Hour), nil),
				weekly("c", "", in(24*time.Hour), nil),
			},
			want: []string{"c", "c"},
		},
		{
			name:     "cooled-down higher tier falls back to the lower tier",
			provider: "claude",
			auths: []*Auth{
				cooled(weekly("high", "10", in(time.Hour), nil)),
				weekly("low-late", "0", in(48*time.Hour), nil),
				weekly("low-early", "0", in(24*time.Hour), nil),
			},
			want: []string{"low-early", "low-early"},
		},
		{
			name:     "exhausted credential is the last resort",
			provider: "claude",
			auths: []*Auth{
				cooled(weekly("a", "", in(time.Hour), nil)),
				weekly("b", "", in(24*time.Hour), map[string]string{"Anthropic-Ratelimit-Unified-5h-Status": "rejected"}),
			},
			want: []string{"b", "b"},
		},
		{
			name:     "mixed providers",
			provider: "mixed",
			auths: []*Auth{
				weekly("a-claude", "", in(48*time.Hour), nil),
				soonestResetAuth("b-codex", "codex", time.Now(), map[string]string{
					"X-Codex-Secondary-Window-Minutes": "10080",
					"X-Codex-Secondary-Reset-At":       in(24 * time.Hour),
				}),
				{ID: "c-gemini", Provider: "gemini"},
			},
			want: []string{"c-gemini", "b-codex", "b-codex"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			selector := &SoonestResetSelector{}
			got := make([]string, 0, len(tt.want))
			for range tt.want {
				picked, err := selector.Pick(context.Background(), tt.provider, model, cliproxyexecutor.Options{}, tt.auths)
				if err != nil {
					t.Fatalf("Pick() error = %v", err)
				}
				if picked == nil {
					t.Fatal("Pick() auth = nil")
				}
				got = append(got, picked.ID)
			}
			if !equalStrings(got, tt.want) {
				t.Fatalf("Pick() sequence = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSoonestResetSelectorPick_AllCoolingReturnsModelCooldown(t *testing.T) {
	t.Parallel()

	const model = "claude-soonest-reset-cooling"
	next := time.Now().Add(30 * time.Minute)
	auth := &Auth{
		ID:       "cooling",
		Provider: "claude",
		ModelStates: map[string]*ModelState{
			model: {Status: StatusActive, Unavailable: true, NextRetryAfter: next, Quota: QuotaState{Exceeded: true, NextRecoverAt: next}},
		},
	}

	_, errPick := (&SoonestResetSelector{}).Pick(context.Background(), "claude", model, cliproxyexecutor.Options{}, []*Auth{auth})
	var cooldownErr *modelCooldownError
	if !errors.As(errPick, &cooldownErr) {
		t.Fatalf("Pick() error = %T %v, want *modelCooldownError", errPick, errPick)
	}
}
