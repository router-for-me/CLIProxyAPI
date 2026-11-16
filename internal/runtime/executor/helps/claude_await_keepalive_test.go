package helps

import (
	"bufio"
	"bytes"
	"context"
	gotls "crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// pipeRecorder drains the server end of a net.Pipe so client writes never
// block, and exposes everything received so far.
type pipeRecorder struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func recordPipe(t *testing.T, server net.Conn) *pipeRecorder {
	t.Helper()
	rec := &pipeRecorder{}
	go func() {
		chunk := make([]byte, 256)
		for {
			n, errRead := server.Read(chunk)
			if n > 0 {
				rec.mu.Lock()
				rec.buf.Write(chunk[:n])
				rec.mu.Unlock()
			}
			if errRead != nil {
				return
			}
		}
	}()
	return rec
}

func (r *pipeRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

func identityConn(conn net.Conn) net.Conn { return conn }

func newAwaitKeepAlivePipe(t *testing.T, interval time.Duration) (net.Conn, net.Conn, *pipeRecorder) {
	t.Helper()
	client, server := net.Pipe()
	conn, ok := newAwaitKeepAliveWire(client, interval, identityConn).(*awaitKeepAliveReuseGuard)
	if !ok {
		t.Fatalf("newAwaitKeepAliveWire did not wrap the connection")
	}
	rec := recordPipe(t, server)
	t.Cleanup(func() {
		_ = conn.Close()
		_ = server.Close()
	})
	return conn, server, rec
}

func TestAwaitKeepAliveConnSendsBlankLinesUntilFirstResponseByte(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const interval = 40 * time.Second
		conn, server, rec := newAwaitKeepAlivePipe(t, interval)

		if _, errWrite := conn.Write([]byte("REQ")); errWrite != nil {
			t.Fatalf("write request: %v", errWrite)
		}
		time.Sleep(interval - time.Second)
		synctest.Wait()
		if got := rec.String(); got != "REQ" {
			t.Fatalf("before interval got %q, want %q", got, "REQ")
		}
		time.Sleep(interval + 2*time.Second)
		synctest.Wait()
		if got := rec.String(); got != "REQ\r\n\r\n" {
			t.Fatalf("after two intervals got %q, want %q", got, "REQ\r\n\r\n")
		}

		go func() { _, _ = server.Write([]byte("H")) }()
		if _, errRead := conn.Read(make([]byte, 1)); errRead != nil {
			t.Fatalf("read response byte: %v", errRead)
		}
		time.Sleep(5 * interval)
		synctest.Wait()
		if got := rec.String(); got != "REQ\r\n\r\n" {
			t.Fatalf("after first response byte got %q, want no more keepalives", got)
		}
	})
}

func TestAwaitKeepAliveConnNeverInterleavesWithRequestWrites(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const interval = 40 * time.Second
		conn, _, rec := newAwaitKeepAlivePipe(t, interval)

		for _, part := range []string{"A", "B", "C"} {
			if _, errWrite := conn.Write([]byte(part)); errWrite != nil {
				t.Fatalf("write %q: %v", part, errWrite)
			}
			time.Sleep(interval - time.Second)
		}
		synctest.Wait()
		if got := rec.String(); got != "ABC" {
			t.Fatalf("while request is written got %q, want %q", got, "ABC")
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if got := rec.String(); got != "ABC\r\n" {
			t.Fatalf("after an idle interval got %q, want %q", got, "ABC\r\n")
		}
	})
}

func TestAwaitKeepAliveConnStopsOnClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const interval = 40 * time.Second
		conn, _, rec := newAwaitKeepAlivePipe(t, interval)

		if _, errWrite := conn.Write([]byte("REQ")); errWrite != nil {
			t.Fatalf("write request: %v", errWrite)
		}
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close: %v", errClose)
		}
		time.Sleep(3 * interval)
		synctest.Wait()
		if got := rec.String(); got != "REQ" {
			t.Fatalf("after close got %q, want %q", got, "REQ")
		}
	})
}

