package openai

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// terminalCloseHangGuard only fails a test that would otherwise hang; no ordering depends on it.
const terminalCloseHangGuard = 5 * time.Second

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
	_ = conn.SetReadDeadline(time.Now().Add(terminalCloseHangGuard))
	_, frame, errRead := conn.ReadMessage()
	<-served
	return frame, errRead
}

// closeWaits reports each time a close on w starts waiting on another write.
func closeWaits(w *responsesWebsocketWriter) <-chan struct{} {
	waits := make(chan struct{}, 4)
	w.onCloseWait = func() { waits <- struct{}{} }
	return waits
}

func awaitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(terminalCloseHangGuard):
		t.Errorf("timed out waiting for %s", what)
	}
}

// releaseWhenCloseWaits releases a held writeMu once a close is waiting behind it.
func releaseWhenCloseWaits(t *testing.T, w *responsesWebsocketWriter, waits <-chan struct{}) {
	go func() {
		awaitSignal(t, waits, "the terminal close to wait for the active write")
		w.writeMu.Unlock()
	}()
}

// A terminal error that lands while another write is finishing still reaches the client,
// instead of the socket closing under it with no reason (1006).
func TestResponsesWebsocketTerminalErrorFrameWaitsForAFinishingWriter(t *testing.T) {
	payload := []byte(`{"type":"error","status":429}`)
	frame, errRead := terminalCloseRead(t, func(w *responsesWebsocketWriter, _ *websocket.Conn) {
		waits := closeWaits(w)
		w.writeMu.Lock()
		releaseWhenCloseWaits(t, w, waits)
		_, _ = w.closeWithPayload(payload)
	})
	if errRead != nil || string(frame) != string(payload) {
		t.Fatalf("client read %q, err=%v; want the terminal error frame", frame, errRead)
	}
}

// The replay-required close (1012) is not degraded to a bare disconnect behind a finishing write.
func TestResponsesWebsocketReplayCloseWaitsForAFinishingWriter(t *testing.T) {
	_, errRead := terminalCloseRead(t, func(w *responsesWebsocketWriter, _ *websocket.Conn) {
		waits := closeWaits(w)
		w.writeMu.Lock()
		releaseWhenCloseWaits(t, w, waits)
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
		waits := closeWaits(w)
		w.writeMu.Lock()
		terminalDone := make(chan struct{})
		go func() {
			defer close(terminalDone)
			_, _ = w.closeWithPayload(payload)
		}()
		awaitSignal(t, waits, "the terminal close to wait for the active write")
		teardownDone := make(chan struct{})
		go func() {
			defer close(teardownDone)
			_, _ = w.closeWithoutError()
			_ = conn.Close()
		}()
		awaitSignal(t, waits, "the teardown to wait for the terminal close")
		w.writeMu.Unlock()
		awaitSignal(t, teardownDone, "the teardown")
		awaitSignal(t, terminalDone, "the terminal close")
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
	if took < responsesWebsocketCloseWait || took > responsesWebsocketCloseWait+time.Second {
		t.Fatalf("close behind a blocked writer took %s; want about %s", took, responsesWebsocketCloseWait)
	}
}

// blockingTerminalCloseConn returns a server socket whose writes can be made to block,
// like a client that stopped reading.
func blockingTerminalCloseConn(t *testing.T) (*websocket.Conn, *blockingWebsocketWriteConn) {
	t.Helper()
	clientRaw, serverRaw := net.Pipe()
	t.Cleanup(func() { _ = clientRaw.Close() })
	blocking := newBlockingWebsocketWriteConn(serverRaw)
	t.Cleanup(func() { _ = blocking.Close() })

	type upgraded struct {
		conn *websocket.Conn
		err  error
	}
	result := make(chan upgraded, 1)
	go func() {
		req, errReq := http.NewRequest(http.MethodGet, "http://example.com/v1/responses", nil)
		if errReq != nil {
			result <- upgraded{err: errReq}
			return
		}
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Sec-WebSocket-Version", "13")
		req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		conn, errUpgrade := responsesWebsocketUpgrader.Upgrade(newHijackableWebsocketResponseWriter(blocking), req, nil)
		result <- upgraded{conn: conn, err: errUpgrade}
	}()
	// Drain the HTTP 101 Switching Protocols response on the client side of the pipe.
	buf := make([]byte, 512)
	if _, errRead := clientRaw.Read(buf); errRead != nil {
		t.Fatalf("read upgrade response: %v", errRead)
	}
	got := <-result
	if got.err != nil {
		t.Fatalf("upgrade websocket: %v", got.err)
	}
	return got.conn, blocking
}

// A terminal frame stuck on a client that stopped reading cannot hold the session open: the
// handler's teardown (closeWithoutError, the socket, then waiting for the disconnect watcher)
// still closes the socket and unblocks the write, without any network deadline.
func TestResponsesWebsocketTeardownAbortsAStuckTerminalWrite(t *testing.T) {
	cases := map[string]func(*responsesWebsocketWriter){
		"closeWithPayload": func(w *responsesWebsocketWriter) {
			_, _ = w.closeWithPayload([]byte(`{"type":"error","status":429}`))
		},
		"closeForUpstreamError": func(w *responsesWebsocketWriter) {
			_, _ = w.closeForUpstreamError(cliproxyexecutor.NewUpstreamWebsocketReplayRequiredError())
		},
	}
	for name, terminalClose := range cases {
		t.Run(name, func(t *testing.T) {
			conn, blocking := blockingTerminalCloseConn(t)
			w := newResponsesWebsocketWriter(conn)
			blocking.blockWrites.Store(true)

			terminalDone := make(chan struct{})
			go func() {
				defer close(terminalDone)
				terminalClose(w)
			}()
			awaitSignal(t, blocking.writeBlock, "the terminal frame to block in the network write")

			teardownDone := make(chan struct{})
			go func() {
				defer close(teardownDone)
				_, _ = w.closeWithoutError()
				_ = conn.Close()
				<-terminalDone
			}()
			select {
			case <-teardownDone:
			case <-time.After(terminalCloseHangGuard):
				_ = blocking.Close()
				t.Fatal("teardown hung behind a stuck terminal write")
			}
		})
	}
}
