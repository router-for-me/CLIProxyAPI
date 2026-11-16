package wstunnel

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type deadlineTrackingTunnelConn struct {
	net.Conn
	monitoring     atomic.Bool
	deadlineCalls  atomic.Int64
	deadlines      atomic.Int64
	readDeadlines  atomic.Int64
	writeDeadlines atomic.Int64
}

func (c *deadlineTrackingTunnelConn) SetDeadline(deadline time.Time) error {
	c.deadlineCalls.Add(1)
	if c.monitoring.Load() && !deadline.IsZero() {
		c.deadlines.Add(1)
	}
	return c.Conn.SetDeadline(deadline)
}

func (c *deadlineTrackingTunnelConn) SetReadDeadline(deadline time.Time) error {
	c.deadlineCalls.Add(1)
	if c.monitoring.Load() && !deadline.IsZero() {
		c.readDeadlines.Add(1)
	}
	return c.Conn.SetReadDeadline(deadline)
}

func (c *deadlineTrackingTunnelConn) SetWriteDeadline(deadline time.Time) error {
	c.deadlineCalls.Add(1)
	if c.monitoring.Load() && !deadline.IsZero() {
		c.writeDeadlines.Add(1)
	}
	return c.Conn.SetWriteDeadline(deadline)
}

type deadlineTrackingTunnelListener struct{ net.Listener }

func (l deadlineTrackingTunnelListener) Accept() (net.Conn, error) {
	conn, errAccept := l.Listener.Accept()
	if errAccept != nil {
		return nil, errAccept
	}
	return &deadlineTrackingTunnelConn{Conn: conn}, nil
}

func newDeadlineTrackingTunnelServer(t *testing.T, hub *Hub) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(hub)
	srv.Listener = deadlineTrackingTunnelListener{srv.Listener}
	srv.Start()
	t.Cleanup(srv.Close)
	t.Cleanup(hub.Close)
	return srv
}

func assertNoFiniteTunnelDeadlines(t *testing.T, transport *deadlineTrackingTunnelConn) {
	t.Helper()
	if all, reads, writes := transport.deadlines.Load(), transport.readDeadlines.Load(), transport.writeDeadlines.Load(); all != 0 || reads != 0 || writes != 0 {
		t.Fatalf("finite post-upgrade deadlines: all=%d reads=%d writes=%d, want zero", all, reads, writes)
	}
}

func TestPeerControl_PingAndCloseNeverSetFiniteDeadlines(t *testing.T) {
	hub := NewHub(Options{PingInterval: time.Hour})
	hub.UpdateAllowedAPIKeys([]string{"key"})
	srv := newDeadlineTrackingTunnelServer(t, hub)
	client := dialRole(t, srv.URL, "key", RoleClient, "device", "Device")
	_ = readTextJSONFrame(t, client)
	p := lookupTunnelPeer(t, hub, "key", "device")
	transport := p.conn.UnderlyingConn().(*deadlineGuardConn).Conn.(*deadlineTrackingTunnelConn)
	transport.monitoring.Store(true)
	pongs := make(chan string, 1)
	client.SetPongHandler(func(payload string) error {
		pongs <- payload
		return nil
	})
	client.SetCloseHandler(func(int, string) error { return nil })
	readDone := make(chan error, 1)
	go func() {
		_, _, errRead := client.ReadMessage()
		readDone <- errRead
	}()
	for _, payload := range []string{"", "matching-pong", strings.Repeat("p", 125)} {
		if errWrite := client.WriteControl(websocket.PingMessage, []byte(payload), time.Time{}); errWrite != nil {
			t.Fatal(errWrite)
		}
		select {
		case got := <-pongs:
			if got != payload {
				t.Fatalf("pong payload = %q, want %q", got, payload)
			}
		case errRead := <-readDone:
			t.Fatalf("connection ended before pong: %v", errRead)
		case <-time.After(5 * time.Second):
			t.Fatal("missing pong response")
		}
	}
	if errWrite := client.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "client close"), time.Time{}); errWrite != nil {
		t.Fatal(errWrite)
	}
	select {
	case errRead := <-readDone:
		var closeErr *websocket.CloseError
		if !errors.As(errRead, &closeErr) || closeErr.Code != websocket.CloseNormalClosure || closeErr.Text != "" {
			t.Fatalf("close response = %v, want 1000 with empty reason", errRead)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("missing close response")
	}
	waitTunnelSignal(t, p.writerDone, "close response and writer exit")
	assertNoFiniteTunnelDeadlines(t, transport)
}

