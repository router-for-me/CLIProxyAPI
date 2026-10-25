package management

import (
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestOAuthSessionSaveSurvivesExpiryUntilPersistenceResolves(t *testing.T) {
	for _, outcome := range []string{"success", "error"} {
		t.Run(outcome, func(t *testing.T) {
			store := newOAuthSessionStore(oauthSessionTTL)
			replaceOAuthSessionStoreForTest(t, store)
			store.Register("saving-state", "codex")
			store.Register("pending-state", "codex")
			if !store.claimCommit("saving-state", "codex") {
				t.Fatal("could not claim session for persistence")
			}
			store.mu.Lock()
			for state, session := range store.sessions {
				session.ExpiresAt = time.Now().Add(-time.Second)
				store.sessions[state] = session
			}
			store.mu.Unlock()

			if !store.IsPending("saving-state", "codex") {
				t.Fatal("session expired while persistence was still in flight")
			}
			if store.IsPending("pending-state", "codex") {
				t.Fatal("unclaimed session survived expiry")
			}
			if store.Cancel("saving-state") {
				t.Fatal("cancelled a session while persistence was in flight")
			}
			if got := store.CompleteProvider("codex", oauthSessionSourceBuiltin); got != 0 {
				t.Fatal("another login completed the session while persistence was in flight")
			}
			router := gin.New()
			handler := &Handler{}
			router.GET("/status", handler.GetAuthStatus)
			if status := performOAuthStatusRequest(t, router, "saving-state"); status.Status != "wait" {
				t.Fatalf("status during persistence = %#v, want wait", status)
			}

			if outcome == "success" {
				store.Complete("saving-state")
			} else {
				store.FailCommit("saving-state", "persistence failed")
			}
			status := performOAuthStatusRequest(t, router, "saving-state")
			if outcome == "success" && status.Status != "ok" {
				t.Fatalf("status after saving = %#v, want ok", status)
			}
			if outcome == "error" && (status.Status != "error" || status.Error != "persistence failed") {
				t.Fatalf("status after saving = %#v, want persistence error", status)
			}
			session, ok := store.Get("saving-state")
			if !ok || session.Committing || !session.ExpiresAt.After(time.Now()) {
				t.Fatalf("resolved session did not resume normal expiry: %#v, exists=%t", session, ok)
			}
			store.mu.Lock()
			session.ExpiresAt = time.Now().Add(-time.Second)
			store.sessions["saving-state"] = session
			store.mu.Unlock()
			if _, ok := store.Get("saving-state"); ok {
				t.Fatal("resolved session survived expiry")
			}
		})
	}
}
