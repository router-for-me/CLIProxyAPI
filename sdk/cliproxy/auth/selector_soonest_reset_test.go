package auth

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestSoonestResetFirstPrefersEarlierResetOverLargerQuota(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	selector := &SoonestResetFirstSelector{now: func() time.Time { return now }}
	selector.replaceResetHints(map[string]QuotaResetHint{
		"a-later":  {ObservedAt: now, ResetAt: now.Add(4 * 24 * time.Hour)},
		"z-sooner": {ObservedAt: now, ResetAt: now.Add(time.Hour)},
	})
	auths := []*Auth{
		{ID: "a-later", Provider: "codex", Attributes: map[string]string{"weight": "20"}},
		{ID: "z-sooner", Provider: "codex", Attributes: map[string]string{"weight": "1"}},
	}
	for range 10 {
		got, err := selector.Pick(context.Background(), "codex", "model", cliproxyexecutor.Options{}, auths)
		if err != nil || got == nil || got.ID != "z-sooner" {
			t.Fatalf("pick = %v, %v; want z-sooner", got, err)
		}
	}
}

func TestSoonestResetFirstAvailabilityAndFallback(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		mutate func(*Auth, map[string]QuotaResetHint)
	}{
		{"disabled", func(a *Auth, _ map[string]QuotaResetHint) { a.Disabled = true }},
		{"zero weight", func(a *Auth, _ map[string]QuotaResetHint) { a.Attributes["weight"] = "0" }},
		{"cooldown", func(a *Auth, _ map[string]QuotaResetHint) {
			a.Unavailable = true
			a.NextRetryAfter = now.Add(time.Minute)
		}},
		{"lower manual priority", func(a *Auth, _ map[string]QuotaResetHint) { a.Attributes["priority"] = "-1" }},
		{"missing hint", func(a *Auth, hints map[string]QuotaResetHint) { delete(hints, a.ID) }},
		{"stale hint", func(a *Auth, hints map[string]QuotaResetHint) {
			h := hints[a.ID]
			h.ObservedAt = now.Add(-QuotaResetHintMaxAge - time.Second)
			hints[a.ID] = h
		}},
		{"future observation", func(a *Auth, hints map[string]QuotaResetHint) {
			h := hints[a.ID]
			h.ObservedAt = now.Add(time.Second)
			hints[a.ID] = h
		}},
		{"elapsed reset", func(a *Auth, hints map[string]QuotaResetHint) { h := hints[a.ID]; h.ResetAt = now; hints[a.ID] = h }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Auth{ID: "early", Provider: "codex", Attributes: map[string]string{"weight": "20"}}
			b := &Auth{ID: "late", Provider: "codex", Attributes: map[string]string{"weight": "1"}}
			hints := map[string]QuotaResetHint{
				a.ID: {ObservedAt: now, ResetAt: now.Add(time.Hour)},
				b.ID: {ObservedAt: now, ResetAt: now.Add(24 * time.Hour)},
			}
			tc.mutate(a, hints)
			selector := &SoonestResetFirstSelector{now: func() time.Time { return now }}
			selector.replaceResetHints(hints)
			got, err := selector.Pick(context.Background(), "codex", "model", cliproxyexecutor.Options{}, []*Auth{a, b})
			if err != nil || got == nil || got.ID != b.ID {
				t.Fatalf("pick = %v, %v; want late", got, err)
			}
		})
	}
}

func TestSoonestResetFirstWeightedTiesAndMissingHints(t *testing.T) {
	now := time.Now()
	for _, known := range []bool{false, true} {
		t.Run(strconv.FormatBool(known), func(t *testing.T) {
			selector := &SoonestResetFirstSelector{now: func() time.Time { return now }}
			auths := []*Auth{
				{ID: "a", Attributes: map[string]string{"weight": "1"}},
				{ID: "b", Attributes: map[string]string{"weight": "3"}},
			}
			if known {
				selector.replaceResetHints(map[string]QuotaResetHint{"a": {ObservedAt: now, ResetAt: now.Add(time.Hour)}, "b": {ObservedAt: now, ResetAt: now.Add(time.Hour)}})
			}
			counts := map[string]int{}
			for range 40 {
				got, err := selector.Pick(context.Background(), "codex", "model", cliproxyexecutor.Options{}, auths)
				if err != nil {
					t.Fatal(err)
				}
				counts[got.ID]++
			}
			if counts["a"] != 10 || counts["b"] != 30 {
				t.Fatalf("counts = %v", counts)
			}
		})
	}
}

