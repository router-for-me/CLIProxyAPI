package executor

import (
	"errors"
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
	sess.setActive(conn, activeCh)
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

func TestDeliverActiveReadLeavesCapturedChannelOpen(t *testing.T) {
	sess := &codexWebsocketSession{}
	conn := &websocket.Conn{}
	activeCh := make(chan codexWebsocketRead, 1)
	sess.setActive(conn, activeCh)

	closeErr := errors.New("codex websockets executor: session closed: auth_removed")
	if !sess.deliverActiveRead(codexWebsocketRead{err: closeErr}) {
		t.Fatal("active request did not receive the close error")
	}

	event, ok := <-activeCh
	if !ok {
		t.Fatal("active request channel closed before delivering the close error")
	}
	if !errors.Is(event.err, closeErr) {
		t.Fatalf("close error = %v, want %v", event.err, closeErr)
	}

	select {
	case _, ok = <-activeCh:
		if !ok {
			t.Fatal("active request channel was closed while a reader could still hold it")
		}
		t.Fatal("active request received an unexpected duplicate event")
	default:
	}
}

func TestDeliverActiveReadWaitsForFullChannel(t *testing.T) {
	sess := &codexWebsocketSession{}
	conn := &websocket.Conn{}
	activeCh := make(chan codexWebsocketRead, 1)
	sess.setActive(conn, activeCh)
	activeCh <- codexWebsocketRead{conn: conn, payload: []byte("queued")}

	closeErr := errors.New("upstream closed")
	deliveredCh := make(chan bool, 1)
	go func() {
		deliveredCh <- sess.deliverActiveRead(codexWebsocketRead{conn: conn, err: closeErr})
	}()

	select {
	case delivered := <-deliveredCh:
		t.Fatalf("deliverActiveRead() returned %v before the full channel was drained", delivered)
	case <-time.After(50 * time.Millisecond):
	}

	<-activeCh
	select {
	case event := <-activeCh:
		if !errors.Is(event.err, closeErr) {
			t.Fatalf("close error = %v, want %v", event.err, closeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for close error after draining the channel")
	}
	select {
	case delivered := <-deliveredCh:
		if !delivered {
			t.Fatal("deliverActiveRead() reported that the close error was not delivered")
		}
	case <-time.After(time.Second):
		t.Fatal("deliverActiveRead() did not return after delivering the close error")
	}
}
