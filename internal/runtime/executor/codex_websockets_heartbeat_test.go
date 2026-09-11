package executor

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestCodexWebsocketHeartbeatSendsPingsAndStopsWithConnection(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	pingSeen := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()
		conn.SetPingHandler(func(appData string) error {
			select {
			case pingSeen <- struct{}{}:
			default:
			}
			return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(time.Second))
		})
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	closer := newWebsocketConnectionCloser(conn)
	stopped := closer.startHeartbeat(10*time.Millisecond, time.Second, nil)
	if duplicate := closer.startHeartbeat(10*time.Millisecond, time.Second, nil); duplicate != stopped {
		t.Fatal("starting heartbeat twice returned a different stop channel")
	}

	select {
	case <-pingSeen:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for heartbeat ping")
	}
	if errClose := closer.Close(); errClose != nil {
		t.Fatalf("close websocket: %v", errClose)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not stop after connection close")
	}
}

func TestCodexWebsocketHeartbeatReportsPingFailure(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	serverConn := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		serverConn <- conn
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	defer func() { _ = conn.Close() }()
	peer := <-serverConn
	defer func() { _ = peer.Close() }()

	var callbackCalls atomic.Int32
	heartbeatErr := make(chan error, 1)
	closer := newWebsocketConnectionCloser(conn)
	stopped := closer.startHeartbeat(10*time.Millisecond, time.Second, func(err error) {
		callbackCalls.Add(1)
		heartbeatErr <- err
	})
	if errClose := conn.Close(); errClose != nil {
		t.Fatalf("close raw websocket: %v", errClose)
	}

	select {
	case errHeartbeat := <-heartbeatErr:
		if errHeartbeat == nil || !strings.Contains(errHeartbeat.Error(), "heartbeat ping failed") {
			t.Fatalf("heartbeat error = %v, want wrapped ping failure", errHeartbeat)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for heartbeat error callback")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not stop after ping failure")
	}
	if got := callbackCalls.Load(); got != 1 {
		t.Fatalf("heartbeat callback calls = %d, want 1", got)
	}
}

func TestCodexWebsocketHeartbeatPongExtendsReadDeadline(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()
		writeDone := make(chan struct{})
		go func() {
			defer close(writeDone)
			time.Sleep(500 * time.Millisecond)
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte("ready")); errWrite != nil {
				t.Errorf("write websocket message: %v", errWrite)
			}
		}()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				<-writeDone
				return
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	closer := newWebsocketConnectionCloser(conn)
	defer func() { _ = closer.Close() }()

	idleTimeout := 200 * time.Millisecond
	configureCodexWebsocketPongHandler(conn, idleTimeout)
	if errDeadline := conn.SetReadDeadline(time.Now().Add(idleTimeout)); errDeadline != nil {
		t.Fatalf("set initial read deadline: %v", errDeadline)
	}
	closer.startHeartbeat(30*time.Millisecond, time.Second, nil)

	msgType, payload, errRead := conn.ReadMessage()
	if errRead != nil {
		t.Fatalf("read websocket message after initial deadline: %v", errRead)
	}
	if msgType != websocket.TextMessage || string(payload) != "ready" {
		t.Fatalf("websocket message = type %d payload %q, want text ready", msgType, payload)
	}
}
