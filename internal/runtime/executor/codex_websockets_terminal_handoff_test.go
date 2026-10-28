package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// Window (a): once a turn hands its terminal event downstream it no longer takes
// interrupts, so a late interrupt for another response is not written upstream.
// The test holds the terminal chunk unread, which pins the stream goroutine at the
// hand-off, before its deferred teardown.
func TestInterruptExecutionSessionAfterTerminalHandoff(t *testing.T) {
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	upstreamReads := make(chan string, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()
		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			return
		}
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r1"}}`)); errWrite != nil {
			t.Errorf("write created: %v", errWrite)
			return
		}
		<-release
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`)); errWrite != nil {
			t.Errorf("write completed: %v", errWrite)
			return
		}
		for {
			_, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				return
			}
			upstreamReads <- string(payload)
		}
	}))
	defer upstream.Close()
	defer releaseOnce()
	exec := NewCodexWebsocketsExecutor(&config.Config{})
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	const sessionID = "terminal-handoff"
	defer exec.CloseExecutionSession(sessionID)
	auth := &cliproxyauth.Auth{ID: "terminal-handoff-auth", Provider: "codex", Attributes: map[string]string{"api_key": "test-key", "base_url": upstream.URL}}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	result, errStream := exec.ExecuteStream(cliproxyexecutor.WithDownstreamWebsocket(ctx), auth, cliproxyexecutor.Request{Model: "model-a", Payload: []byte(`{"model":"model-a","input":[]}`)}, cliproxyexecutor.Options{
		Stream: true, SourceFormat: sdktranslator.FormatOpenAIResponse,
		Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: sessionID},
	})
	if errStream != nil {
		t.Fatal(errStream)
	}
	next := func() string {
		t.Helper()
		select {
		case chunk, ok := <-result.Chunks:
			if !ok || chunk.Err != nil {
				t.Fatalf("stream ended early: ok=%v err=%v", ok, chunk.Err)
			}
			return gjson.GetBytes(chunk.Payload, "type").String()
		case <-time.After(5 * time.Second):
			t.Fatal("stream chunk timed out")
			return ""
		}
	}
	if got := next(); got != "response.created" {
		t.Fatalf("first event = %s, want response.created", got)
	}
	sess := exec.getOrCreateSession(sessionID)
	sess.connMu.Lock()
	conn := sess.conn
	sess.connMu.Unlock()
	_, activeDone := sess.activeForConn(conn)
	if activeDone == nil {
		t.Fatal("turn is not active while its response is in flight")
	}
	releaseOnce()
	select {
	case <-activeDone:
	case <-time.After(3 * time.Second):
		t.Error("turn still takes interrupts while its terminal event is handed downstream")
	}
	late := []byte(`{"type":"response.interrupt","response_id":"unknown"}`)
	if errInterrupt := exec.InterruptExecutionSession(context.Background(), sessionID, late); !errors.Is(errInterrupt, cliproxyexecutor.ErrNoActiveUpstreamWebsocket) {
		t.Errorf("late interrupt = %v, want ErrNoActiveUpstreamWebsocket", errInterrupt)
	}
	// A transport-order barrier shows whether the interrupt was written first.
	if errWrite := writeCodexWebsocketMessage(sess, conn, []byte("barrier")); errWrite != nil {
		t.Fatal(errWrite)
	}
	select {
	case got := <-upstreamReads:
		if got != "barrier" {
			t.Errorf("late interrupt reached upstream: %s", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("upstream barrier timed out")
	}
	if got := next(); got != "response.completed" {
		t.Fatalf("terminal event = %s, want response.completed", got)
	}
	for range result.Chunks {
	}
}
