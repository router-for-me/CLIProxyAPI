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

// On a duplex socket a rejected queued create can fail while another response
// is still running. That failure must not end the live response's interrupt
// window, the running response's own terminal event must, and an unknown id
// must be rejected even while a different response is active.
func TestInterruptExecutionSessionTracksActiveResponseIdentity(t *testing.T) {
	received := make(chan string, 8)
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
		// Hold the completion until the active-r1 interrupt has arrived, so
		// the read loop cannot process it before the checks below run.
		for _, payload := range []string{
			`{"type":"response.created","response":{"id":"r1"}}`,
			// A queued create rejected before its own response.created.
			`{"type":"response.failed","response":{"id":"queued-1"}}`,
		} {
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(payload)); errWrite != nil {
				t.Errorf("write: %v", errWrite)
				return
			}
		}
		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			return
		}
		received <- string(payload)
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"r1","output":[]}}`)); errWrite != nil {
			t.Errorf("write completion: %v", errWrite)
			return
		}
		for {
			_, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				return
			}
			received <- string(payload)
			if string(payload) == "barrier" {
				return
			}
		}
	}))
	defer upstream.Close()

	client, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(upstream.URL, "http"), nil)
	if errDial != nil {
		t.Fatal(errDial)
	}
	exec := NewCodexWebsocketsExecutor(nil)
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	const sessionID = "active-response-identity"
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
	interrupt := func(payload string) error {
		t.Helper()
		return exec.InterruptExecutionSession(context.Background(), sessionID, []byte(payload))
	}

	read() // response.created r1
	read() // response.failed queued-1

	// The queued rejection must not close r1's interrupt window.
	if errInterrupt := interrupt(`{"type":"response.interrupt","response_id":"r1"}`); errInterrupt != nil {
		t.Fatalf("interrupt active r1 after queued failure = %v, want nil (forwarded)", errInterrupt)
	}
	// An unknown id is not the running response: reject outright, without
	// inviting a local HTTP fallback against the live websocket turn.
	if errUnknown := interrupt(`{"type":"response.interrupt","response_id":"unknown"}`); !errors.Is(errUnknown, cliproxyexecutor.ErrUpstreamWebsocketInterruptRejected) {
		t.Fatalf("unknown interrupt during r1 = %v, want ErrUpstreamWebsocketInterruptRejected", errUnknown)
	}

	read() // response.completed r1

	// The terminal id itself stays idempotent, but an id that never ran must
	// be rejected while teardown of the active channel is still pending.
	if errTerminal := interrupt(`{"type":"response.interrupt","response_id":"r1"}`); errTerminal != nil {
		t.Fatalf("interrupt completed r1 = %v, want nil (already terminal)", errTerminal)
	}
	if errUnknown := interrupt(`{"type":"response.interrupt","response_id":"unknown-2"}`); !errors.Is(errUnknown, cliproxyexecutor.ErrUpstreamWebsocketInterruptRejected) {
		t.Fatalf("unknown interrupt after completion = %v, want ErrUpstreamWebsocketInterruptRejected", errUnknown)
	}

	// Only the active-r1 interrupt may have reached the socket.
	if errWrite := client.WriteMessage(websocket.TextMessage, []byte("barrier")); errWrite != nil {
		t.Fatal(errWrite)
	}
	var got []string
loop:
	for {
		select {
		case payload := <-received:
			got = append(got, payload)
			if payload == "barrier" {
				break loop
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("upstream barrier timed out, received %v", got)
		}
	}
	want := []string{`{"type":"response.interrupt","response_id":"r1"}`, "barrier"}
	if len(got) != len(want) {
		t.Fatalf("upstream received %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("upstream received %v, want %v", got, want)
		}
	}
	sess.clearActive(client, readCh)
}
