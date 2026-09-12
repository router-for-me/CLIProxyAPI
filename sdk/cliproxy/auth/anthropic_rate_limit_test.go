package auth

import (
	"context"
	"fmt"
	"sort"
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

// windowSlugs lists the stored window keys in a stable order, so a failure
// message says which windows survived rather than printing a randomized map.
func windowSlugs(hint AnthropicRateLimitHint) []string {
	slugs := make([]string, 0, len(hint.Windows))
	for slug := range hint.Windows {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	return slugs
}

// TestSetAnthropicRateLimitHint_CarriesWindowOmittedByPartialCapture pins the
// merge. Anthropic reports only the windows belonging to the responding model's
// tier, so the first ordinary-tier response after a premium one carries no
// `7d_oi` header at all. Replacing the stored windows wholesale erased that
// window while its quota was still live — observed in production as a premium
// weekly reading that vanished minutes after appearing, hours before its reset.
func TestSetAnthropicRateLimitHint_CarriesWindowOmittedByPartialCapture(t *testing.T) {
	const authID = "auth-carry-premium-weekly"
	DeleteAnthropicRateLimitHint(authID)
	t.Cleanup(func() { DeleteAnthropicRateLimitHint(authID) })

	premiumAt := time.Date(2026, 5, 1, 16, 1, 0, 0, time.UTC)
	ordinaryAt := premiumAt.Add(4 * time.Minute)
	oiReset := premiumAt.Add(30 * time.Hour)
	// An identified account on both sides, so the carry rides the same
	// fingerprint comparison production does rather than the both-empty branch.
	const fingerprint = "fp-carry-premium-weekly"

	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known:              true,
		ObservedAt:         premiumAt,
		Status:             "allowed_warning",
		AccountFingerprint: fingerprint,
		Windows: map[string]AnthropicQuotaWindow{
			"5h":    {Status: "allowed", Reset: premiumAt.Add(2 * time.Hour), Utilization: 0.20, HasUtilization: true},
			"7d":    {Status: "allowed", Reset: premiumAt.Add(72 * time.Hour), Utilization: 0.40, HasUtilization: true},
			"7d_oi": {Status: "allowed_warning", Reset: oiReset, Utilization: 0.88, HasUtilization: true},
		},
	})

	// An ordinary-tier response: same account, no premium window in the family.
	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known:              true,
		ObservedAt:         ordinaryAt,
		Status:             "allowed",
		AccountFingerprint: fingerprint,
		Windows: map[string]AnthropicQuotaWindow{
			"5h": {Status: "allowed", Reset: ordinaryAt.Add(2 * time.Hour), Utilization: 0.30, HasUtilization: true},
			"7d": {Status: "allowed", Reset: ordinaryAt.Add(72 * time.Hour), Utilization: 0.45, HasUtilization: true},
		},
	})

	got, ok := GetAnthropicRateLimitHint(authID)
	if !ok {
		t.Fatal("GetAnthropicRateLimitHint() ok = false, want true")
	}
	if len(got.Windows) != 3 {
		t.Fatalf("windows = %v, want 5h, 7d and a carried 7d_oi", windowSlugs(got))
	}

	oi, ok := got.Windows["7d_oi"]
	if !ok {
		t.Fatalf("7d_oi erased by a capture that never reported it; windows = %v", windowSlugs(got))
	}
	if oi.Utilization != 0.88 || oi.Status != "allowed_warning" || !oi.Reset.Equal(oiReset) {
		t.Errorf("carried 7d_oi = %+v, want the premium capture's reading unchanged", oi)
	}
	if !oi.ObservedAt.Equal(premiumAt) {
		t.Errorf("carried 7d_oi ObservedAt = %v, want %v — a carried window must keep the stamp of the capture that saw it", oi.ObservedAt, premiumAt)
	}

	// Windows the newest capture did report come from it, stamp included.
	fiveHour := got.Windows["5h"]
	if fiveHour.Utilization != 0.30 {
		t.Errorf("5h utilization = %v, want 0.30 from the newest capture", fiveHour.Utilization)
	}
	if !fiveHour.ObservedAt.Equal(ordinaryAt) {
		t.Errorf("5h ObservedAt = %v, want %v", fiveHour.ObservedAt, ordinaryAt)
	}
	// Everything outside Windows still describes the newest capture alone.
	if got.Status != "allowed" || !got.ObservedAt.Equal(ordinaryAt) {
		t.Errorf("hint-level state = (%q, %v), want the newest capture's (%q, %v)", got.Status, got.ObservedAt, "allowed", ordinaryAt)
	}
}

