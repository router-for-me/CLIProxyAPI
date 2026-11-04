package auth

import (
	"context"
	"strconv"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func codexWeeklySignals(resetAt time.Time) QuotaState {
	return QuotaState{
		ObservedAt: resetAt.Add(-24 * time.Hour),
		Signals: map[string]string{
			"X-Codex-Primary-Window-Minutes": "10080",
			"X-Codex-Primary-Reset-At":       strconv.FormatInt(resetAt.Unix(), 10),
			"X-Codex-Primary-Used-Percent":   "12",
		},
	}
}

func TestSoonestResetSelectorPick_PrefersSoonestReset(t *testing.T) {
	t.Parallel()

	now := time.Unix(1790000000, 0)
	selector := &SoonestResetSelector{nowFunc: func() time.Time { return now }}
	auths := []*Auth{
		{ID: "a", Quota: codexWeeklySignals(now.Add(6 * 24 * time.Hour))},
		{ID: "b", Quota: codexWeeklySignals(now.Add(5 * time.Hour))},
		{ID: "c", Quota: codexWeeklySignals(now.Add(2 * 24 * time.Hour))},
	}

	got, err := selector.Pick(context.Background(), "codex", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got == nil || got.ID != "b" {
		t.Fatalf("Pick() = %v, want b", got)
	}
}

func TestSoonestResetSelectorPick_UnknownResetComesLast(t *testing.T) {
	t.Parallel()

	now := time.Unix(1790000000, 0)
	selector := &SoonestResetSelector{nowFunc: func() time.Time { return now }}
	auths := []*Auth{
		{ID: "a"},
		{ID: "b", Quota: codexWeeklySignals(now.Add(-time.Hour))},
		{ID: "c", Quota: codexWeeklySignals(now.Add(3 * 24 * time.Hour))},
	}

	got, err := selector.Pick(context.Background(), "codex", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got == nil || got.ID != "c" {
		t.Fatalf("Pick() = %v, want c (a has no snapshot, b already reset)", got)
	}
}

func TestSoonestResetSelectorPick_FallsBackToFillFirst(t *testing.T) {
	t.Parallel()

	selector := &SoonestResetSelector{}
	auths := []*Auth{{ID: "b"}, {ID: "a"}, {ID: "c"}}

	got, err := selector.Pick(context.Background(), "codex", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got == nil || got.ID != "a" {
		t.Fatalf("Pick() = %v, want a", got)
	}
}

func TestSoonestResetSelectorPick_RespectsPriority(t *testing.T) {
	t.Parallel()

	now := time.Unix(1790000000, 0)
	selector := &SoonestResetSelector{nowFunc: func() time.Time { return now }}
	auths := []*Auth{
		{ID: "high", Attributes: map[string]string{"priority": "10"}, Quota: codexWeeklySignals(now.Add(6 * 24 * time.Hour))},
		{ID: "low", Quota: codexWeeklySignals(now.Add(time.Hour))},
	}

	got, err := selector.Pick(context.Background(), "codex", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got == nil || got.ID != "high" {
		t.Fatalf("Pick() = %v, want high", got)
	}
}

func TestSoonestResetSelectorPick_SkipsCoolingAuth(t *testing.T) {
	t.Parallel()

	now := time.Unix(1790000000, 0)
	selector := &SoonestResetSelector{nowFunc: func() time.Time { return now }}
	exhausted := &Auth{ID: "a", Quota: codexWeeklySignals(now.Add(time.Hour))}
	exhausted.Quota.Exceeded = true
	exhausted.Quota.Reason = "credential_quota"
	exhausted.Quota.NextRecoverAt = now.Add(time.Hour)
	auths := []*Auth{exhausted, {ID: "b", Quota: codexWeeklySignals(now.Add(4 * 24 * time.Hour))}}

	got, err := selector.Pick(context.Background(), "codex", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got == nil || got.ID != "b" {
		t.Fatalf("Pick() = %v, want b", got)
	}
}

func TestQuotaWindowResetAt(t *testing.T) {
	t.Parallel()

	now := time.Unix(1790000000, 0)
	weekly := now.Add(4 * 24 * time.Hour)
	fiveHour := now.Add(2 * time.Hour)

	tests := []struct {
		name  string
		quota QuotaState
		want  time.Time
		ok    bool
	}{
		{
			name: "codex uses the longest window",
			quota: QuotaState{Signals: map[string]string{
				"X-Codex-Primary-Window-Minutes":   "300",
				"X-Codex-Primary-Reset-At":         strconv.FormatInt(fiveHour.Unix(), 10),
				"X-Codex-Secondary-Window-Minutes": "10080",
				"X-Codex-Secondary-Reset-At":       strconv.FormatInt(weekly.Unix(), 10),
			}},
			want: weekly,
			ok:   true,
		},
		{
			// Shape of a live Codex weekly-only response: the unused secondary
			// window reports zero minutes and an empty reset-at (which the
			// snapshot drops as invalid).
			name: "codex ignores an empty secondary window",
			quota: QuotaState{
				ObservedAt: now,
				Signals: map[string]string{
					"X-Codex-Primary-Window-Minutes":        "10080",
					"X-Codex-Primary-Reset-After-Seconds":   "15439",
					"X-Codex-Primary-Reset-At":              strconv.FormatInt(weekly.Unix(), 10),
					"X-Codex-Secondary-Window-Minutes":      "0",
					"X-Codex-Secondary-Reset-After-Seconds": "0",
				},
			},
			want: weekly,
			ok:   true,
		},
		{
			name: "codex reset-after is relative to the observation",
			quota: QuotaState{
				ObservedAt: now.Add(-time.Hour),
				Signals: map[string]string{
					"X-Codex-Primary-Window-Minutes":      "10080",
					"X-Codex-Primary-Reset-After-Seconds": "7200",
				},
			},
			want: now.Add(time.Hour),
			ok:   true,
		},
		{
			name: "claude prefers the 7d window",
			quota: QuotaState{Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-5h-Reset": strconv.FormatInt(fiveHour.Unix(), 10),
				"Anthropic-Ratelimit-Unified-7d-Reset": strconv.FormatInt(weekly.Unix(), 10),
				"Anthropic-Ratelimit-Unified-Reset":    strconv.FormatInt(fiveHour.Unix(), 10),
			}},
			want: weekly,
			ok:   true,
		},
		{
			name: "claude falls back to the representative reset",
			quota: QuotaState{Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-Reset": strconv.FormatInt(fiveHour.Unix(), 10),
			}},
			want: fiveHour,
			ok:   true,
		},
		{
			name: "past reset is ignored",
			quota: QuotaState{Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-7d-Reset": strconv.FormatInt(now.Add(-time.Minute).Unix(), 10),
			}},
		},
		{
			name: "no snapshot",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := quotaWindowResetAt(&Auth{ID: "x", Quota: tt.quota}, now)
			if ok != tt.ok || !got.Equal(tt.want) {
				t.Fatalf("quotaWindowResetAt() = %v, %v; want %v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestSoonestResetSelectorPick_ProbesUnobservedCredentials(t *testing.T) {
	t.Parallel()

	now := time.Unix(1790000000, 0)
	selector := &SoonestResetSelector{nowFunc: func() time.Time { return now }}
	known := &Auth{ID: "a", Provider: "codex", Quota: codexWeeklySignals(now.Add(6 * 24 * time.Hour))}
	unobserved := &Auth{ID: "b", Provider: "codex"}
	auths := []*Auth{known, unobserved}

	pick := func() string {
		t.Helper()
		got, err := selector.Pick(context.Background(), "codex", "", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick() error = %v", err)
		}
		return got.ID
	}

	if got := pick(); got != "b" {
		t.Fatalf("first pick = %q, want unobserved b probed", got)
	}
	// b was probed but its response carried no quota headers: no repeat probe within the interval.
	if got := pick(); got != "a" {
		t.Fatalf("second pick = %q, want a", got)
	}
	now = now.Add(soonestResetProbeInterval)
	if got := pick(); got != "b" {
		t.Fatalf("pick after probe interval = %q, want b probed again", got)
	}
}

func TestSoonestResetSelectorPick_DoesNotProbeProvidersWithoutQuotaHeaders(t *testing.T) {
	t.Parallel()

	now := time.Unix(1790000000, 0)
	selector := &SoonestResetSelector{nowFunc: func() time.Time { return now }}
	auths := []*Auth{
		{ID: "a", Provider: "gemini"},
		{ID: "b", Provider: "codex", Quota: codexWeeklySignals(now.Add(time.Hour))},
	}

	got, err := selector.Pick(context.Background(), "mixed", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "b" {
		t.Fatalf("Pick() = %q, want b", got.ID)
	}
}

func TestSoonestResetSelectorUsesPrevalidatedCandidates(t *testing.T) {
	t.Parallel()

	selector := &SoonestResetSelector{}
	if !selectorUsesPrevalidatedCandidates(selector) {
		t.Fatal("soonest-reset must use prevalidated candidates")
	}
	if isBuiltInSelector(selector) {
		t.Fatal("soonest-reset must not take the scheduler fast path")
	}
	if got := selectionArgForSelector(selector, "route-alias"); got != "" {
		t.Fatalf("selection model = %q, want empty (manager resolves upstream models)", got)
	}
	ctx := selectorContextForAvailableAuths(context.Background(), selector, "route-alias")
	if validated, _ := ctx.Value(prevalidatedAuthCandidatesKey{}).(bool); !validated {
		t.Fatal("selector context is not marked prevalidated")
	}
}

func TestSoonestResetSelectorPick_ReprobesPartialSnapshots(t *testing.T) {
	t.Parallel()

	now := time.Unix(1790000000, 0)
	selector := &SoonestResetSelector{nowFunc: func() time.Time { return now }}
	partial := &Auth{ID: "b", Provider: "claude", Quota: QuotaState{
		ObservedAt: now.Add(-time.Minute),
		Signals:    map[string]string{"Anthropic-Ratelimit-Unified-Status": "allowed"},
	}}
	auths := []*Auth{
		{ID: "a", Provider: "claude", Quota: codexWeeklySignals(now.Add(3 * 24 * time.Hour))},
		partial,
	}

	got, err := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "b" {
		t.Fatalf("Pick() = %q, want partial snapshot b probed", got.ID)
	}
}

func TestSoonestResetSelectorPick_DoesNotProbeAfterObservedReset(t *testing.T) {
	t.Parallel()

	now := time.Unix(1790000000, 0)
	selector := &SoonestResetSelector{nowFunc: func() time.Time { return now }}
	auths := []*Auth{
		{ID: "a", Provider: "codex", Quota: codexWeeklySignals(now.Add(-time.Hour))},
		{ID: "b", Provider: "codex", Quota: codexWeeklySignals(now.Add(3 * 24 * time.Hour))},
	}

	got, err := selector.Pick(context.Background(), "codex", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "b" {
		t.Fatalf("Pick() = %q, want b (a's window already reset; its new window starts on use)", got.ID)
	}
}
