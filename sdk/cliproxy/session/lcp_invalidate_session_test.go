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
