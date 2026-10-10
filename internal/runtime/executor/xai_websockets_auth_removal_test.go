package executor

import (
	"context"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestXAIWebsocketsExecutor_AuthRemovalPreservesOtherAuthSessions(t *testing.T) {
	manager := cliproxyauth.NewManager(nil, nil, nil)
	exec := NewXAIAutoExecutor(nil)
	manager.RegisterExecutor(exec)
	ctx := context.Background()
	sessions := make(map[string]*codexWebsocketSession)
	for _, authID := range []string{"removed-auth", "remaining-auth"} {
		if _, err := manager.Register(ctx, &cliproxyauth.Auth{ID: authID, Provider: "xai", Status: cliproxyauth.StatusActive}); err != nil {
			t.Fatalf("register auth: %v", err)
		}
		sessionID := t.Name() + "/" + authID
		sess := exec.wsExec.getOrCreateSession(sessionID)
		sess.connMu.Lock()
		sess.authID = authID
		sess.connMu.Unlock()
		sessions[authID] = sess
		t.Cleanup(func() { exec.CloseExecutionSession(sessionID) })
	}

	// Management API removal calls Manager.Remove directly, without the service
	// watcher cleanup. It must still close only the removed auth sessions.
	manager.Remove(ctx, "removed-auth")

	globalXAIWebsocketSessionStore.mu.Lock()
	removed := globalXAIWebsocketSessionStore.sessions[sessions["removed-auth"].sessionID]
	remaining := globalXAIWebsocketSessionStore.sessions[sessions["remaining-auth"].sessionID]
	globalXAIWebsocketSessionStore.mu.Unlock()
	if removed != nil {
		t.Fatal("removed auth session remains in the store")
	}
	if remaining != sessions["remaining-auth"] {
		t.Fatal("removing one auth closed another auth's websocket session")
	}
}
