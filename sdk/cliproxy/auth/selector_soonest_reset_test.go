package auth

import (
	"context"
	"strconv"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func claudeAuthResettingAt(id string, resetAt time.Time) *Auth {
	return &Auth{
		ID:       id,
		Provider: "claude",
		Quota: QuotaState{
			ObservedAt: resetAt.Add(-time.Hour),
			Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-7d-Reset": strconv.FormatInt(resetAt.Unix(), 10),
			},
		},
	}
}

func TestSoonestResetSelectorPick_PrefersSoonestWeeklyReset(t *testing.T) {
	t.Parallel()

	now := time.Now()
	auths := []*Auth{
		claudeAuthResettingAt("a", now.Add(5*24*time.Hour)),
		claudeAuthResettingAt("b", now.Add(2*time.Hour)),
		claudeAuthResettingAt("c", now.Add(3*24*time.Hour)),
	}

	got, err := (&SoonestResetSelector{}).Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "b" {
		t.Fatalf("Pick() auth.ID = %q, want %q", got.ID, "b")
	}
}

func TestSoonestResetSelectorPick_SkipsCoolingDownAuth(t *testing.T) {
	t.Parallel()

	now := time.Now()
	exhausted := claudeAuthResettingAt("b", now.Add(2*time.Hour))
	exhausted.Quota.Exceeded = true
	exhausted.Quota.Reason = "credential_quota"
	exhausted.Quota.NextRecoverAt = now.Add(2 * time.Hour)
	auths := []*Auth{
		claudeAuthResettingAt("a", now.Add(5*24*time.Hour)),
		exhausted,
		claudeAuthResettingAt("c", now.Add(3*24*time.Hour)),
	}

	got, err := (&SoonestResetSelector{}).Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "c" {
		t.Fatalf("Pick() auth.ID = %q, want %q", got.ID, "c")
	}
}

func TestSoonestResetSelectorPick_ProbesUnknownAndStaleResetsFirst(t *testing.T) {
	t.Parallel()

	now := time.Now()
	auths := []*Auth{
		claudeAuthResettingAt("a", now.Add(time.Hour)),
		claudeAuthResettingAt("c", now.Add(-time.Hour)),
		{ID: "d", Provider: "claude"},
	}

	got, err := (&SoonestResetSelector{}).Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "c" {
		t.Fatalf("Pick() auth.ID = %q, want %q (stale reset sorts with unknown, ties by ID)", got.ID, "c")
	}
}