func TestSoonestResetFirstManagerAffinityAndFailover(t *testing.T) {
	for _, sticky := range []bool{false, true} {
		for _, mixed := range []bool{false, true} {
			t.Run("sticky="+strconv.FormatBool(sticky)+"/mixed="+strconv.FormatBool(mixed), func(t *testing.T) {
				ctx := context.Background()
				provider := "reset-test"
				model := "reset-test-model"
				now := time.Now()
				resetSelector := &SoonestResetFirstSelector{now: func() time.Time { return now }}
				var selector Selector = resetSelector
				if sticky {
					affinity := NewSessionAffinitySelector(resetSelector)
					defer affinity.Stop()
					selector = affinity
				}
				manager := NewManager(nil, selector, nil)
				manager.RegisterExecutor(schedulerTestExecutor{provider: provider})
				for _, id := range []string{"reset-a", "reset-b"} {
					a := &Auth{ID: id, Provider: provider, Status: StatusActive, Attributes: map[string]string{"weight": "1"}}
					if _, err := manager.Register(WithSkipPersist(ctx), a); err != nil {
						t.Fatal(err)
					}
					registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				}
				publish := func(early, late string) {
					t.Helper()
					if err := manager.ReplaceQuotaResetHints(map[string]QuotaResetHint{early: {ObservedAt: now, ResetAt: now.Add(time.Hour)}, late: {ObservedAt: now, ResetAt: now.Add(24 * time.Hour)}}); err != nil {
						t.Fatal(err)
					}
				}
				pick := func(session string, tried map[string]struct{}, want string) {
					t.Helper()
					opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: session}}
					var got *Auth
					var err error
					if mixed {
						got, _, _, err = manager.pickNextMixed(ctx, []string{provider}, model, opts, tried)
					} else {
						got, _, err = manager.pickNext(ctx, provider, model, opts, tried)
					}
					if err != nil || got == nil || got.ID != want {
						t.Fatalf("pick = %v, %v; want %s", got, err, want)
					}
				}
				publish("reset-b", "reset-a")
				pick("old", nil, "reset-b")
				publish("reset-a", "reset-b")
				want := "reset-a"
				if sticky {
					want = "reset-b"
				}
				pick("old", nil, want)
				pick("new", nil, "reset-a")
				pick("retry", map[string]struct{}{"reset-a": {}}, "reset-b")
				// Exhausted high-priority credentials must not hide lower tiers.
				a, _ := manager.GetByID("reset-b")
				if _, _, err := manager.Modify(WithSkipPersist(ctx), a.ID, a.RegistrationEpoch, func(current *Auth) error {
					current.Attributes["weight"] = "0"
					current.Attributes["priority"] = "100"
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				pick("old", nil, "reset-a")
			})
		}
	}
}

func TestSoonestResetHintsConcurrentReplacement(t *testing.T) {
	now := time.Now()
	selector := &SoonestResetFirstSelector{now: func() time.Time { return now }}
	manager := NewManager(nil, selector, nil)
	auths := []*Auth{{ID: "a"}, {ID: "b"}}
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 100 {
				if err := manager.ReplaceQuotaResetHints(map[string]QuotaResetHint{"b": {ObservedAt: now, ResetAt: now.Add(time.Hour)}}); err != nil {
					t.Error(err)
				}
				if got, err := selector.Pick(context.Background(), "codex", "m", cliproxyexecutor.Options{}, auths); err != nil || got == nil {
					t.Errorf("pick = %v, %v", got, err)
				}
			}
		}()
	}
	workers.Wait()
}

func TestSoonestResetHintsSnapshotAndExpiry(t *testing.T) {
	now := time.Now()
	selector := &SoonestResetFirstSelector{now: func() time.Time { return now }}
	hints := map[string]QuotaResetHint{"b": {ObservedAt: now, ResetAt: now.Add(time.Hour)}}
	selector.replaceResetHints(hints)
	delete(hints, "b")
	auths := []*Auth{{ID: "a", Attributes: map[string]string{"weight": "20"}}, {ID: "b", Attributes: map[string]string{"weight": "1"}}}
	assertPick := func(want string) {
		t.Helper()
		got, err := selector.Pick(context.Background(), "codex", "m", cliproxyexecutor.Options{}, auths)
		if err != nil || got.ID != want {
			t.Fatalf("pick = %v, %v; want %s", got, err, want)
		}
	}
	assertPick("b")
	now = now.Add(QuotaResetHintMaxAge + time.Second)
	assertPick("a")
	selector.replaceResetHints(map[string]QuotaResetHint{"b": {ObservedAt: now, ResetAt: now.Add(time.Hour)}})
	selector.replaceResetHints(nil)
	assertPick("a")
}
