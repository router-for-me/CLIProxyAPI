package auth

import (
	"context"
	"testing"
)

// registerPoolStrategyAuth registers one pool auth (compound provider_key plus
// per-entry entry_provider_key) carrying the given in-pool routing strategy.
func registerPoolStrategyAuth(t *testing.T, m *Manager, id, pool, strategy string) *Auth {
	t.Helper()
	attrs := map[string]string{
		"provider_key":            pool,
		AttributeEntryProviderKey: pool + ":key-71",
		AttributeAuthKind:         AuthKindAPIKey,
		AttributeAPIKey:           "k-" + id,
	}
	if strategy != "" {
		attrs[AttributePoolStrategy] = strategy
	}
	auth := &Auth{
		ID:         id,
		Provider:   "claude",
		Status:     StatusActive,
		Attributes: attrs,
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register(%s) error = %v", id, errRegister)
	}
	return auth
}

// TestManager_PoolStrategyForProviderKeys_AgreesWithinPool covers the route
// default resolution: every matching auth's pool strategy must agree, matching
// must follow auth-selection semantics (compound row key plus per-entry key),
// and auths without a strategy contribute nothing.
func TestManager_PoolStrategyForProviderKeys_AgreesWithinPool(t *testing.T) {
	cases := []struct {
		name string
		// pools maps auth id -> (pool routing key, pool strategy)
		pools map[string][2]string
		keys  []string
		want  string
	}{
		{
			name: "single_pool_same_strategy_returned",
			pools: map[string][2]string{
				"auth-a": {"claude:7", "round-robin"},
				"auth-b": {"claude:7", "round-robin"},
			},
			keys: []string{"claude:7"},
			want: "round-robin",
		},
		{
			name: "pool_row_key_matches_via_entry_key",
			pools: map[string][2]string{
				"auth-a": {"claude:7", "fill-first"},
			},
			keys: []string{"claude:7:key-71"},
			want: "fill-first",
		},
		{
			name: "disagreeing_pools_return_empty",
			pools: map[string][2]string{
				"auth-a": {"claude:7", "round-robin"},
				"auth-b": {"claude:9", "fill-first"},
			},
			keys: []string{"claude:7", "claude:9"},
			want: "",
		},
		{
			name: "no_strategy_anywhere_returns_empty",
			pools: map[string][2]string{
				"auth-a": {"claude:7", ""},
				"auth-b": {"claude:9", ""},
			},
			keys: []string{"claude:7", "claude:9"},
			want: "",
		},
		{
			// A pool without a strategy has no opinion and does not veto the
			// agreeing pools: the route default is the one expressed strategy.
			name: "strategyless_pool_does_not_veto_agreeing_strategy",
			pools: map[string][2]string{
				"auth-a": {"claude:7", "round-robin"},
				"auth-b": {"claude:9", ""},
			},
			keys: []string{"claude:7", "claude:9"},
			want: "round-robin",
		},
		{
			name: "no_matching_auth_returns_empty",
			pools: map[string][2]string{
				"auth-a": {"claude:7", "round-robin"},
			},
			keys: []string{"claude:42"},
			want: "",
		},
		{
			name: "empty_keys_returns_empty",
			pools: map[string][2]string{
				"auth-a": {"claude:7", "round-robin"},
			},
			keys: nil,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			for id, pool := range tc.pools {
				registerPoolStrategyAuth(t, manager, id, pool[0], pool[1])
			}
			if got := manager.PoolStrategyForProviderKeys(tc.keys...); got != tc.want {
				t.Fatalf("PoolStrategyForProviderKeys(%v) = %q, want %q", tc.keys, got, tc.want)
			}
		})
	}
}

// TestManager_PoolStrategyForProviderKeys_NilManager pins the defensive
// contract: a nil manager (handlers constructed without one) reports no
// strategy instead of panicking.
func TestManager_PoolStrategyForProviderKeys_NilManager(t *testing.T) {
	var manager *Manager
	if got := manager.PoolStrategyForProviderKeys("claude:7"); got != "" {
		t.Fatalf("PoolStrategyForProviderKeys on nil manager = %q, want empty", got)
	}
}

// TestManager_PoolStrategyForProviderKeys_StrategylessAuthInSamePool pins
// that a pool with a strategy is not vetoed by an unrelated strategyless auth
// of the same pool row: the route pins the pool, and the pool's own auths
// agree.
func TestManager_PoolStrategyForProviderKeys_StrategylessAuthInSamePoolIgnoredWhenOthersAgree(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	registerPoolStrategyAuth(t, manager, "auth-a", "claude:7", "round-robin")
	// A second auth of the same pool row without the attribute: the pool still
	// agrees (the synthesizer stamps every entry, but a partially-rendered or
	// legacy row must not block the default).
	registerPoolStrategyAuth(t, manager, "auth-b", "claude:7", "")
	if got := manager.PoolStrategyForProviderKeys("claude:7"); got != "round-robin" {
		t.Fatalf("PoolStrategyForProviderKeys(claude:7) = %q, want round-robin (pool row agrees)", got)
	}
}