func TestAwaitKeepAliveConnRejectsReuseAfterKeepalive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const interval = 40 * time.Second
		conn, server, rec := newAwaitKeepAlivePipe(t, interval)

		if _, errWrite := conn.Write([]byte("REQ")); errWrite != nil {
			t.Fatalf("write request: %v", errWrite)
		}
		time.Sleep(interval + time.Second)
		synctest.Wait()
		go func() { _, _ = server.Write([]byte("H")) }()
		if _, errRead := conn.Read(make([]byte, 1)); errRead != nil {
			t.Fatalf("read response byte: %v", errRead)
		}

		n, errWrite := conn.Write([]byte("NEXT"))
		if !errors.Is(errWrite, errClaudeAwaitKeepAliveConnReused) || n != 0 {
			t.Fatalf("reuse write = (%d, %v), want (0, %v)", n, errWrite, errClaudeAwaitKeepAliveConnReused)
		}
		synctest.Wait()
		if got := rec.String(); got != "REQ\r\n" {
			t.Fatalf("reused connection got %q, want %q", got, "REQ\r\n")
		}
	})
}

func TestAwaitKeepAliveConnKeepsReuseWithoutKeepalive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const interval = 40 * time.Second
		conn, server, rec := newAwaitKeepAlivePipe(t, interval)

		if _, errWrite := conn.Write([]byte("REQ")); errWrite != nil {
			t.Fatalf("write request: %v", errWrite)
		}
		go func() { _, _ = server.Write([]byte("H")) }()
		if _, errRead := conn.Read(make([]byte, 1)); errRead != nil {
			t.Fatalf("read response byte: %v", errRead)
		}
		if _, errWrite := conn.Write([]byte("NEXT")); errWrite != nil {
			t.Fatalf("reuse without keepalive: %v", errWrite)
		}
		synctest.Wait()
		if got := rec.String(); got != "REQNEXT" {
			t.Fatalf("got %q, want %q", got, "REQNEXT")
		}
	})
}

func TestNewAwaitKeepAliveWireDisabledReturnsConn(t *testing.T) {
	client, server := net.Pipe()
	defer func() {
		_ = client.Close()
		_ = server.Close()
	}()
	for _, interval := range []time.Duration{0, -time.Second} {
		if got := newAwaitKeepAliveWire(client, interval, identityConn); got != client {
			t.Fatalf("interval %v wrapped the connection", interval)
		}
	}
}

// TestClaudeCodeTransportAwaitKeepAliveEndToEnd drives the real Claude Code
// transport (uTLS, header reordering, net/http connection pooling) against a
// TLS server that delays its first response byte past two keepalive
// intervals. The server must see only blank lines after the complete
// request, the response must arrive intact, and the next request must be
// replayed on a fresh connection instead of following the blank lines.
func TestClaudeCodeTransportAwaitKeepAliveEndToEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const interval = 40 * time.Second
		certificate := newResumptionTestCertificateAt(t, time.Now())
		roots := x509.NewCertPool()
		roots.AddCert(certificate.Leaf)

		type serverResult struct {
			body      string
			trailing  string
			nextBytes int
			err       error
		}
		results := make(chan serverResult, 2)
		var serverConns []net.Conn
		var dials int
		var dialMu sync.Mutex
		dialer := contextDialerFunc(func(context.Context, string, string) (net.Conn, error) {
			clientConn, serverConn := net.Pipe()
			dialMu.Lock()
			dials++
			delayed := dials == 1
			serverConns = append(serverConns, serverConn)
			dialMu.Unlock()
			go func() {
				results <- serveAwaitKeepAliveConn(serverConn, certificate, delayed, interval)
			}()
			return clientConn, nil
		})
		t.Cleanup(func() {
			dialMu.Lock()
			defer dialMu.Unlock()
			for _, conn := range serverConns {
				_ = conn.Close()
			}
		})

		transport := newClaudeCodeRoundTripperForDialerAndRoots(dialer, roots)
		transport.MaxIdleConnsPerHost = 4 // pooled credentials reuse connections
		client := &http.Client{Transport: &claudeAwaitKeepAliveRoundTripper{base: transport, interval: interval}}

		post := func(body string) (string, error) {
			req, errReq := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(body))
			if errReq != nil {
				return "", errReq
			}
			req.Header.Set("Content-Type", "application/json")
			resp, errDo := client.Do(req)
			if errDo != nil {
				return "", errDo
			}
			defer func() { _ = resp.Body.Close() }()
			data, errRead := io.ReadAll(resp.Body)
			return string(data), errRead
		}

		got, errFirst := post(`{"n":1}`)
		if errFirst != nil || got != `{"ok":1}` {
			t.Fatalf("first response = %q, %v; want {\"ok\":1}", got, errFirst)
		}
		got, errSecond := post(`{"n":2}`)
		if errSecond != nil || got != `{"ok":2}` {
			t.Fatalf("second response = %q, %v; want {\"ok\":2}", got, errSecond)
		}
		transport.CloseIdleConnections()

		first := <-results
		if first.err != nil {
			t.Fatalf("delayed server: %v", first.err)
		}
		if first.body != `{"n":1}` {
			t.Fatalf("delayed server body = %q, want {\"n\":1}", first.body)
		}
		if first.trailing != "\r\n\r\n" {
			t.Fatalf("delayed server trailing bytes = %q, want two blank lines", first.trailing)
		}
		if first.nextBytes != 0 {
			t.Fatalf("delayed connection received %d bytes of a reused request", first.nextBytes)
		}
		second := <-results
		if second.err != nil || second.body != `{"n":2}` {
			t.Fatalf("fresh server = %+v, want body {\"n\":2}", second)
		}
		dialMu.Lock()
		defer dialMu.Unlock()
		if dials != 2 {
			t.Fatalf("dials = %d, want 2 (second request replayed on a fresh connection)", dials)
		}
	})
}