// TestSetAnthropicRateLimitHint_DoesNotCarryAcrossAccounts pins the identity
// gate. Carrying is a claim that two captures describe the same quota, so it
// requires the same account on both sides. An unknown account on either side
// is not a match: unlike the read path — where serving an unidentifiable hint
// only risks showing the operator a stale reading — carrying would fabricate a
// window the current credential may never have had.
func TestSetAnthropicRateLimitHint_DoesNotCarryAcrossAccounts(t *testing.T) {
	first := time.Date(2026, 5, 1, 16, 1, 0, 0, time.UTC)
	second := first.Add(time.Minute)

	cases := []struct {
		name string
		prev string
		next string
	}{
		{name: "different accounts", prev: "fingerprint-before", next: "fingerprint-after"},
		{name: "prior identified, capture unknown", prev: "fingerprint-before", next: ""},
		{name: "prior unknown, capture identified", prev: "", next: "fingerprint-after"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authID := "auth-carry-identity-" + tc.name
			DeleteAnthropicRateLimitHint(authID)
			t.Cleanup(func() { DeleteAnthropicRateLimitHint(authID) })

			SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
				Known:              true,
				ObservedAt:         first,
				AccountFingerprint: tc.prev,
				Windows: map[string]AnthropicQuotaWindow{
					"7d_oi": {Status: "allowed", Reset: first.Add(30 * time.Hour)},
				},
			})
			SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
				Known:              true,
				ObservedAt:         second,
				AccountFingerprint: tc.next,
				Windows: map[string]AnthropicQuotaWindow{
					"5h": {Status: "allowed", Reset: second.Add(2 * time.Hour)},
				},
			})

			got, _ := GetAnthropicRateLimitHint(authID)
			if _, present := got.Windows["7d_oi"]; present {
				t.Errorf("carried a window across unproven identity (%q → %q); windows = %v", tc.prev, tc.next, windowSlugs(got))
			}
		})
	}
}

// TestSetAnthropicRateLimitHint_FreshWindowReplacesStored pins that the merge
// works at window granularity and never at field granularity. A slug the new
// capture reports is that capture's reading in full: a field the older capture
// had and the newer one lacks must not survive inside it, or a cleared warning
// would look permanent.
func TestSetAnthropicRateLimitHint_FreshWindowReplacesStored(t *testing.T) {
	const authID = "auth-carry-fresh-wins"
	DeleteAnthropicRateLimitHint(authID)
	t.Cleanup(func() { DeleteAnthropicRateLimitHint(authID) })

	first := time.Date(2026, 5, 1, 16, 1, 0, 0, time.UTC)
	second := first.Add(time.Minute)

	const fingerprint = "fp-fresh-wins"
	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known:              true,
		ObservedAt:         first,
		AccountFingerprint: fingerprint,
		Windows: map[string]AnthropicQuotaWindow{
			"5h": {
				Status:                "allowed_warning",
				Reset:                 first.Add(2 * time.Hour),
				Utilization:           0.91,
				HasUtilization:        true,
				SurpassedThreshold:    0.90,
				HasSurpassedThreshold: true,
			},
		},
	})
	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known:              true,
		ObservedAt:         second,
		AccountFingerprint: fingerprint,
		Windows: map[string]AnthropicQuotaWindow{
			"5h": {Status: "allowed", Reset: second.Add(5 * time.Hour), Utilization: 0.05, HasUtilization: true},
		},
	})

	got, _ := GetAnthropicRateLimitHint(authID)
	fiveHour := got.Windows["5h"]
	if fiveHour.Status != "allowed" || fiveHour.Utilization != 0.05 {
		t.Errorf("5h = %+v, want the newest capture's reading", fiveHour)
	}
	if fiveHour.HasSurpassedThreshold || fiveHour.SurpassedThreshold != 0 {
		t.Errorf("5h kept the stored capture's surpassed_threshold (%v) — windows merge whole, never field by field", fiveHour.SurpassedThreshold)
	}
	if !fiveHour.ObservedAt.Equal(second) {
		t.Errorf("5h ObservedAt = %v, want %v", fiveHour.ObservedAt, second)
	}
}

