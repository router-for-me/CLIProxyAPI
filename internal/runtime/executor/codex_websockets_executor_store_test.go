package executor

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
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

func TestManagerRegisterExecutor_PreservesSharedWebsocketSessionsAcrossExecutorReplacement(t *testing.T) {
	tests := []struct {
		name                string
		store               *codexWebsocketSessionStore
		newExecutor         func(*config.Config) cliproxyauth.ProviderExecutor
		newIsolatedExecutor func(*config.Config) cliproxyauth.ProviderExecutor
		getSession          func(cliproxyauth.ProviderExecutor, string) *codexWebsocketSession
	}{
		{
			name:  "CodexAutoExecutor",
			store: globalCodexWebsocketSessionStore,
			newExecutor: func(cfg *config.Config) cliproxyauth.ProviderExecutor {
				return NewCodexAutoExecutor(cfg)
			},
			newIsolatedExecutor: func(cfg *config.Config) cliproxyauth.ProviderExecutor {
				exec := NewCodexAutoExecutor(cfg)
				exec.wsExec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
				return exec
			},
			getSession: func(exec cliproxyauth.ProviderExecutor, sessionID string) *codexWebsocketSession {
				return exec.(*CodexAutoExecutor).wsExec.getOrCreateSession(sessionID)
			},
		},
		{
			name:  "XAIAutoExecutor",
			store: globalXAIWebsocketSessionStore,
			newExecutor: func(cfg *config.Config) cliproxyauth.ProviderExecutor {
				return NewXAIAutoExecutor(cfg)
			},
			newIsolatedExecutor: func(cfg *config.Config) cliproxyauth.ProviderExecutor {
				exec := NewXAIAutoExecutor(cfg)
				exec.wsExec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
				return exec
			},
			getSession: func(exec cliproxyauth.ProviderExecutor, sessionID string) *codexWebsocketSession {
				return exec.(*XAIAutoExecutor).wsExec.getOrCreateSession(sessionID)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionID := "test-hot-reload-preserves-ws-" + tt.name
			tt.store.mu.Lock()
			delete(tt.store.sessions, sessionID)
			tt.store.mu.Unlock()

			server, _ := newWebsocketTargetServer(t)
			defer server.Close()
			wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

			manager := cliproxyauth.NewManager(nil, nil, nil)
			exec1 := tt.newExecutor(&config.Config{})
			manager.RegisterExecutor(exec1)
			t.Cleanup(func() { manager.CloseExecutionSession(sessionID) })

			sess1 := tt.getSession(exec1, sessionID)
			if sess1 == nil {
				t.Fatal("expected session to be created")
			}
			conn, physical := newCloseCountingWebsocketConn(t, wsURL)
			sess1.connMu.Lock()
			sess1.conn = conn
			sess1.connCloser = newWebsocketConnectionCloser(conn)
			sess1.wsURL = wsURL
			sess1.authID = "auth-1"
			sess1.readerConn = conn
			sess1.connMu.Unlock()

			exec2 := tt.newExecutor(&config.Config{})
			manager.RegisterExecutor(exec2)

			if got := physical.closes.Load(); got != 0 {
				t.Fatalf("physical websocket closes after executor replacement = %d, want 0", got)
			}
			tt.store.mu.Lock()
			preserved := tt.store.sessions[sessionID]
			tt.store.mu.Unlock()
			if preserved != sess1 {
				t.Fatalf("session after executor replacement = %v, want %v", preserved, sess1)
			}

			manager.RegisterExecutor(tt.newIsolatedExecutor(&config.Config{}))
			if got := physical.closes.Load(); got != 1 {
				t.Fatalf("physical websocket closes after isolated executor replacement = %d, want 1", got)
			}
		})
	}
}

