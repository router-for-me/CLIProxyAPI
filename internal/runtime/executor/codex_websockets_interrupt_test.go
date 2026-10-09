package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestInterruptExecutionSessionRequiresActiveRead(t *testing.T) {
	received := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			return
		}
		received <- payload
	}))
	defer upstream.Close()

	client, _, errDial := websocket.DefaultDialer.Dial("ws"+upstream.URL[len("http"):], nil)
	if errDial != nil {
		t.Fatal(errDial)
	}
	defer func() { _ = client.Close() }()

	const sessionID = "interrupt-requires-active-read"
	executor := NewCodexWebsocketsExecutor(nil)
	defer executor.CloseExecutionSession(sessionID)
	sess := executor.getOrCreateSession(sessionID)
	sess.connMu.Lock()
	sess.conn = client
	sess.authID = "auth"
	sess.wsURL = upstream.URL
	sess.connMu.Unlock()

	interrupt := []byte(`{"type":"response.interrupt","response_id":"r1","mode":"discard_partial_items"}`)
	errIdle := executor.InterruptExecutionSession(context.Background(), sessionID, interrupt)
	if !errors.Is(errIdle, cliproxyexecutor.ErrNoActiveUpstreamWebsocket) {
		t.Fatalf("idle socket error = %v, want ErrNoActiveUpstreamWebsocket", errIdle)
	}
	select {
	case payload := <-received:
		t.Fatalf("idle socket was written: %s", payload)
	case <-time.After(200 * time.Millisecond):
	}

	readCh := sess.activate(client)
	defer sess.clearActive(client, readCh)
	if errActive := executor.InterruptExecutionSession(context.Background(), sessionID, interrupt); errActive != nil {
		t.Fatal(errActive)
	}
	select {
	case payload := <-received:
		if !bytes.Equal(payload, interrupt) {
			t.Fatalf("active interrupt = %s", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("active socket did not receive the interrupt")
	}
	terminal := []byte(`{"response":{"id":"r1","incomplete_details":{"reason":"interrupted"}}}`)
	if !sess.awaitMatchingInterrupt(context.Background(), client, readCh, terminal) {
		t.Fatal("successful matching write did not authorize the interruption")
	}
	for _, mismatch := range [][]byte{
		bytes.Replace(terminal, []byte(`"r1"`), []byte(`"r2"`), 1),
		bytes.Replace(terminal, []byte(`"interrupted"`), []byte(`"max_output_tokens"`), 1),
	} {
		if sess.awaitMatchingInterrupt(context.Background(), client, readCh, mismatch) {
			t.Fatalf("unmatched terminal was accepted: %s", mismatch)
		}
	}
	_ = client.Close()
	if errWrite := executor.InterruptExecutionSession(context.Background(), sessionID, interrupt); errWrite == nil {
		t.Fatal("interrupt write to a closed connection must fail")
	}
	if sess.awaitMatchingInterrupt(context.Background(), client, readCh, terminal) {
		t.Fatal("failed write authorized the interruption")
	}
}

func TestCodexWebsocketEmptyInterruptedResponseRequiresMatchingInterrupt(t *testing.T) {
	for _, tc := range []struct{ name, mode, interruptEvent string }{
		{"execute/unrequested", "execute", ""},
		{"execute/matching", "execute", "response.created"},
		{"stream/unrequested", "stream", ""},
		{"stream/matching", "stream", "response.created"},
		{"buffered_stream/unrequested", "buffered_stream", ""},
		{"buffered_stream/matching", "buffered_stream", "response.created"},
		{"stream/late", "stream", "response.incomplete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			interrupt := []byte(`{"type":"response.interrupt","response_id":"resp_1","mode":"discard_partial_items","extension":"preserved"}`)
			terminal := []byte(`{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"interrupted"},"output":[],"usage":{"input_tokens":10,"output_tokens":0,"total_tokens":10}}}`)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if errUpgrade != nil {
					t.Errorf("upgrade: %v", errUpgrade)
					return
				}
				defer func() { _ = conn.Close() }()
				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					t.Errorf("read create: %v", errRead)
					return
				}
				for _, payload := range [][]byte{[]byte(codexCreatedEvent), terminal} {
					if errWrite := conn.WriteMessage(websocket.TextMessage, payload); errWrite != nil {
						t.Errorf("write event: %v", errWrite)
						return
					}
					if tc.interruptEvent != "" && gjson.GetBytes(payload, "type").String() == tc.interruptEvent {
						_, got, errRead := conn.ReadMessage()
						if errRead != nil || !bytes.Equal(got, interrupt) {
							t.Errorf("interrupt = %s, error = %v", got, errRead)
							return
						}
					}
				}
				_, _, _ = conn.ReadMessage()
			}))
			defer upstream.Close()

			const sessionID = "empty-interrupt"
			executor := NewCodexWebsocketsExecutor(codexBufferingConfig(tc.mode == "buffered_stream"))
			executor.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
			defer executor.CloseExecutionSession(sessionID)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx = cliproxyexecutor.WithDownstreamWebsocket(ctx)
			req, opts := codexWebsocketRequest()
			opts.Stream = tc.mode != "execute"
			opts.Metadata = map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: sessionID}
			interruptCalled := false
			opts.WebSocketResponseObserver = func(_ context.Context, event cliproxyexecutor.WebSocketResponseEvent) {
				if event.EventType == tc.interruptEvent {
					interruptCalled = true
					if errInterrupt := executor.InterruptExecutionSession(ctx, sessionID, interrupt); errInterrupt != nil {
						t.Errorf("interrupt: %v", errInterrupt)
					}
				}
			}
			var err error
			var payload string
			if opts.Stream {
				var result *cliproxyexecutor.StreamResult
				result, err = executor.ExecuteStream(ctx, codexTestAuth(upstream.URL), req, opts)
				if err == nil {
					payload, err = drainChunks(result)
				}
			} else {
				var result cliproxyexecutor.Response
				result, err = executor.Execute(ctx, codexTestAuth(upstream.URL), req, opts)
				payload = string(result.Payload)
			}
			if interruptCalled != (tc.interruptEvent != "") {
				t.Fatal("did not exercise the expected interrupt path")
			}
			if tc.interruptEvent == "response.created" {
				if err != nil || !strings.Contains(payload, `"reason":"interrupted"`) || !strings.Contains(payload, `"output_tokens":0`) {
					t.Fatalf("matching interruption: payload = %s, error = %v", payload, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), helps.CodexEmptyIncompleteStreamMessage) {
				t.Fatalf("unmatched interruption error = %v, want empty-incomplete failure", err)
			}
		})
	}
}

