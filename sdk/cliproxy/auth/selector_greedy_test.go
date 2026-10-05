package auth

import (
	"context"
	"reflect"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestGreedyRanking(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	for _, tc := range []struct {
		name           string
		aw, bw, af, bf time.Time
		aid, bid, want string
	}{
		{"weekly", future, future.Add(time.Hour), time.Time{}, time.Time{}, "z", "a", "z"},
		{"missing weekly", time.Time{}, future, time.Time{}, time.Time{}, "a", "z", "z"},
		{"past weekly", now.Add(-time.Hour), future, time.Time{}, time.Time{}, "a", "z", "z"},
		{"five hour", future, future, future, future.Add(time.Hour), "z", "a", "z"},
		{"missing five hour", future, future, time.Time{}, future, "a", "z", "z"},
		{"past five hour", future, future, now.Add(-time.Hour), future, "a", "z", "z"},
		{"unknown weekly five hour", time.Time{}, now.Add(-time.Hour), future, future.Add(time.Hour), "z", "a", "z"},
		{"ID", time.Time{}, time.Time{}, time.Time{}, time.Time{}, "z", "a", "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Auth{ID: tc.aid, Provider: "claude", QuotaResetSchedule: QuotaResetSchedule{WeeklyResetAt: tc.aw, FiveHourResetAt: tc.af, ObservedAt: now}}
			b := &Auth{ID: tc.bid, Provider: "claude", QuotaResetSchedule: QuotaResetSchedule{WeeklyResetAt: tc.bw, FiveHourResetAt: tc.bf, ObservedAt: now}}
			beforeA, beforeB := a.Clone(), b.Clone()
			for _, candidates := range [][]*Auth{{a, b}, {b, a}} {
				got, err := (&GreedySelector{}).Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, candidates)
				if err != nil || got.ID != tc.want {
					t.Fatalf("got %v err %v want %s", got, err, tc.want)
				}
			}
			if !reflect.DeepEqual(a, beforeA) || !reflect.DeepEqual(b, beforeB) {
				t.Fatal("selection mutated auth")
			}
		})
	}
}

func TestGreedySchedulerEligibilityAndMixed(t *testing.T) {
	now := time.Now()
	late := now.Add(2 * time.Hour)
	early := now.Add(time.Hour)
	for _, mixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "mixed"}[mixed], func(t *testing.T) {
			a := &Auth{ID: "a", Provider: "claude", QuotaResetSchedule: QuotaResetSchedule{WeeklyResetAt: late, ObservedAt: now}}
			b := &Auth{ID: "b", Provider: "claude", QuotaResetSchedule: QuotaResetSchedule{WeeklyResetAt: early, ObservedAt: now}}
			if mixed {
				b.Provider = "codex"
			}
			s := newAuthScheduler(&GreedySelector{})
			s.rebuild([]*Auth{a, b})
			pick := func(tried map[string]struct{}) *Auth {
				var got *Auth
				var err error
				if mixed {
					got, _, err = s.pickMixed(context.Background(), []string{"claude", "codex"}, "", cliproxyexecutor.Options{}, tried)
				} else {
					got, err = s.pickSingle(context.Background(), "claude", "", cliproxyexecutor.Options{}, tried)
				}
				if err != nil {
					t.Fatal(err)
				}
				return got
			}
			for i := 0; i < 3; i++ {
				if got := pick(nil); got.ID != "b" {
					t.Fatalf("greedy must not rotate: %s", got.ID)
				}
			}
			if got := pick(map[string]struct{}{"b": {}}); got.ID != "a" {
				t.Fatal("tried credential selected")
			}
			a.Attributes = map[string]string{"priority": "10"}
			s.upsertAuth(a)
			if got := pick(nil); got.ID != "a" {
				t.Fatal("quota rank overrode priority")
			}
			a.Attributes = nil
			s.upsertAuth(a)
			b.Unavailable = true
			b.NextRetryAfter = now.Add(time.Hour)
			b.Quota.Exceeded = true
			s.upsertAuth(b)
			if got := pick(nil); got.ID != "a" {
				t.Fatal("cooling credential selected")
			}
			b.Unavailable = false
			b.NextRetryAfter = time.Time{}
			b.Quota.Exceeded = false
			s.upsertAuth(b)
			a.Disabled = true
			s.upsertAuth(a)
			if got := pick(nil); got.ID != "b" {
				t.Fatal("disabled credential selected")
			}
		})
	}
}

func TestGreedyWebsocketAndManagerFastPath(t *testing.T) {
	now := time.Now()
	a := &Auth{ID: "a", Provider: "codex", QuotaResetSchedule: QuotaResetSchedule{WeeklyResetAt: now.Add(time.Hour), ObservedAt: now}}
	b := &Auth{ID: "b", Provider: "codex", Attributes: map[string]string{"websockets": "true"}, QuotaResetSchedule: QuotaResetSchedule{WeeklyResetAt: now.Add(2 * time.Hour), ObservedAt: now}}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	selector := &GreedySelector{}
	got, err := selector.Pick(ctx, "codex", "", cliproxyexecutor.Options{}, []*Auth{a, b})
	if err != nil || got.ID != "b" {
		t.Fatalf("WS preference lost: %v %v", got, err)
	}
	scheduler := newAuthScheduler(selector)
	scheduler.rebuild([]*Auth{a, b})
	got, err = scheduler.pickSingle(ctx, "codex", "", cliproxyexecutor.Options{}, nil)
	if err != nil || got.ID != "b" {
		t.Fatalf("scheduler WS preference lost: %v %v", got, err)
	}
	m := NewManager(nil, selector, nil)
	m.RegisterExecutor(schedulerTestExecutor{provider: "codex"})
	for _, auth := range []*Auth{a, b} {
		if _, err = m.Register(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
	}
	if !m.useSchedulerFastPath() {
		t.Fatal("greedy bypassed builtin fast path")
	}
	for i := 0; i < 3; i++ {
		got, _, err = m.pickNext(context.Background(), "codex", "", cliproxyexecutor.Options{}, nil)
		if err != nil || got.ID != "a" {
			t.Fatalf("manager used wrong strategy: %v %v", got, err)
		}
	}
}
