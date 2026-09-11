package auth

import (
	"testing"
	"time"
)

// TestSessionCacheSnapshot_SplitsCompositeKey verifies that Snapshot returns
// structured provider/sessionID/model fields rather than the internal
// "provider::sessionID::model" cache key, and that the result is sorted
// deterministically by provider → sessionID → model.
func TestSessionCacheSnapshot_SplitsCompositeKey(t *testing.T) {
	t.Parallel()

	// Stop the cleanup goroutine so the test doesn't leak on a short TTL.
	c := NewSessionCache(time.Hour)
	defer c.Stop()

	c.Set("gemini::header:abc123::gemini-2.5-pro", "auth-g0")
	c.Set("gemini::header:abc123::gemini-3-flash", "auth-g0")
	c.Set("codex::claude:d9f3-22a1::gpt-5", "auth-c1")

	got := c.Snapshot()
	if len(got) != 3 {
		t.Fatalf("Snapshot() len = %d, want 3", len(got))
	}

	want := []SessionAffinityBinding{
		{Provider: "codex", SessionID: "claude:d9f3-22a1", Model: "gpt-5", AuthID: "auth-c1"},
		{Provider: "gemini", SessionID: "header:abc123", Model: "gemini-2.5-pro", AuthID: "auth-g0"},
		{Provider: "gemini", SessionID: "header:abc123", Model: "gemini-3-flash", AuthID: "auth-g0"},
	}
	for i, w := range want {
		if got[i].Provider != w.Provider || got[i].SessionID != w.SessionID ||
			got[i].Model != w.Model || got[i].AuthID != w.AuthID {
			t.Errorf("Snapshot()[%d] = %+v, want %+v", i, got[i], w)
		}
		if got[i].ExpiresAt.IsZero() {
			t.Errorf("Snapshot()[%d].ExpiresAt is zero, want a real expiry", i)
		}
	}
}

// TestSessionCacheSnapshot_SkipsExpired ensures expired entries are not
// surfaced (they are cleaned lazily on access), so an operator never sees a
// binding whose TTL has already elapsed.
func TestSessionCacheSnapshot_SkipsExpired(t *testing.T) {
	t.Parallel()

	c := NewSessionCache(10 * time.Millisecond)
	defer c.Stop()

	c.Set("gemini::sess-a::m1", "auth-g0")

	// Force expiry by backdating the entry past the TTL.
	c.mu.Lock()
	e := c.entries["gemini::sess-a::m1"]
	e.expiresAt = time.Now().Add(-time.Hour)
	c.entries["gemini::sess-a::m1"] = e
	c.mu.Unlock()

	c.Set("gemini::sess-b::m1", "auth-g0") // still valid

	got := c.Snapshot()
	if len(got) != 1 {
		t.Fatalf("Snapshot() len = %d, want 1 (expired entry skipped)", len(got))
	}
	if got[0].SessionID != "sess-b" {
		t.Errorf("Snapshot()[0].SessionID = %q, want %q", got[0].SessionID, "sess-b")
	}
}

// TestSplitAffinityKey_RoundTrip confirms the composite key built by
// SessionAffinitySelector.Pick ("provider::sessionID::model") splits back into
// its exact original parts, including when the model is empty.
func TestSplitAffinityKey_RoundTrip(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, key, wProvider, wSession, wModel string
	}{
		{"full", "gemini::header:abc123::gemini-2.5-pro", "gemini", "header:abc123", "gemini-2.5-pro"},
		{"empty model", "gemini::header:abc123::", "gemini", "header:abc123", ""},
		{"unparseable", "just-a-session-id", "", "just-a-session-id", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, s, m := splitAffinityKeyLocked(tc.key)
			if p != tc.wProvider || s != tc.wSession || m != tc.wModel {
				t.Errorf("splitAffinityKeyLocked(%q) = (%q,%q,%q), want (%q,%q,%q)",
					tc.key, p, s, m, tc.wProvider, tc.wSession, tc.wModel)
			}
		})
	}
}

// TestSessionCacheInvalidateSessionID_DropsAllModelsForSession verifies that
// revoking a session drops every binding for that sessionID across models
// (the composite key has one entry per model), leaving other sessions intact.
// This is the unit backing the operator-facing "revoke session" action.
func TestSessionCacheInvalidateSessionID_DropsAllModelsForSession(t *testing.T) {
	t.Parallel()

	c := NewSessionCache(time.Hour)
	defer c.Stop()

	c.Set("gemini::sess-A::m1", "auth-g0")
	c.Set("gemini::sess-A::m2", "auth-g0")
	c.Set("codex::sess-A::gpt-5", "auth-c1") // same sessionID across providers → also dropped
	c.Set("gemini::sess-B::m1", "auth-g0")   // different session → preserved

	c.invalidateSessionID("sess-A")

	got := c.Snapshot()
	if len(got) != 1 {
		t.Fatalf("Snapshot() len = %d after invalidate, want 1 (only sess-B)", len(got))
	}
	if got[0].SessionID != "sess-B" {
		t.Errorf("remaining session = %q, want %q", got[0].SessionID, "sess-B")
	}
}

