package auth

import (
	"sync"
	"testing"
	"time"
)

func TestSetAnthropicRateLimitHint_RoundTrip(t *testing.T) {
	authID := "claude-test-roundtrip@example.com"
	t.Cleanup(func() { anthropicRateLimitHintByAuth.Delete(authID) })

	want := AnthropicRateLimitHint{
		Known:               true,
		Status:              "allowed",
		RepresentativeClaim: "five_hour",
		Reset:               time.Unix(1777500000, 0).UTC(),
		Windows: map[string]AnthropicQuotaWindow{
			"5h": {
				Status:      "allowed",
				Reset:       time.Unix(1777500000, 0).UTC(),
				Utilization: 0.26,
			},
		},
		FallbackPercentage: 0.5,
	}
	SetAnthropicRateLimitHint(authID, want)

	got, ok := GetAnthropicRateLimitHint(authID)
	if !ok {
		t.Fatalf("expected hint to be present after Set")
	}
	if got.Status != want.Status || got.RepresentativeClaim != want.RepresentativeClaim {
		t.Fatalf("scalar mismatch: got=%+v want=%+v", got, want)
	}
	if !got.Reset.Equal(want.Reset) {
		t.Fatalf("Reset mismatch: got=%v want=%v", got.Reset, want.Reset)
	}
	if got.Windows["5h"].Utilization != 0.26 {
		t.Fatalf("window utilization mismatch: got=%v", got.Windows["5h"].Utilization)
	}
	if got.FallbackPercentage != 0.5 {
		t.Fatalf("FallbackPercentage mismatch: got=%v", got.FallbackPercentage)
	}
	if got.ObservedAt.IsZero() {
		t.Fatalf("ObservedAt should default to non-zero on Set when zero is passed")
	}
}

func TestSetAnthropicRateLimitHint_PreservesNonZeroObservedAt(t *testing.T) {
	authID := "claude-test-preserve-observed-at@example.com"
	t.Cleanup(func() { anthropicRateLimitHintByAuth.Delete(authID) })

	pinned := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known:      true,
		ObservedAt: pinned,
	})

	got, _ := GetAnthropicRateLimitHint(authID)
	if !got.ObservedAt.Equal(pinned) {
		t.Fatalf("ObservedAt overwritten: got=%v want=%v", got.ObservedAt, pinned)
	}
}

func TestGetAnthropicRateLimitHint_AbsentAuth(t *testing.T) {
	if _, ok := GetAnthropicRateLimitHint("claude-test-absent@example.com"); ok {
		t.Fatal("expected ok=false for never-set auth")
	}
}

func TestGetAnthropicRateLimitHint_EmptyAuthID(t *testing.T) {
	if _, ok := GetAnthropicRateLimitHint(""); ok {
		t.Fatal("expected ok=false for empty authID")
	}
	if _, ok := GetAnthropicRateLimitHint("   "); ok {
		t.Fatal("expected ok=false for whitespace authID")
	}
}

func TestSetAnthropicRateLimitHint_EmptyAuthIDIsNoop(t *testing.T) {
	SetAnthropicRateLimitHint("", AnthropicRateLimitHint{Known: true, Status: "allowed"})
	SetAnthropicRateLimitHint("   ", AnthropicRateLimitHint{Known: true, Status: "allowed"})
	// No assertion needed beyond "doesn't panic"; subsequent Get with empty
	// ID returns ok=false (covered by TestGetAnthropicRateLimitHint_EmptyAuthID).
}

func TestSetAnthropicRateLimitHint_OverwritesPriorHint(t *testing.T) {
	authID := "claude-test-overwrite@example.com"
	t.Cleanup(func() { anthropicRateLimitHintByAuth.Delete(authID) })

	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{Known: true, Status: "allowed"})
	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{Known: true, Status: "rejected"})

	got, _ := GetAnthropicRateLimitHint(authID)
	if got.Status != "rejected" {
		t.Fatalf("expected overwrite to win: got=%q", got.Status)
	}
}