func TestPeerControl_EmptyCloseReceivesReplyWithoutFiniteDeadlines(t *testing.T) {
	forceClose := make(chan time.Time)
	hub := NewHub(Options{PingInterval: time.Hour})
	hub.testForceClose = forceClose
	t.Cleanup(func() { close(forceClose) })
	hub.UpdateAllowedAPIKeys([]string{"key"})
	srv := newDeadlineTrackingTunnelServer(t, hub)
	client := dialRole(t, srv.URL, "key", RoleClient, "device", "Device")
	_ = readTextJSONFrame(t, client)
	p := lookupTunnelPeer(t, hub, "key", "device")
	transport := p.conn.UnderlyingConn().(*deadlineGuardConn).Conn.(*deadlineTrackingTunnelConn)
	transport.monitoring.Store(true)
	client.SetCloseHandler(func(int, string) error { return nil })

	if errWrite := client.WriteControl(websocket.CloseMessage, []byte{}, time.Time{}); errWrite != nil {
		t.Fatal(errWrite)
	}
	assertTunnelClose(t, client, websocket.CloseNoStatusReceived)
	waitTunnelSignal(t, p.done, "empty close transport exit")
	waitTunnelSignal(t, p.writerDone, "empty close response and writer exit")
	assertNoFiniteTunnelDeadlines(t, transport)
}

func TestPeerControl_PendingPongIsBoundedAndKeepsLatestPayload(t *testing.T) {
	p := &peer{pongCh: make(chan string, 1), sendCh: make(chan outboundFrame)}
	p.requestPong("first")
	latest := strings.Repeat("p", 125)
	p.requestPong(latest)
	p.requestPong(strings.Repeat("x", 126))
	select {
	case got := <-p.pongCh:
		if got != latest {
			t.Fatalf("pending pong = %q, want latest valid payload", got)
		}
	default:
		t.Fatal("missing pending pong")
	}
	p.shutdownAndDrainQueue()
	p.requestPong("after close")
	if len(p.pongCh) != 0 {
		t.Fatal("closing peer accepted another pong")
	}
}

func TestPeerControl_UnknownWebSocketOpcodeIsFatalProtocolError(t *testing.T) {
	for _, opcode := range []byte{0x3, 0x7, 0xb, 0xf} {
		t.Run(fmt.Sprintf("opcode=%x", opcode), func(t *testing.T) {
			hub := NewHub(Options{})
			hub.UpdateAllowedAPIKeys([]string{"key"})
			srv := newDeadlineTrackingTunnelServer(t, hub)
			client := dialRole(t, srv.URL, "key", RoleClient, "device", "Device")
			_ = readTextJSONFrame(t, client)
			p := lookupTunnelPeer(t, hub, "key", "device")
			transport := p.conn.UnderlyingConn().(*deadlineGuardConn).Conn.(*deadlineTrackingTunnelConn)
			transport.monitoring.Store(true)
			// Send a final, masked, empty frame using a reserved data/control opcode.
			raw := []byte{0x80 | opcode, 0x80, 0, 0, 0, 0}
			if n, errWrite := client.UnderlyingConn().Write(raw); errWrite != nil {
				t.Fatal(errWrite)
			} else if n != len(raw) {
				t.Fatal(io.ErrShortWrite)
			}
			// No non-fatal JSON error may precede the transport close.
			assertTunnelClose(t, client, websocket.CloseProtocolError)
			waitTunnelSignal(t, p.done, "protocol error transport close")
			waitTunnelSignal(t, p.writerDone, "protocol error writer exit")
			assertNoFiniteTunnelDeadlines(t, transport)
			hub.mu.RLock()
			_, registered := hub.realms["key"]
			hub.mu.RUnlock()
			if registered {
				t.Fatal("protocol error left the peer registered")
			}
		})
	}
}
