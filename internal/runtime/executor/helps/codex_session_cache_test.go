package helps

import (
	"testing"
	"time"
)

func TestCodexSessionCache_UpdateAndGet(t *testing.T) {
	ClearCodexSessions()
	t.Cleanup(ClearCodexSessions)

	key := "auth-1|session-A"
	UpdateCodexSession(key, func(s *CodexSessionState) {
		s.SessionID = "sess-1"
		s.TurnState = "turn-token-1"
		s.TurnMetadata = `{"turn_id":"t1"}`
	})

	got, ok := GetCodexSession(key)
	if !ok {
		t.Fatalf("expected entry for key %q", key)
	}
	if got.SessionID != "sess-1" || got.TurnState != "turn-token-1" || got.TurnMetadata != `{"turn_id":"t1"}` {
		t.Fatalf("unexpected state: %+v", got)
	}
	if !got.Expire.After(time.Now()) {
		t.Fatalf("expected Expire in the future, got %v", got.Expire)
	}
}

func TestCodexSessionCache_EmptyKeyNoop(t *testing.T) {
	ClearCodexSessions()
	t.Cleanup(ClearCodexSessions)

	UpdateCodexSession("", func(s *CodexSessionState) {
		s.SessionID = "should-not-be-stored"
	})
	if _, ok := GetCodexSession(""); ok {
		t.Fatalf("empty key must not yield a hit")
	}
}

func TestCodexSessionCache_DeletedWhenAllFieldsBlank(t *testing.T) {
	ClearCodexSessions()
	t.Cleanup(ClearCodexSessions)

	key := "auth-2|session-B"
	UpdateCodexSession(key, func(s *CodexSessionState) {
		s.SessionID = "x"
	})
	if _, ok := GetCodexSession(key); !ok {
		t.Fatalf("precondition: expected entry for key %q", key)
	}

	UpdateCodexSession(key, func(s *CodexSessionState) {
		s.SessionID = ""
	})
	if _, ok := GetCodexSession(key); ok {
		t.Fatalf("entry should be deleted when all fields are blank")
	}
}

func TestCodexSessionCache_ExpiredEntriesReturnMiss(t *testing.T) {
	ClearCodexSessions()
	t.Cleanup(ClearCodexSessions)

	key := "auth-3|session-C"
	SetCodexSession(key, CodexSessionState{
		SessionID: "s",
		Expire:    time.Now().Add(-1 * time.Second),
	})
	if _, ok := GetCodexSession(key); ok {
		t.Fatalf("expired entry must not be returned")
	}
}

func TestCodexSessionCache_PreservesUnchangedFields(t *testing.T) {
	ClearCodexSessions()
	t.Cleanup(ClearCodexSessions)

	key := "auth-4|session-D"
	UpdateCodexSession(key, func(s *CodexSessionState) {
		s.SessionID = "sid"
		s.TurnState = "ts1"
		s.TurnMetadata = "tm1"
	})
	UpdateCodexSession(key, func(s *CodexSessionState) {
		s.TurnState = "ts2"
	})

	got, ok := GetCodexSession(key)
	if !ok {
		t.Fatalf("expected hit")
	}
	if got.SessionID != "sid" {
		t.Fatalf("SessionID should be preserved, got %q", got.SessionID)
	}
	if got.TurnState != "ts2" {
		t.Fatalf("TurnState should be updated, got %q", got.TurnState)
	}
	if got.TurnMetadata != "tm1" {
		t.Fatalf("TurnMetadata should be preserved, got %q", got.TurnMetadata)
	}
}
