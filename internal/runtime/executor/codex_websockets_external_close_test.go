package executor

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestCloseCodexWebsocketSessionFailsActiveRequest(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}

	sess := &codexWebsocketSession{
		sessionID:  "session-external-close",
		conn:       conn,
		readerConn: conn,
	}
	activeCh := make(chan codexWebsocketRead, 1)
	sess.setActive(activeCh)
	exec := NewCodexWebsocketsExecutor(&config.Config{})
	go exec.readUpstreamLoop(sess, conn)

	closeCodexWebsocketSession(sess, "auth_removed")

	select {
	case event, ok := <-activeCh:
		if !ok {
			t.Fatal("active request channel closed without an error")
		}
		if event.err == nil {
			t.Fatal("active request received no close error")
		}
		if !strings.Contains(event.err.Error(), "auth_removed") {
			t.Fatalf("close error = %q, want auth_removed reason", event.err)
		}
	case <-time.After(time.Second):
		t.Fatal("active request was not notified when its websocket session closed")
	}
}
