package claude

import (
	"errors"
	"net"
	"testing"
	"time"
)

type claudeTestDialer struct {
	conn net.Conn
}

func (d claudeTestDialer) Dial(_, _ string) (net.Conn, error) {
	return d.conn, nil
}

func TestUtlsRoundTripperBoundsTLSHandshake(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer func() {
		if errClose := serverConn.Close(); errClose != nil {
			t.Errorf("server connection close returned error: %v", errClose)
		}
	}()

	transport := &utlsRoundTripper{dialer: claudeTestDialer{conn: clientConn}}
	startedAt := time.Now()
	_, err := transport.createConnection("example.com", "unused", 20*time.Millisecond)
	if err == nil {
		t.Fatal("expected TLS handshake timeout")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("error = %v, want timeout error", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("TLS handshake took %s, want less than one second", elapsed)
	}
}
