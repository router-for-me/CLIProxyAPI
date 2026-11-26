package openai

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// terminalCloseRead runs serve on one server socket and returns the client's first read.
func terminalCloseRead(t *testing.T, serve func(*responsesWebsocketWriter, *websocket.Conn)) ([]byte, error) {
	t.Helper()
	served := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(served)
		conn, errUpgrade := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Error(errUpgrade)
			return
		}
		serve(newResponsesWebsocketWriter(conn), conn)
	}))
	defer server.Close()
	conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if errDial != nil {
		t.Fatalf("dial: %v", errDial)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, frame, errRead := conn.ReadMessage()
	<-served
	return frame, errRead
}

// A terminal error that lands while another write is finishing still reaches the client,
// instead of the socket closing under it with no reason (1006).
func TestResponsesWebsocketTerminalErrorFrameWaitsForAFinishingWriter(t *testing.T) {
	payload := []byte(`{"type":"error","status":429}`)
	frame, errRead := terminalCloseRead(t, func(w *responsesWebsocketWriter, _ *websocket.Conn) {
		w.writeMu.Lock()
		time.AfterFunc(20*time.Millisecond, w.writeMu.Unlock)
		_, _ = w.closeWithPayload(payload)
	})
	if errRead != nil || string(frame) != string(payload) {
		t.Fatalf("client read %q, err=%v; want the terminal error frame", frame, errRead)
	}
}

// The replay-required close (1012) is not degraded to a bare disconnect behind a finishing write.
func TestResponsesWebsocketReplayCloseWaitsForAFinishingWriter(t *testing.T) {
	_, errRead := terminalCloseRead(t, func(w *responsesWebsocketWriter, _ *websocket.Conn) {
		w.writeMu.Lock()
		time.AfterFunc(20*time.Millisecond, w.writeMu.Unlock)
		_, _ = w.closeForUpstreamError(cliproxyexecutor.NewUpstreamWebsocketReplayRequiredError())
	})
	var closeErr *websocket.CloseError
	if !errors.As(errRead, &closeErr) || closeErr.Code != websocket.CloseServiceRestart {
		t.Fatalf("client read err=%v; want close %d", errRead, websocket.CloseServiceRestart)
	}
}

// The session's own teardown (closeWithoutError, then the socket) waits for a terminal frame
// another goroutine is already writing, instead of closing the socket under it.
func TestResponsesWebsocketSessionTeardownWaitsForAnInFlightTerminalFrame(t *testing.T) {
	payload := []byte(`{"type":"error","status":429}`)
	frame, errRead := terminalCloseRead(t, func(w *responsesWebsocketWriter, conn *websocket.Conn) {
		w.writeMu.Lock()
		go func() { _, _ = w.closeWithPayload(payload) }()
		for !w.closing.Load() {
			runtime.Gosched()
		}
		time.AfterFunc(20*time.Millisecond, w.writeMu.Unlock)
		_, _ = w.closeWithoutError()
		_ = conn.Close()
	})
	if errRead != nil || string(frame) != string(payload) {
		t.Fatalf("client read %q, err=%v; want the terminal error frame", frame, errRead)
	}
}

// A writer that never finishes is still cut off: the close gives up within its bound.
func TestResponsesWebsocketTerminalCloseStillCutsOffABlockedWriter(t *testing.T) {
	var took time.Duration
	_, errRead := terminalCloseRead(t, func(w *responsesWebsocketWriter, _ *websocket.Conn) {
		w.writeMu.Lock()
		defer w.writeMu.Unlock()
		started := time.Now()
		_, _ = w.closeWithPayload([]byte(`{"type":"error","status":429}`))
		took = time.Since(started)
	})
	if errRead == nil {
		t.Fatal("client read a frame behind a blocked writer; want the connection closed")
	}
	if took < responsesWebsocketCloseLockWait || took > responsesWebsocketCloseLockWait+time.Second {
		t.Fatalf("close behind a blocked writer took %s; want about %s", took, responsesWebsocketCloseLockWait)
	}
}
