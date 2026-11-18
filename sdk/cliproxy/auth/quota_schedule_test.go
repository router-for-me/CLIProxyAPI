package auth

import (
	"context"
	"net/http"
	"reflect"
	"strconv"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

func TestQuotaResetScheduleHeaderFreshness(t *testing.T) {
	now := time.Date(2026, 10, 5, 1, 0, 0, 0, time.UTC)
	weekly, five := now.Add(24*time.Hour), now.Add(time.Hour)
	for _, tc := range []struct {
		name, provider string
		headers        http.Header
		weekly, five   time.Time
	}{
		{"claude", "claude", http.Header{"Anthropic-Ratelimit-Unified-7d-Reset": {strconv.FormatInt(weekly.Unix(), 10)}, "Anthropic-Ratelimit-Unified-5h-Reset": {strconv.FormatInt(five.Unix(), 10)}}, weekly, five},
		{"codex reversed", "codex", http.Header{"X-Codex-Primary-Window-Minutes": {"10080"}, "X-Codex-Primary-Reset-At": {strconv.FormatInt(weekly.Unix(), 10)}, "X-Codex-Secondary-Window-Minutes": {"300"}, "X-Codex-Secondary-Reset-After-Seconds": {"3600"}}, weekly, five},
		{"wrong provider", "xai", http.Header{"Anthropic-Ratelimit-Unified-7d-Reset": {strconv.FormatInt(weekly.Unix(), 10)}}, time.Time{}, time.Time{}},
		{"unknown durations", "codex", http.Header{"X-Codex-Primary-Window-Minutes": {"60"}, "X-Codex-Primary-Reset-At": {strconv.FormatInt(weekly.Unix(), 10)}}, time.Time{}, time.Time{}},
		{"malformed reset", "claude", http.Header{"Anthropic-Ratelimit-Unified-7d-Reset": {"not-a-date"}}, time.Time{}, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schedule := quotaScheduleFromHeaders(tc.provider, tc.headers, now)
			if !schedule.WeeklyResetAt.Equal(tc.weekly) || !schedule.FiveHourResetAt.Equal(tc.five) {
				t.Fatalf("schedule = %+v", schedule)
			}
		})
	}
	a := &Auth{Provider: "claude", QuotaResetSchedule: QuotaResetSchedule{WeeklyResetAt: weekly.Add(time.Hour), ObservedAt: now}}
	a.Quota = QuotaState{ObservedAt: now.Add(time.Minute), Signals: map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": strconv.FormatInt(weekly.Unix(), 10)}}
	if got := EffectiveQuotaResetSchedule(a); !got.WeeklyResetAt.Equal(weekly) {
		t.Fatalf("new header observation ignored: %+v", got)
	}
	a.Quota.ObservedAt = now.Add(-time.Minute)
	if got := EffectiveQuotaResetSchedule(a); !got.WeeklyResetAt.Equal(weekly.Add(time.Hour)) {
		t.Fatalf("old headers replaced usage: %+v", got)
	}
}

func TestQuotaResetSchedulePartialHeaders(t *testing.T) {
	now := time.Date(2026, 10, 5, 1, 0, 0, 0, time.UTC)
	for _, window := range []string{"weekly", "five_hour"} {
		for _, expired := range []bool{false, true} {
			t.Run(window+"/expired="+strconv.FormatBool(expired), func(t *testing.T) {
				oldObservation := now.Add(-time.Hour)
				stored := QuotaResetSchedule{WeeklyResetAt: now.Add(24 * time.Hour), FiveHourResetAt: now.Add(time.Hour), ObservedAt: oldObservation}
				other := now.Add(2 * time.Hour)
				if expired {
					other = now
				}
				headers := map[string]string{}
				if window == "weekly" {
					stored.FiveHourResetAt = other
					headers["Anthropic-Ratelimit-Unified-7d-Reset"] = strconv.FormatInt(now.Add(48*time.Hour).Unix(), 10)
				} else {
					stored.WeeklyResetAt = other
					headers["Anthropic-Ratelimit-Unified-5h-Reset"] = strconv.FormatInt(now.Add(3*time.Hour).Unix(), 10)
				}
				got := EffectiveQuotaResetSchedule(&Auth{Provider: "claude", QuotaResetSchedule: stored, Quota: QuotaState{ObservedAt: now, Signals: headers}})
				fresh, carried := got.WeeklyResetAt, got.FiveHourResetAt
				if window == "five_hour" {
					fresh, carried = got.FiveHourResetAt, got.WeeklyResetAt
				}
				wantFresh := now.Add(48 * time.Hour)
				if window == "five_hour" {
					wantFresh = now.Add(3 * time.Hour)
				}
				if !fresh.Equal(wantFresh) || !got.ObservedAt.Equal(now) {
					t.Fatalf("fresh observation incorrect: %+v", got)
				}
				if expired && !carried.IsZero() {
					t.Fatal("expired stored window revived")
				}
				if !expired && !carried.Equal(other) {
					t.Fatalf("known other window discarded: %+v", got)
				}
			})
		}
	}
}

func TestRecordQuotaResetScheduleGuard(t *testing.T) {
	now := time.Now().UTC().Round(0)
	for _, tc := range []struct {
		name                     string
		mutate                   func(*Auth)
		home, canceled, accepted bool
	}{
		{name: "current", accepted: true},
		{name: "newer generation", mutate: func(a *Auth) { a.Generation++ }},
		{name: "new registration", mutate: func(a *Auth) { a.RegistrationEpoch++ }},
		{name: "different provider", mutate: func(a *Auth) { a.Provider = "codex" }},
		{name: "disabled", mutate: func(a *Auth) { a.Disabled = true }},
		{name: "home", home: true},
		{name: "canceled", canceled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			a, err := m.Register(context.Background(), &Auth{ID: "schedule", Provider: "claude", Status: StatusError, Unavailable: true, Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(time.Hour)}, NextRetryAfter: now.Add(time.Hour), LastError: &Error{HTTPStatus: 429}})
			if err != nil {
				t.Fatal(err)
			}
			expected := a.Clone()
			if tc.mutate != nil {
				m.mu.Lock()
				tc.mutate(m.auths[a.ID])
				m.mu.Unlock()
			}
			if tc.home {
				cfg := &internalconfig.Config{}
				cfg.Home.Enabled = true
				m.SetConfig(cfg)
				expected, _ = m.GetByID(a.ID)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			before, _ := m.GetByID(a.ID)
			schedule := QuotaResetSchedule{WeeklyResetAt: now.Add(2 * time.Hour), ObservedAt: now}
			got := m.RecordQuotaResetScheduleIfUnchanged(ctx, expected, schedule)
			after, _ := m.GetByID(a.ID)
			if (got != nil) != tc.accepted {
				t.Fatalf("accepted=%v want %v", got != nil, tc.accepted)
			}
			if !reflect.DeepEqual(before.Quota, after.Quota) || before.Unavailable != after.Unavailable || before.Status != after.Status || !before.NextRetryAfter.Equal(after.NextRetryAfter) {
				t.Fatal("routing observation changed cooldown")
			}
			if tc.accepted {
				if after.QuotaResetSchedule != schedule || after.Generation != before.Generation+1 {
					t.Fatal("schedule not published")
				}
			} else if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected observation mutated auth")
			}
		})
	}
}