func TestHasKnownAnthropicRateLimitHint(t *testing.T) {
	authID := "claude-test-known-flag@example.com"
	t.Cleanup(func() { anthropicRateLimitHintByAuth.Delete(authID) })

	if HasKnownAnthropicRateLimitHint(authID) {
		t.Fatal("expected false before any Set")
	}
	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{Known: false})
	if HasKnownAnthropicRateLimitHint(authID) {
		t.Fatal("expected false when stored hint has Known=false")
	}
	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{Known: true, Status: "allowed"})
	if !HasKnownAnthropicRateLimitHint(authID) {
		t.Fatal("expected true after Set with Known=true")
	}
}

func TestAnthropicRateLimitHint_ConcurrentSafety(t *testing.T) {
	const goroutines = 64
	const iterations = 256
	authIDs := []string{
		"claude-test-concurrent-a@example.com",
		"claude-test-concurrent-b@example.com",
		"claude-test-concurrent-c@example.com",
	}
	t.Cleanup(func() {
		for _, id := range authIDs {
			anthropicRateLimitHintByAuth.Delete(id)
		}
	})

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				authID := authIDs[(idx+j)%len(authIDs)]
				if j%2 == 0 {
					SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
						Known:  true,
						Status: "allowed",
					})
				} else {
					_, _ = GetAnthropicRateLimitHint(authID)
				}
			}
		}(i)
	}
	wg.Wait()

	// Sanity: each auth has a hint with Known=true after the storm.
	for _, id := range authIDs {
		hint, ok := GetAnthropicRateLimitHint(id)
		if !ok || !hint.Known {
			t.Fatalf("expected stable hint for %s after concurrent storm", id)
		}
	}
}

func TestDeleteAnthropicRateLimitHint(t *testing.T) {
	const authID = "claude-test-delete@example.com"

	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{Known: true, Status: "allowed"})
	if _, ok := GetAnthropicRateLimitHint(authID); !ok {
		t.Fatal("expected hint to be present after Set")
	}
	DeleteAnthropicRateLimitHint(authID)
	if _, ok := GetAnthropicRateLimitHint(authID); ok {
		t.Fatal("expected hint to be absent after Delete")
	}
}

func TestSetAnthropicRateLimitHint_ClonesMapsBeforeStoring(t *testing.T) {
	// Symmetric to the Get-side defensive copy: Set must clone caller-owned
	// maps so post-Set mutation by the caller can't corrupt shared store
	// state or race with concurrent Get iteration.
	const authID = "claude-test-set-clones@example.com"
	t.Cleanup(func() { anthropicRateLimitHintByAuth.Delete(authID) })

	callerWindows := map[string]AnthropicQuotaWindow{
		"5h": {Status: "allowed", Utilization: 0.25},
	}
	callerRaw := map[string]string{
		"anthropic-ratelimit-unified-status": "allowed",
	}
	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known:      true,
		Status:     "allowed",
		Windows:    callerWindows,
		RawHeaders: callerRaw,
	})

	// Mutate the caller's local maps after Set.
	callerWindows["5h"] = AnthropicQuotaWindow{Status: "rejected", Utilization: 1.5}
	callerWindows["7d"] = AnthropicQuotaWindow{Status: "rejected", Utilization: 1.0}
	delete(callerWindows, "5h")
	callerRaw["anthropic-ratelimit-unified-status"] = "rejected"
	callerRaw["injected-key"] = "evil"
	delete(callerRaw, "anthropic-ratelimit-unified-status")

	// Stored copy must reflect the Set-time state, not the caller's mutations.
	got, ok := GetAnthropicRateLimitHint(authID)
	if !ok {
		t.Fatal("expected hint to be retrievable after Set")
	}
	if len(got.Windows) != 1 {
		t.Fatalf("Windows: post-Set caller mutations leaked into store; len=%d %+v", len(got.Windows), got.Windows)
	}
	w, ok := got.Windows["5h"]
	if !ok {
		t.Fatal("Windows[5h] missing — caller's delete leaked into store")
	}
	if w.Status != "allowed" || w.Utilization != 0.25 {
		t.Fatalf("Windows[5h] mutated by caller: %+v", w)
	}
	if len(got.RawHeaders) != 1 {
		t.Fatalf("RawHeaders: post-Set caller mutations leaked; len=%d %+v", len(got.RawHeaders), got.RawHeaders)
	}
	if got.RawHeaders["anthropic-ratelimit-unified-status"] != "allowed" {
		t.Fatalf("RawHeaders mutated by caller: got=%q", got.RawHeaders["anthropic-ratelimit-unified-status"])
	}
	if _, present := got.RawHeaders["injected-key"]; present {
		t.Fatal("RawHeaders gained caller-injected key after Set")
	}
}

