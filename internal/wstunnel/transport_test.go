package wstunnel

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestRawTransportConn_UnwrapsLayersAndRejectsNilOrCycles(t *testing.T) {
	local, remote := net.Pipe()
	for _, conn := range []net.Conn{local, remote} {
		t.Cleanup(func() {
			if errClose := conn.Close(); errClose != nil {
				t.Errorf("close test transport: %v", errClose)
			}
		})
	}
	var typedNil *deadlineGuardConn
	selfCycle := &deadlineGuardConn{}
	selfCycle.Conn = selfCycle
	first := &deadlineGuardConn{}
	second := &deadlineGuardConn{Conn: first}
	first.Conn = second
	tlsConn := tls.Client(&deadlineGuardConn{Conn: local}, &tls.Config{})
	for _, tc := range []struct {
		name string
		conn net.Conn
		want net.Conn
	}{
		{"raw transport", local, local},
		{"nested wrappers and TLS", &deadlineGuardConn{Conn: &deadlineGuardConn{Conn: tlsConn}}, local},
		{"nil", nil, nil},
		{"typed nil", typedNil, nil},
		{"nil wrapped connection", &deadlineGuardConn{}, nil},
		{"typed nil wrapped connection", &deadlineGuardConn{Conn: typedNil}, nil},
		{"self cycle", selfCycle, nil},
		{"multi-wrapper cycle", first, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := rawTransportConn(tc.conn); got != tc.want {
				t.Fatalf("rawTransportConn() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDeadlineGuardConn_OnlyHandshakeAllowsFiniteDeadlines(t *testing.T) {
	local, remote := net.Pipe()
	for _, conn := range []net.Conn{local, remote} {
		t.Cleanup(func() {
			if errClose := conn.Close(); errClose != nil {
				t.Errorf("close test transport: %v", errClose)
			}
		})
	}
	transport := &deadlineTrackingTunnelConn{Conn: local}
	transport.monitoring.Store(true)
	guard := &deadlineGuardConn{Conn: transport}
	methods := []struct {
		name   string
		set    func(time.Time) error
		finite *atomic.Int64
	}{
		{"SetDeadline", guard.SetDeadline, &transport.deadlines},
		{"SetReadDeadline", guard.SetReadDeadline, &transport.readDeadlines},
		{"SetWriteDeadline", guard.SetWriteDeadline, &transport.writeDeadlines},
	}
	finite := time.Now().Add(time.Hour)
	for _, method := range methods {
		if errSet := method.set(finite); errSet != nil || method.finite.Load() != 1 {
			t.Fatalf("%s did not forward handshake deadline: %v", method.name, errSet)
		}
	}
	guard.postUpgrade.Store(true)
	for _, method := range methods {
		if errSet := method.set(finite); errSet != nil || method.finite.Load() != 1 {
			t.Fatalf("%s forwarded finite upgraded deadline: %v", method.name, errSet)
		}
		if errSet := method.set(time.Time{}); errSet != nil {
			t.Fatalf("%s failed to clear deadline: %v", method.name, errSet)
		}
	}
	if calls := transport.deadlineCalls.Load(); calls != 6 {
		t.Fatalf("underlying deadline calls = %d, want 3 handshake + 3 zero deadlines", calls)
	}
}

func TestPeerControl_OversizedMessagesUseWriterCloseWithoutFiniteDeadlines(t *testing.T) {
	for _, tc := range []struct {
		name        string
		messageType int
		size        int
		reason      string
	}{
		{"binary", websocket.BinaryMessage, MaxInboundFrameBytes + 1, "binary frame exceeds maximum allowed size"},
		{"text", websocket.TextMessage, MaxControlFrameBytes + 1, "control frame exceeds 4096 bytes"},
		{"text beyond binary limit", websocket.TextMessage, MaxInboundFrameBytes + 1, "control frame exceeds 4096 bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := NewHub(Options{PingInterval: time.Hour})
			hub.UpdateAllowedAPIKeys([]string{"key"})
			srv := newDeadlineTrackingTunnelServer(t, hub)
			client := dialRole(t, srv.URL, "key", RoleClient, "device", "Device")
			_ = readTextJSONFrame(t, client)
			p := lookupTunnelPeer(t, hub, "key", "device")
			transport := p.conn.UnderlyingConn().(*deadlineGuardConn).Conn.(*deadlineTrackingTunnelConn)
			transport.monitoring.Store(true)
			client.SetCloseHandler(func(int, string) error { return nil })
			written := make(chan struct{})
			go func() {
				// A close may interrupt the oversized sender before it finishes writing.
				_ = client.WriteMessage(tc.messageType, make([]byte, tc.size))
				close(written)
			}()
			_, _, errRead := readTunnelTestMessage(client)
			var closeErr *websocket.CloseError
			if !errors.As(errRead, &closeErr) || closeErr.Code != websocket.CloseMessageTooBig || closeErr.Text != tc.reason {
				t.Fatalf("close = %v, want 1009 with reason %q from peer writer", errRead, tc.reason)
			}
			waitTunnelSignal(t, written, "oversized sender exit")
			waitTunnelSignal(t, p.done, "oversized transport close")
			waitTunnelSignal(t, p.writerDone, "oversized writer exit")
			assertNoFiniteTunnelDeadlines(t, transport)
		})
	}
}

func TestPeerControl_ProtocolErrorReclaimsSaturatedTransport(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"invalid opcode", []byte{0x83, 0x80, 0, 0, 0, 0}},
		{"reserved close code", []byte{0x88, 0x82, 0, 0, 0, 0, 0x03, 0xed}},
		{"invalid UTF-8 close reason", []byte{0x88, 0x83, 0, 0, 0, 0, 0x03, 0xe8, 0xff}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forceClose := make(chan time.Time)
			var forceOnce sync.Once
			reclaim := func() { forceOnce.Do(func() { close(forceClose) }) }
			hub := NewHub(Options{PingInterval: time.Hour})
			hub.testForceClose = forceClose
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
			t.Cleanup(reclaim)

			observer := dialRole(t, srv.URL, "key", RoleClient, "observer", "Observer")
			_ = readTextJSONFrame(t, observer)
			stalled := dialRole(t, srv.URL, "key", RoleServer, "server", "Server")
			_ = readTextJSONFrame(t, observer)
			p := lookupTunnelPeer(t, hub, "key", "server")
			transport := p.conn.UnderlyingConn().(*deadlineGuardConn).Conn.(*deadlineTrackingTunnelConn)
			transport.monitoring.Store(true)
			gated := transport.Conn.(*gatedTunnelConn)
			// Servers receive no initial frames, so the protocol-error close is the next write.
			gated.arm()
			if n, errWrite := stalled.UnderlyingConn().Write(tc.raw); errWrite != nil || n != len(tc.raw) {
				t.Fatalf("write malformed frame: bytes=%d, error=%v", n, errWrite)
			}
			waitTunnelSignal(t, gated.blocked, "single-writer protocol-error close write")
			waitTunnelSignal(t, unregistered, "protocol-error peer unregistration before socket reclamation")
			offline := readTextJSONFrame(t, observer)
			if offline.Get("type").String() != ControlTypeServerOffline || offline.Get("server.uuid").String() != "server" {
				t.Fatalf("unexpected server_offline: %s", offline.Raw)
			}
			select {
			case <-p.done:
				t.Fatal("socket closed before the local grace period elapsed")
			default:
			}
			// The old socket is still blocked, but its UUID must already be reusable.
			replacement := dialRole(t, srv.URL, "key", RoleServer, "server", "Replacement")
			online := readTextJSONFrame(t, observer)
			if online.Get("type").String() != ControlTypeServerOnline || online.Get("server.name").String() != "Replacement" {
				t.Fatalf("unexpected replacement server_online: %s", online.Raw)
			}
			replacementPeer := lookupTunnelPeer(t, hub, "key", "server")
			if replacementPeer == p {
				t.Fatal("protocol error left the old peer registered")
			}
			assertNoFiniteTunnelDeadlines(t, transport)
			// Advance only the local close grace period, without socket deadlines or sleeps.
			reclaim()
			waitTunnelSignal(t, gated.closed, "forced underlying socket close")
			waitTunnelSignal(t, p.done, "protocol-error socket reclamation")
			waitTunnelSignal(t, p.writerDone, "protocol-error writer exit")
			assertNoFiniteTunnelDeadlines(t, transport)
			if lookupTunnelPeer(t, hub, "key", "server") != replacementPeer {
				t.Fatal("old peer cleanup removed the replacement")
			}
			if errWrite := replacement.WriteMessage(websocket.BinaryMessage, []byte{ProtocolVersion1}); errWrite != nil {
				t.Fatalf("replacement write: %v", errWrite)
			}
			if got := readTextJSONFrame(t, replacement); got.Get("code").String() != ErrCodeInvalidDataFrame {
				t.Fatalf("replacement did not remain usable: %s", got.Raw)
			}
		})
	}
}