// serveAwaitKeepAliveConn answers one request. When delayed, it waits two
// keepalive intervals before the response and records the bytes received
// meanwhile, then counts any bytes that arrive afterwards.
func serveAwaitKeepAliveConn(raw net.Conn, certificate gotls.Certificate, delayed bool, interval time.Duration) (res struct {
	body      string
	trailing  string
	nextBytes int
	err       error
}) {
	server := gotls.Server(raw, &gotls.Config{
		Certificates: []gotls.Certificate{certificate},
		MinVersion:   gotls.VersionTLS13,
		NextProtos:   []string{"http/1.1"},
	})
	if errHandshake := server.Handshake(); errHandshake != nil {
		res.err = fmt.Errorf("handshake: %w", errHandshake)
		return res
	}
	reader := bufio.NewReader(server)
	req, errReq := http.ReadRequest(reader)
	if errReq != nil {
		res.err = fmt.Errorf("read request: %w", errReq)
		return res
	}
	body, errBody := io.ReadAll(req.Body)
	if errBody != nil {
		res.err = fmt.Errorf("read body: %w", errBody)
		return res
	}
	res.body = string(body)
	n := strings.TrimPrefix(strings.TrimSuffix(res.body, "}"), `{"n":`)
	if delayed {
		trailing := make([]byte, 4)
		deadline := time.Now().Add(2*interval + time.Second)
		if _, errRead := io.ReadFull(reader, trailing); errRead != nil {
			res.err = fmt.Errorf("read keepalive lines: %w", errRead)
			return res
		}
		res.trailing = string(trailing)
		if wait := time.Until(deadline); wait > 0 {
			time.Sleep(wait)
		}
	}
	response := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n{\"ok\":%s}", len(n)+7, n)
	if _, errWrite := io.WriteString(server, response); errWrite != nil {
		res.err = fmt.Errorf("write response: %w", errWrite)
		return res
	}
	if delayed {
		// A reused request would arrive here; a rejected reuse closes the
		// connection instead, which ends the read with close_notify or EOF.
		next, _ := io.Copy(io.Discard, reader)
		res.nextBytes = int(next)
	}
	return res
}

// TestNewUtlsHTTPClientStampsConfiguredAwaitKeepAlive verifies the config
// wiring at the client seam.
func TestNewUtlsHTTPClientStampsConfiguredAwaitKeepAlive(t *testing.T) {
	tests := []struct {
		name    string
		seconds int
		hasCfg  bool
		want    time.Duration
	}{
		{name: "positive seconds stamp the interval", seconds: 40, hasCfg: true, want: 40 * time.Second},
		{name: "zero leaves requests unstamped", seconds: 0, hasCfg: true, want: 0},
		{name: "negative leaves requests unstamped", seconds: -3, hasCfg: true, want: 0},
		{name: "nil config leaves requests unstamped", hasCfg: false, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen := time.Duration(-1)
			capture := utlsClientRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				seen = claudeAwaitKeepAliveFromContext(req.Context())
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}")), Request: req}, nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(capture))
			var cfg *config.Config
			if tt.hasCfg {
				cfg = &config.Config{}
				cfg.ClaudeAwaitKeepAliveSeconds = tt.seconds
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
				t.Fatalf("request keepalive interval = %v, want %v", seen, tt.want)
			}
		})
	}
}
