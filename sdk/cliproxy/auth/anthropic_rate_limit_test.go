package auth

import (
	"context"
	"strings"
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

// TestManagerUpdatePreservesHintAcrossTokenRefresh guards against scrubbing the
// hint in Manager.Update. Update is not only the rotation path: conductor
// refresh calls it after every routine OAuth token refresh, with the same
// account and a new access token. Scrubbing there drops valid quota state on
// ordinary refreshes, which — given how often Claude OAuth tokens refresh —
// leaves /v0/management/auth-files reporting no rate_limit for much of a
// credential's life.
//
// Staleness across a genuine rotation is handled on read instead, by the
// account fingerprint (see TestAnthropicRateLimitHintFor_*).
func TestManagerUpdatePreservesHintAcrossTokenRefresh(t *testing.T) {
	const authID = "claude-manager-update@example.com"
	t.Cleanup(func() { anthropicRateLimitHintByAuth.Delete(authID) })

	manager := NewManager(nil, nil, nil)
	account := map[string]any{"email": "steady@example.com"}
	if _, err := manager.Register(context.Background(), &Auth{ID: authID, Provider: "claude", Metadata: account}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{Known: true, Status: "rejected"})
	if _, ok := GetAnthropicRateLimitHint(authID); !ok {
		t.Fatal("expected hint to be present before Update")
	}

	// What conductor refresh does: same account, new access token.
	refreshed := &Auth{ID: authID, Provider: "claude", Metadata: map[string]any{
		"email":        "steady@example.com",
		"access_token": "rotated-token",
	}}
	if _, err := manager.Update(context.Background(), refreshed); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	if _, ok := GetAnthropicRateLimitHint(authID); !ok {
		t.Fatal("hint must survive a routine token refresh for the same account")
	}
}

// TestAnthropicRateLimitHintFor_RejectsDifferentAccount is the read-side
// replacement for the removed Update scrub: a capture tagged with one account
// must not be served for an auth that now holds a different one.
func TestAnthropicRateLimitHintFor_RejectsDifferentAccount(t *testing.T) {
	const authID = "claude-fingerprint-rotate@example.com"
	t.Cleanup(func() { anthropicRateLimitHintByAuth.Delete(authID) })

	before := &Auth{ID: authID, Provider: "claude", Metadata: map[string]any{"email": "before@example.com"}}
	after := &Auth{ID: authID, Provider: "claude", Metadata: map[string]any{"email": "after@example.com"}}

	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known:              true,
		Status:             "rejected",
		AccountFingerprint: AnthropicAccountFingerprint(before),
	})

	if _, ok := AnthropicRateLimitHintFor(after); ok {
		t.Fatal("expected a capture from a different account to be rejected")
	}
	if _, ok := AnthropicRateLimitHintFor(before); !ok {
		t.Fatal("expected the capturing account to still read its own hint")
	}
}

// TestAnthropicRateLimitHintFor_UnknownAccountServes pins the deliberate
// asymmetry: rejection requires proof of a mismatch. An empty fingerprint on
// either side means the account is unidentifiable, which must resolve to
// serving the hint — rejecting on unknown would re-introduce the same data
// loss the Update scrub caused, via a narrower door (a token refresh that
// returns no email blanks the account).
func TestAnthropicRateLimitHintFor_UnknownAccountServes(t *testing.T) {
	const authID = "claude-fingerprint-unknown@example.com"
	t.Cleanup(func() { anthropicRateLimitHintByAuth.Delete(authID) })

	identified := &Auth{ID: authID, Provider: "claude", Metadata: map[string]any{"email": "someone@example.com"}}
	anonymous := &Auth{ID: authID, Provider: "claude"}

	if got := AnthropicAccountFingerprint(anonymous); got != "" {
		t.Fatalf("expected empty fingerprint for an auth with no account, got %q", got)
	}

	// Stored without a fingerprint, read by an identified auth.
	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{Known: true, Status: "allowed"})
	if _, ok := AnthropicRateLimitHintFor(identified); !ok {
		t.Fatal("hint stored without a fingerprint must still be served")
	}

	// Stored with a fingerprint, read by an auth whose account went blank.
	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known:              true,
		Status:             "allowed",
		AccountFingerprint: AnthropicAccountFingerprint(identified),
	})
	if _, ok := AnthropicRateLimitHintFor(anonymous); !ok {
		t.Fatal("hint must survive an auth whose account became unidentifiable")
	}
}

