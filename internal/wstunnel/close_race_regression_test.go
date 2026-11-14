package wstunnel

import (
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestPeerClose_CloseFrameWrittenBeforeSocketClose proves that a close frame
// requested through close() reaches the wire before the transport is
// reclaimed, even when the writeLoop goroutine is descheduled on a busy
// builder.
//
// Regression: on the unpatched peer.go, writeLoop emitted the Close frame
// without holding writeMu while a concurrent finishClose() could reclaim the
// underlying socket before the writer goroutine finished. The client then
// observed an abnormal 1006 closure instead of the expected close code.
func TestPeerClose_CloseFrameWrittenBeforeSocketClose(t *testing.T) {
	for _, tc := range []struct {
		name         string
		closeCode    int
		reason       string
		triggerClose func(p *peer)
	}{
		{
			name:      "policy violation close is delivered",
			closeCode: websocket.ClosePolicyViolation,
			reason:    "api key revoked",
			triggerClose: func(p *peer) {
				p.close(websocket.ClosePolicyViolation, "api key revoked", true)
			},
		},
		{
			name:      "protocol error close is delivered",
			closeCode: websocket.CloseProtocolError,
			reason:    "",
			triggerClose: func(p *peer) {
				p.close(websocket.CloseProtocolError, "", true)
			},
		},
		{
			name:      "server shutdown close is delivered",
			closeCode: websocket.CloseGoingAway,
			reason:    "server shutting down",
			triggerClose: func(p *peer) {
				p.close(websocket.CloseGoingAway, "server shutting down", true)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := NewHub(Options{PingInterval: 0})
			hub.UpdateAllowedAPIKeys([]string{"key"})
			srv := httptest.NewServer(hub)
			t.Cleanup(srv.Close)
			t.Cleanup(hub.Close)

			client := dialRole(t, srv.URL, "key", RoleClient, "device", "Device")
			_ = readTextJSONFrame(t, client)

			p := lookupTunnelPeer(t, hub, "key", "device")

			// Trigger the close from the hub side. The close path spawns
			// awaitClose which calls finishClose on timeout; on the unpatched
			// code finishClose could race the writeLoop close frame and reset
			// the socket first.
			tc.triggerClose(p)

			// The client must see the peer's close frame with the requested
			// code, not a bare TCP reset.
			_, _, errRead := readTunnelTestMessage(client)
			var closeErr *websocket.CloseError
			if !errors.As(errRead, &closeErr) {
				t.Fatalf("close = %v, want CloseError with code %d", errRead, tc.closeCode)
			}
			if closeErr.Code != tc.closeCode {
				t.Fatalf("close code = %d, want %d", closeErr.Code, tc.closeCode)
			}
		})
	}
}

// TestPeerClose_ProtocolErrorReclaimsPermanentlyBlockedWriter drives the
// busy-writer branch of onProtocolError: an in-flight data write parks inside
// the gated transport holding writeMu, and the protocol-error callback must
// still reclaim the socket (unblocking the writer) without deadlocking on a
// close frame that can never be flushed.
//
// Regression coverage for issue #7504: a permanently blocked writer cannot
// deliver 1002, so the bounded wait must give up and close the transport.
func TestPeerClose_ProtocolErrorReclaimsPermanentlyBlockedWriter(t *testing.T) {
	forceClose := make(chan time.Time)
	hub := NewHub(Options{PingInterval: 0})
	hub.testForceClose = forceClose
	t.Cleanup(func() { close(forceClose) })
	hub.UpdateAllowedAPIKeys([]string{"key"})
	srv := newGatedTunnelServer(t, hub)

	client := dialRole(t, srv.URL, "key", RoleClient, "device", "Device")
	_ = readTextJSONFrame(t, client)
	p := lookupTunnelPeer(t, hub, "key", "device")
	guarded := p.conn.UnderlyingConn().(*deadlineGuardConn)
	transport := guarded.Conn.(*gatedTunnelConn)

	// Arm the transport so the in-flight data write stays parked inside the
	// socket Write call holding writeMu.
	transport.arm()
	if !p.tryEnqueueData([]byte("in-flight write holds writeMu")) {
		t.Fatal("could not enqueue active write")
	}
	waitTunnelSignal(t, transport.blocked, "active write holding writeMu")
	if p.writeMu.TryLock() {
		p.writeMu.Unlock()
		t.Fatal("expected writeMu to be held by the active write")
	}

	// Fire the protocol-error callback while writeMu is genuinely busy.
	guarded.onProtocolError()

	// The bounded wait gives up, reclaims the raw socket, unblocks the gated
	// write, and lets writeLoop exit.
	waitTunnelSignal(t, transport.closed, "raw transport close")
	waitTunnelSignal(t, p.writerDone, "blocked writer exit")
	waitTunnelSignal(t, p.done, "socket reclamation")
}

// TestPeerClose_ProtocolErrorDeliversCloseAfterWriterReleases verifies that
// when the busy writer is transient (the write finishes promptly), the
// protocol-error close frame is emitted on the wire before the transport is
// reclaimed — the 1002-vs-1006 ordering that issue #7504 reported as flaky.
func TestPeerClose_ProtocolErrorDeliversCloseAfterWriterReleases(t *testing.T) {
	forceClose := make(chan time.Time)
	hub := NewHub(Options{PingInterval: 0})
	hub.testForceClose = forceClose
	t.Cleanup(func() { close(forceClose) })
	hub.UpdateAllowedAPIKeys([]string{"key"})
	srv := newGatedTunnelServer(t, hub)

	client := dialRole(t, srv.URL, "key", RoleClient, "device", "Device")
	_ = readTextJSONFrame(t, client)
	p := lookupTunnelPeer(t, hub, "key", "device")
	guarded := p.conn.UnderlyingConn().(*deadlineGuardConn)
	transport := guarded.Conn.(*gatedTunnelConn)

	// Park the writer, fire the busy-writer protocol-error path, then release
	// the gate so writeLoop can still flush the queued close frame inside the
	// bounded grace window.
	transport.arm()
	if !p.tryEnqueueData([]byte("transient busy write")) {
		t.Fatal("could not enqueue active write")
	}
	waitTunnelSignal(t, transport.blocked, "active write holding writeMu")
	guarded.onProtocolError()
	close(transport.gate)

	// The released in-flight data frame is flushed first; the protocol-error
	// close frame must follow it before the transport is reclaimed.
	_, _, errRead := readTunnelTestMessage(client)
	if errRead != nil {
		t.Fatalf("expected the released data frame first, got %v", errRead)
	}
	_, _, errRead = readTunnelTestMessage(client)
	var closeErr *websocket.CloseError
	if !errors.As(errRead, &closeErr) || closeErr.Code != websocket.CloseProtocolError {
		t.Fatalf("close = %v, want CloseError code %d", errRead, websocket.CloseProtocolError)
	}
	waitTunnelSignal(t, p.writerDone, "writer exit")
	waitTunnelSignal(t, p.done, "socket reclamation")
}
