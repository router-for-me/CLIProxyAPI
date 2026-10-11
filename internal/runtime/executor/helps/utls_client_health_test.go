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

// shortenUtlsHTTP2HealthCheck makes the health check ping after 100ms without a frame. The PING timeout
// stays generous, so a slow ACK on a loaded machine cannot fail a healthy connection.
func shortenUtlsHTTP2HealthCheck(t *testing.T) {
	t.Helper()
	readIdle, ping := utlsHTTP2ReadIdleTimeout, utlsHTTP2PingTimeout
	utlsHTTP2ReadIdleTimeout, utlsHTTP2PingTimeout = 100*time.Millisecond, time.Second
	t.Cleanup(func() { utlsHTTP2ReadIdleTimeout, utlsHTTP2PingTimeout = readIdle, ping })
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

func TestUtlsHTTP2StreamFailsWhenThePathGoesDead(t *testing.T) {
	shortenUtlsHTTP2HealthCheck(t)
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
	resp, errRoundTrip := roundTripUtlsConnection(req, tlsConn)
	if errRoundTrip != nil {
		t.Fatal(errRoundTrip)
	}
	defer func() { _ = resp.Body.Close() }()
	reader := bufio.NewReader(resp.Body)
	if line, errRead := reader.ReadString('\n'); errRead != nil || !strings.Contains(line, "response.created") {
		t.Fatalf("first line = %q, error = %v", line, errRead)
	}
	path.stall()
	started := time.Now()
	done := make(chan error, 1)
	go func() {
		_, errRead := reader.ReadString('\n')
		for errRead == nil {
			_, errRead = reader.ReadString('\n')
		}
		done <- errRead
	}()
	select {
	case errRead := <-done:
		if errRead == nil || errors.Is(errRead, http.ErrBodyReadAfterClose) {
			t.Fatalf("read error = %v, want a lost-connection error", errRead)
		}
		t.Logf("dead path detected after %v: %v", time.Since(started), errRead)
	case <-time.After(10 * time.Second):
		t.Fatal("stream read still blocked on a dead path; the HTTP/2 health check did not close the connection")
	}
}

func TestUtlsHTTP2HealthCheckKeepsASilentHealthyStream(t *testing.T) {
	shortenUtlsHTTP2HealthCheck(t)
	release := make(chan struct{})
	tlsConn, _, serverURL := newALPNTestConnection(t, "h2", sseUntilReleased(t, release))
	req, errRequest := http.NewRequestWithContext(t.Context(), http.MethodPost, serverURL, strings.NewReader("{}"))
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	resp, errRoundTrip := roundTripUtlsConnection(req, tlsConn)
	if errRoundTrip != nil {
		t.Fatal(errRoundTrip)
	}
	defer func() { _ = resp.Body.Close() }()
	reader := bufio.NewReader(resp.Body)
	if line, errRead := reader.ReadString('\n'); errRead != nil || !strings.Contains(line, "response.created") {
		t.Fatalf("first line = %q, error = %v", line, errRead)
	}
	// Ten health-check periods of silence: the server answers each PING, so the stream survives.
	time.AfterFunc(10*utlsHTTP2ReadIdleTimeout, func() { close(release) })
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