// TestSetAnthropicRateLimitHint_CarriesIntoCaptureWithoutWindows covers the
// nil-map path: a capture can carry the unified-* family with no per-window
// headers at all (RecordAnthropicRateLimit leaves Windows nil then), and the
// carry has to allocate rather than write to a nil map.
func TestSetAnthropicRateLimitHint_CarriesIntoCaptureWithoutWindows(t *testing.T) {
	const authID = "auth-carry-into-nil-windows"
	DeleteAnthropicRateLimitHint(authID)
	t.Cleanup(func() { DeleteAnthropicRateLimitHint(authID) })

	first := time.Date(2026, 5, 1, 16, 1, 0, 0, time.UTC)
	second := first.Add(time.Minute)
	oiReset := first.Add(30 * time.Hour)
	const fingerprint = "fp-carry-into-nil-windows"

	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known:              true,
		ObservedAt:         first,
		AccountFingerprint: fingerprint,
		Windows: map[string]AnthropicQuotaWindow{
			"7d_oi": {Status: "allowed", Reset: oiReset, Utilization: 0.88, HasUtilization: true},
		},
	})
	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known:              true,
		ObservedAt:         second,
		Status:             "allowed",
		AccountFingerprint: fingerprint,
	})

	got, _ := GetAnthropicRateLimitHint(authID)
	oi, ok := got.Windows["7d_oi"]
	if !ok {
		t.Fatalf("7d_oi lost to a capture with no windows of its own; windows = %v", windowSlugs(got))
	}
	if !oi.ObservedAt.Equal(first) || oi.Utilization != 0.88 {
		t.Errorf("carried 7d_oi = %+v, want the first capture's reading and stamp", oi)
	}
}

// TestSetAnthropicRateLimitHint_StampsWindowObservedAt pins that the store owns
// the per-window timestamp. The field only means "which capture saw this" if no
// caller can set it, and the carry policy reads it back as exactly that.
func TestSetAnthropicRateLimitHint_StampsWindowObservedAt(t *testing.T) {
	const authID = "auth-window-observed-at-stamp"
	DeleteAnthropicRateLimitHint(authID)
	t.Cleanup(func() { DeleteAnthropicRateLimitHint(authID) })

	capturedAt := time.Date(2026, 5, 1, 16, 1, 0, 0, time.UTC)
	callerSupplied := capturedAt.Add(-72 * time.Hour)

	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known:      true,
		ObservedAt: capturedAt,
		Windows: map[string]AnthropicQuotaWindow{
			"5h": {Status: "allowed", Reset: capturedAt.Add(2 * time.Hour), ObservedAt: callerSupplied},
		},
	})

	got, _ := GetAnthropicRateLimitHint(authID)
	if !got.Windows["5h"].ObservedAt.Equal(capturedAt) {
		t.Errorf("5h ObservedAt = %v, want %v — the store stamps this field, the caller does not", got.Windows["5h"].ObservedAt, capturedAt)
	}
}

