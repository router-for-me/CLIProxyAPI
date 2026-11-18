package auth

import (
	"context"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"net/http"
	"reflect"
	"strconv"
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

func TestGreedyResultUpdatesExistingModelShards(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		greedy, five, changed bool
	}{
		{"weekly", true, false, true}, {"five_hour", true, true, true}, {"observation_only", true, false, false}, {"round_robin", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var selector Selector = &GreedySelector{}
			if !tc.greedy {
				selector = &RoundRobinSelector{}
			}
			m := NewManager(nil, selector, nil)
			reg := registry.GetGlobalRegistry()
			now := time.Now().UTC().Truncate(time.Second)
			a := &Auth{ID: "greedy-shard-a-" + tc.name, Provider: "claude", Status: StatusActive, QuotaResetSchedule: QuotaResetSchedule{WeeklyResetAt: now.Add(2 * time.Hour), ObservedAt: now.Add(-time.Hour)}}
			b := &Auth{ID: "greedy-shard-b-" + tc.name, Provider: "claude", Status: StatusActive, QuotaResetSchedule: QuotaResetSchedule{WeeklyResetAt: now.Add(3 * time.Hour), ObservedAt: now.Add(-time.Hour)}}
			header := "Anthropic-Ratelimit-Unified-7d-Reset"
			updatedReset := a.QuotaResetSchedule.WeeklyResetAt
			if tc.changed {
				updatedReset = now.Add(4 * time.Hour)
			}
			if tc.five {
				a.QuotaResetSchedule.WeeklyResetAt = now.Add(24 * time.Hour)
				b.QuotaResetSchedule.WeeklyResetAt = a.QuotaResetSchedule.WeeklyResetAt
				a.QuotaResetSchedule.FiveHourResetAt = now.Add(time.Hour)
				b.QuotaResetSchedule.FiveHourResetAt = now.Add(2 * time.Hour)
				header = "Anthropic-Ratelimit-Unified-5h-Reset"
				updatedReset = now.Add(3 * time.Hour)
			}
			for _, auth := range []*Auth{a, b} {
				reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: "model-a"}, {ID: "model-b"}})
				id := auth.ID
				t.Cleanup(func() { reg.UnregisterClient(id) })
				if _, err := m.Register(WithSkipPersist(context.Background()), auth); err != nil {
					t.Fatal(err)
				}
			}
			pick := func(model string) *Auth {
				t.Helper()
				got, err := m.scheduler.pickSingle(context.Background(), "claude", model, cliproxyexecutor.Options{}, nil)
				if err != nil || got == nil {
					t.Fatalf("pick %s: %v %v", model, got, err)
				}
				return got
			}
			for _, model := range []string{"model-a", "model-b"} {
				if got := pick(model); tc.greedy && got.ID != a.ID {
					t.Fatalf("initial %s winner=%s", model, got.ID)
				}
			}
			m.scheduler.mu.Lock()
			before := m.scheduler.providers["claude"].modelShards["model-b"].entries[a.ID].meta
			m.scheduler.mu.Unlock()
			ctx := internallogging.WithResponseHeadersHolder(WithSkipPersist(context.Background()))
			h := http.Header{}
			h.Set(header, strconv.FormatInt(updatedReset.Unix(), 10))
			internallogging.SetResponseHeaders(ctx, h)
			m.MarkResult(ctx, Result{AuthID: a.ID, Provider: "claude", Model: "model-a", Success: true})
			m.scheduler.mu.Lock()
			after := m.scheduler.providers["claude"].modelShards["model-b"].entries[a.ID].meta
			m.scheduler.mu.Unlock()
			if tc.greedy && tc.changed {
				if got := pick("model-b"); got.ID != b.ID {
					t.Fatalf("other model kept old reset rank: got=%s want=%s", got.ID, b.ID)
				}
			} else if after != before {
				t.Fatal("unrelated shard refreshed without greedy reset change")
			}
			current, _ := m.GetByID(a.ID)
			if !current.QuotaResetSchedule.ObservedAt.After(a.QuotaResetSchedule.ObservedAt) {
				t.Fatal("ordinary observation did not advance")
			}
		})
	}
}
