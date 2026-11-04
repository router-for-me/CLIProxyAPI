package helps

import (
	"bufio"
	"context"
	gotls "crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// silentTLSListener accepts one TCP connection, drains the TLS ClientHello,
// and never sends anything back, reproducing the silent-upstream shape from
// Issue #6615. The returned channel closes once the client side has closed
// the connection (the drain observes EOF), which also proves the failed
// socket was released.
func silentTLSListener(t *testing.T) (addr string, drained <-chan struct{}) {
	t.Helper()
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	t.Cleanup(func() {
		if errClose := listener.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close listener: %v", errClose)
		}
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, errAccept := listener.Accept()
		if errAccept != nil {
			return
		}
		defer func() {
			// The client may already have reset this intentionally stalled
			// connection, so its close outcome is not part of the assertion.
			_ = conn.Close()
		}()
		_, _ = io.Copy(io.Discard, conn)
	}()
	return listener.Addr().String(), done
}

// TestClaudeCodeTLSHandshakeTimeoutBoundsSilentPeer reproduces Issue #6615
// through the real Claude Code transport: the peer completes the TCP
// connection but never answers the TLS ClientHello. With the opt-in
// handshake timeout configured, connection establishment must fail with a
// deadline error inside a bounded interval so retry logic can proceed.
func TestClaudeCodeTLSHandshakeTimeoutBoundsSilentPeer(t *testing.T) {
	addr, drained := silentTLSListener(t)

	transport, ok := newClaudeCodeRoundTripper("").(*http.Transport)
	if !ok {
		t.Fatalf("Claude Code transport type = %T, want *http.Transport", newClaudeCodeRoundTripper(""))
	}
	ctx := withTLSHandshakeTimeout(context.Background(), 100*time.Millisecond)

	dialDone := make(chan error, 1)
	go func() {
		conn, errDial := transport.DialTLSContext(ctx, "tcp", addr)
		if conn != nil {
			errDial = errors.Join(errDial, conn.Close())
		}
		dialDone <- errDial
	}()

	select {
	case errDial := <-dialDone:
		if !errors.Is(errDial, context.DeadlineExceeded) {
			t.Fatalf("DialTLSContext error = %v, want context deadline exceeded", errDial)
		}
		if !cliproxyexecutor.IsConfiguredTLSHandshakeTimeoutError(errDial) {
			t.Fatalf("DialTLSContext error = %q, want typed configured TLS handshake timeout", errDial)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TLS handshake with silent peer was not bounded by the configured timeout")
	}

	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not observe the failed handshake connection being closed")
	}
}

// TestClaudeCodeTLSHandshakeTimeoutZeroKeepsUnboundedWait verifies the
// default (timeout 0) preserves the pre-existing behavior: with the bound
// disabled, a silent peer keeps the handshake pending and only caller
// context cancellation ends the wait.
func TestClaudeCodeTLSHandshakeTimeoutZeroKeepsUnboundedWait(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		if errClose := serverConn.Close(); errClose != nil && !errors.Is(errClose, io.ErrClosedPipe) && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close server connection: %v", errClose)
		}
	})

	dialer := contextDialerFunc(func(context.Context, string, string) (net.Conn, error) {
		return clientConn, nil
	})
	transport := newClaudeCodeRoundTripperForDialer(dialer)

	ctx, cancel := context.WithCancel(withTLSHandshakeTimeout(context.Background(), 0))
	dialDone := make(chan error, 1)
	go func() {
		conn, errDial := transport.DialTLSContext(ctx, "tcp", "api.anthropic.com:443")
		if conn != nil {
			errDial = errors.Join(errDial, conn.Close())
		}
		dialDone <- errDial
	}()

	// Consume the full ClientHello record so the handshake is known to be
	// in flight and blocked reading the peer's answer.
	header := make([]byte, 5)
	if _, errRead := io.ReadFull(serverConn, header); errRead != nil {
		t.Fatalf("read ClientHello record header: %v", errRead)
	}
	recordLength := int(header[3])<<8 | int(header[4])
	if _, errRead := io.ReadFull(serverConn, make([]byte, recordLength)); errRead != nil {
		t.Fatalf("read ClientHello record body: %v", errRead)
	}

	select {
	case errDial := <-dialDone:
		t.Fatalf("DialTLSContext returned before caller cancellation: %v", errDial)
	default:
	}

	cancel()
	select {
	case errDial := <-dialDone:
		if !errors.Is(errDial, context.Canceled) {
			t.Fatalf("DialTLSContext error = %v, want context canceled", errDial)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TLS handshake did not stop after caller context cancellation")
	}
}

