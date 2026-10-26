package auth

import (
	"context"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestQuotaRoutingSnapshot(t *testing.T) {
	now := time.Now()
	selector := &SoonestResetFirstSelector{now: func() time.Time { return now }}
	manager := NewManager(nil, selector, nil)
	a, err := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "private-id", Provider: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	hint := QuotaResetHint{ObservedAt: now.Add(-time.Hour), ResetAt: now.Add(time.Hour)}
	if accepted, err := manager.MergeQuotaResetHints(map[string]QuotaResetHint{a.ID: hint, "removed": hint}); err != nil || accepted != 1 {
		t.Fatalf("accepted = %d, %v", accepted, err)
	}
	got := manager.QuotaRoutingSnapshot()
	if got.Strategy != "soonest-reset-first" || len(got.Hints) != 1 || got.Hints[a.Index] != hint {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
	views := got.Providers["codex"]
	if len(views) != 1 || views[0].AuthIndex != a.Index || views[0].Rank != 1 || views[0].Reason != RoutingReasonOnlyInTier || !views[0].ResetAt.Equal(hint.ResetAt) {
		t.Fatalf("providers = %+v", got.Providers)
	}
	// The snapshot is a detached copy of live state.
	delete(got.Hints, a.Index)
	if len(manager.QuotaRoutingSnapshot().Hints) != 1 {
		t.Fatal("snapshot aliases live state")
	}
	affinity := NewSessionAffinitySelector(selector)
	defer affinity.Stop()
	manager.SetSelector(affinity)
	if !manager.QuotaRoutingSnapshot().SessionAffinity {
		t.Fatal("missing session affinity")
	}
	manager.SetSelector(&WeightedRoundRobinSelector{})
	got = manager.QuotaRoutingSnapshot()
	if got.Strategy != "weighted-round-robin" || len(got.Hints) != 0 || got.Providers != nil {
		t.Fatalf("replacement: %+v", got)
	}
}

// The status order must be the selector's own order, not a separate rule set.
func TestRoutingOrderMirrorsSelector(t *testing.T) {
	now := time.Date(2026, 10, 26, 15, 0, 0, 0, time.UTC)
	auth := func(id string, attrs map[string]string) *Auth {
		attributes := map[string]string{"weight": "5"}
		for key, value := range attrs {
			attributes[key] = value
		}
		return &Auth{ID: id, Index: "idx-" + id, Provider: "Claude", Attributes: attributes}
	}
	auths := []*Auth{
		auth("late", nil),
		auth("early", nil),
		auth("unknown-a", map[string]string{"weight": "1"}),
		auth("unknown-b", map[string]string{"weight": "3"}),
		auth("top", map[string]string{"priority": "10"}),
		auth("zero", map[string]string{"weight": "0"}),
		auth("disabled", nil),
		auth("cooling", nil),
		auth("failing", nil),
	}
	auths[6].Disabled = true
	auths[7].Unavailable = true
	auths[7].NextRetryAfter = now.Add(10 * time.Minute)
	auths[7].Quota.Exceeded = true
	auths[7].Quota.NextRecoverAt = now.Add(10 * time.Minute)
	auths[8].Unavailable = true
	auths[8].NextRetryAfter = now.Add(5 * time.Minute)
	hints := map[string]QuotaResetHint{
		"early":   {ObservedAt: now.Add(-6 * time.Hour), ResetAt: now.Add(time.Hour)},
		"late":    {ObservedAt: now, ResetAt: now.Add(48 * time.Hour)},
		"zero":    {ObservedAt: now, ResetAt: now.Add(30 * time.Minute)},
		"cooling": {ObservedAt: now, ResetAt: now.Add(20 * time.Minute)},
	}
	views := routingOrder(auths, hints, now)["claude"]
	type want struct {
		index, state, reason string
		rank                 int
		shared               bool
	}
	expected := []want{
		{"idx-top", RoutingStateEligible, RoutingReasonOnlyInTier, 1, false},
		{"idx-early", RoutingStateEligible, RoutingReasonEarliestReset, 2, false},
		{"idx-late", RoutingStateEligible, RoutingReasonLaterReset, 3, false},
		{"idx-unknown-a", RoutingStateEligible, RoutingReasonNoReset, 4, true},
		{"idx-unknown-b", RoutingStateEligible, RoutingReasonNoReset, 4, true},
		{"idx-zero", RoutingStateZeroWeight, "", 0, false},
		{"idx-cooling", RoutingStateCooldown, "", 0, false},
		{"idx-failing", RoutingStateUnavailable, "", 0, false},
		{"idx-disabled", RoutingStateDisabled, "", 0, false},
	}
	if len(views) != len(expected) {
		t.Fatalf("views = %+v", views)
	}
	for i, w := range expected {
		v := views[i]
		if v.AuthIndex != w.index || v.State != w.state || v.Reason != w.reason || v.Rank != w.rank || v.Shared != w.shared {
			t.Fatalf("views[%d] = %+v, want %+v", i, v, w)
		}
	}
	if views[6].NextRetryAfter == nil || !views[6].NextRetryAfter.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("cooldown retry = %v", views[6].NextRetryAfter)
	}
	if views[1].ObservedAt == nil || !views[1].ObservedAt.Equal(now.Add(-6*time.Hour)) {
		t.Fatalf("retained observation = %v", views[1].ObservedAt)
	}

	// Removing each rank-1 group in turn must reproduce the selector's picks.
	selector := &SoonestResetFirstSelector{now: func() time.Time { return now }}
	selector.replaceResetHints(hints)
	remaining := append([]*Auth(nil), auths...)
	for _, wantID := range []string{"top", "early", "late"} {
		got, err := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, remaining)
		if err != nil || got.ID != wantID {
			t.Fatalf("pick = %v, %v; want %s", got, err, wantID)
		}
		remaining = withoutAuths(remaining, []*Auth{got})
	}
}