// TestSetAnthropicRateLimitHint_ConcurrentMergeKeepsCarriedWindow runs the
// merge itself under concurrency, which the ordering tests do not: they store
// window-less hints, so the carry loop never executes there. Premium- and
// ordinary-tier captures race for one auth, and 7d_oi must survive whichever
// one lands last — every capture after the seed either reports it or merges
// from a stored hint that already carries it.
//
// Worth running under -race: the merge reads the store-owned window map of the
// previous hint while other goroutines clone it on Get.
func TestSetAnthropicRateLimitHint_ConcurrentMergeKeepsCarriedWindow(t *testing.T) {
	const authID = "auth-concurrent-merge"
	const writers = 64
	const iterations = 32
	const concurrentFingerprint = "fp-concurrent-merge"

	base := time.Unix(1777500000, 0).UTC()
	DeleteAnthropicRateLimitHint(authID)
	t.Cleanup(func() { DeleteAnthropicRateLimitHint(authID) })

	premiumWindows := func() map[string]AnthropicQuotaWindow {
		return map[string]AnthropicQuotaWindow{
			"5h":    {Status: "allowed", Reset: base.Add(2 * time.Hour), Utilization: 0.2, HasUtilization: true},
			"7d":    {Status: "allowed", Reset: base.Add(72 * time.Hour), Utilization: 0.4, HasUtilization: true},
			"7d_oi": {Status: "allowed", Reset: base.Add(96 * time.Hour), Utilization: 0.6, HasUtilization: true},
		}
	}

	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known: true, ObservedAt: base, AccountFingerprint: concurrentFingerprint, Windows: premiumWindows(),
	})

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				windows := premiumWindows()
				if i%2 == 1 {
					// Ordinary tier: the premium window is not in the family.
					delete(windows, "7d_oi")
				}
				SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
					Known:              true,
					ObservedAt:         base.Add(time.Duration(i*iterations+j+1) * time.Millisecond),
					AccountFingerprint: concurrentFingerprint,
					Windows:            windows,
				})
				_, _ = GetAnthropicRateLimitHint(authID)
			}
		}(i)
	}
	wg.Wait()

	got, ok := GetAnthropicRateLimitHint(authID)
	if !ok {
		t.Fatal("GetAnthropicRateLimitHint() ok = false, want true")
	}
	if _, present := got.Windows["7d_oi"]; !present {
		t.Errorf("7d_oi lost under concurrent partial-family captures; windows = %v", windowSlugs(got))
	}
}

// TestSetAnthropicRateLimitHint_UnknownCaptureCarriesNothing pins the Known
// gate. A capture with no unified-* content says nothing about the account's
// quota — it is either a scrub or a record that the auth exists — so it must
// not inherit windows and quietly become a hint that reports quota nobody
// observed.
func TestSetAnthropicRateLimitHint_UnknownCaptureCarriesNothing(t *testing.T) {
	const authID = "auth-carry-unknown-capture"
	DeleteAnthropicRateLimitHint(authID)
	t.Cleanup(func() { DeleteAnthropicRateLimitHint(authID) })

	first := time.Date(2026, 5, 1, 16, 1, 0, 0, time.UTC)
	second := first.Add(time.Minute)

	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known:      true,
		ObservedAt: first,
		Windows: map[string]AnthropicQuotaWindow{
			"7d_oi": {Status: "allowed", Reset: first.Add(30 * time.Hour), Utilization: 0.88, HasUtilization: true},
		},
	})
	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known:      false,
		ObservedAt: second,
	})

	got, ok := GetAnthropicRateLimitHint(authID)
	if !ok {
		t.Fatal("GetAnthropicRateLimitHint() ok = false, want true")
	}
	if got.Known {
		t.Error("Known = true, want false — the scrub itself must still land")
	}
	if len(got.Windows) != 0 {
		t.Errorf("a capture with no unified-* content inherited windows %v", windowSlugs(got))
	}
}

