package wstunnel

import (
	"bufio"
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestPeerControl_ProtocolErrorInterruptsActiveWriteImmediately(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"invalid opcode", []byte{0x83, 0x80}},
		{"reserved close code", maskedTunnelFrame(0x88, []byte{0x03, 0xed}, 0)},
		{"one-byte close", maskedTunnelFrame(0x88, []byte{0x03}, 0)},
		{"invalid UTF-8 close reason", maskedTunnelFrame(0x88, []byte{0x03, 0xe8, 0xff}, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forceClose := make(chan time.Time)
			t.Cleanup(func() { close(forceClose) })
			hub := NewHub(Options{PingInterval: time.Hour})
			// Never advance the grace reaper: an active write must be interrupted now.
			hub.testForceClose = forceClose
			inspected := make(chan struct{})
			resumeRead := make(chan struct{})
			t.Cleanup(func() { close(resumeRead) })
			hub.testBeforeProtocolErrorReturn = func() {
				close(inspected)
				<-resumeRead
			}
			unregistered := make(chan struct{}, 1)
			hub.testOnPeerUnregistered = func(_, uuid string) {
				if uuid == "server" {
					unregistered <- struct{}{}
				}
			}
			hub.UpdateAllowedAPIKeys([]string{"key"})
			srv := httptest.NewUnstartedServer(hub)
			srv.Listener = deadlineTrackingTunnelListener{gatedTunnelListener{srv.Listener}}
			srv.Start()
			t.Cleanup(srv.Close)
			t.Cleanup(hub.Close)
			observer := dialRole(t, srv.URL, "key", RoleClient, "observer", "Observer")
			_ = readTextJSONFrame(t, observer)
			stalled := dialRole(t, srv.URL, "key", RoleServer, "server", "Server")
			_ = readTextJSONFrame(t, observer)
			p := lookupTunnelPeer(t, hub, "key", "server")
			guard := p.conn.UnderlyingConn().(*deadlineGuardConn)
			transport := guard.Conn.(*deadlineTrackingTunnelConn)
			transport.monitoring.Store(true)
			gated := transport.Conn.(*gatedTunnelConn)
			gated.arm()
			if !p.tryEnqueueData([]byte("active WriteMessage holds Gorilla's write token")) {
				t.Fatal("could not enqueue active write")
			}
			waitTunnelSignal(t, gated.blocked, "WriteMessage blocked with Gorilla write token held")
			if p.writeMu.TryLock() {
				p.writeMu.Unlock()
				t.Fatal("transport write is not active")
			}

			if n, errWrite := stalled.UnderlyingConn().Write(tc.raw); errWrite != nil || n != len(tc.raw) {
				t.Fatalf("write malformed frame: bytes=%d, error=%v", n, errWrite)
			}
			for _, signal := range []struct {
				ch   <-chan struct{}
				name string
			}{
				{inspected, "fatal frame intercepted inside guarded Read"},
				{unregistered, "peer unregistration"},
				{gated.closed, "raw transport close unblocking WriteMessage"},
				{p.done, "raw socket reclamation"},
				{p.writerDone, "blocked writer exit"},
			} {
				waitTunnelSignal(t, signal.ch, signal.name)
			}
			offline := readTextJSONFrame(t, observer)
			if offline.Get("type").String() != ControlTypeServerOffline || offline.Get("server.uuid").String() != "server" {
				t.Fatalf("unexpected server_offline: %s", offline.Raw)
			}
			replacement := dialRole(t, srv.URL, "key", RoleServer, "server", "Replacement")
			if errWrite := replacement.WriteMessage(websocket.BinaryMessage, []byte{ProtocolVersion1}); errWrite != nil {
				t.Fatalf("replacement write: %v", errWrite)
			}
			if got := readTextJSONFrame(t, replacement); got.Get("code").String() != ErrCodeInvalidDataFrame {
				t.Fatalf("replacement is not usable: %s", got.Raw)
			}
			// Guarded Read has not returned to Gorilla, and neither the write gate
			// nor the grace reaper was released. Only raw Close can unblock the writer.
			select {
			case <-forceClose:
				t.Fatal("grace reaper fired before replacement handshake completed")
			default:
			}
			select {
			case <-gated.gate:
				t.Fatal("blocked write gate was released before replacement handshake completed")
			default:
			}
			if lookupTunnelPeer(t, hub, "key", "server") == p {
				t.Fatal("old peer remains registered")
			}
			if p.queuedBytes.Load() != 0 {
				t.Fatal("interrupted active write leaked its byte reservation")
			}
			assertNoFiniteTunnelDeadlines(t, transport)
		})
	}
}

type bufferedTunnelHijacker struct {
	http.ResponseWriter
	conn net.Conn
	rw   *bufio.ReadWriter
}

func (w bufferedTunnelHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, w.rw, nil
}

func TestDeadlineGuardUpgrade_RejectsBufferedPipelinedFramesBeforeHTTP101(t *testing.T) {
	for _, raw := range [][]byte{
		maskedTunnelFrame(0x82, []byte("valid but premature"), 0),
		{0x83, 0x80, 0, 0, 0, 0},
	} {
		local, remote := net.Pipe()
		for _, conn := range []net.Conn{local, remote} {
			t.Cleanup(func() {
				if errClose := conn.Close(); errClose != nil {
					t.Errorf("close pipelined transport: %v", errClose)
				}
			})
		}
		reader := bufio.NewReader(bytes.NewReader(raw))
		if _, errPeek := reader.Peek(len(raw)); errPeek != nil {
			t.Fatal(errPeek)
		}
		writer := &deadlineGuardResponseWriter{ResponseWriter: bufferedTunnelHijacker{
			ResponseWriter: httptest.NewRecorder(),
			conn:           local,
			rw:             bufio.NewReadWriter(reader, bufio.NewWriter(local)),
		}}
		req := httptest.NewRequest(http.MethodGet, "http://example.test/v1/tunnel", nil)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Sec-WebSocket-Version", "13")
		req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		hub := NewHub(Options{})
		conn, errUpgrade := hub.upgrader.Upgrade(writer, req, nil)
		if errUpgrade == nil {
			if errClose := conn.Close(); errClose != nil {
				t.Errorf("close unexpected upgrade: %v", errClose)
			}
			t.Fatal("buffered client data bypassed the guarded reader")
		}
		if !strings.Contains(errUpgrade.Error(), "client sent data before handshake is complete") {
			t.Fatalf("unexpected buffered-handshake rejection: %v", errUpgrade)
		}
		if hub.upgrader.ReadBufferSize == 0 || writer.conn.postUpgrade.Load() {
			t.Fatal("accepted post-upgrade data could bypass the guard")
		}
	}
}
