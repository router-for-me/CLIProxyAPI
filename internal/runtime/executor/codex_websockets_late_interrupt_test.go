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

// A completion arriving while credential validation is in progress must be
// reflected in the interrupt decision, not forwarded into the following turn.
func TestInterruptExecutionSessionCompletesDuringAuthCheck(t *testing.T) {
	complete := make(chan struct{})
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
		<-complete
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"r1","output":[]}}`)); errWrite != nil {
			t.Errorf("write completion: %v", errWrite)
			return
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
	const sessionID = "completion-during-auth-check"
	defer exec.CloseExecutionSession(sessionID)
	sess := exec.getOrCreateSession(sessionID)
	sess.conn = client
	sess.connCloser = newWebsocketConnectionCloser(client)
	sess.configureConn(client)
	readCh := sess.activate(client)
	go exec.readUpstreamLoop(sess, client)
	ctx := cliproxyexecutor.WithWebsocketAuthCheck(context.Background(), func(string) bool {
		close(complete)
		select {
		case event := <-readCh:
			if event.err != nil {
				t.Fatal(event.err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("completion timed out")
		}
		sess.clearActive(client, readCh)
		return true
	})
	if errInterrupt := exec.InterruptExecutionSession(ctx, sessionID, []byte(`{"type":"response.interrupt","response_id":"r1"}`)); errInterrupt != nil {
		t.Fatalf("completed interrupt = %v", errInterrupt)
	}
	// A transport-order barrier avoids sleeps or assumptions about whether the
	// upstream reader has already consumed a wrongly forwarded interrupt.
	if errWrite := client.WriteMessage(websocket.TextMessage, []byte("barrier")); errWrite != nil {
		t.Fatal(errWrite)
	}
	select {
	case count := <-interruptCount:
		if count != 0 {
			t.Fatalf("forwarded %d interrupt(s) after the response became terminal", count)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("upstream barrier timed out")
	}
}

// Clearing a completed turn must not turn its delayed interrupt into an error,
// or forward it into the next response on the retained upstream socket.
func TestInterruptExecutionSessionAfterTerminal(t *testing.T) {
	for _, terminal := range []string{"response.completed", "response.done", "response.incomplete", "response.failed"} {
		t.Run(terminal, func(t *testing.T) {
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
					`{"type":"` + terminal + `","response":{"id":"r1","output":[]}}`,
				} {
					if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(payload)); errWrite != nil {
						t.Errorf("write: %v", errWrite)
						return
					}
				}
				// The only forwarded interrupt must target the active r2, not r1.
				_, payload, errRead := conn.ReadMessage()
				if errRead != nil {
					return
				}
				if string(payload) != `{"type":"response.interrupt","response_id":"r2"}` {
					t.Errorf("late interrupt contaminated next response: %s", payload)
				}
				if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.incomplete","response":{"id":"r2"}}`)); errWrite != nil {
					t.Errorf("write r2 terminal: %v", errWrite)
				}
				_, _, _ = conn.ReadMessage()
			}))
			defer upstream.Close()
			client, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(upstream.URL, "http"), nil)
			if errDial != nil {
				t.Fatal(errDial)
			}
			exec := NewCodexWebsocketsExecutor(nil)
			exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
			const sessionID = "late-interrupt"
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
			sess.clearActive(client, readCh)
			late := []byte(`{"type":"response.interrupt","response_id":"r1"}`)
			for i := 0; i < 2; i++ {
				if errInterrupt := exec.InterruptExecutionSession(context.Background(), sessionID, late); errInterrupt != nil {
					t.Fatalf("terminal r1 interrupt %d must be idempotent, got %v", i+1, errInterrupt)
				}
			}
			unknown := []byte(`{"type":"response.interrupt","response_id":"unknown"}`)
			if errUnknown := exec.InterruptExecutionSession(context.Background(), sessionID, unknown); !errors.Is(errUnknown, cliproxyexecutor.ErrNoActiveUpstreamWebsocket) {
				t.Fatalf("unknown idle interrupt = %v, want ErrNoActiveUpstreamWebsocket for local HTTP fallback", errUnknown)
			}
			readCh = sess.activate(client)
			if errLate := exec.InterruptExecutionSession(context.Background(), sessionID, late); errLate != nil {
				t.Fatalf("late r1 during r2 = %v", errLate)
			}
			if errActive := exec.InterruptExecutionSession(context.Background(), sessionID, []byte(`{"type":"response.interrupt","response_id":"r2"}`)); errActive != nil {
				t.Fatal(errActive)
			}
			read()
			sess.clearActive(client, readCh)
			for _, id := range []string{"r1", "r2"} {
				if errLate := exec.InterruptExecutionSession(context.Background(), sessionID, []byte(`{"type":"response.interrupt","response_id":"`+id+`"}`)); errLate != nil {
					t.Fatalf("earlier terminal %s = %v", id, errLate)
				}
			}
			if errWrite := client.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "test disconnect")); errWrite != nil {
				t.Fatal(errWrite)
			}
			select {
			case <-sess.upstreamDisconnectCh:
			case <-time.After(3 * time.Second):
				t.Fatal("real disconnect was not reported")
			}
			if errDisconnected := exec.InterruptExecutionSession(context.Background(), sessionID, []byte(`{"type":"response.interrupt","response_id":"r2"}`)); !errors.Is(errDisconnected, cliproxyexecutor.ErrNoActiveUpstreamWebsocket) {
				t.Fatalf("disconnected interrupt = %v, want ErrNoActiveUpstreamWebsocket", errDisconnected)
			}
		})
	}
}