func TestDeleteAnthropicRateLimitHint_AbsentAuth(t *testing.T) {
	// Delete on never-set auth should be a no-op (no panic, no error).
	DeleteAnthropicRateLimitHint("claude-test-delete-absent@example.com")
}

func TestGetAnthropicRateLimitHint_ReturnsDefensiveCopies(t *testing.T) {
	// Regression: GetAnthropicRateLimitHint used to return the stored value
	// by-value but with map fields (Windows, RawHeaders) whose underlying
	// references were shared with the global store. A caller mutating the
	// returned map would race against other readers and could trigger
	// `concurrent map read and map write` panics under load. The fix clones
	// both maps on every Get; this test pins that contract.
	const authID = "claude-test-defensive-copy@example.com"
	t.Cleanup(func() { anthropicRateLimitHintByAuth.Delete(authID) })

	original := AnthropicRateLimitHint{
		Known:  true,
		Status: "allowed",
		Windows: map[string]AnthropicQuotaWindow{
			"5h": {Status: "allowed", Utilization: 0.26},
		},
		RawHeaders: map[string]string{
			"anthropic-ratelimit-unified-status": "allowed",
		},
	}
	SetAnthropicRateLimitHint(authID, original)

	first, ok := GetAnthropicRateLimitHint(authID)
	if !ok {
		t.Fatal("expected hint to be present after Set")
	}

	// Mutate the returned maps in every way a caller might:
	//   - replace an existing key's value
	//   - add a new key
	//   - delete a key
	first.Windows["5h"] = AnthropicQuotaWindow{Status: "rejected", Utilization: 9.99}
	first.Windows["7d_FAKE"] = AnthropicQuotaWindow{Status: "fabricated"}
	first.RawHeaders["x-fake-header"] = "injected"
	delete(first.RawHeaders, "anthropic-ratelimit-unified-status")

	// A subsequent Get must return the original, unmutated state.
	second, ok := GetAnthropicRateLimitHint(authID)
	if !ok {
		t.Fatal("expected hint to still be present after caller mutation")
	}
	if w, ok := second.Windows["5h"]; !ok {
		t.Fatal("Windows[5h] disappeared after caller mutation")
	} else if w.Status != "allowed" || w.Utilization != 0.26 {
		t.Fatalf("Windows[5h] was mutated through the returned map: got=%+v", w)
	}
	if _, ok := second.Windows["7d_FAKE"]; ok {
		t.Fatal("caller's fabricated window leaked into the store")
	}
	if got := second.RawHeaders["anthropic-ratelimit-unified-status"]; got != "allowed" {
		t.Fatalf("RawHeaders entry mutated/deleted by caller: got=%q", got)
	}
	if _, ok := second.RawHeaders["x-fake-header"]; ok {
		t.Fatal("caller's injected raw header leaked into the store")
	}

	// The two Get results must reference distinct map instances — confirm via
	// add-then-not-leak rather than &-pointer equality (which is unstable for
	// map headers across loads).
	second.Windows["5h_test"] = AnthropicQuotaWindow{Status: "test"}
	third, _ := GetAnthropicRateLimitHint(authID)
	if _, ok := third.Windows["5h_test"]; ok {
		t.Fatal("second Get returned the same map instance as third Get — clone is shallow")
	}
}

func TestDeleteAnthropicRateLimitHint_EmptyAuthIDIsNoop(t *testing.T) {
	// Set a hint under a real ID, then call Delete with empty/whitespace
	// authIDs. The real hint must remain.
	const realID = "claude-test-delete-noop@example.com"
	t.Cleanup(func() { anthropicRateLimitHintByAuth.Delete(realID) })

	SetAnthropicRateLimitHint(realID, AnthropicRateLimitHint{Known: true, Status: "allowed"})
	DeleteAnthropicRateLimitHint("")
	DeleteAnthropicRateLimitHint("   ")
	if _, ok := GetAnthropicRateLimitHint(realID); !ok {
		t.Fatal("Delete with empty authID must not affect other entries")
	}
}
