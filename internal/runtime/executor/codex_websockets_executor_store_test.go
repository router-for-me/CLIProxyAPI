package executor

import (
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestCodexWebsocketsExecutor_SessionStoreSurvivesExecutorReplacement(t *testing.T) {
	sessionID := "test-session-store-survives-replace"
	storeKey := sessionStoreKey("", sessionID)

	globalCodexWebsocketSessionStore.mu.Lock()
	delete(globalCodexWebsocketSessionStore.sessions, storeKey)
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
	_, stillPresent := globalCodexWebsocketSessionStore.sessions[storeKey]
	globalCodexWebsocketSessionStore.mu.Unlock()
	if !stillPresent {
		t.Fatalf("expected session to remain after executor replacement close marker")
	}

	exec2.CloseExecutionSession(sessionID)

	globalCodexWebsocketSessionStore.mu.Lock()
	_, presentAfterClose := globalCodexWebsocketSessionStore.sessions[storeKey]
	globalCodexWebsocketSessionStore.mu.Unlock()
	if presentAfterClose {
		t.Fatalf("expected session to be removed after explicit close")
	}
}

func TestCodexWebsocketsExecutor_ScopedSessionIsolation(t *testing.T) {
	sessionID := "test-scoped-isolation"

	globalCodexWebsocketSessionStore.mu.Lock()
	for k := range globalCodexWebsocketSessionStore.sessions {
		if strings.HasSuffix(k, sessionID) {
			delete(globalCodexWebsocketSessionStore.sessions, k)
		}
	}
	globalCodexWebsocketSessionStore.mu.Unlock()

	exec := NewCodexWebsocketsExecutor(nil)

	sessA := exec.getOrCreateSessionScoped("auth-A", sessionID)
	sessB := exec.getOrCreateSessionScoped("auth-B", sessionID)
	if sessA == nil || sessB == nil {
		t.Fatal("expected both sessions to be created")
	}
	if sessA == sessB {
		t.Fatal("sessions with different authIDs must be distinct")
	}

	exec.CloseExecutionSession(sessionID)

	globalCodexWebsocketSessionStore.mu.Lock()
	remaining := 0
	for k := range globalCodexWebsocketSessionStore.sessions {
		if strings.HasSuffix(k, sessionID) {
			remaining++
		}
	}
	globalCodexWebsocketSessionStore.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("expected all scoped sessions to be closed, got %d remaining", remaining)
	}
}
