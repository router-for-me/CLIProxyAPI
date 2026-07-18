package executor

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestClearUpstreamConnMarksTerminalStateLost(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer server.Close()

	tests := []struct {
		name     string
		reason   string
		wantLost bool
	}{
		{name: "upstream error", reason: "upstream_error", wantLost: true},
		{name: "fresh context reset", reason: codexWebsocketFreshContextResetReason, wantLost: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
			conn, resp, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
			closeHTTPResponseBody(resp, "test close terminal-state handshake body")
			if errDial != nil {
				t.Fatalf("dial websocket: %v", errDial)
			}

			sess := &codexWebsocketSession{
				sessionID:         "terminal-state-clear-" + test.name,
				conn:              conn,
				readerConn:        conn,
				terminalStateConn: conn,
			}
			exec := NewCodexWebsocketsExecutor(&config.Config{})
			exec.clearUpstreamConn(sess, conn, test.reason, errors.New("upstream closed"), false)

			if got := sess.lostTerminalState(); got != test.wantLost {
				t.Fatalf("lostTerminalState() = %v, want %v", got, test.wantLost)
			}
			incrementalCreate := []byte(`{"type":"response.create","model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"next"}]}`)
			if got := codexWebsocketRequestRequiresExistingUpstream(sess, incrementalCreate); got != test.wantLost {
				t.Fatalf("codexWebsocketRequestRequiresExistingUpstream() = %v, want %v", got, test.wantLost)
			}
		})
	}
}