func TestWeeklyQuotaResetAt_Codex(t *testing.T) {
	t.Parallel()

	now := time.Now()
	observed := now.Add(-time.Minute)
	resetAt := now.Add(48 * time.Hour).Truncate(time.Second)

	tests := []struct {
		name    string
		signals map[string]string
		want    time.Time
	}{
		{
			name:    "secondary reset-at",
			signals: map[string]string{"X-Codex-Secondary-Reset-At": strconv.FormatInt(resetAt.Unix(), 10)},
			want:    resetAt,
		},
		{
			name:    "secondary reset-after-seconds",
			signals: map[string]string{"X-Codex-Secondary-Reset-After-Seconds": "3600"},
			want:    observed.Add(time.Hour),
		},
		{
			name: "weekly primary window",
			signals: map[string]string{
				"X-Codex-Primary-Window-Minutes": "10080",
				"X-Codex-Primary-Reset-At":       strconv.FormatInt(resetAt.Unix(), 10),
			},
			want: resetAt,
		},
		{
			name: "short primary window ignored",
			signals: map[string]string{
				"X-Codex-Primary-Window-Minutes": "300",
				"X-Codex-Primary-Reset-At":       strconv.FormatInt(resetAt.Unix(), 10),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth := &Auth{ID: "x", Provider: "codex", Quota: QuotaState{ObservedAt: observed, Signals: tt.signals}}
			if got := weeklyQuotaResetAt(auth, "", now); !got.Equal(tt.want) {
				t.Fatalf("weeklyQuotaResetAt() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSoonestResetSelectorPick_PrefersModelScopedWeeklyReset(t *testing.T) {
	t.Parallel()

	now := time.Now()
	// "a" has the sooner credential-wide reset, but "b" has a sooner Fable window
	// recorded on its model-scoped snapshot, which wins for that model.
	a := claudeAuthResettingAt("a", now.Add(2*24*time.Hour))
	b := claudeAuthResettingAt("b", now.Add(3*24*time.Hour))
	b.ModelStates = map[string]*ModelState{
		"claude-fable-5": {
			Quota: QuotaState{
				ObservedAt: now,
				Signals: map[string]string{
					"Anthropic-Ratelimit-Unified-7d_oi-Reset": strconv.FormatInt(now.Add(24*time.Hour).Unix(), 10),
					"Anthropic-Ratelimit-Unified-7d-Reset":    strconv.FormatInt(now.Add(3*24*time.Hour).Unix(), 10),
				},
			},
		},
	}
	auths := []*Auth{a, b}

	got, err := (&SoonestResetSelector{}).Pick(context.Background(), "claude", "claude-fable-5", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got == nil || got.ID != "b" {
		t.Fatalf("Pick(fable) auth.ID = %v, want %q", got, "b")
	}

	got, err = (&SoonestResetSelector{}).Pick(context.Background(), "claude", "claude-opus-4-7", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got == nil || got.ID != "a" {
		t.Fatalf("Pick(other model) auth.ID = %v, want %q", got, "a")
	}
}

func TestWeeklyQuotaResetAt_IgnoresModelWindowOnCredentialSnapshot(t *testing.T) {
	t.Parallel()

	now := time.Now()
	shared := now.Add(3 * 24 * time.Hour).Truncate(time.Second)
	auth := &Auth{ID: "x", Provider: "claude", Quota: QuotaState{
		ObservedAt: now,
		Signals: map[string]string{
			"Anthropic-Ratelimit-Unified-7d_oi-Reset": strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
			"Anthropic-Ratelimit-Unified-7d-Reset":    strconv.FormatInt(shared.Unix(), 10),
		},
	}}
	if got := weeklyQuotaResetAt(auth, "claude-fable-5", now); !got.Equal(shared) {
		t.Fatalf("weeklyQuotaResetAt() = %v, want shared 7d reset %v", got, shared)
	}
}

func TestSoonestResetSelector_UsesSchedulerFastPath(t *testing.T) {
	t.Parallel()

	if !isBuiltInSelector(&SoonestResetSelector{}) {
		t.Fatalf("isBuiltInSelector(SoonestResetSelector) = false, want true")
	}
	if got := selectorStrategy(&SoonestResetSelector{}); got != schedulerStrategySoonestReset {
		t.Fatalf("selectorStrategy() = %v, want %v", got, schedulerStrategySoonestReset)
	}
}

func TestSchedulerPick_SoonestResetHighestPriority(t *testing.T) {
	t.Parallel()

	now := time.Now()
	lowPriority := claudeAuthResettingAt("low", now.Add(time.Hour))
	lowPriority.Attributes = map[string]string{"priority": "0"}
	highLate := claudeAuthResettingAt("high-a", now.Add(5*24*time.Hour))
	highLate.Attributes = map[string]string{"priority": "10"}
	highSoon := claudeAuthResettingAt("high-b", now.Add(2*24*time.Hour))
	highSoon.Attributes = map[string]string{"priority": "10"}
	scheduler := newSchedulerForTest(&SoonestResetSelector{}, lowPriority, highLate, highSoon)

	for index := 0; index < 3; index++ {
		got, errPick := scheduler.pickSingle(context.Background(), "claude", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got.ID != "high-b" {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, "high-b")
		}
	}

	got, errPick := scheduler.pickSingle(context.Background(), "claude", "", cliproxyexecutor.Options{}, map[string]struct{}{"high-b": {}})
	if errPick != nil {
		t.Fatalf("pickSingle() with tried error = %v", errPick)
	}
	if got.ID != "high-a" {
		t.Fatalf("pickSingle() with tried auth.ID = %q, want %q", got.ID, "high-a")
	}
}

func TestSchedulerPick_SoonestResetFollowsObservedSignals(t *testing.T) {
	t.Parallel()

	now := time.Now()
	scheduler := newSchedulerForTest(
		&SoonestResetSelector{},
		claudeAuthResettingAt("a", now.Add(2*24*time.Hour)),
		claudeAuthResettingAt("b", now.Add(4*24*time.Hour)),
	)

	got, errPick := scheduler.pickSingle(context.Background(), "claude", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() error = %v", errPick)
	}
	if got.ID != "a" {
		t.Fatalf("pickSingle() auth.ID = %q, want %q", got.ID, "a")
	}

	// A response for "b" reports an earlier weekly reset than "a".
	updated := claudeAuthResettingAt("b", now.Add(time.Hour))
	updated.Generation = 1
	updated.UpdatedAt = now
	scheduler.upsertAuth(updated)

	got, errPick = scheduler.pickSingle(context.Background(), "claude", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() after update error = %v", errPick)
	}
	if got.ID != "b" {
		t.Fatalf("pickSingle() after update auth.ID = %q, want %q", got.ID, "b")
	}
}

func TestSchedulerPick_MixedProvidersSoonestReset(t *testing.T) {
	t.Parallel()

	now := time.Now()
	codexSoon := &Auth{
		ID:       "codex-a",
		Provider: "codex",
		Quota: QuotaState{
			ObservedAt: now,
			Signals:    map[string]string{"X-Codex-Secondary-Reset-At": strconv.FormatInt(now.Add(time.Hour).Unix(), 10)},
		},
	}
	scheduler := newSchedulerForTest(
		&SoonestResetSelector{},
		claudeAuthResettingAt("claude-a", now.Add(3*24*time.Hour)),
		codexSoon,
		claudeAuthResettingAt("claude-b", now.Add(2*24*time.Hour)),
	)

	wantIDs := []string{"codex-a", "claude-b", "claude-a"}
	tried := map[string]struct{}{}
	for index, wantID := range wantIDs {
		got, provider, errPick := scheduler.pickMixed(context.Background(), []string{"claude", "codex"}, "", cliproxyexecutor.Options{}, tried)
		if errPick != nil {
			t.Fatalf("pickMixed() #%d error = %v", index, errPick)
		}
		if got.ID != wantID {
			t.Fatalf("pickMixed() #%d auth.ID = %q, want %q", index, got.ID, wantID)
		}
		if provider != got.Provider {
			t.Fatalf("pickMixed() #%d provider = %q, want %q", index, provider, got.Provider)
		}
		tried[got.ID] = struct{}{}
	}
}
