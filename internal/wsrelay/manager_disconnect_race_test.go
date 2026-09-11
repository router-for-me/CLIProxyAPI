package wsrelay

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// When a disconnecting session no longer owns the provider key, a newer session
// has replaced it: the disconnect must be reported as a replacement so the
// service layer skips the Delete that would drop the replacement's auth (#5392,
// Race B).
func TestHandleSessionClosed_ReportsReplacementInsteadOfOriginalCause(t *testing.T) {
	var mu sync.Mutex
	var causes []error
	m := NewManager(Options{OnDisconnected: func(provider string, err error) {
		mu.Lock()
		defer mu.Unlock()
		causes = append(causes, err)
	}})

	s1 := &session{provider: "prov", closed: make(chan struct{})}
	s2 := &session{provider: "prov", closed: make(chan struct{})}
	m.sessMutex.Lock()
	m.sessions["prov"] = s1
	m.sessMutex.Unlock()
	m.sessMutex.Lock()
	m.sessions["prov"] = s2
	m.sessMutex.Unlock()

	m.handleSessionClosed(s1, errors.New("original read error"))

	if got := m.session("prov"); got != s2 {
		t.Fatal("replacement session was removed from the provider key")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(causes) != 1 {
		t.Fatalf("onDisconnected calls = %d, want 1", len(causes))
	}
	if causes[0] == nil || !strings.Contains(causes[0].Error(), "replaced by new connection") {
		t.Fatalf("cause = %v, want replacement notice; the original cause would drop the live replacement's auth", causes[0])
	}
}

// A disconnecting session that still owns the provider key keeps the original
// cause and removes the entry.
func TestHandleSessionClosed_OwnerKeepsCauseAndDeletes(t *testing.T) {
	var mu sync.Mutex
	var causes []error
	m := NewManager(Options{OnDisconnected: func(provider string, err error) {
		mu.Lock()
		defer mu.Unlock()
		causes = append(causes, err)
	}})

	s1 := &session{provider: "owner", closed: make(chan struct{})}
	m.sessMutex.Lock()
	m.sessions["owner"] = s1
	m.sessMutex.Unlock()

	m.handleSessionClosed(s1, errors.New("connection reset"))

	if got := m.session("owner"); got != nil {
		t.Fatal("closed owner session was not removed from the provider key")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(causes) != 1 || causes[0] == nil || causes[0].Error() != "connection reset" {
		t.Fatalf("cause = %v, want the original error", causes)
	}
}

// A replacement connection must emit its onConnected Add only after the
// previous session's disconnect callback (and its queued Delete) completed,
// and only while it still owns the provider key (#5520 review).
func TestHandleWebsocket_OrdersReplacementConnectAfterDisconnectCallback(t *testing.T) {
	type event struct {
		kind string
	}
	events := make(chan event, 16)

	releaseCh := make(chan struct{})
	var disconnectOnce sync.Once
	handlerEntered := make(chan struct{}, 4)
	var dialMu sync.Mutex
	dialCount := 0

	relay := NewManager(Options{
		ProviderFactory: func(*http.Request) (string, error) {
			dialMu.Lock()
			dialCount++
			n := dialCount
			dialMu.Unlock()
			if n >= 2 {
				// Deterministic barrier: the replacement handler has entered
				// handleWebsocket before the test proceeds past its checks.
				handlerEntered <- struct{}{}
			}
			return "p", nil
		},
		OnConnected: func(string) {
			events <- event{kind: "connect"}
		},
		OnDisconnected: func(provider string, err error) {
			if err != nil && strings.Contains(err.Error(), "replaced by new connection") {
				events <- event{kind: "disconnect-replaced"}
				return
			}
			// First (owner) disconnect: block until the test releases it.
			disconnectOnce.Do(func() {
				events <- event{kind: "disconnect-owner-start"}
				<-releaseCh
			})
			events <- event{kind: "disconnect-owner-done"}
		},
	})
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + relay.Path()

	dial := func() *websocket.Conn {
		conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
		if errDial != nil {
			t.Fatalf("dial websocket: %v", errDial)
		}
		return conn
	}

	connA := dial()
	select {
	case ev := <-events:
		if ev.kind != "connect" {
			t.Fatalf("first event = %v, want connect", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first connect")
	}

	// Drop A: its owner disconnect callback blocks until released.
	_ = connA.Close()
waitLoop:
	for {
		select {
		case ev := <-events:
			if ev.kind == "disconnect-owner-start" {
				break waitLoop
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for owner disconnect to start")
		}
	}

	// Replacement dials while the disconnect callback is still blocked. The
	// handler-entry barrier proves the replacement reached handleWebsocket and
	// is parked on the key lock; its connect must wait for the callback.
	connB := dial()
	select {
	case <-handlerEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the replacement handler to enter")
	}
	select {
	case ev := <-events:
		t.Fatalf("replacement connected before the disconnect callback finished: %v", ev)
	case <-time.After(150 * time.Millisecond):
	}

	close(releaseCh)
	select {
	case ev := <-events:
		if ev.kind != "disconnect-owner-done" {
			t.Fatalf("event after release = %v, want disconnect-owner-done", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for disconnect completion")
	}
	select {
	case ev := <-events:
		if ev.kind != "connect" {
			t.Fatalf("event after release = %v, want the replacement connect", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("replacement connect never fired after the disconnect callback finished")
	}

	_ = connB.Close()
}