// TestAnthropicAccountFingerprint_DoesNotLeakAPIKey pins that the fingerprint
// is a hash: for API-key auths AccountInfo returns the key itself, which must
// not be stored in a process-wide map or reach a management response.
func TestAnthropicAccountFingerprint_DoesNotLeakAPIKey(t *testing.T) {
	const secret = "sk-ant-super-secret-key"
	auth := &Auth{
		ID:         "claude-apikey@example.com",
		Provider:   "claude",
		Attributes: map[string]string{AttributeAPIKey: secret},
	}

	got := AnthropicAccountFingerprint(auth)
	if got == "" {
		t.Fatal("expected a fingerprint for an api-key auth")
	}
	if strings.Contains(got, secret) {
		t.Fatalf("fingerprint leaks the API key: %q", got)
	}
}

// TestManagerRemoveScrubsAnthropicRateLimitHint is the removal-side companion:
// the management handlers call Manager.Remove directly (removeAuth), so an
// auth recreated with the same ID must not inherit the removed credential's
// quota state.
func TestManagerRemoveScrubsAnthropicRateLimitHint(t *testing.T) {
	const authID = "claude-manager-remove@example.com"
	t.Cleanup(func() { anthropicRateLimitHintByAuth.Delete(authID) })

	manager := NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &Auth{ID: authID, Provider: "claude"}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{Known: true, Status: "allowed"})
	if _, ok := GetAnthropicRateLimitHint(authID); !ok {
		t.Fatal("expected hint to be present before Remove")
	}

	manager.Remove(context.Background(), authID)

	if _, ok := GetAnthropicRateLimitHint(authID); ok {
		t.Fatal("expected hint to be scrubbed by Manager.Remove")
	}
}

// TestSetAnthropicRateLimitHint_OlderCaptureDoesNotOverwriteNewer pins the
// ordering guard. ObservedAt is stamped by the caller before it parses the
// response, so concurrent requests on one credential can stamp t1 < t2 and
// still reach the store in the reverse order; the newer snapshot must win.
func TestSetAnthropicRateLimitHint_OlderCaptureDoesNotOverwriteNewer(t *testing.T) {
	const authID = "auth-ordering-guard"
	DeleteAnthropicRateLimitHint(authID)
	t.Cleanup(func() { DeleteAnthropicRateLimitHint(authID) })

	newer := time.Unix(1777500000, 0).UTC()
	older := newer.Add(-30 * time.Second)

	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known: true, Status: "rejected", ObservedAt: newer,
	})
	// The older capture arrives second — it must not win.
	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known: true, Status: "allowed", ObservedAt: older,
	})

	got, ok := GetAnthropicRateLimitHint(authID)
	if !ok {
		t.Fatal("GetAnthropicRateLimitHint() ok = false, want true")
	}
	if got.Status != "rejected" {
		t.Errorf("Status = %q, want %q — an older capture overwrote a newer one", got.Status, "rejected")
	}
	if !got.ObservedAt.Equal(newer) {
		t.Errorf("ObservedAt = %v, want %v — ObservedAt went backwards", got.ObservedAt, newer)
	}
}

// TestSetAnthropicRateLimitHint_EqualOrNewerCaptureWins guards the boundary:
// only a strictly older capture is dropped, so a same-instant or newer
// observation still updates the store.
func TestSetAnthropicRateLimitHint_EqualOrNewerCaptureWins(t *testing.T) {
	const authID = "auth-ordering-boundary"
	DeleteAnthropicRateLimitHint(authID)
	t.Cleanup(func() { DeleteAnthropicRateLimitHint(authID) })

	base := time.Unix(1777500000, 0).UTC()
	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{Known: true, Status: "first", ObservedAt: base})

	// Same instant must still apply — the guard drops only strictly-older.
	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{Known: true, Status: "same-instant", ObservedAt: base})
	if got, _ := GetAnthropicRateLimitHint(authID); got.Status != "same-instant" {
		t.Errorf("Status = %q, want %q — same-instant capture was wrongly dropped", got.Status, "same-instant")
	}

	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{Known: true, Status: "newer", ObservedAt: base.Add(time.Second)})
	if got, _ := GetAnthropicRateLimitHint(authID); got.Status != "newer" {
		t.Errorf("Status = %q, want %q — newer capture did not win", got.Status, "newer")
	}
}

