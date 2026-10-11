package helps

import (
	"bufio"
	stdtls "crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	tls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// deadPathConn models a connection whose far side stopped answering without a FIN or RST, as a NAT or
// a TCP-terminating proxy can leave it: after stall, writes are accepted and dropped and reads block
// until the connection is closed locally.
type deadPathConn struct {
	net.Conn
	mu      sync.Mutex
	stalled bool
	closed  chan struct{}
	once    sync.Once
}

func newDeadPathConn(conn net.Conn) *deadPathConn {
	return &deadPathConn{Conn: conn, closed: make(chan struct{})}
}

func (c *deadPathConn) stall() {
	c.mu.Lock()
	c.stalled = true
	c.mu.Unlock()
}

func (c *deadPathConn) isStalled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stalled
}

func (c *deadPathConn) Read(p []byte) (int, error) {
	if c.isStalled() {
		<-c.closed
		return 0, net.ErrClosed
	}
	return c.Conn.Read(p)
}

func (c *deadPathConn) Write(p []byte) (int, error) {
	if c.isStalled() {
		return len(p), nil
	}
	return c.Conn.Write(p)
}

func (c *deadPathConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

// newALPNTestConnectionThrough is newALPNTestConnection with the client's TCP connection wrapped
// before the TLS handshake, so a test can model the network path under the TLS session.
func newALPNTestConnectionThrough(t *testing.T, protocol string, handler http.Handler, wrap func(net.Conn) net.Conn) (*tls.UConn, string) {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = protocol == "h2"
	server.TLS = &stdtls.Config{NextProtos: []string{protocol}}
	server.StartTLS()
	t.Cleanup(server.Close)
	rootCAs := x509.NewCertPool()
	rootCAs.AddCert(server.Certificate())
	serverURL, errParse := url.Parse(server.URL)
	if errParse != nil {
		t.Fatal(errParse)
	}
	conn, errDial := (&net.Dialer{}).DialContext(t.Context(), "tcp", serverURL.Host)
	if errDial != nil {
		t.Fatal(errDial)
	}
	tlsConn := tls.UClient(wrap(conn), &tls.Config{ServerName: serverURL.Hostname(), RootCAs: rootCAs}, tls.HelloChrome_Auto)
	t.Cleanup(func() { _ = tlsConn.Close() })
	if errHandshake := tlsConn.HandshakeContext(t.Context()); errHandshake != nil {
		t.Fatal(errHandshake)
	}
	if got := tlsConn.ConnectionState().NegotiatedProtocol; got != protocol {
		t.Fatalf("negotiated protocol = %q, want %q", got, protocol)
	}
	return tlsConn, server.URL
}

// testHTTP2ReadIdle is the health-check idle time the tests enable. The PING timeout is shortened but
// stays generous, so a slow ACK on a loaded machine cannot fail a healthy connection.
const testHTTP2ReadIdle = 100 * time.Millisecond

func shortenUtlsHTTP2PingTimeout(t *testing.T) {
	t.Helper()
	ping := utlsHTTP2PingTimeout
	utlsHTTP2PingTimeout = time.Second
	t.Cleanup(func() { utlsHTTP2PingTimeout = ping })
}

// sseUntilReleased writes one event, then holds the stream open and silent until release closes,
// and then writes a second event.
func sseUntilReleased(t *testing.T, release <-chan struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if _, errWrite := w.Write([]byte("data: {\"type\":\"response.created\"}\n\n")); errWrite != nil {
			t.Errorf("write first event: %v", errWrite)
			return
		}
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		if _, errWrite := w.Write([]byte("data: {\"type\":\"response.completed\"}\n\n")); errWrite == nil {
			w.(http.Flusher).Flush()
		}
	})
}