// TestSetAnthropicRateLimitHint_CapsCarriedWindows bounds the union. Carrying
// windows forward means the stored map is no longer sized by one response, and
// both the slug and the reset come from upstream — so a base that rotates slug
// names would grow one auth's map for the life of the process. The cap falls on
// the carried side, which is the unbounded one: what this capture reported is
// never given up, and a capture that fills its own budget still gets its carry.
func TestSetAnthropicRateLimitHint_CapsCarriedWindows(t *testing.T) {
	const authID = "auth-carry-cap"
	const stored = 100
	const fingerprint = "fp-carry-cap"
	DeleteAnthropicRateLimitHint(authID)
	t.Cleanup(func() { DeleteAnthropicRateLimitHint(authID) })

	first := time.Date(2026, 5, 1, 16, 1, 0, 0, time.UTC)
	second := first.Add(time.Minute)

	// Every window is live at `second` and inside the carry horizon, each
	// resetting an hour later than the one before, so eviction order is
	// unambiguous.
	windows := make(map[string]AnthropicQuotaWindow, stored)
	for i := 0; i < stored; i++ {
		windows[fmt.Sprintf("w%03d", i)] = AnthropicQuotaWindow{
			Status: "allowed",
			Reset:  second.Add(time.Duration(i+1) * time.Hour),
		}
	}
	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known: true, ObservedAt: first, AccountFingerprint: fingerprint, Windows: windows,
	})

	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known:              true,
		ObservedAt:         second,
		AccountFingerprint: fingerprint,
		Windows: map[string]AnthropicQuotaWindow{
			"5h": {Status: "allowed", Reset: second.Add(2 * time.Hour)},
			"7d": {Status: "allowed", Reset: second.Add(72 * time.Hour)},
		},
	})

	got, _ := GetAnthropicRateLimitHint(authID)
	// The capture's own two windows, plus a full carry budget on top.
	if want := 2 + maxCarriedAnthropicWindows; len(got.Windows) != want {
		t.Fatalf("stored %d windows, want %d (2 reported + %d carried)", len(got.Windows), want, maxCarriedAnthropicWindows)
	}
	for _, slug := range []string{"5h", "7d"} {
		if _, present := got.Windows[slug]; !present {
			t.Errorf("%s: a window this capture reported was evicted to make room for a carried one", slug)
		}
	}
	// Keep the furthest resets: w099 (+100h) down to w036 (+37h). Those are the
	// long-horizon windows an ordinary capture stops re-reporting; the soonest
	// candidates are re-reported anyway and age out on their own.
	firstKept := stored - maxCarriedAnthropicWindows
	for i := 0; i < stored; i++ {
		slug := fmt.Sprintf("w%03d", i)
		_, present := got.Windows[slug]
		if want := i >= firstKept; present != want {
			t.Errorf("%s (reset +%dh) present = %v, want %v — the soonest resets go first", slug, i+1, present, want)
		}
	}
}

// TestSetAnthropicRateLimitHint_CarryEvictionTieBreaksOnSlug contests the last
// carry seat between two windows resetting at the same instant. Without a
// tie-break the winner is whichever the map happened to yield first, so the
// store would evict a different window on identical input.
//
// Repeated: Go randomizes map iteration per range, so one round would let an
// order-dependent implementation through about half the time.
func TestSetAnthropicRateLimitHint_CarryEvictionTieBreaksOnSlug(t *testing.T) {
	const authID = "auth-carry-cap-tiebreak"
	const rounds = 20
	const fingerprint = "fp-carry-tiebreak"
	t.Cleanup(func() { DeleteAnthropicRateLimitHint(authID) })

	first := time.Date(2026, 5, 1, 16, 1, 0, 0, time.UTC)
	second := first.Add(time.Minute)

	// One seat short of the budget goes to windows resetting far out, which
	// eviction keeps; the two tied windows reset sooner and contest what is
	// left.
	sharedReset := second.Add(10 * time.Hour)
	prior := map[string]AnthropicQuotaWindow{
		"aaa_window": {Status: "allowed", Reset: sharedReset},
		"bbb_window": {Status: "allowed", Reset: sharedReset},
	}
	for i := 0; i < maxCarriedAnthropicWindows-1; i++ {
		prior[fmt.Sprintf("far%03d", i)] = AnthropicQuotaWindow{
			Status: "allowed",
			Reset:  second.Add(time.Duration(100+i) * time.Hour),
		}
	}

	for round := 0; round < rounds; round++ {
		DeleteAnthropicRateLimitHint(authID)

		SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
			Known: true, ObservedAt: first, AccountFingerprint: fingerprint, Windows: prior,
		})
		SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
			Known:              true,
			ObservedAt:         second,
			AccountFingerprint: fingerprint,
			Windows: map[string]AnthropicQuotaWindow{
				"5h": {Status: "allowed", Reset: second.Add(2 * time.Hour)},
			},
		})

		got, _ := GetAnthropicRateLimitHint(authID)
		if want := 1 + maxCarriedAnthropicWindows; len(got.Windows) != want {
			t.Fatalf("round %d: stored %d windows, want %d", round, len(got.Windows), want)
		}
		if _, present := got.Windows["aaa_window"]; !present {
			t.Fatalf("round %d: equal resets must break toward the lower slug, kept %v", round, windowSlugs(got))
		}
		if _, present := got.Windows["bbb_window"]; present {
			t.Fatalf("round %d: both tied windows were kept, so the cap did not bind", round)
		}
	}
}

