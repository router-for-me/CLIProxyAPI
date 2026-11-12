package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// An unknown-id interrupt arriving after the upstream terminal event was
// delivered, but before the stream goroutine cleared the active channel,
// must be rejected outright instead of being written to the still-active
// socket or falling back to a local cancellation. The test does not call
// clearActive on purpose: it reproduces the window between completion
// delivery and turn teardown.
func TestInterruptExecutionSessionUnknownAfterTerminalDelivery(t *testing.T) {
	interruptCount := make(chan int, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade: %v", errUpgrade)
			return
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				t.Errorf("close upstream: %v", errClose)
			}
		}()
		for _, payload := range []string{
			`{"type":"response.created","response":{"id":"r1"}}`,
			`{"type":"response.completed","response":{"id":"r1","output":[]}}`,
		} {
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(payload)); errWrite != nil {
				t.Errorf("write: %v", errWrite)
				return
			}
		}
		count := 0
		for {
			_, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				return
			}
			if string(payload) == "barrier" {
				interruptCount <- count
				return
			}
			count++
		}
	}))
	defer upstream.Close()

	client, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(upstream.URL, "http"), nil)
	if errDial != nil {
		t.Fatal(errDial)
	}
	exec := NewCodexWebsocketsExecutor(nil)
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	const sessionID = "unknown-after-terminal"
	defer exec.CloseExecutionSession(sessionID)
	sess := exec.getOrCreateSession(sessionID)
	sess.conn = client
	sess.connCloser = newWebsocketConnectionCloser(client)
	sess.configureConn(client)
	readCh := sess.activate(client)
	go exec.readUpstreamLoop(sess, client)
	read := func() codexWebsocketRead {
		t.Helper()
		select {
		case event := <-readCh:
			if event.err != nil {
				t.Fatal(event.err)
			}
			return event
		case <-time.After(3 * time.Second):
			t.Fatal("upstream event timed out")
			return codexWebsocketRead{}
		}
	}
	read()
	read()

	// Deliberately no clearActive here: the stream goroutine may not have
	// run its deferred teardown yet, but the client has already seen
	// response.completed and now sends an unknown interrupt.
	errInterrupt := exec.InterruptExecutionSession(context.Background(), sessionID, []byte(`{"type":"response.interrupt","response_id":"unknown"}`))
	if !errors.Is(errInterrupt, cliproxyexecutor.ErrUpstreamWebsocketInterruptRejected) {
		t.Fatalf("unknown interrupt after completion = %v, want ErrUpstreamWebsocketInterruptRejected", errInterrupt)
	}
	// A transport-order barrier avoids sleeps or assumptions about whether
	// the upstream reader has already consumed a wrongly forwarded frame.
	if errWrite := client.WriteMessage(websocket.TextMessage, []byte("barrier")); errWrite != nil {
		t.Fatal(errWrite)
	}
	select {
	case count := <-interruptCount:
		if count != 0 {
			t.Fatalf("forwarded %d interrupt(s) upstream after completion", count)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("upstream barrier timed out")
	}
	sess.clearActive(client, readCh)
}