// TestUtlsRoundTripperHandshakeTimeoutBoundsSilentPeer covers the Chrome
// fingerprint path (ChatGPT) with the same silent-peer shape: when the
// caller configured the bound, createConnection must also fail in a
// bounded interval instead of waiting forever.
func TestUtlsRoundTripperHandshakeTimeoutBoundsSilentPeer(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		if errClose := serverConn.Close(); errClose != nil && !errors.Is(errClose, io.ErrClosedPipe) && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close server connection: %v", errClose)
		}
	})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		_, _ = io.Copy(io.Discard, serverConn)
	}()

	trackedConn := &trackedNetConn{Conn: clientConn}
	roundTripper := &utlsRoundTripper{dialer: contextDialerFunc(func(context.Context, string, string) (net.Conn, error) {
		return trackedConn, nil
	})}
	ctx := withTLSHandshakeTimeout(context.Background(), 100*time.Millisecond)

	connectionDone := make(chan error, 1)
	go func() {
		tlsConn, errConnect := roundTripper.createConnection(ctx, "chatgpt.com", "chatgpt.com:443")
		if tlsConn != nil {
			errConnect = errors.Join(errConnect, tlsConn.Close())
		}
		connectionDone <- errConnect
	}()

	select {
	case errConnect := <-connectionDone:
		if !errors.Is(errConnect, context.DeadlineExceeded) {
			t.Fatalf("createConnection error = %v, want context deadline exceeded", errConnect)
		}
		if !cliproxyexecutor.IsConfiguredTLSHandshakeTimeoutError(errConnect) {
			t.Fatalf("createConnection error = %q, want typed configured TLS handshake timeout", errConnect)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Chrome-path TLS handshake with silent peer was not bounded by the configured timeout")
	}
	select {
	case <-serverDone:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not observe the failed handshake connection being closed")
	}
}

// TestUtlsRoundTripperWithoutHandshakeBoundWaitsForCaller mirrors the
// zero/disabled behavior on the Chrome path: without the ctx carrier the
// handshake keeps waiting and only caller cancellation ends it, exactly
// like TestUtlsRoundTripperHandshakeUsesRequestContext.
func TestUtlsRoundTripperWithoutHandshakeBoundWaitsForCaller(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		if errClose := serverConn.Close(); errClose != nil && !errors.Is(errClose, io.ErrClosedPipe) && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close server connection: %v", errClose)
		}
	})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		_, _ = io.Copy(io.Discard, serverConn)
	}()

	roundTripper := &utlsRoundTripper{dialer: contextDialerFunc(func(context.Context, string, string) (net.Conn, error) {
		return clientConn, nil
	})}
	ctx, cancel := context.WithCancel(context.Background())

	connectionDone := make(chan error, 1)
	go func() {
		tlsConn, errConnect := roundTripper.createConnection(ctx, "chatgpt.com", "chatgpt.com:443")
		if tlsConn != nil {
			errConnect = errors.Join(errConnect, tlsConn.Close())
		}
		connectionDone <- errConnect
	}()

	// The handshake must still be in flight here; only the explicit cancel
	// below may end it. There is no deterministic "handshake started"
	// signal on this path, so this check is intentionally one-sided: it
	// fails only if the handshake already terminated on its own.
	select {
	case errConnect := <-connectionDone:
		t.Fatalf("createConnection returned before caller cancellation: %v", errConnect)
	default:
	}

	cancel()
	select {
	case errConnect := <-connectionDone:
		if !errors.Is(errConnect, context.Canceled) {
			t.Fatalf("createConnection error = %v, want context canceled", errConnect)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Chrome-path TLS handshake did not stop after caller context cancellation")
	}
}