// TestSetAnthropicRateLimitHint_ContentFreeWindowDoesNotDisplaceStored covers
// the way a capture can name a window without observing it. The parser seeds a
// slug as soon as any header mentions it, so a single malformed header — say
// `unified-7d_oi-reset: garbage` — produces a zero-value entry. Treating that
// as a reported window would be the worst of both outcomes: it destroys the
// rich stored reading, and having no reset of its own it can never be carried
// afterwards, so the window is gone until the next premium response.
func TestSetAnthropicRateLimitHint_ContentFreeWindowDoesNotDisplaceStored(t *testing.T) {
	const authID = "auth-carry-content-free-fresh"
	const fingerprint = "fp-content-free"
	DeleteAnthropicRateLimitHint(authID)
	t.Cleanup(func() { DeleteAnthropicRateLimitHint(authID) })

	first := time.Date(2026, 5, 1, 16, 1, 0, 0, time.UTC)
	second := first.Add(time.Minute)
	oiReset := first.Add(30 * time.Hour)

	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known:              true,
		ObservedAt:         first,
		AccountFingerprint: fingerprint,
		Windows: map[string]AnthropicQuotaWindow{
			"7d_oi": {Status: "allowed_warning", Reset: oiReset, Utilization: 0.88, HasUtilization: true},
		},
	})
	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known:              true,
		ObservedAt:         second,
		AccountFingerprint: fingerprint,
		Windows: map[string]AnthropicQuotaWindow{
			"5h":    {Status: "allowed", Reset: second.Add(2 * time.Hour)},
			"7d_oi": {},
		},
	})

	got, _ := GetAnthropicRateLimitHint(authID)
	oi, ok := got.Windows["7d_oi"]
	if !ok {
		t.Fatalf("7d_oi missing entirely; windows = %v", windowSlugs(got))
	}
	if oi.Utilization != 0.88 || oi.Status != "allowed_warning" || !oi.Reset.Equal(oiReset) {
		t.Errorf("7d_oi = %+v, want the stored reading — a slug named without content is not an observation", oi)
	}
	if !oi.ObservedAt.Equal(first) {
		t.Errorf("7d_oi ObservedAt = %v, want %v — the surviving reading is the carried one", oi.ObservedAt, first)
	}
}