func TestCodexWebsocketInterruptAcknowledgementWaitsForWrite(t *testing.T) {
	for _, tc := range []struct {
		name     string
		writeErr error
	}{
		{name: "success"},
		{name: "failure", writeErr: errors.New("write failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				conn := &websocket.Conn{}
				sess := &codexWebsocketSession{}
				readCh := sess.activate(conn)
				defer sess.clearActive(conn, readCh)
				interrupt := sess.beginInterrupt(conn, readCh, []byte(`{"type":"response.interrupt","response_id":"r1"}`))
				accepted := make(chan bool, 1)
				go func() {
					accepted <- sess.awaitMatchingInterrupt(context.Background(), conn, readCh, []byte(`{"response":{"id":"r1","incomplete_details":{"reason":"interrupted"}}}`))
				}()
				synctest.Wait()
				select {
				case got := <-accepted:
					t.Fatalf("acknowledgement returned %v before the interrupt write completed", got)
				default:
				}
				interrupt.err = tc.writeErr
				close(interrupt.done)
				if got := <-accepted; got != (tc.writeErr == nil) {
					t.Fatalf("accepted = %v, write error = %v", got, tc.writeErr)
				}
			})
		})
	}
}

func TestCodexWebsocketInterruptDoesNotCrossTurns(t *testing.T) {
	for _, replaceConnection := range []bool{false, true} {
		t.Run(fmt.Sprintf("replace_connection=%v", replaceConnection), func(t *testing.T) {
			conn := &websocket.Conn{}
			sess := &codexWebsocketSession{}
			readCh := sess.activate(conn)
			interrupt := sess.beginInterrupt(conn, readCh, []byte(`{"type":"response.interrupt","response_id":"r1"}`))
			sess.clearActive(conn, readCh)
			if replaceConnection {
				conn = &websocket.Conn{}
			}
			readCh = sess.activate(conn)
			defer sess.clearActive(conn, readCh)
			// A late successful write from the previous turn must not authorize this turn.
			close(interrupt.done)
			if sess.awaitMatchingInterrupt(context.Background(), conn, readCh, []byte(`{"response":{"id":"r1","incomplete_details":{"reason":"interrupted"}}}`)) {
				t.Fatal("previous turn's interrupt exempted the current turn")
			}
		})
	}
}