// deadlineRecordingConn records connection deadlines so the successful
// handshake test can prove no establishment deadline remains installed.
type deadlineRecordingConn struct {
	net.Conn
	deadlines chan time.Time
}

func (c *deadlineRecordingConn) SetDeadline(deadline time.Time) error {
	c.deadlines <- deadline
	return c.Conn.SetDeadline(deadline)
}

func (c *deadlineRecordingConn) SetReadDeadline(deadline time.Time) error {
	c.deadlines <- deadline
	return c.Conn.SetReadDeadline(deadline)
}

func (c *deadlineRecordingConn) SetWriteDeadline(deadline time.Time) error {
	c.deadlines <- deadline
	return c.Conn.SetWriteDeadline(deadline)
}

// TestClaudeCodeTLSHandshakeTimeoutPreservesSuccessfulConnection exercises
// a real successful Claude uTLS handshake over net.Pipe inside synctest.
// synctest advances its fake clock only when every goroutine is durably
// blocked, so the one-second response delay below proves timers beyond the
// 100ms handshake bound fire before the established connection is used; no
// test wall-clock time is consumed by that delay.
func TestClaudeCodeTLSHandshakeTimeoutPreservesSuccessfulConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// synctest starts at its own fake epoch, so mint the certificate
		// against that clock rather than the host wall clock.
		certificate := newResumptionTestCertificateAt(t, time.Now())
		roots := x509.NewCertPool()
		roots.AddCert(certificate.Leaf)
		clientConn, serverConn := net.Pipe()
		t.Cleanup(func() {
			if errClose := serverConn.Close(); errClose != nil && !errors.Is(errClose, io.ErrClosedPipe) && !errors.Is(errClose, net.ErrClosed) {
				t.Errorf("close server connection: %v", errClose)
			}
		})
		recorded := &deadlineRecordingConn{Conn: clientConn, deadlines: make(chan time.Time, 16)}
		dialer := contextDialerFunc(func(context.Context, string, string) (net.Conn, error) {
			return recorded, nil
		})
		transport := newClaudeCodeRoundTripperForDialerAndRoots(dialer, roots)
		const handshakeTimeout = 100 * time.Millisecond
		ctx := withTLSHandshakeTimeout(context.Background(), handshakeTimeout)

		dialDone := make(chan struct {
			conn net.Conn
			err  error
		}, 1)
		go func() {
			conn, errDial := transport.DialTLSContext(ctx, "tcp", "api.anthropic.com:443")
			dialDone <- struct {
				conn net.Conn
				err  error
			}{conn: conn, err: errDial}
		}()

		server := gotls.Server(serverConn, &gotls.Config{
			Certificates: []gotls.Certificate{certificate},
			MinVersion:   gotls.VersionTLS13,
			NextProtos:   []string{"http/1.1"},
		})
		serverDone := make(chan error, 1)
		go func() {
			if errHandshake := server.Handshake(); errHandshake != nil {
				serverDone <- fmt.Errorf("server handshake: %w", errHandshake)
				return
			}
			reader := bufio.NewReader(server)
			if _, errRead := http.ReadRequest(reader); errRead != nil {
				serverDone <- fmt.Errorf("server read request: %w", errRead)
				return
			}
			// Fake time advances to this timer while all goroutines block,
			// placing response delivery well beyond the handshake bound.
			time.Sleep(time.Second)
			response := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}"
			if _, errWrite := io.WriteString(server, response); errWrite != nil {
				serverDone <- fmt.Errorf("server write response: %w", errWrite)
				return
			}
			// net.Pipe has no half-close: a TLS close_notify write blocks until
			// the peer reads again, but this test stops after Content-Length bytes.
			// Response delivery is the behavior under test, so report completion
			// once those bytes are written and let cleanup close the raw pipe.
			serverDone <- nil
		}()

		var established net.Conn
		select {
		case result := <-dialDone:
			if result.err != nil {
				t.Fatalf("DialTLSContext error = %v, want successful handshake", result.err)
			}
			established = result.conn
		case <-time.After(5 * time.Second):
			t.Fatal("successful TLS handshake did not complete")
		}
		defer func() {
			// Close the raw pipe from the other end first. Otherwise uTLS can
			// spend its own five-second alert timeout trying to write close_notify
			// to a net.Pipe peer that has intentionally stopped reading after the
			// complete HTTP response.
			if errClose := serverConn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) && !errors.Is(errClose, io.ErrClosedPipe) {
				t.Errorf("close server connection before client: %v", errClose)
			}
			if errClose := established.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) && !errors.Is(errClose, io.ErrClosedPipe) {
				t.Errorf("close established connection: %v", errClose)
			}
		}()

		select {
		case deadline := <-recorded.deadlines:
			t.Fatalf("successful handshake installed a residual connection deadline: %v", deadline)
		default:
		}
		if _, errWrite := io.WriteString(established, "GET /v1/messages HTTP/1.1\r\nHost: api.anthropic.com\r\nConnection: close\r\n\r\n"); errWrite != nil {
			t.Fatalf("write after successful handshake: %v", errWrite)
		}
		request, errRequest := http.NewRequest(http.MethodGet, "https://api.anthropic.com/v1/messages", nil)
		if errRequest != nil {
			t.Fatal(errRequest)
		}
		resp, errReadResponse := http.ReadResponse(bufio.NewReader(established), request)
		if errReadResponse != nil {
			t.Fatalf("read response beyond handshake bound: %v", errReadResponse)
		}
		body, errReadBody := io.ReadAll(resp.Body)
		if errReadBody != nil {
			t.Fatalf("read response body beyond handshake bound: %v", errReadBody)
		}
		if errCloseBody := resp.Body.Close(); errCloseBody != nil {
			t.Fatalf("close response body: %v", errCloseBody)
		}
		if resp.StatusCode != http.StatusOK || string(body) != "{}" {
			t.Fatalf("response = %d %q, want 200 {}", resp.StatusCode, body)
		}
		select {
		case errServer := <-serverDone:
			if errServer != nil {
				t.Fatalf("server completion: %v", errServer)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("TLS server did not finish")
		}
	})
}