func TestPeerControl_MalformedCloseNeverSetsFiniteDeadlines(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload []byte
	}{
		{"reserved close code", []byte{0x03, 0xed}},
		{"invalid UTF-8 close reason", []byte{0x03, 0xe8, 0xff}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := NewHub(Options{PingInterval: time.Hour})
			hub.UpdateAllowedAPIKeys([]string{"key"})
			srv := newDeadlineTrackingTunnelServer(t, hub)
			client := dialRole(t, srv.URL, "key", RoleClient, "device", "Device")
			_ = readTextJSONFrame(t, client)
			p := lookupTunnelPeer(t, hub, "key", "device")
			transport := p.conn.UnderlyingConn().(*deadlineGuardConn).Conn.(*deadlineTrackingTunnelConn)
			transport.monitoring.Store(true)
			// A zero mask preserves the malformed payload without mutating any slices.
			raw := append([]byte{0x88, 0x80 | byte(len(tc.payload)), 0, 0, 0, 0}, tc.payload...)
			if n, errWrite := client.UnderlyingConn().Write(raw); errWrite != nil || n != len(raw) {
				t.Fatalf("write malformed close: bytes=%d, error=%v", n, errWrite)
			}
			assertTunnelClose(t, client, websocket.CloseProtocolError)
			waitTunnelSignal(t, p.done, "malformed close transport exit")
			waitTunnelSignal(t, p.writerDone, "malformed close writer exit")
			assertNoFiniteTunnelDeadlines(t, transport)
		})
	}
}
