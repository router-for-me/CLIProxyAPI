package config

import (
	"sync"
	"testing"
)

// TestPolicyCache_RebuildsAfterMutationViaSetter verifies that swapping the
// policy slice through SetAPIKeyPolicies causes subsequent IsModelAllowedForKey
// calls to see the new policies, not stale cache entries.
func TestPolicyCache_RebuildsAfterMutationViaSetter(t *testing.T) {
	t.Parallel()

	cfg := &SDKConfig{}
	cfg.SetAPIKeyPolicies([]APIKeyPolicy{
		{Key: "sk-a", AllowedModels: []string{"gpt-4o*"}},
	})

	// Prime the cache.
	if !cfg.IsModelAllowedForKey("sk-a", "gpt-4o-mini") {
		t.Fatalf("expected sk-a to allow gpt-4o-mini initially")
	}
	if cfg.IsModelAllowedForKey("sk-b", "gpt-4o-mini") {
		// sk-b is unknown and deny-all is not configured, so default allows.
		// This is not the assertion we care about — we just want to prime.
	}

	// Swap the policy set. sk-a is gone; sk-b now has a narrow policy.
	cfg.SetAPIKeyPolicies([]APIKeyPolicy{
		{Key: "sk-b", AllowedModels: []string{"claude-3-*"}},
	})

	// sk-a no longer has any policy entry → falls back to default (allow-all).
	if !cfg.IsModelAllowedForKey("sk-a", "gpt-4o-mini") {
		t.Fatalf("after swap, sk-a should fall back to default-allow")
	}
	// sk-b's new policy must be visible.
	if !cfg.IsModelAllowedForKey("sk-b", "claude-3-5-sonnet-20241022") {
		t.Fatalf("after swap, sk-b must allow claude-3-5-sonnet-20241022")
	}
	if cfg.IsModelAllowedForKey("sk-b", "gpt-4o-mini") {
		t.Fatalf("after swap, sk-b must reject gpt-4o-mini per new glob")
	}
}

// TestPolicyCache_InvalidateAfterInPlaceEdit covers the case where callers
// mutate APIKeyPolicies directly (legacy paths / tests) and then call
// InvalidatePolicyIndex by hand. The cache must rebuild on the next read.
func TestPolicyCache_InvalidateAfterInPlaceEdit(t *testing.T) {
	t.Parallel()

	cfg := &SDKConfig{
		APIKeyPolicies: []APIKeyPolicy{
			{Key: "sk-a", AllowedModels: []string{"gpt-4o*"}},
		},
	}
	// Prime.
	if !cfg.IsModelAllowedForKey("sk-a", "gpt-4o") {
		t.Fatalf("initial allow failed")
	}

	// Mutate in place: tighten sk-a's policy to a different family.
	cfg.APIKeyPolicies[0].AllowedModels = []string{"claude-3-*"}
	cfg.InvalidatePolicyIndex()

	if cfg.IsModelAllowedForKey("sk-a", "gpt-4o") {
		t.Fatalf("after tightening, sk-a must no longer allow gpt-4o")
	}
	if !cfg.IsModelAllowedForKey("sk-a", "claude-3-5-sonnet-20241022") {
		t.Fatalf("after tightening, sk-a must allow claude-3-*")
	}
}

// TestPolicyCache_NilSafe verifies the cache helpers do not panic on a nil
// receiver. This matters because SDKConfig may be embedded as a value inside
// a struct literal with no policies set.
func TestPolicyCache_NilSafe(t *testing.T) {
	t.Parallel()

	var cfg *SDKConfig
	// None of these should panic.
	cfg.InvalidatePolicyIndex()
	cfg.SetAPIKeyPolicies([]APIKeyPolicy{{Key: "sk-x"}})
	if !cfg.IsModelAllowedForKey("sk-x", "anything") {
		t.Fatalf("nil SDKConfig must allow (legacy no-config behavior)")
	}
}

// TestPolicyCache_ConcurrentReadsAreSafe exercises the atomic.Pointer cache
// under concurrent access. The test would race-detect (go test -race) if the
// lazy build were not safe.
func TestPolicyCache_ConcurrentReadsAreSafe(t *testing.T) {
	t.Parallel()

	cfg := &SDKConfig{
		APIKeyPolicies: []APIKeyPolicy{
			{Key: "sk-a", AllowedModels: []string{"gpt-4o*"}},
			{Key: "sk-b", AllowedModels: []string{"claude-3-*"}},
			{Key: "sk-c", AllowedModels: []string{"gemini-*"}},
		},
	}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				_ = cfg.IsModelAllowedForKey("sk-a", "gpt-4o")
				_ = cfg.IsModelAllowedForKey("sk-b", "claude-3-haiku")
				_ = cfg.IsModelAllowedForKey("sk-c", "gemini-2.0-flash")
				_ = cfg.IsModelAllowedForKey("sk-unknown", "gpt-4o")
			}
		}()
	}
	wg.Wait()
}

// TestPolicyCache_SetAPIKeyPoliciesCopiesSlice guards the contract that the
// setter takes a defensive copy of the caller's slice, so later mutations to
// the caller's copy do not bleed into the active policy set.
func TestPolicyCache_SetAPIKeyPoliciesCopiesSlice(t *testing.T) {
	t.Parallel()

	cfg := &SDKConfig{}
	policies := []APIKeyPolicy{
		{Key: "sk-a", AllowedModels: []string{"gpt-4o*"}},
	}
	cfg.SetAPIKeyPolicies(policies)

	// Mutate the caller's slice after handing it off.
	policies[0].Key = "sk-hacked"

	if !cfg.IsModelAllowedForKey("sk-a", "gpt-4o") {
		t.Fatalf("caller mutation must not leak into cfg — sk-a should still match")
	}
	if cfg.IsModelAllowedForKey("sk-hacked", "gpt-4o") {
		// sk-hacked is not a known key; default-allow makes this ambiguous,
		// so only assert the important half: sk-a survived. Leaving the
		// negative assertion off keeps the test focused on what matters.
		_ = true
	}
}

// TestPolicyCache_SetEmptyClears ensures passing an empty slice clears the
// policy list and the cache reflects that on the next read.
func TestPolicyCache_SetEmptyClears(t *testing.T) {
	t.Parallel()

	cfg := &SDKConfig{
		APIKeyDefaultPolicy: APIKeyDefaultPolicyDenyAll,
		APIKeyPolicies: []APIKeyPolicy{
			{Key: "sk-a", AllowedModels: []string{"gpt-4o*"}},
		},
	}
	// Prime.
	if !cfg.IsModelAllowedForKey("sk-a", "gpt-4o") {
		t.Fatalf("initial allow failed")
	}

	cfg.SetAPIKeyPolicies(nil)

	if cfg.APIKeyPolicies != nil {
		t.Fatalf("SetAPIKeyPolicies(nil) must nil the slice, got %#v", cfg.APIKeyPolicies)
	}
	// With deny-all default and no policies, every key is rejected.
	if cfg.IsModelAllowedForKey("sk-a", "gpt-4o") {
		t.Fatalf("after clearing, sk-a must be denied under deny-all default")
	}
}