// TestSessionAffinitySelector_SatisfiesView pins the contract the management
// layer relies on: the selector must implement SessionAffinityView so the
// Manager type-assertion routes through to Snapshot/InvalidateSession.
func TestSessionAffinitySelector_SatisfiesView(t *testing.T) {
	t.Parallel()

	s := NewSessionAffinitySelector(&RoundRobinSelector{})
	var _ SessionAffinityView = s // compile-time interface satisfaction

	s.cache.Set("gemini::sess-A::m1", "auth-g0")
	s.cache.Set("gemini::sess-A::m2", "auth-g0")

	got := s.Snapshot()
	if len(got) != 2 {
		t.Fatalf("Snapshot() len = %d, want 2", len(got))
	}

	s.InvalidateSession("sess-A")
	if rem := len(s.Snapshot()); rem != 0 {
		t.Errorf("Snapshot() len after InvalidateSession = %d, want 0", rem)
	}
}

// TestSessionAffinitySelector_SnapshotNilWhenNoCache guards the nil-receiver
// path used when affinity is effectively disabled.
func TestSessionAffinitySelector_SnapshotNilWhenNoCache(t *testing.T) {
	t.Parallel()

	s := &SessionAffinitySelector{fallback: &RoundRobinSelector{}} // cache intentionally nil
	if got := s.Snapshot(); got != nil {
		t.Errorf("Snapshot() with nil cache = %v, want nil", got)
	}
	s.InvalidateSession("sess-A") // must not panic
	s.InvalidateAuth("auth-g0")   // must not panic
}

// TestInvalidateAuthReturnsDeletedCount verifies that InvalidateAuth reports
// how many cache keys it dropped, so callers can log the blast radius, and
// that unrelated bindings survive.
func TestInvalidateAuthReturnsDeletedCount(t *testing.T) {
	c := NewSessionCache(time.Hour)
	defer c.Stop()
	c.Set("mixed::s1::m", "auth-a")
	c.Set("mixed::s2::m", "auth-a")
	c.Set("mixed::s3::m", "auth-b")
	if got := c.InvalidateAuth("auth-a"); got != 2 {
		t.Fatalf("InvalidateAuth = %d, want 2", got)
	}
	if got := c.InvalidateAuth("auth-a"); got != 0 {
		t.Fatalf("second InvalidateAuth = %d, want 0", got)
	}
	if _, ok := c.Get("mixed::s3::m"); !ok {
		t.Fatal("unrelated binding was removed")
	}
	// Alias groups expand to one entry per cache key; the count is per cache
	// key, not per logical session.
	c.SetAliases("auth-c", "mixed::s9::m", "pck:s9")
	if got := c.InvalidateAuth("auth-c"); got != 2 {
		t.Fatalf("alias-group InvalidateAuth = %d, want 2 (one per cache key)", got)
	}
}

func TestSessionCacheAdoptTransfersUnexpiredEntries(t *testing.T) {
	src := NewSessionCache(time.Hour)
	dst := NewSessionCache(time.Hour)
	defer src.Stop()
	defer dst.Stop()
	src.Set("mixed::s1::m", "auth-a")
	expired := sessionEntry{authID: "auth-b", expiresAt: time.Now().Add(-time.Minute)}
	src.mu.Lock()
	src.entries["mixed::dead::m"] = expired
	src.mu.Unlock()

	adopted := dst.Adopt(src)
	if adopted != 1 {
		t.Fatalf("Adopt = %d, want 1 (expired entry skipped)", adopted)
	}
	if id, ok := dst.Get("mixed::s1::m"); !ok || id != "auth-a" {
		t.Fatalf("adopted binding missing or wrong: %q %v", id, ok)
	}

	// Adopting again does not clobber a live destination entry.
	dst.Set("mixed::s1::m", "auth-live")
	if got := dst.Adopt(src); got != 0 {
		t.Fatalf("second Adopt = %d, want 0 (live destination entries kept)", got)
	}
	if id, _ := dst.Get("mixed::s1::m"); id != "auth-live" {
		t.Fatalf("destination entry clobbered: %q", id)
	}
}