func TestManagerRemove_ClosesOnlyRemovedAuthWebsocketSessions(t *testing.T) {
	tests := []struct {
		name        string
		provider    string
		store       *codexWebsocketSessionStore
		newExecutor func(*config.Config) cliproxyauth.ProviderExecutor
		getSession  func(cliproxyauth.ProviderExecutor, string) *codexWebsocketSession
	}{
		{
			name:     "CodexAutoExecutor",
			provider: "codex",
			store:    globalCodexWebsocketSessionStore,
			newExecutor: func(cfg *config.Config) cliproxyauth.ProviderExecutor {
				return NewCodexAutoExecutor(cfg)
			},
			getSession: func(exec cliproxyauth.ProviderExecutor, sessionID string) *codexWebsocketSession {
				return exec.(*CodexAutoExecutor).wsExec.getOrCreateSession(sessionID)
			},
		},
		{
			name:     "XAIAutoExecutor",
			provider: "xai",
			store:    globalXAIWebsocketSessionStore,
			newExecutor: func(cfg *config.Config) cliproxyauth.ProviderExecutor {
				return NewXAIAutoExecutor(cfg)
			},
			getSession: func(exec cliproxyauth.ProviderExecutor, sessionID string) *codexWebsocketSession {
				return exec.(*XAIAutoExecutor).wsExec.getOrCreateSession(sessionID)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessKeepID := "test-remove-keep-ws-" + tt.name
			sessDropID := "test-remove-drop-ws-" + tt.name
			tt.store.mu.Lock()
			delete(tt.store.sessions, sessKeepID)
			delete(tt.store.sessions, sessDropID)
			tt.store.mu.Unlock()

			server, _ := newWebsocketTargetServer(t)
			defer server.Close()
			wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

			manager := cliproxyauth.NewManager(nil, nil, nil)
			exec := tt.newExecutor(&config.Config{})
			manager.RegisterExecutor(exec)
			t.Cleanup(func() {
				manager.CloseExecutionSession(sessKeepID)
				manager.CloseExecutionSession(sessDropID)
			})

			authKeep := &cliproxyauth.Auth{ID: "auth-keep-" + tt.provider, Provider: tt.provider, Status: cliproxyauth.StatusActive}
			authDrop := &cliproxyauth.Auth{ID: "auth-drop-" + tt.provider, Provider: tt.provider, Status: cliproxyauth.StatusActive}
			if _, errReg := manager.Register(t.Context(), authKeep); errReg != nil {
				t.Fatalf("register authKeep: %v", errReg)
			}
			if _, errReg := manager.Register(t.Context(), authDrop); errReg != nil {
				t.Fatalf("register authDrop: %v", errReg)
			}

			sessKeep := tt.getSession(exec, sessKeepID)
			connKeep, physicalKeep := newCloseCountingWebsocketConn(t, wsURL)
			sessKeep.connMu.Lock()
			sessKeep.conn = connKeep
			sessKeep.connCloser = newWebsocketConnectionCloser(connKeep)
			sessKeep.wsURL = wsURL
			sessKeep.authID = authKeep.ID
			sessKeep.readerConn = connKeep
			sessKeep.connMu.Unlock()

			sessDrop := tt.getSession(exec, sessDropID)
			connDrop, physicalDrop := newCloseCountingWebsocketConn(t, wsURL)
			sessDrop.connMu.Lock()
			sessDrop.conn = connDrop
			sessDrop.connCloser = newWebsocketConnectionCloser(connDrop)
			sessDrop.wsURL = wsURL
			sessDrop.authID = authDrop.ID
			sessDrop.readerConn = connDrop
			sessDrop.connMu.Unlock()

			manager.Remove(t.Context(), authDrop.ID)

			if got := physicalDrop.closes.Load(); got != 1 {
				t.Fatalf("removed auth physical websocket closes = %d, want 1", got)
			}
			if got := physicalKeep.closes.Load(); got != 0 {
				t.Fatalf("active sibling auth physical websocket closes = %d, want 0", got)
			}
		})
	}
}