// TestSetAnthropicRateLimitHint_PartiallyReportedWindowReplacesStored is the
// other side of that line. A capture that reported even one field for the slug
// observed something, so it replaces the stored window whole rather than being
// treated as a gap to fill.
func TestSetAnthropicRateLimitHint_PartiallyReportedWindowReplacesStored(t *testing.T) {
	const authID = "auth-carry-partial-fresh"
	const fingerprint = "fp-partial-fresh"
	DeleteAnthropicRateLimitHint(authID)
	t.Cleanup(func() { DeleteAnthropicRateLimitHint(authID) })

	first := time.Date(2026, 5, 1, 16, 1, 0, 0, time.UTC)
	second := first.Add(time.Minute)

	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known:              true,
		ObservedAt:         first,
		AccountFingerprint: fingerprint,
		Windows: map[string]AnthropicQuotaWindow{
			"7d_oi": {Status: "allowed_warning", Reset: first.Add(30 * time.Hour), Utilization: 0.88, HasUtilization: true},
		},
	})
	// Status arrived, reset did not.
	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known:              true,
		ObservedAt:         second,
		AccountFingerprint: fingerprint,
		Windows: map[string]AnthropicQuotaWindow{
			"7d_oi": {Status: "rejected"},
		},
	})

	got, _ := GetAnthropicRateLimitHint(authID)
	oi := got.Windows["7d_oi"]
	if oi.Status != "rejected" {
		t.Errorf("7d_oi status = %q, want %q — a reported window replaces the stored one", oi.Status, "rejected")
	}
	if oi.HasUtilization || !oi.Reset.IsZero() {
		t.Errorf("7d_oi = %+v, want no stored fields surviving inside a replaced window", oi)
	}
	if !oi.ObservedAt.Equal(second) {
		t.Errorf("7d_oi ObservedAt = %v, want %v", oi.ObservedAt, second)
	}
}

// TestSetAnthropicRateLimitHint_CarriesWhenBothAccountsUnknown keeps the
// both-empty branch under test on purpose. Equality is the gate, so two
// captures that both failed to identify an account match and carry. That is
// deliberate — it mirrors the read path, which serves an unidentifiable hint
// rather than discarding it — and it is the one case where the gate does not
// actually prove the two captures came from the same credential.
func TestSetAnthropicRateLimitHint_CarriesWhenBothAccountsUnknown(t *testing.T) {
	const authID = "auth-carry-both-unknown"
	DeleteAnthropicRateLimitHint(authID)
	t.Cleanup(func() { DeleteAnthropicRateLimitHint(authID) })

	first := time.Date(2026, 5, 1, 16, 1, 0, 0, time.UTC)
	second := first.Add(time.Minute)

	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known:      true,
		ObservedAt: first,
		Windows: map[string]AnthropicQuotaWindow{
			"7d_oi": {Status: "allowed", Reset: first.Add(30 * time.Hour)},
		},
	})
	SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
		Known:      true,
		ObservedAt: second,
		Windows: map[string]AnthropicQuotaWindow{
			"5h": {Status: "allowed", Reset: second.Add(2 * time.Hour)},
		},
	})

	got, _ := GetAnthropicRateLimitHint(authID)
	if _, present := got.Windows["7d_oi"]; !present {
		t.Errorf("two unidentified captures must still match and carry; windows = %v", windowSlugs(got))
	}
}

