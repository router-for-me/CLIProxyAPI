package executor

import (
	"context"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestCodexWebsocketsExecutor_CloseAllReleasesSessions(t *testing.T) {
	sessionID := "test-session-store-survives-replace"

	globalCodexWebsocketSessionStore.mu.Lock()
	delete(globalCodexWebsocketSessionStore.sessions, sessionID)
	globalCodexWebsocketSessionStore.mu.Unlock()

	exec1 := NewCodexWebsocketsExecutor(nil)
	sess1 := exec1.getOrCreateSession(sessionID)
	if sess1 == nil {
		t.Fatalf("expected session to be created")
	}

	exec2 := NewCodexWebsocketsExecutor(nil)
	sess2 := exec2.getOrCreateSession(sessionID)
	if sess2 == nil {
		t.Fatalf("expected session to be available across executors")
	}
	if sess1 != sess2 {
		t.Fatalf("expected the same session instance across executors")
	}

	exec1.CloseExecutionSession(cliproxyauth.CloseAllExecutionSessionsID)

	globalCodexWebsocketSessionStore.mu.Lock()
	_, stillPresent := globalCodexWebsocketSessionStore.sessions[sessionID]
	globalCodexWebsocketSessionStore.mu.Unlock()
	if stillPresent {
		t.Fatalf("expected session to be removed after executor shutdown")
	}

	exec2.CloseExecutionSession(sessionID)
}

func TestCodexWebsocketsExecutor_AuthRemovalPreservesOtherAuthSessions(t *testing.T) {
	manager := cliproxyauth.NewManager(nil, nil, nil)
	exec := NewCodexAutoExecutor(nil)
	manager.RegisterExecutor(exec)
	ctx := context.Background()
	sessions := make(map[string]*codexWebsocketSession)
	for _, authID := range []string{"removed-auth", "remaining-auth"} {
		if _, err := manager.Register(ctx, &cliproxyauth.Auth{ID: authID, Provider: "codex", Status: cliproxyauth.StatusActive}); err != nil {
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

	globalCodexWebsocketSessionStore.mu.Lock()
	removed := globalCodexWebsocketSessionStore.sessions[sessions["removed-auth"].sessionID]
	remaining := globalCodexWebsocketSessionStore.sessions[sessions["remaining-auth"].sessionID]
	globalCodexWebsocketSessionStore.mu.Unlock()
	if removed != nil {
		t.Fatal("removed auth session remains in the store")
	}
	if remaining != sessions["remaining-auth"] {
		t.Fatal("removing one auth closed another auth's websocket session")
	}
}
