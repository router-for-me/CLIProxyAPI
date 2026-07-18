package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestCodexWebsocketSessionActiveChannelBelongsToConnection(t *testing.T) {
	sess := &codexWebsocketSession{}
	oldConn := &websocket.Conn{}
	newConn := &websocket.Conn{}
	oldCh := make(chan codexWebsocketRead, 1)
	newCh := make(chan codexWebsocketRead, 1)

	sess.setActive(oldConn, oldCh)
	if ch, _ := sess.activeForConn(oldConn); ch != oldCh {
		t.Fatal("old connection did not own its active channel")
	}

	sess.setActive(newConn, newCh)
	if sess.clearActive(oldConn, oldCh) {
		t.Fatal("old connection cleared the new active channel")
	}
	if ch, _ := sess.activeForConn(oldConn); ch != nil {
		t.Fatal("old connection retained access to an active channel")
	}
	if ch, _ := sess.activeForConn(newConn); ch != newCh {
		t.Fatal("new connection lost its active channel")
	}
	if !sess.clearActive(newConn, newCh) {
		t.Fatal("new connection could not clear its active channel")
	}

	closedOldCh := sess.activate(oldConn)
	if !sess.clearActive(oldConn, closedOldCh) {
		t.Fatal("old connection could not clear its active channel before retry")
	}
	close(closedOldCh)
	retryCh := sess.activate(newConn)
	if retryCh == closedOldCh {
		t.Fatal("retry reused the old connection's read channel")
	}
	select {
	case retryCh <- codexWebsocketRead{conn: newConn}:
	default:
		t.Fatal("retry read channel was not writable")
	}
}

func TestWebsocketExecutorsReconnectWhenSessionTargetChanges(t *testing.T) {
	t.Run("Codex", func(t *testing.T) {
		exec := NewCodexWebsocketsExecutor(&config.Config{})
		exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
		testWebsocketExecutorReconnectsWhenSessionTargetChanges(
			t,
			exec.UpstreamDisconnectChan,
			exec.getOrCreateSession,
			exec.ensureUpstreamConn,
			exec.CloseExecutionSession,
		)
	})

	t.Run("xAI", func(t *testing.T) {
		exec := NewXAIWebsocketsExecutor(&config.Config{})
		exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
		testWebsocketExecutorReconnectsWhenSessionTargetChanges(
			t,
			exec.UpstreamDisconnectChan,
			exec.getOrCreateSession,
			exec.ensureUpstreamConn,
			exec.CloseExecutionSession,
		)
	})
}

func testWebsocketExecutorReconnectsWhenSessionTargetChanges(
	t *testing.T,
	disconnectChan func(string) <-chan error,
	getSession func(string) *codexWebsocketSession,
	ensureConn func(context.Context, *cliproxyauth.Auth, *codexWebsocketSession, string, string, http.Header) (*websocket.Conn, *http.Response, error),
	closeSession func(string),
) {
	t.Helper()

	serverA, closedA := newWebsocketTargetServer(t)
	defer serverA.Close()
	serverB, closedB := newWebsocketTargetServer(t)
	defer serverB.Close()

	sessionID := "target-switch-session"
	disconnectCh := disconnectChan(sessionID)
	sess := getSession(sessionID)
	if sess == nil {
		t.Fatal("expected websocket session")
	}
	defer closeSession(sessionID)

	authA := &cliproxyauth.Auth{ID: "auth-a"}
	authB := &cliproxyauth.Auth{ID: "auth-b"}
	wsURLA := "ws" + strings.TrimPrefix(serverA.URL, "http")
	wsURLB := "ws" + strings.TrimPrefix(serverB.URL, "http")

	connA := ensureWebsocketTargetConn(t, ensureConn, authA, sess, authA.ID, wsURLA)
	connAReused := ensureWebsocketTargetConn(t, ensureConn, authA, sess, authA.ID, wsURLA)
	if connAReused != connA {
		t.Fatal("matching websocket target did not reuse the existing connection")
	}

	connURLB := ensureWebsocketTargetConn(t, ensureConn, authA, sess, authA.ID, wsURLB)
	if connURLB == connA {
		t.Fatal("websocket URL change reused the existing connection")
	}
	if got := <-closedA; got != authA.ID {
		t.Fatalf("closed server A auth = %q, want %q", got, authA.ID)
	}

	connAuthB := ensureWebsocketTargetConn(t, ensureConn, authB, sess, authB.ID, wsURLB)
	if connAuthB == connURLB {
		t.Fatal("websocket auth change reused the existing connection")
	}
	if got := <-closedB; got != authA.ID {
		t.Fatalf("first closed server B auth = %q, want %q", got, authA.ID)
	}

	sess.connMu.Lock()
	gotAuthID := sess.authID
	gotURL := sess.wsURL
	sess.connMu.Unlock()
	if gotAuthID != authB.ID || gotURL != wsURLB {
		t.Fatalf("session target = {%q %q}, want {%q %q}", gotAuthID, gotURL, authB.ID, wsURLB)
	}

	select {
	case errDisconnect := <-disconnectCh:
		t.Fatalf("controlled websocket target switch notified downstream: %v", errDisconnect)
	default:
	}
}

func ensureWebsocketTargetConn(
	t *testing.T,
	ensureConn func(context.Context, *cliproxyauth.Auth, *codexWebsocketSession, string, string, http.Header) (*websocket.Conn, *http.Response, error),
	auth *cliproxyauth.Auth,
	sess *codexWebsocketSession,
	authID string,
	wsURL string,
) *websocket.Conn {
	t.Helper()
	headers := http.Header{"X-Test-Auth": []string{authID}}
	conn, resp, errEnsure := ensureConn(context.Background(), auth, sess, authID, wsURL, headers)
	if resp != nil && resp.Body != nil {
		defer func() {
			if errClose := resp.Body.Close(); errClose != nil {
				t.Errorf("close handshake response body: %v", errClose)
			}
		}()
	}
	if errEnsure != nil {
		t.Fatalf("ensure websocket connection: %v", errEnsure)
	}
	if conn == nil {
		t.Fatal("ensure websocket connection returned nil")
	}
	return conn
}

func newWebsocketTargetServer(t *testing.T) (*httptest.Server, <-chan string) {
	t.Helper()
	closed := make(chan string, 4)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authID := r.Header.Get("X-Test-Auth")
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				t.Errorf("close upstream websocket: %v", errClose)
			}
			closed <- authID
		}()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	return server, closed
}