// TestManagerRemoveScrubsHintForAlreadyAbsentAuth pins the ordering inside
// Manager.Remove. A response still in flight when an auth is removed lands
// after the scrub and re-creates the entry; the operator then deletes again.
// That second Remove finds no auth in the map and returns early, so a scrub
// placed after that guard would never reach the resurrected hint and it would
// outlive the process.
func TestManagerRemoveScrubsHintForAlreadyAbsentAuth(t *testing.T) {
	const authID = "auth-resurrected-after-removal"
	DeleteAnthropicRateLimitHint(authID)
	t.Cleanup(func() { DeleteAnthropicRateLimitHint(authID) })

	manager := NewManager(nil, nil, nil)
	ctx := context.Background()

	// The auth is not (or no longer) known to the manager, but a late capture
	// has left a hint behind.
	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known:      true,
		Status:     "allowed",
		ObservedAt: time.Unix(1777500000, 0).UTC(),
	})
	if _, ok := GetAnthropicRateLimitHint(authID); !ok {
		t.Fatal("precondition: expected a stored hint")
	}

	manager.Remove(ctx, authID)

	if _, ok := GetAnthropicRateLimitHint(authID); ok {
		t.Error("hint survived Remove for an auth the manager no longer holds; a resurrected entry would never be reclaimed")
	}
}

// TestManagerRemoveScrubsHintForAnyProvider keeps the provider-agnostic
// guarantee under regression protection. The scrub is keyed by auth ID and is a
// no-op for IDs without a hint, so it must not become conditional on the auth
// being a Claude one.
func TestManagerRemoveScrubsHintForAnyProvider(t *testing.T) {
	for _, provider := range []string{"claude", "gemini", "codex"} {
		t.Run(provider, func(t *testing.T) {
			authID := "manager-remove-provider-" + provider
			DeleteAnthropicRateLimitHint(authID)
			t.Cleanup(func() { DeleteAnthropicRateLimitHint(authID) })

			manager := NewManager(nil, nil, nil)
			if _, err := manager.Register(context.Background(), &Auth{ID: authID, Provider: provider}); err != nil {
				t.Fatalf("Register() error = %v", err)
			}
			SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{Known: true, Status: "allowed"})

			manager.Remove(context.Background(), authID)

			if _, ok := GetAnthropicRateLimitHint(authID); ok {
				t.Errorf("hint survived Remove for provider %q; the scrub must not be provider-conditional", provider)
			}
		})
	}
}

// TestSetAnthropicRateLimitHint_ConcurrentOrderingHolds drives concurrent
// captures for one auth and asserts the newest observation is the one left
// standing. A bare Load-then-Store passes a single-threaded ordering check but
// loses here: two writers interleave between the compare and the store, and the
// older snapshot lands last.
//
// Repeated deliberately. One round reproduces the race only a fraction of the
// time, so a single round would be a weak gate — a regression would pass most
// CI runs. Rounds are independent, so the miss probability falls off
// exponentially.
func TestSetAnthropicRateLimitHint_ConcurrentOrderingHolds(t *testing.T) {
	const authID = "auth-concurrent-ordering"
	const rounds = 200
	const writers = 64

	base := time.Unix(1777500000, 0).UTC()
	t.Cleanup(func() { DeleteAnthropicRateLimitHint(authID) })

	for round := 0; round < rounds; round++ {
		DeleteAnthropicRateLimitHint(authID)

		var wg sync.WaitGroup
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				// Newest observation is i == 0; every other writer is older.
				SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
					Known:      true,
					Status:     "allowed",
					ObservedAt: base.Add(-time.Duration(i) * time.Second),
				})
			}(i)
		}
		wg.Wait()

		got, ok := GetAnthropicRateLimitHint(authID)
		if !ok {
			t.Fatalf("round %d: no hint stored", round)
		}
		if !got.ObservedAt.Equal(base) {
			t.Fatalf("round %d: ObservedAt = %v, want %v — an older concurrent capture won",
				round, got.ObservedAt, base)
		}
	}
}
