package auth

import (
	"context"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestSessionAffinitySelector_MaxRequestsRebinds(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:    &RoundRobinSelector{},
		TTL:         time.Minute,
		MaxRequests: 2,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"metadata":{"user_id":"user_xxx_account__session_max-req-0001-0000-0000-0000-000000000001"}}`),
	}

	first, err := selector.Pick(context.Background(), "xai", "grok-4.5", opts, auths)
	if err != nil {
		t.Fatalf("Pick #1 error = %v", err)
	}
	second, err := selector.Pick(context.Background(), "xai", "grok-4.5", opts, auths)
	if err != nil {
		t.Fatalf("Pick #2 error = %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("Pick #2 auth = %q, want sticky %q", second.ID, first.ID)
	}

	third, err := selector.Pick(context.Background(), "xai", "grok-4.5", opts, auths)
	if err != nil {
		t.Fatalf("Pick #3 error = %v", err)
	}
	if third.ID == first.ID {
		t.Fatalf("Pick #3 should rebind after max-requests=2, still got %q", third.ID)
	}
}

// Fill-first rebind still returns the first available credential, so max-requests
// does not rotate accounts while that credential remains available. This is
// intentional fill-first semantics, not a max-requests bug.
func TestSessionAffinitySelector_MaxRequestsFillFirstDoesNotRotateWhileFirstAvailable(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:    &FillFirstSelector{},
		TTL:         time.Minute,
		MaxRequests: 2,
	})
	defer selector.Stop()

	// IDs sort so fill-first always prefers auth-a while both are available.
	auths := []*Auth{{ID: "auth-b"}, {ID: "auth-a"}}
	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"metadata":{"user_id":"user_xxx_account__session_max-req-fill-0000-0000-0000-000000000010"}}`),
	}

	first, err := selector.Pick(context.Background(), "xai", "grok-4.5", opts, auths)
	if err != nil {
		t.Fatalf("Pick #1 error = %v", err)
	}
	if first.ID != "auth-a" {
		t.Fatalf("Pick #1 auth = %q, want auth-a (fill-first first available)", first.ID)
	}

	for i := 0; i < 5; i++ {
		got, errPick := selector.Pick(context.Background(), "xai", "grok-4.5", opts, auths)
		if errPick != nil {
			t.Fatalf("Pick #%d error = %v", i+2, errPick)
		}
		if got.ID != first.ID {
			t.Fatalf("Pick #%d auth = %q, want sticky fill-first %q (max-requests rebind must not rotate while first is available)", i+2, got.ID, first.ID)
		}
	}
}

