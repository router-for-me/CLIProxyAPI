package session

import (
	"testing"
	"time"
)

// TestMerklePrefixMatcherInvalidateSession is the matcher-side counterpart of
// InvalidateAuth: releasing one conversation must leave every other
// conversation bound, including ones on the same credential.
func TestMerklePrefixMatcherInvalidateSession(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()

	namespace := "lcp:v1:invalidate-session:model:caller"
	moving := turnsFromTexts("moving-1", "moving-2", "moving-3")
	staying := turnsFromTexts("staying-1", "staying-2", "staying-3")

	movingSessionID := matcher.Bind(namespace, moving, "auth-a")
	stayingSessionID := matcher.Bind(namespace, staying, "auth-a")
	if movingSessionID == "" || stayingSessionID == "" || movingSessionID == stayingSessionID {
		t.Fatalf("Bind() session ids = %q and %q, want two distinct non-empty ids", movingSessionID, stayingSessionID)
	}

	if removed := matcher.InvalidateSession("no-such-session"); removed != 0 {
		t.Fatalf("InvalidateSession() on an unknown session removed %d groups", removed)
	}

	if removed := matcher.InvalidateSession(movingSessionID); removed != 1 {
		t.Fatalf("InvalidateSession() removed %d groups, want 1", removed)
	}
	if match, ok := matcher.Match(namespace, moving); ok {
		t.Fatalf("invalidated session still resolves to %#v", match)
	}
	if match, ok := matcher.Match(namespace, staying); !ok || match.AuthID != "auth-a" {
		t.Fatalf("untouched session on the same auth was released: %#v, %v", match, ok)
	}

	if removed := matcher.InvalidateSession(movingSessionID); removed != 0 {
		t.Fatalf("repeat InvalidateSession() removed %d groups, want 0", removed)
	}
}

func TestMerklePrefixMatcherInvalidateSessionNilSafe(t *testing.T) {
	t.Parallel()

	var matcher *MerklePrefixMatcher
	if removed := matcher.InvalidateSession("anything"); removed != 0 {
		t.Fatalf("nil matcher removed %d groups", removed)
	}
	live := NewMerklePrefixMatcher(time.Hour)
	defer live.Clear()
	if removed := live.InvalidateSession(""); removed != 0 {
		t.Fatalf("empty session id removed %d groups", removed)
	}
}

// A request routed before InvalidateSession must not restore the released
// binding when its success is reported afterwards; a request routed after the
// release binds and refreshes normally.
func TestMerklePrefixMatcherInvalidateSessionIgnoresStaleTouch(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()

	namespace := "lcp:v1:invalidate-stale:model:caller"
	fingerprints := []string{"fp-one", "fp-two"}
	inFlight := matcher.BindFingerprintsWithResult(namespace, fingerprints, 1, "auth-a")
	if inFlight.SessionID == "" || inFlight.AccessNumber == 0 {
		t.Fatalf("bind result = %#v", inFlight)
	}

	if removed := matcher.InvalidateSession(inFlight.SessionID); removed != 1 {
		t.Fatalf("InvalidateSession() removed %d groups, want 1", removed)
	}
	if matcher.TouchSessionFingerprintsWithContext(namespace, fingerprints, nil, "", 1, "auth-a", inFlight.SessionID, inFlight.AccessNumber) {
		t.Fatal("stale touch from a request routed before the release was applied")
	}
	if match, ok := matcher.MatchFingerprints(namespace, fingerprints, 1); ok {
		t.Fatalf("stale touch re-created the released binding: %#v", match)
	}

	rebound := matcher.BindFingerprintsWithResult(namespace, fingerprints, 1, "auth-b")
	if rebound.AccessNumber <= inFlight.AccessNumber {
		t.Fatalf("rebind access number %d not after %d", rebound.AccessNumber, inFlight.AccessNumber)
	}
	if !matcher.TouchSessionFingerprintsWithContext(namespace, fingerprints, nil, "", 1, "auth-b", rebound.SessionID, rebound.AccessNumber) {
		t.Fatal("touch from a request routed after the release was ignored")
	}
	if match, ok := matcher.MatchFingerprints(namespace, fingerprints, 1); !ok || match.AuthID != "auth-b" {
		t.Fatalf("rebound match = %#v, %v; want auth-b", match, ok)
	}
}

// The release must outlive the affinity TTL: an upstream request can run
// longer than the TTL and report success afterwards.
func TestMerklePrefixMatcherInvalidateSessionOutlivesTTL(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	matcher := NewMerklePrefixMatcherWithConfig(MerklePrefixMatcherConfig{TTL: time.Minute, NowFunc: func() time.Time { return now }})
	defer matcher.Clear()

	namespace := "lcp:v1:invalidate-ttl:model:caller"
	fingerprints := []string{"fp-one", "fp-two"}
	inFlight := matcher.BindFingerprintsWithResult(namespace, fingerprints, 1, "auth-a")
	if removed := matcher.InvalidateSession(inFlight.SessionID); removed != 1 {
		t.Fatalf("InvalidateSession() removed %d groups, want 1", removed)
	}

	now = now.Add(10 * time.Minute)
	if matcher.TouchSessionFingerprintsWithContext(namespace, fingerprints, nil, "", 1, "auth-a", inFlight.SessionID, inFlight.AccessNumber) {
		t.Fatal("stale touch after the TTL re-created the released binding")
	}
	if match, ok := matcher.MatchFingerprints(namespace, fingerprints, 1); ok {
		t.Fatalf("released binding came back: %#v", match)
	}
}

// A derived session can be routed before its first group exists; releasing it
// must still stop that request's success from creating it on the old auth.
func TestMerklePrefixMatcherInvalidateSessionCoversUnboundSession(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()

	namespace := "lcp:v1:invalidate-unbound:model:caller"
	fingerprints := []string{"fp-one", "fp-two"}
	// Advance the access counter as routing would, without inserting a group for
	// the derived session.
	routed := matcher.BindFingerprintsWithResult("lcp:v1:other:model:caller", []string{"fp-x"}, 1, "auth-a")
	derivedSessionID := "lcp-derived-child"

	if removed := matcher.InvalidateSession(derivedSessionID); removed != 0 {
		t.Fatalf("InvalidateSession() removed %d groups, want 0", removed)
	}
	if matcher.TouchSessionFingerprintsWithContext(namespace, fingerprints, nil, "", 1, "auth-a", derivedSessionID, routed.AccessNumber) {
		t.Fatal("success from a request routed before the release created the released session")
	}
	if match, ok := matcher.MatchFingerprints(namespace, fingerprints, 1); ok {
		t.Fatalf("released derived session was created: %#v", match)
	}
}

func TestMerklePrefixMatcherSessionReleasedSince(t *testing.T) {
	t.Parallel()

	matcher := NewMerklePrefixMatcher(time.Hour)
	defer matcher.Clear()

	matched := matcher.BindFingerprintsWithResult("lcp:v1:released-since:model:caller", []string{"fp-one"}, 1, "auth-a")
	if matcher.SessionReleasedSince(matched.SessionID, matched.AccessNumber) {
		t.Fatal("session reported released before InvalidateSession")
	}
	matcher.InvalidateSession(matched.SessionID)
	if !matcher.SessionReleasedSince(matched.SessionID, matched.AccessNumber) {
		t.Fatal("release after the match was not reported")
	}
	later := matcher.BindFingerprintsWithResult("lcp:v1:released-since:model:caller", []string{"fp-one"}, 1, "auth-b")
	if matcher.SessionReleasedSince(later.SessionID, later.AccessNumber) {
		t.Fatal("a match made after the release was reported as released")
	}
}
