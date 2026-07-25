package executor

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
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

func TestCodexAutoExecutorReplacementPreservesSharedSessionStore(t *testing.T) {
	server, _ := newWebsocketTargetServer(t)
	defer server.Close()

	sessionID := "test-session-store-survives-manager-replace"
	globalCodexWebsocketSessionStore.mu.Lock()
	delete(globalCodexWebsocketSessionStore.sessions, sessionID)
	globalCodexWebsocketSessionStore.mu.Unlock()

	firstConfig := &config.Config{RequestRetry: 1}
	secondConfig := &config.Config{RequestRetry: 2}
	first := NewCodexAutoExecutor(firstConfig)
	second := NewCodexAutoExecutor(secondConfig)
	auth := &cliproxyauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Metadata: map[string]any{
			"access_token": "oauth-token",
			"account_id":   "account-a",
		},
	}
	sess := first.wsExec.getOrCreateSession(sessionID)
	if sess == nil {
		t.Fatal("expected session to be created")
	}
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, physical := newCloseCountingWebsocketConn(t, wsURL)
	sess.connMu.Lock()
	sess.conn = conn
	sess.connCloser = newWebsocketConnectionCloser(conn)
	sess.wsURL = wsURL
	sess.authID = auth.ID
	sess.connectionKey = codexWebsocketConnectionKey(firstConfig, auth)
	sess.readerConn = conn
	sess.connMu.Unlock()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(first)
	manager.RegisterExecutor(second)

	globalCodexWebsocketSessionStore.mu.Lock()
	_, stillPresent := globalCodexWebsocketSessionStore.sessions[sessionID]
	globalCodexWebsocketSessionStore.mu.Unlock()
	if !stillPresent {
		t.Fatal("expected shared websocket session to survive executor replacement")
	}
	if got := physical.closes.Load(); got != 0 {
		t.Fatalf("physical websocket closes after executor replacement = %d, want 0", got)
	}
	wantConnectionKey := codexWebsocketConnectionKey(secondConfig, auth)
	gotConn, gotCloser := existingWebsocketSessionConn(sess, auth.ID, wsURL, wantConnectionKey)
	if gotConn != conn || gotCloser != sess.connCloser {
		t.Fatal("replacement executor could not reuse the preserved websocket session")
	}

	second.CloseExecutionSession(sessionID)
	if got := physical.closes.Load(); got != 1 {
		t.Fatalf("physical websocket closes after explicit session close = %d, want 1", got)
	}
}

func TestCodexAutoExecutorReplacementPreservesOAuthSessionWhenUnrelatedAPIKeyChanges(t *testing.T) {
	server, _ := newWebsocketTargetServer(t)
	defer server.Close()

	sessionID := "test-session-store-ignores-unrelated-api-key-change"
	globalCodexWebsocketSessionStore.mu.Lock()
	delete(globalCodexWebsocketSessionStore.sessions, sessionID)
	globalCodexWebsocketSessionStore.mu.Unlock()

	firstConfig := &config.Config{CodexKey: []config.CodexKey{{
		APIKey:     "unrelated-key-a",
		BaseURL:    "https://api.example.test/v1",
		Websockets: true,
	}}}
	secondConfig := &config.Config{CodexKey: []config.CodexKey{{
		APIKey:     "unrelated-key-b",
		BaseURL:    "https://api.example.test/v1",
		Websockets: true,
	}}}
	first := NewCodexAutoExecutor(firstConfig)
	second := NewCodexAutoExecutor(secondConfig)
	auth := &cliproxyauth.Auth{
		ID:       "oauth-auth",
		Provider: "codex",
		Metadata: map[string]any{
			"access_token": "oauth-token",
			"account_id":   "account-a",
		},
	}
	sess := first.wsExec.getOrCreateSession(sessionID)
	if sess == nil {
		t.Fatal("expected session to be created")
	}
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, physical := newCloseCountingWebsocketConn(t, wsURL)
	sess.connMu.Lock()
	sess.conn = conn
	sess.connCloser = newWebsocketConnectionCloser(conn)
	sess.wsURL = wsURL
	sess.authID = auth.ID
	sess.connectionKey = codexWebsocketConnectionKey(firstConfig, auth)
	sess.readerConn = conn
	sess.connMu.Unlock()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(first)
	manager.RegisterExecutor(second)

	globalCodexWebsocketSessionStore.mu.Lock()
	_, stillPresent := globalCodexWebsocketSessionStore.sessions[sessionID]
	globalCodexWebsocketSessionStore.mu.Unlock()
	if !stillPresent {
		t.Fatal("unrelated API-key change removed OAuth websocket session")
	}
	if got := physical.closes.Load(); got != 0 {
		t.Fatalf("physical websocket closes after unrelated API-key change = %d, want 0", got)
	}
	wantConnectionKey := codexWebsocketConnectionKey(secondConfig, auth)
	gotConn, _ := existingWebsocketSessionConn(sess, auth.ID, wsURL, wantConnectionKey)
	if gotConn != conn {
		t.Fatal("replacement executor could not reuse OAuth session after unrelated API-key change")
	}

	second.CloseExecutionSession(sessionID)
}

func TestCodexAutoExecutorReplacementClosesSessionsWhenConnectionConfigChanges(t *testing.T) {
	server, _ := newWebsocketTargetServer(t)
	defer server.Close()

	sessionID := "test-session-store-closes-on-connection-config-change"
	globalCodexWebsocketSessionStore.mu.Lock()
	delete(globalCodexWebsocketSessionStore.sessions, sessionID)
	globalCodexWebsocketSessionStore.mu.Unlock()

	first := NewCodexAutoExecutor(&config.Config{SDKConfig: config.SDKConfig{
		ProxyURL: "http://proxy-a.example:8080",
	}})
	second := NewCodexAutoExecutor(&config.Config{SDKConfig: config.SDKConfig{
		ProxyURL: "http://proxy-b.example:8080",
	}})
	sess := first.wsExec.getOrCreateSession(sessionID)
	if sess == nil {
		t.Fatal("expected session to be created")
	}
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, physical := newCloseCountingWebsocketConn(t, wsURL)
	sess.connMu.Lock()
	sess.conn = conn
	sess.connCloser = newWebsocketConnectionCloser(conn)
	sess.wsURL = wsURL
	sess.authID = "codex-auth"
	sess.readerConn = conn
	sess.connMu.Unlock()

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(first)
	manager.RegisterExecutor(second)

	globalCodexWebsocketSessionStore.mu.Lock()
	_, stillPresent := globalCodexWebsocketSessionStore.sessions[sessionID]
	globalCodexWebsocketSessionStore.mu.Unlock()
	if stillPresent {
		t.Fatal("websocket session survived incompatible executor connection settings")
	}
	if got := physical.closes.Load(); got != 1 {
		t.Fatalf("physical websocket closes after incompatible executor replacement = %d, want 1", got)
	}
}