func TestSessionAffinitySelector_MaxRequestsUnlimitedDefault(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:    &RoundRobinSelector{},
		TTL:         time.Minute,
		MaxRequests: -1,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"metadata":{"user_id":"user_xxx_account__session_max-req-unlim-0000-0000-0000-000000000002"}}`),
	}

	first, err := selector.Pick(context.Background(), "xai", "grok-4.5", opts, auths)
	if err != nil {
		t.Fatalf("Pick #1 error = %v", err)
	}
	for i := 0; i < 20; i++ {
		got, errPick := selector.Pick(context.Background(), "xai", "grok-4.5", opts, auths)
		if errPick != nil {
			t.Fatalf("Pick #%d error = %v", i+2, errPick)
		}
		if got.ID != first.ID {
			t.Fatalf("Pick #%d auth = %q, want sticky %q with unlimited max-requests", i+2, got.ID, first.ID)
		}
	}
}

func TestSessionAffinitySelector_ModelRuleOverridesGlobal(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:    &RoundRobinSelector{},
		TTL:         time.Minute,
		MaxRequests: -1,
		Rules: []SessionAffinityRuleLimit{
			{Provider: "xai", Model: "grok-4.5", MaxRequests: 1},
		},
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	grokOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"metadata":{"user_id":"user_xxx_account__session_rule-grok-0000-0000-0000-000000000003"}}`),
	}
	otherOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"metadata":{"user_id":"user_xxx_account__session_rule-other-0000-0000-0000-000000000004"}}`),
	}

	firstGrok, err := selector.Pick(context.Background(), "xai", "grok-4.5", grokOpts, auths)
	if err != nil {
		t.Fatalf("grok Pick #1 error = %v", err)
	}
	secondGrok, err := selector.Pick(context.Background(), "xai", "grok-4.5", grokOpts, auths)
	if err != nil {
		t.Fatalf("grok Pick #2 error = %v", err)
	}
	if secondGrok.ID == firstGrok.ID {
		t.Fatalf("grok should rebind after max-requests=1 rule, still got %q", secondGrok.ID)
	}

	firstOther, err := selector.Pick(context.Background(), "claude", "claude-3", otherOpts, auths)
	if err != nil {
		t.Fatalf("other Pick #1 error = %v", err)
	}
	for i := 0; i < 5; i++ {
		got, errPick := selector.Pick(context.Background(), "claude", "claude-3", otherOpts, auths)
		if errPick != nil {
			t.Fatalf("other Pick #%d error = %v", i+2, errPick)
		}
		if got.ID != firstOther.ID {
			t.Fatalf("other model should stay sticky under global unlimited, got %q want %q", got.ID, firstOther.ID)
		}
	}
}

func TestSessionAffinitySelector_MixedRouteHonorsProviderRule(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:    &RoundRobinSelector{},
		TTL:         time.Minute,
		MaxRequests: -1,
		Rules: []SessionAffinityRuleLimit{
			{Provider: "xai", MaxRequests: 1},
		},
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-a", Provider: "xai"},
		{ID: "auth-b", Provider: "xai"},
	}
	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"metadata":{"user_id":"user_xxx_account__session_mixed-xai-0000-0000-0000-000000000005"}}`),
	}

	first, err := selector.Pick(context.Background(), "mixed", "grok-4.5", opts, auths)
	if err != nil {
		t.Fatalf("Pick #1 error = %v", err)
	}
	second, err := selector.Pick(context.Background(), "mixed", "grok-4.5", opts, auths)
	if err != nil {
		t.Fatalf("Pick #2 error = %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("mixed route should honor provider:xai max-requests=1 and rebind, still got %q", second.ID)
	}
}

func TestSessionAffinitySelector_MixedRouteIgnoresOtherProviderRule(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:    &RoundRobinSelector{},
		TTL:         time.Minute,
		MaxRequests: -1,
		Rules: []SessionAffinityRuleLimit{
			{Provider: "claude", MaxRequests: 1},
		},
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-a", Provider: "xai"},
		{ID: "auth-b", Provider: "xai"},
	}
	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"metadata":{"user_id":"user_xxx_account__session_mixed-other-0000-0000-0000-000000000006"}}`),
	}

	first, err := selector.Pick(context.Background(), "mixed", "grok-4.5", opts, auths)
	if err != nil {
		t.Fatalf("Pick #1 error = %v", err)
	}
	for i := 0; i < 5; i++ {
		got, errPick := selector.Pick(context.Background(), "mixed", "grok-4.5", opts, auths)
		if errPick != nil {
			t.Fatalf("Pick #%d error = %v", i+2, errPick)
		}
		if got.ID != first.ID {
			t.Fatalf("mixed route bound to xai must ignore claude provider rule, got %q want sticky %q", got.ID, first.ID)
		}
	}
}

func TestSessionCache_GetAndRefreshWithHitsIncrements(t *testing.T) {
	t.Parallel()

	cache := NewSessionCache(time.Minute)
	defer cache.Stop()
	cache.Set("s1", "auth-a")

	_, hits, ok := cache.GetAndRefreshWithHits("s1")
	if !ok || hits != 2 {
		t.Fatalf("after Set(hits=1) first refresh hits = %d ok=%v, want 2 true", hits, ok)
	}
	_, hits, ok = cache.GetAndRefreshWithHits("s1")
	if !ok || hits != 3 {
		t.Fatalf("second refresh hits = %d ok=%v, want 3 true", hits, ok)
	}
}