func TestQuotaResetScheduleSurvivesSameAccountUpdate(t *testing.T) {
	for _, changed := range []bool{false, true} {
		m := NewManager(nil, nil, nil)
		a, err := m.Register(context.Background(), &Auth{ID: "reload", Provider: "codex", Metadata: map[string]any{"account_id": "account-a"}})
		if err != nil {
			t.Fatal(err)
		}
		schedule := QuotaResetSchedule{WeeklyResetAt: time.Now().Add(time.Hour), ObservedAt: time.Now()}
		m.RecordQuotaResetScheduleIfUnchanged(context.Background(), a, schedule)
		id := "account-a"
		if changed {
			id = "account-b"
		}
		updated, err := m.Update(context.Background(), &Auth{ID: a.ID, Provider: a.Provider, Metadata: map[string]any{"account_id": id}})
		if err != nil {
			t.Fatal(err)
		}
		if changed {
			if !updated.QuotaResetSchedule.ObservedAt.IsZero() {
				t.Fatal("different account inherited schedule")
			}
		} else if updated.QuotaResetSchedule != schedule {
			t.Fatal("same account lost schedule")
		}
	}
}

func TestQuotaResetScheduleObservationChronology(t *testing.T) {
	now := time.Date(2026, 10, 5, 1, 0, 0, 0, time.UTC)
	stored := QuotaResetSchedule{WeeklyResetAt: now.Add(24 * time.Hour), FiveHourResetAt: now.Add(time.Hour), ObservedAt: now}
	for _, tc := range []struct {
		name     string
		observed time.Time
		signals  map[string]string
		want     QuotaResetSchedule
	}{
		{"malformed", now.Add(time.Minute), map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": "bad"}, stored},
		{"older", now.Add(-time.Minute), map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": strconv.FormatInt(now.Add(48*time.Hour).Unix(), 10)}, stored},
		{"equal", now, map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": strconv.FormatInt(now.Add(48*time.Hour).Unix(), 10)}, stored},
		{"both", now.Add(time.Minute), map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": strconv.FormatInt(now.Add(48*time.Hour).Unix(), 10), "Anthropic-Ratelimit-Unified-5h-Reset": strconv.FormatInt(now.Add(3*time.Hour).Unix(), 10)}, QuotaResetSchedule{WeeklyResetAt: now.Add(48 * time.Hour), FiveHourResetAt: now.Add(3 * time.Hour), ObservedAt: now.Add(time.Minute)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := EffectiveQuotaResetSchedule(&Auth{Provider: "claude", QuotaResetSchedule: stored, Quota: QuotaState{ObservedAt: tc.observed, Signals: tc.signals}})
			if got != tc.want {
				t.Fatalf("schedule=%+v want %+v", got, tc.want)
			}
		})
	}
}

func TestQuotaResetScheduleSequentialSparseResults(t *testing.T) {
	m := NewManager(nil, nil, nil)
	a, err := m.Register(WithSkipPersist(context.Background()), &Auth{ID: "sparse", Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	weekly, five := now.Add(24*time.Hour), now.Add(time.Hour)
	for _, window := range []struct {
		name  string
		reset time.Time
	}{{"Anthropic-Ratelimit-Unified-7d-Reset", weekly}, {"Anthropic-Ratelimit-Unified-5h-Reset", five}} {
		ctx := internallogging.WithResponseHeadersHolder(WithSkipPersist(context.Background()))
		h := http.Header{}
		h.Set(window.name, strconv.FormatInt(window.reset.Unix(), 10))
		internallogging.SetResponseHeaders(ctx, h)
		m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Success: true})
	}
	got, _ := m.GetByID(a.ID)
	if !got.QuotaResetSchedule.WeeklyResetAt.Equal(weekly) || !got.QuotaResetSchedule.FiveHourResetAt.Equal(five) {
		t.Fatalf("sparse observations not retained: %+v", got.QuotaResetSchedule)
	}
	if got.QuotaResetSchedule.ObservedAt != got.Quota.ObservedAt {
		t.Fatal("latest merged observation not recorded")
	}
	if len(got.Quota.Signals) != 1 {
		t.Fatal("passive quota signals were accumulated")
	}
	expected := got.Clone()
	published := m.RecordQuotaResetScheduleIfUnchanged(context.Background(), expected, QuotaResetSchedule{WeeklyResetAt: weekly.Add(time.Hour), ObservedAt: got.QuotaResetSchedule.ObservedAt.Add(time.Second)})
	if published == nil || !published.QuotaResetSchedule.FiveHourResetAt.Equal(five) {
		t.Fatal("partial usage observation lost known header window")
	}
	if got.Unavailable || got.Quota.Exceeded || !got.NextRetryAfter.IsZero() {
		t.Fatal("schedule observation changed availability")
	}
}

func TestQuotaResetScheduleDuplicateCodexWindows(t *testing.T) {
	now := time.Date(2026, 10, 5, 1, 0, 0, 0, time.UTC)
	stored := QuotaResetSchedule{WeeklyResetAt: now.Add(24 * time.Hour), FiveHourResetAt: now.Add(time.Hour), ObservedAt: now.Add(-time.Minute)}
	for _, duration := range []string{"10080", "300"} {
		for _, missing := range []string{"neither", "primary", "secondary"} {
			t.Run(duration+"/missing="+missing, func(t *testing.T) {
				h := http.Header{}
				for _, slot := range []string{"primary", "secondary"} {
					prefix := "X-Codex-" + slot
					h.Set(prefix+"-Window-Minutes", duration)
					if slot != missing {
						h.Set(prefix+"-Reset-At", strconv.FormatInt(now.Add(2*time.Hour).Unix(), 10))
					}
				}
				parsed := quotaScheduleFromHeaders("codex", h, now)
				if !parsed.WeeklyResetAt.IsZero() || !parsed.FiveHourResetAt.IsZero() {
					t.Fatalf("ambiguous duplicate observation accepted: %+v", parsed)
				}
				signals := map[string]string{}
				for name := range h {
					signals[name] = h.Get(name)
				}
				got := EffectiveQuotaResetSchedule(&Auth{Provider: "codex", QuotaResetSchedule: stored, Quota: QuotaState{ObservedAt: now, Signals: signals}})
				if got != stored {
					t.Fatalf("rejected duplicate replaced known schedule: %+v", got)
				}
			})
		}
	}
}
