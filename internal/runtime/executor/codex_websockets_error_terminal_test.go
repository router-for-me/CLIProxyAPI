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

// Failure events terminate responses too, in the shapes duplex attribution
// accepts (top-level response_id, or id-less while a response runs), for
// both error and response.failed. The session must close the matching
// interrupt window for them, must keep the running response's window open
// for other responses' failures, and must let the next response.created
// reopen it.
func TestInterruptExecutionSessionErrorTerminalShapes(t *testing.T) {
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
		write := func(payload string) bool {
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(payload)); errWrite != nil {
				t.Errorf("write: %v", errWrite)
				return false
			}
			return true
		}
		readFrame := func() bool {
			_, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				return false
			}
			received <- string(payload)
			return true
		}
		// r1 runs; an error for another response must not end it. Reading
		// the forwarded r1 interrupt barriers before r1's own error.
		if !write(`{"type":"response.created","response":{"id":"r1"}}`) {
			return
		}
		if !write(`{"type":"error","response_id":"queued-1","error":{"message":"rejected"}}`) {
			return
		}
		if !readFrame() {
			return
		}
		// r1 ends with a top-level response_id error. The rejected checks
		// write nothing upstream, so after r2 is created the next frame
		// read here must be the r2 interrupt; reading it before r2's
		// id-less failure is sent keeps the checks deterministic.
		if !write(`{"type":"error","response_id":"r1","error":{"message":"boom"}}`) {
			return
		}
		if !write(`{"type":"response.created","response":{"id":"r2"}}`) {
			return
		}
		if !readFrame() {
			return
		}
		if !write(`{"type":"response.failed","response":{"error":{"message":"boom"}}}`) {
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
	const sessionID = "error-terminal-shapes"
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
	wantRejected := func(payload, what string) {
		t.Helper()
		if errInterrupt := interrupt(payload); !errors.Is(errInterrupt, cliproxyexecutor.ErrUpstreamWebsocketInterruptRejected) {
			t.Fatalf("%s = %v, want ErrUpstreamWebsocketInterruptRejected", what, errInterrupt)
		}
	}

	read() // response.created r1
	read() // error queued-1
	if errInterrupt := interrupt(`{"type":"response.interrupt","response_id":"r1"}`); errInterrupt != nil {
		t.Fatalf("interrupt r1 after queued error = %v, want nil (forwarded)", errInterrupt)
	}
	read() // error r1
	wantRejected(`{"type":"response.interrupt","response_id":"unknown"}`, "unknown interrupt after r1 error")
	wantRejected(`{"type":"response.interrupt","response_id":"r1"}`, "interrupt r1 after its error")

	read() // response.created r2
	if errInterrupt := interrupt(`{"type":"response.interrupt","response_id":"r2"}`); errInterrupt != nil {
		t.Fatalf("interrupt r2 = %v, want nil (forwarded)", errInterrupt)
	}
	read() // id-less response.failed
	wantRejected(`{"type":"response.interrupt","response_id":"unknown-2"}`, "unknown interrupt after id-less failure")
	// r2 itself is already terminal: idempotent via the recorded
	// response.failed terminal id, and never forwarded (proven by the
	// upstream barrier below). Returning nil here is what distinguishes
	// "window closed" from "still active" — the latter would forward.
	if errInterrupt := interrupt(`{"type":"response.interrupt","response_id":"r2"}`); errInterrupt != nil {
		t.Fatalf("interrupt r2 after id-less failure = %v, want nil (already terminal)", errInterrupt)
	}

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
	want := []string{
		`{"type":"response.interrupt","response_id":"r1"}`,
		`{"type":"response.interrupt","response_id":"r2"}`,
		"barrier",
	}
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