func TestUtlsHTTP2HealthCheckOnADeadPath(t *testing.T) {
	for _, tc := range []struct {
		name     string
		readIdle time.Duration
	}{
		{"enabled fails the stream", testHTTP2ReadIdle},
		{"disabled by default keeps waiting", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shortenUtlsHTTP2PingTimeout(t)
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			var path *deadPathConn
			tlsConn, serverURL := newALPNTestConnectionThrough(t, "h2", sseUntilReleased(t, release), func(conn net.Conn) net.Conn {
				path = newDeadPathConn(conn)
				return path
			})
			req, errRequest := http.NewRequestWithContext(t.Context(), http.MethodPost, serverURL, strings.NewReader("{}"))
			if errRequest != nil {
				t.Fatal(errRequest)
			}
			resp, errRoundTrip := roundTripUtlsConnection(req, tlsConn, tc.readIdle)
			if errRoundTrip != nil {
				t.Fatal(errRoundTrip)
			}
			defer func() { _ = resp.Body.Close() }()
			reader := bufio.NewReader(resp.Body)
			if line, errRead := reader.ReadString('\n'); errRead != nil || !strings.Contains(line, "response.created") {
				t.Fatalf("first line = %q, error = %v", line, errRead)
			}
			path.stall()
			done := make(chan error, 1)
			go func() {
				_, errRead := reader.ReadString('\n')
				for errRead == nil {
					_, errRead = reader.ReadString('\n')
				}
				done <- errRead
			}()
			if tc.readIdle == 0 {
				// Several PING timeouts pass without a health check closing the connection.
				select {
				case errRead := <-done:
					t.Fatalf("stream failed without the health check enabled: %v", errRead)
				case <-time.After(3 * utlsHTTP2PingTimeout):
				}
				return
			}
			select {
			case errRead := <-done:
				if errRead == nil || errors.Is(errRead, http.ErrBodyReadAfterClose) {
					t.Fatalf("read error = %v, want a lost-connection error", errRead)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("stream read still blocked on a dead path; the HTTP/2 health check did not close the connection")
			}
		})
	}
}

func TestUtlsHTTP2HealthCheckKeepsASilentHealthyStream(t *testing.T) {
	shortenUtlsHTTP2PingTimeout(t)
	release := make(chan struct{})
	tlsConn, _, serverURL := newALPNTestConnection(t, "h2", sseUntilReleased(t, release))
	req, errRequest := http.NewRequestWithContext(t.Context(), http.MethodPost, serverURL, strings.NewReader("{}"))
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	resp, errRoundTrip := roundTripUtlsConnection(req, tlsConn, testHTTP2ReadIdle)
	if errRoundTrip != nil {
		t.Fatal(errRoundTrip)
	}
	defer func() { _ = resp.Body.Close() }()
	reader := bufio.NewReader(resp.Body)
	if line, errRead := reader.ReadString('\n'); errRead != nil || !strings.Contains(line, "response.created") {
		t.Fatalf("first line = %q, error = %v", line, errRead)
	}
	// Ten health-check periods of silence: the server answers each PING, so the stream survives.
	time.AfterFunc(10*testHTTP2ReadIdle, func() { close(release) })
	for {
		line, errRead := reader.ReadString('\n')
		if errRead != nil {
			t.Fatalf("silent healthy stream failed: %v", errRead)
		}
		if strings.Contains(line, "response.completed") {
			return
		}
	}
}

func TestUtlsHTTP2HealthCheckIsConfiguredExplicitly(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want time.Duration
	}{
		{"unset", "port: 8317\n", 0},
		{"disabled", "streaming:\n  http2-health-check-seconds: 0\n", 0},
		{"legacy layout", "streaming:\n  http2-health-check-seconds: 30\n", 30 * time.Second},
		{"v8 layout", "requests:\n  streaming:\n    http2-health-check-seconds: 30\n", 30 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, errParse := config.ParseConfigBytes([]byte(tc.yaml))
			if errParse != nil {
				t.Fatal(errParse)
			}
			if got := utlsHTTP2HealthCheck(cfg); got != tc.want {
				t.Fatalf("health-check idle time = %v, want %v", got, tc.want)
			}
		})
	}
	if got := utlsHTTP2HealthCheck(nil); got != 0 {
		t.Fatalf("health-check idle time without config = %v, want 0", got)
	}
}
