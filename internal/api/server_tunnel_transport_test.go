package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/wstunnel"
)

// tunnelTLSIngressConn tracks deadlines beneath TLS and deterministically stalls writes.
type tunnelTLSIngressConn struct {
	net.Conn
	postUpgrade     atomic.Bool
	finiteDeadlines atomic.Int64
	allowedWrites   atomic.Int64
	blocked         chan struct{}
	closed          chan struct{}
	closeOnce       sync.Once
}

func (c *tunnelTLSIngressConn) recordDeadline(deadline time.Time) {
	if c.postUpgrade.Load() && !deadline.IsZero() {
		c.finiteDeadlines.Add(1)
	}
}

func (c *tunnelTLSIngressConn) SetDeadline(deadline time.Time) error {
	c.recordDeadline(deadline)
	return c.Conn.SetDeadline(deadline)
}

func (c *tunnelTLSIngressConn) SetReadDeadline(deadline time.Time) error {
	c.recordDeadline(deadline)
	return c.Conn.SetReadDeadline(deadline)
}

func (c *tunnelTLSIngressConn) SetWriteDeadline(deadline time.Time) error {
	c.recordDeadline(deadline)
	return c.Conn.SetWriteDeadline(deadline)
}

func (c *tunnelTLSIngressConn) Write(raw []byte) (int, error) {
	if c.postUpgrade.Load() && c.allowedWrites.Add(-1) < 0 {
		select {
		case c.blocked <- struct{}{}:
		default:
		}
		<-c.closed
		return 0, net.ErrClosed
	}
	return c.Conn.Write(raw)
}

func (c *tunnelTLSIngressConn) Close() error {
	var errClose error
	c.closeOnce.Do(func() {
		errClose = c.Conn.Close()
		close(c.closed)
	})
	return errClose
}

type tunnelTLSIngressListener struct {
	net.Listener
	accepted chan *tunnelTLSIngressConn
}

func (l *tunnelTLSIngressListener) Accept() (net.Conn, error) {
	conn, errAccept := l.Listener.Accept()
	if errAccept != nil {
		return nil, errAccept
	}
	transport := &tunnelTLSIngressConn{
		Conn:    conn,
		blocked: make(chan struct{}, 1),
		closed:  make(chan struct{}),
	}
	l.accepted <- transport
	return transport, nil
}

func waitServerTunnelTransportSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func TestServerWebsocketTunnel_TLSWithoutALPNClosesRawTransport(t *testing.T) {
	certificateServer := httptest.NewTLSServer(nil)
	tlsConfig := certificateServer.TLS.Clone()
	tlsConfig.NextProtos = []string{"h2", "http/1.1"}
	roots := x509.NewCertPool()
	roots.AddCert(certificateServer.Certificate())
	certificateServer.Close()

	for _, tc := range []struct {
		name          string
		allowedWrites int64
	}{
		{"healthy WebSocket close with stalled TLS close_notify", 1},
		{"saturated WebSocket close", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			apiSrv := newTestTunnelAPIServer(t, &config.Config{
				SDKConfig: config.SDKConfig{APIKeys: []string{"key"}},
			})
			baseListener, errListen := net.Listen("tcp", "127.0.0.1:0")
			if errListen != nil {
				t.Fatalf("listen: %v", errListen)
			}
			trackedListener := &tunnelTLSIngressListener{
				Listener: baseListener,
				accepted: make(chan *tunnelTLSIngressConn, 1),
			}
			tlsListener := tls.NewListener(trackedListener, tlsConfig)
			httpListener := newMuxListener(baseListener.Addr(), 1)
			apiSrv.muxBaseListener = tlsListener
			apiSrv.muxHTTPListener = httpListener
			routed := make(chan net.Conn, 1)
			apiSrv.server.ConnContext = func(ctx context.Context, conn net.Conn) context.Context {
				routed <- conn
				return ctx
			}
			serveDone := make(chan error, 1)
			acceptDone := make(chan error, 1)
			go func() { serveDone <- apiSrv.server.Serve(httpListener) }()
			go func() { acceptDone <- apiSrv.acceptMuxConnections(tlsListener, httpListener) }()
			t.Cleanup(func() {
				if errStop := apiSrv.Stop(context.Background()); errStop != nil {
					t.Errorf("stop test server: %v", errStop)
				}
				for _, done := range []<-chan error{serveDone, acceptDone} {
					select {
					case errDone := <-done:
						if errDone != nil && !errors.Is(errDone, net.ErrClosed) && !errors.Is(errDone, http.ErrServerClosed) {
							t.Errorf("server loop exit: %v", errDone)
						}
					case <-time.After(3 * time.Second):
						t.Error("server loop did not exit")
					}
				}
			})

			dialer := websocket.Dialer{
				TLSClientConfig:  &tls.Config{RootCAs: roots},
				HandshakeTimeout: 5 * time.Second,
			}
			client, resp, errDial := dialer.Dial(
				"wss://"+baseListener.Addr().String()+"/v1/tunnel?role=client&uuid=device&name=Device",
				http.Header{"Authorization": {"Bearer key"}},
			)
			if resp != nil && resp.Body != nil {
				if errClose := resp.Body.Close(); errClose != nil {
					t.Errorf("close handshake response body: %v", errClose)
				}
			}
			if errDial != nil {
				t.Fatalf("dial TLS tunnel: %v", errDial)
			}
			t.Cleanup(func() {
				// Close the client TCP transport directly too; TLS Close installs deadlines.
				if errClose := client.UnderlyingConn().(*tls.Conn).NetConn().Close(); errClose != nil {
					t.Errorf("close client transport: %v", errClose)
				}
			})
			client.SetCloseHandler(func(int, string) error { return nil })
			if initial := readServerTunnelJSON(t, client); initial.Get("type").String() != wstunnel.ControlTypeServerList {
				t.Fatalf("unexpected initial server_list: %s", initial.Raw)
			}
			transport := <-trackedListener.accepted
			t.Cleanup(func() {
				if errClose := transport.Close(); errClose != nil {
					t.Errorf("close server transport: %v", errClose)
				}
			})
			buffered, okBuffered := (<-routed).(*bufferedConn)
			if !okBuffered {
				t.Fatal("TLS connection without ALPN did not take the buffered protocol-sniffing path")
			}
			tlsConn, okTLS := buffered.NetConn().(*tls.Conn)
			if !okTLS || tlsConn.ConnectionState().NegotiatedProtocol != "" || tlsConn.NetConn() != transport {
				t.Fatal("expected buffered TLS connection with no ALPN above the tracked raw transport")
			}
			// Let the healthy WebSocket close through, but stall any TLS close_notify.
			transport.allowedWrites.Store(tc.allowedWrites)
			transport.postUpgrade.Store(true)
			closeReturned := make(chan struct{})
			go func() {
				apiSrv.tunnelHub.Close()
				close(closeReturned)
			}()
			waitServerTunnelTransportSignal(t, closeReturned, "non-blocking hub close")
			if tc.allowedWrites == 0 {
				waitServerTunnelTransportSignal(t, transport.blocked, "saturated WebSocket close write")
			}
			waitServerTunnelTransportSignal(t, transport.closed, "raw transport reclamation without TLS close_notify")
			if calls := transport.finiteDeadlines.Load(); calls != 0 {
				t.Fatalf("finite post-upgrade raw transport deadlines = %d, want zero", calls)
			}
			_, _, errRead := readServerTunnelTestMessage(client)
			if tc.allowedWrites > 0 {
				var closeErr *websocket.CloseError
				if !errors.As(errRead, &closeErr) || closeErr.Code != websocket.CloseGoingAway {
					t.Fatalf("healthy WebSocket close = %v, want 1001", errRead)
				}
			} else if errRead == nil {
				t.Fatal("saturated tunnel remained open after raw transport reclamation")
			}
		})
	}
}