// TestSetAnthropicRateLimitHint_CarryResetGates walks the reset axis of the
// carry policy in one place: whether a stored window survives depends on where
// its reset sits relative to this capture, and on how old the reading is.
func TestSetAnthropicRateLimitHint_CarryResetGates(t *testing.T) {
	const fingerprint = "fp-reset-gates"
	storedAt := time.Date(2026, 5, 1, 16, 1, 0, 0, time.UTC)
	soonAfter := storedAt.Add(time.Minute)

	cases := []struct {
		name        string
		storedReset time.Time
		captureAt   time.Time
		wantCarried bool
	}{
		{"no reset known", time.Time{}, soonAfter, false},
		{"already reset", soonAfter.Add(-30 * time.Second), soonAfter, false},
		{"resets exactly at the capture instant", soonAfter, soonAfter, false},
		{"live", soonAfter.Add(30 * time.Hour), soonAfter, true},
		{"resets exactly at the horizon", soonAfter.Add(maxAnthropicCarryHorizon), soonAfter, true},
		{"resets beyond the horizon", soonAfter.Add(maxAnthropicCarryHorizon + time.Hour), soonAfter, false},
		// The horizon is measured from THIS capture, not the stored one. A
		// reset nine days past the original observation is seven days out from
		// a capture two days later, which is a window worth keeping.
		{"outside the stored capture's horizon, inside this one's", storedAt.Add(9 * 24 * time.Hour), storedAt.Add(2 * 24 * time.Hour), true},
		// And the age bound is what stops that from becoming a loophole: after
		// a long gap the same distant reset falls back inside the horizon while
		// the reading itself is weeks old.
		{"reading older than the horizon", storedAt.Add(30 * 24 * time.Hour), storedAt.Add(25 * 24 * time.Hour), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authID := "auth-carry-reset-gate-" + tc.name
			DeleteAnthropicRateLimitHint(authID)
			t.Cleanup(func() { DeleteAnthropicRateLimitHint(authID) })

			SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
				Known: true, ObservedAt: storedAt, AccountFingerprint: fingerprint,
				Windows: map[string]AnthropicQuotaWindow{
					"7d_oi": {Status: "allowed", Reset: tc.storedReset},
				},
			})
			SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
				Known: true, ObservedAt: tc.captureAt, AccountFingerprint: fingerprint,
				Windows: map[string]AnthropicQuotaWindow{
					"5h": {Status: "allowed", Reset: tc.captureAt.Add(2 * time.Hour)},
				},
			})

			got, _ := GetAnthropicRateLimitHint(authID)
			_, carried := got.Windows["7d_oi"]
			if carried != tc.wantCarried {
				t.Errorf("7d_oi carried = %v, want %v (stored reset %v, observed %v, capture at %v); windows = %v",
					carried, tc.wantCarried, tc.storedReset, storedAt, tc.captureAt, windowSlugs(got))
			}
		})
	}
}

// TestSetAnthropicRateLimitHint_SingleReportedFieldCountsAsObservation pins each
// clause of the content test independently. Any one of the four fields makes a
// capture's window a real observation, so it replaces the stored reading rather
// than being treated as a gap for the carry to fill — and dropping any single
// clause would silently turn that field's readings into carry fodder.
func TestSetAnthropicRateLimitHint_SingleReportedFieldCountsAsObservation(t *testing.T) {
	const fingerprint = "fp-single-field"
	storedAt := time.Date(2026, 5, 1, 16, 1, 0, 0, time.UTC)
	captureAt := storedAt.Add(time.Minute)

	cases := []struct {
		name  string
		fresh AnthropicQuotaWindow
	}{
		{"status only", AnthropicQuotaWindow{Status: "rejected"}},
		{"utilization only", AnthropicQuotaWindow{HasUtilization: true}},
		{"surpassed threshold only", AnthropicQuotaWindow{HasSurpassedThreshold: true}},
		{"reset only", AnthropicQuotaWindow{Reset: captureAt.Add(3 * time.Hour)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authID := "auth-single-field-" + tc.name
			DeleteAnthropicRateLimitHint(authID)
			t.Cleanup(func() { DeleteAnthropicRateLimitHint(authID) })

			SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
				Known: true, ObservedAt: storedAt, AccountFingerprint: fingerprint,
				Windows: map[string]AnthropicQuotaWindow{
					"7d_oi": {Status: "allowed_warning", Reset: storedAt.Add(30 * time.Hour), Utilization: 0.88, HasUtilization: true},
				},
			})
			SetAnthropicRateLimitHint(authID, AnthropicRateLimitHint{
				Known: true, ObservedAt: captureAt, AccountFingerprint: fingerprint,
				Windows: map[string]AnthropicQuotaWindow{"7d_oi": tc.fresh},
			})

			got, _ := GetAnthropicRateLimitHint(authID)
			oi := got.Windows["7d_oi"]
			if !oi.ObservedAt.Equal(captureAt) {
				t.Errorf("7d_oi ObservedAt = %v, want %v — the capture reported this window, so its reading must stand",
					oi.ObservedAt, captureAt)
			}
			if oi.Utilization == 0.88 && !tc.fresh.HasUtilization {
				t.Errorf("7d_oi kept the stored utilization: the capture's window was treated as unreported")
			}
		})
	}
}