// TestNewUtlsHTTPClientStampsConfiguredHandshakeBound verifies the config
// wiring end to end at the client seam: when
// requests.tls-handshake-timeout-seconds is positive, every request
// issued by the client carries the bound so the uTLS dial paths can read
// it; when it is 0 (or no config is given), requests stay unstamped and
// behavior is unchanged.
func TestNewUtlsHTTPClientStampsConfiguredHandshakeBound(t *testing.T) {
	capture := func(seen *time.Duration) http.RoundTripper {
		return utlsClientRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			*seen = tlsHandshakeTimeoutFromContext(req.Context())
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("{}")),
				Request:    req,
			}, nil
		})
	}
	tests := []struct {
		name    string
		seconds int
		hasCfg  bool
		want    time.Duration
	}{
		{name: "positive seconds stamp the bound", seconds: 3, hasCfg: true, want: 3 * time.Second},
		{name: "zero leaves requests unstamped", seconds: 0, hasCfg: true, want: 0},
		{name: "negative leaves requests unstamped", seconds: -2, hasCfg: true, want: 0},
		{name: "nil config leaves requests unstamped", hasCfg: false, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen := time.Duration(-1)
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", capture(&seen))
			var cfg *config.Config
			if tt.hasCfg {
				cfg = &config.Config{}
				cfg.TLSHandshakeTimeoutSeconds = tt.seconds
			}
			client := NewUtlsHTTPClient(ctx, cfg, &cliproxyauth.Auth{}, 0)
			resp, errGet := client.Get("https://api.anthropic.com/v1/messages")
			if errGet != nil {
				t.Fatalf("client.Get error = %v", errGet)
			}
			if errClose := resp.Body.Close(); errClose != nil {
				t.Fatalf("close response body: %v", errClose)
			}
			if seen != tt.want {
				t.Fatalf("request handshake bound = %v, want %v", seen, tt.want)
			}
		})
	}
}
