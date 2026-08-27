package proxyutil

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

func mustDefaultTransport(t *testing.T) *http.Transport {
	t.Helper()

	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatal("http.DefaultTransport is not an *http.Transport")
	}
	return transport
}

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    Mode
		wantErr bool
	}{
		{name: "inherit", input: "", want: ModeInherit},
		{name: "direct", input: "direct", want: ModeDirect},
		{name: "none", input: "none", want: ModeDirect},
		{name: "http", input: "http://proxy.example.com:8080", want: ModeProxy},
		{name: "https", input: "https://proxy.example.com:8443", want: ModeProxy},
		{name: "socks5", input: "socks5://proxy.example.com:1080", want: ModeProxy},
		{name: "socks5h", input: "socks5h://proxy.example.com:1080", want: ModeProxy},
		{name: "invalid", input: "bad-value", want: ModeInvalid, wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			setting, errParse := Parse(tt.input)
			if tt.wantErr && errParse == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && errParse != nil {
				t.Fatalf("unexpected error: %v", errParse)
			}
			if setting.Mode != tt.want {
				t.Fatalf("mode = %d, want %d", setting.Mode, tt.want)
			}
		})
	}
}

func TestBuildHTTPTransportDirectBypassesProxy(t *testing.T) {
	t.Parallel()

	transport, mode, errBuild := BuildHTTPTransport("direct")
	if errBuild != nil {
		t.Fatalf("BuildHTTPTransport returned error: %v", errBuild)
	}
	if mode != ModeDirect {
		t.Fatalf("mode = %d, want %d", mode, ModeDirect)
	}
	if transport == nil {
		t.Fatal("expected transport, got nil")
	}
	if transport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestBuildHTTPTransportHTTPProxy(t *testing.T) {
	t.Parallel()

	transport, mode, errBuild := BuildHTTPTransport("http://proxy.example.com:8080")
	if errBuild != nil {
		t.Fatalf("BuildHTTPTransport returned error: %v", errBuild)
	}
	if mode != ModeProxy {
		t.Fatalf("mode = %d, want %d", mode, ModeProxy)
	}
	if transport == nil {
		t.Fatal("expected transport, got nil")
	}

	req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errRequest != nil {
		t.Fatalf("http.NewRequest returned error: %v", errRequest)
	}

	proxyURL, errProxy := transport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("transport.Proxy returned error: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://proxy.example.com:8080", proxyURL)
	}

	defaultTransport := mustDefaultTransport(t)
	if transport.ForceAttemptHTTP2 != defaultTransport.ForceAttemptHTTP2 {
		t.Fatalf("ForceAttemptHTTP2 = %v, want %v", transport.ForceAttemptHTTP2, defaultTransport.ForceAttemptHTTP2)
	}
	if transport.IdleConnTimeout != defaultTransport.IdleConnTimeout {
		t.Fatalf("IdleConnTimeout = %v, want %v", transport.IdleConnTimeout, defaultTransport.IdleConnTimeout)
	}
	if transport.TLSHandshakeTimeout != defaultTransport.TLSHandshakeTimeout {
		t.Fatalf("TLSHandshakeTimeout = %v, want %v", transport.TLSHandshakeTimeout, defaultTransport.TLSHandshakeTimeout)
	}
}

func TestBuildHTTPTransportSOCKS5ProxyInheritsDefaultTransportSettings(t *testing.T) {
	t.Parallel()

	transport, mode, errBuild := BuildHTTPTransport("socks5://proxy.example.com:1080")
	if errBuild != nil {
		t.Fatalf("BuildHTTPTransport returned error: %v", errBuild)
	}
	if mode != ModeProxy {
		t.Fatalf("mode = %d, want %d", mode, ModeProxy)
	}
	if transport == nil {
		t.Fatal("expected transport, got nil")
	}
	if transport.Proxy != nil {
		t.Fatal("expected SOCKS5 transport to bypass http proxy function")
	}

	defaultTransport := mustDefaultTransport(t)
	if transport.ForceAttemptHTTP2 != defaultTransport.ForceAttemptHTTP2 {
		t.Fatalf("ForceAttemptHTTP2 = %v, want %v", transport.ForceAttemptHTTP2, defaultTransport.ForceAttemptHTTP2)
	}
	if transport.IdleConnTimeout != defaultTransport.IdleConnTimeout {
		t.Fatalf("IdleConnTimeout = %v, want %v", transport.IdleConnTimeout, defaultTransport.IdleConnTimeout)
	}
	if transport.TLSHandshakeTimeout != defaultTransport.TLSHandshakeTimeout {
		t.Fatalf("TLSHandshakeTimeout = %v, want %v", transport.TLSHandshakeTimeout, defaultTransport.TLSHandshakeTimeout)
	}
}

func TestBuildHTTPTransportSOCKS5HProxy(t *testing.T) {
	t.Parallel()

	transport, mode, errBuild := BuildHTTPTransport("socks5h://proxy.example.com:1080")
	if errBuild != nil {
		t.Fatalf("BuildHTTPTransport returned error: %v", errBuild)
	}
	if mode != ModeProxy {
		t.Fatalf("mode = %d, want %d", mode, ModeProxy)
	}
	if transport == nil {
		t.Fatal("expected transport, got nil")
	}
	if transport.Proxy != nil {
		t.Fatal("expected SOCKS5H transport to bypass http proxy function")
	}
	if transport.DialContext == nil {
		t.Fatal("expected SOCKS5H transport to have custom DialContext")
	}
}

func TestBuildDialerHTTPProxyCONNECT(t *testing.T) {
	t.Parallel()

	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("net.Listen returned error: %v", errListen)
	}
	defer func() {
		if errClose := listener.Close(); errClose != nil {
			t.Errorf("listener.Close returned error: %v", errClose)
		}
	}()

	done := make(chan error, 1)
	go func() {
		conn, errAccept := listener.Accept()
		if errAccept != nil {
			done <- errAccept
			return
		}
		defer func() { _ = conn.Close() }()
		if errDeadline := conn.SetDeadline(time.Now().Add(5 * time.Second)); errDeadline != nil {
			done <- errDeadline
			return
		}

		req, errRead := http.ReadRequest(bufio.NewReader(conn))
		if errRead != nil {
			done <- fmt.Errorf("read CONNECT request failed: %w", errRead)
			return
		}
		if req.Method != http.MethodConnect {
			done <- fmt.Errorf("method = %s, want CONNECT", req.Method)
			return
		}
		if req.Host != "target.example.com:443" {
			done <- fmt.Errorf("host = %s, want target.example.com:443", req.Host)
			return
		}
		wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))
		if gotAuth := req.Header.Get("Proxy-Authorization"); gotAuth != wantAuth {
			done <- fmt.Errorf("Proxy-Authorization = %q, want %q", gotAuth, wantAuth)
			return
		}

		if _, errWrite := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\nok"); errWrite != nil {
			done <- fmt.Errorf("write CONNECT response failed: %w", errWrite)
			return
		}

		buf := make([]byte, 4)
		n, errReadTunnel := io.ReadFull(conn, buf)
		if errReadTunnel != nil {
			done <- fmt.Errorf("read tunneled payload failed after %d bytes: %w", n, errReadTunnel)
			return
		}
		if string(buf) != "ping" {
			done <- fmt.Errorf("tunneled payload = %q, want ping", string(buf))
			return
		}
		done <- nil
	}()

	dialer, mode, errBuild := BuildDialer("http://user:pass@" + listener.Addr().String())
	if errBuild != nil {
		t.Fatalf("BuildDialer returned error: %v", errBuild)
	}
	if mode != ModeProxy {
		t.Fatalf("mode = %d, want %d", mode, ModeProxy)
	}
	if dialer == nil {
		t.Fatal("expected dialer, got nil")
	}

	conn, errDial := dialer.Dial("tcp", "target.example.com:443")
	if errDial != nil {
		t.Fatalf("dialer.Dial returned error: %v", errDial)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Errorf("conn.Close returned error: %v", errClose)
		}
	}()

	buf := make([]byte, 2)
	n, errRead := io.ReadFull(conn, buf)
	if errRead != nil {
		t.Fatalf("conn.Read returned error after %d bytes: %v", n, errRead)
	}
	if string(buf) != "ok" {
		t.Fatalf("buffered tunnel payload = %q, want ok", string(buf))
	}

	if _, errWrite := conn.Write([]byte("ping")); errWrite != nil {
		t.Fatalf("conn.Write returned error: %v", errWrite)
	}

	if errServer := <-done; errServer != nil {
		t.Fatalf("proxy server returned error: %v", errServer)
	}
}

func TestBuildDialerHTTPProxyCONNECTCancellation(t *testing.T) {
	t.Parallel()

	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("net.Listen returned error: %v", errListen)
	}
	defer func() { _ = listener.Close() }()
	requestRead := make(chan struct{})
	serverDone := make(chan error, 1)
	go func() {
		connection, errAccept := listener.Accept()
		if errAccept != nil {
			serverDone <- errAccept
			return
		}
		defer func() { _ = connection.Close() }()
		if _, errRead := http.ReadRequest(bufio.NewReader(connection)); errRead != nil {
			serverDone <- errRead
			return
		}
		close(requestRead)
		if errDeadline := connection.SetReadDeadline(time.Now().Add(5 * time.Second)); errDeadline != nil {
			serverDone <- errDeadline
			return
		}
		var buffer [1]byte
		_, errRead := connection.Read(buffer[:])
		serverDone <- errRead
	}()

	dialer, mode, errBuild := BuildDialer("http://" + listener.Addr().String())
	if errBuild != nil || mode != ModeProxy {
		t.Fatalf("BuildDialer mode=%d error=%v", mode, errBuild)
	}
	contextDialer, ok := dialer.(interface {
		DialContext(context.Context, string, string) (net.Conn, error)
	})
	if !ok {
		t.Fatal("HTTP CONNECT dialer does not support context cancellation")
	}
	ctx, cancel := context.WithCancel(context.Background())
	dialDone := make(chan error, 1)
	go func() {
		connection, errDial := contextDialer.DialContext(ctx, "tcp", "20.42.0.20:443")
		if connection != nil {
			_ = connection.Close()
		}
		dialDone <- errDial
	}()
	select {
	case <-requestRead:
	case <-time.After(time.Second):
		t.Fatal("proxy did not receive CONNECT request")
	}
	cancel()
	select {
	case errDial := <-dialDone:
		if errDial == nil {
			t.Fatal("canceled CONNECT dial returned nil error")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled CONNECT dial did not return")
	}
	select {
	case errServer := <-serverDone:
		if errServer == nil {
			t.Fatal("proxy connection stayed open after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("proxy connection was not closed after cancellation")
	}
}

func TestRedactProxyURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "with credentials",
			input: "http://user:pass@proxy.example.com:8080/path?token=secret",
			want:  "http://redacted@proxy.example.com:8080",
		},
		{
			name:  "without credentials",
			input: "socks5://proxy.example.com:1080",
			want:  "socks5://proxy.example.com:1080",
		},
		{
			name:  "invalid",
			input: "bad-value",
			want:  "<invalid proxy URL>",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := Redact(tt.input); got != tt.want {
				t.Fatalf("Redact() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseErrorDoesNotExposeProxyCredentials(t *testing.T) {
	t.Parallel()

	input := "http://user:secret%@proxy.example.com:8080"
	_, errParse := Parse(input)
	if errParse == nil {
		t.Fatal("expected Parse to return an error")
	}
	if strings.Contains(errParse.Error(), input) ||
		strings.Contains(errParse.Error(), "user") ||
		strings.Contains(errParse.Error(), "secret") {
		t.Fatalf("parse error exposes proxy credentials: %q", errParse.Error())
	}
}

func TestBuildHTTPTransportHTTPSProxyUsesCONNECTDialer(t *testing.T) {
	t.Parallel()

	transport, mode, errBuild := BuildHTTPTransport("https://proxy.example.com:8443")
	if errBuild != nil {
		t.Fatalf("BuildHTTPTransport returned error: %v", errBuild)
	}
	if mode != ModeProxy {
		t.Fatalf("mode = %d, want %d", mode, ModeProxy)
	}
	if transport == nil {
		t.Fatal("expected transport, got nil")
	}
	if transport.Proxy != nil {
		t.Fatal("expected HTTPS proxy transport to bypass http proxy function")
	}
	if transport.DialContext == nil {
		t.Fatal("expected HTTPS proxy transport to have custom DialContext")
	}
}

// TestBuildHTTPTransportHTTPSProxyTunnelsThroughH2CapableProxy pins the reason the HTTPS
// proxy branch cannot use http.Transport.Proxy: Go reuses TLSClientConfig (whose NextProtos
// carry "h2" once HTTP/2 is configured) for the handshake with the proxy itself, then writes
// an HTTP/1.1 CONNECT over whatever was negotiated. A proxy that selects h2 receives a
// malformed stream and drops the connection, surfacing as "unexpected EOF".
func TestBuildHTTPTransportHTTPSProxyTunnelsThroughH2CapableProxy(t *testing.T) {
	t.Parallel()

	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, errWrite := io.WriteString(w, "pong"); errWrite != nil {
			t.Errorf("target write failed: %v", errWrite)
		}
	}))
	defer target.Close()

	var negotiatedProtocol atomic.Value
	proxyServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			negotiatedProtocol.Store(r.TLS.NegotiatedProtocol)
		}
		if r.Method != http.MethodConnect {
			http.Error(w, "expected CONNECT", http.StatusMethodNotAllowed)
			return
		}
		wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))
		if r.Header.Get("Proxy-Authorization") != wantAuth {
			http.Error(w, "missing proxy credentials", http.StatusProxyAuthRequired)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "connection is not hijackable", http.StatusInternalServerError)
			return
		}
		upstreamConn, errDial := net.Dial("tcp", r.Host)
		if errDial != nil {
			http.Error(w, "dial upstream failed", http.StatusBadGateway)
			return
		}
		clientConn, buffered, errHijack := hijacker.Hijack()
		if errHijack != nil {
			_ = upstreamConn.Close()
			return
		}
		if _, errWrite := io.WriteString(clientConn, "HTTP/1.1 200 Connection Established\r\n\r\n"); errWrite != nil {
			_ = upstreamConn.Close()
			_ = clientConn.Close()
			return
		}
		go func() {
			defer func() { _ = upstreamConn.Close() }()
			_, _ = io.Copy(upstreamConn, buffered)
		}()
		go func() {
			defer func() { _ = clientConn.Close() }()
			_, _ = io.Copy(clientConn, upstreamConn)
		}()
	}))
	// Mirror a real HTTPS proxy: h2 is offered and preferred, http/1.1 stays available.
	proxyServer.EnableHTTP2 = true
	proxyServer.TLS = &tls.Config{NextProtos: []string{"h2", "http/1.1"}}
	proxyServer.StartTLS()
	defer proxyServer.Close()

	proxyURL, errParseProxy := url.Parse(proxyServer.URL)
	if errParseProxy != nil {
		t.Fatalf("url.Parse returned error: %v", errParseProxy)
	}

	transport, mode, errBuild := BuildHTTPTransport("https://user:pass@" + proxyURL.Host)
	if errBuild != nil {
		t.Fatalf("BuildHTTPTransport returned error: %v", errBuild)
	}
	if mode != ModeProxy {
		t.Fatalf("mode = %d, want %d", mode, ModeProxy)
	}
	proxyClientTransport, ok := proxyServer.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatal("httptest proxy client transport is not an *http.Transport")
	}
	// The httptest servers share one self-signed certificate, so a single pool covers both legs.
	transport.TLSClientConfig = proxyClientTransport.TLSClientConfig.Clone()
	defer transport.CloseIdleConnections()

	response, errGet := (&http.Client{Transport: transport, Timeout: 10 * time.Second}).Get(target.URL)
	if errGet != nil {
		t.Fatalf("request through HTTPS proxy failed: %v", errGet)
	}
	defer func() {
		if errClose := response.Body.Close(); errClose != nil {
			t.Errorf("response.Body.Close returned error: %v", errClose)
		}
	}()

	body, errRead := io.ReadAll(response.Body)
	if errRead != nil {
		t.Fatalf("io.ReadAll returned error: %v", errRead)
	}
	if string(body) != "pong" {
		t.Fatalf("body = %q, want pong", string(body))
	}
	if got, _ := negotiatedProtocol.Load().(string); got == "h2" {
		t.Fatal("CONNECT was sent over an h2 connection; ALPN must stay on http/1.1 for the proxy leg")
	}
}

// TestBuildHTTPTransportHTTPSProxyBoundsTLSHandshake guards the proxy-setup timeouts that
// move out of http.Transport's reach once the tunnel is established inside DialContext:
// a proxy that accepts the TCP connection and then stalls must not hang the caller.
func TestBuildHTTPTransportHTTPSProxyBoundsTLSHandshake(t *testing.T) {
	t.Parallel()

	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("net.Listen returned error: %v", errListen)
	}
	defer func() {
		if errClose := listener.Close(); errClose != nil {
			t.Errorf("listener.Close returned error: %v", errClose)
		}
	}()
	go func() {
		conn, errAccept := listener.Accept()
		if errAccept != nil {
			return
		}
		// Accept and stall: never complete the TLS handshake.
		<-time.After(30 * time.Second)
		_ = conn.Close()
	}()

	transport, _, errBuild := BuildHTTPTransport("https://" + listener.Addr().String())
	if errBuild != nil {
		t.Fatalf("BuildHTTPTransport returned error: %v", errBuild)
	}
	transport.TLSHandshakeTimeout = 200 * time.Millisecond
	defer transport.CloseIdleConnections()

	dialDone := make(chan error, 1)
	go func() {
		conn, errDial := transport.DialContext(context.Background(), "tcp", "target.example.com:443")
		if conn != nil {
			_ = conn.Close()
		}
		dialDone <- errDial
	}()

	select {
	case errDial := <-dialDone:
		if errDial == nil {
			t.Fatal("dial through a stalled HTTPS proxy returned nil error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dial through a stalled HTTPS proxy was not bounded by TLSHandshakeTimeout")
	}
}

// TestBuildDialerHTTPProxyBoundsCONNECTResponse pins the guard net/http applies in
// dialConn: a proxy that takes the connection and then never answers CONNECT must not
// hold a caller whose context has no deadline.
func TestBuildDialerHTTPProxyBoundsCONNECTResponse(t *testing.T) {
	t.Parallel()

	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("net.Listen returned error: %v", errListen)
	}
	defer func() {
		if errClose := listener.Close(); errClose != nil {
			t.Errorf("listener.Close returned error: %v", errClose)
		}
	}()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, errAccept := listener.Accept()
		if errAccept != nil {
			return
		}
		accepted <- conn
		// Read the CONNECT request, then go quiet — never write a response.
		_, _ = http.ReadRequest(bufio.NewReader(conn))
	}()
	defer func() {
		select {
		case conn := <-accepted:
			_ = conn.Close()
		default:
		}
	}()

	proxyURL, errParse := url.Parse("http://" + listener.Addr().String())
	if errParse != nil {
		t.Fatalf("url.Parse returned error: %v", errParse)
	}
	// Built directly rather than via BuildDialer: the bound is a field, so the test can
	// carry a short one instead of waiting out the production minute.
	dialer := &httpConnectDialer{
		proxyURL:       proxyURL,
		dialer:         proxy.Direct,
		connectTimeout: 200 * time.Millisecond,
	}

	dialDone := make(chan error, 1)
	go func() {
		conn, errDial := dialer.DialContext(context.Background(), "tcp", "target.example.com:443")
		if conn != nil {
			_ = conn.Close()
		}
		dialDone <- errDial
	}()

	select {
	case errDial := <-dialDone:
		if errDial == nil {
			t.Fatal("dial through a silent proxy returned nil error")
		}
		if !errors.Is(errDial, os.ErrDeadlineExceeded) {
			t.Fatalf("error = %v, want it to wrap os.ErrDeadlineExceeded", errDial)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dial through a silent proxy was not bounded by connectTimeout")
	}
}

// TestBuildDialerHTTPProxyCarriesCONNECTTimeout keeps the wiring honest: the guard above
// is only worth anything if the constructor actually installs it.
func TestBuildDialerHTTPProxyCarriesCONNECTTimeout(t *testing.T) {
	t.Parallel()

	dialer, _, errBuild := BuildDialer("http://proxy.example.com:8080")
	if errBuild != nil {
		t.Fatalf("BuildDialer returned error: %v", errBuild)
	}
	connectDialer, ok := dialer.(*httpConnectDialer)
	if !ok {
		t.Fatalf("dialer is %T, want *httpConnectDialer", dialer)
	}
	if connectDialer.connectTimeout != defaultProxyConnectTimeout {
		t.Fatalf("connectTimeout = %v, want %v", connectDialer.connectTimeout, defaultProxyConnectTimeout)
	}
}

// TestBuildDialerHTTPProxyClearsCONNECTDeadline guards the failure mode that would be
// worst in production and quietest in review: the CONNECT deadline is set on the socket
// the caller goes on to use, so failing to clear it would cut real traffic off at
// whatever was left of the bound, mid-stream and with no proxy involved.
func TestBuildDialerHTTPProxyClearsCONNECTDeadline(t *testing.T) {
	t.Parallel()

	const connectTimeout = 200 * time.Millisecond

	target, errTarget := net.Listen("tcp", "127.0.0.1:0")
	if errTarget != nil {
		t.Fatalf("net.Listen returned error: %v", errTarget)
	}
	defer func() { _ = target.Close() }()
	go func() {
		conn, errAccept := target.Accept()
		if errAccept != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = io.Copy(conn, conn)
	}()

	proxyListener, errProxy := net.Listen("tcp", "127.0.0.1:0")
	if errProxy != nil {
		t.Fatalf("net.Listen returned error: %v", errProxy)
	}
	defer func() { _ = proxyListener.Close() }()
	go func() {
		clientConn, errAccept := proxyListener.Accept()
		if errAccept != nil {
			return
		}
		defer func() { _ = clientConn.Close() }()
		request, errRead := http.ReadRequest(bufio.NewReader(clientConn))
		if errRead != nil {
			return
		}
		upstreamConn, errDial := net.Dial("tcp", request.Host)
		if errDial != nil {
			return
		}
		defer func() { _ = upstreamConn.Close() }()
		if _, errWrite := io.WriteString(clientConn, "HTTP/1.1 200 Connection Established\r\n\r\n"); errWrite != nil {
			return
		}
		go func() { _, _ = io.Copy(upstreamConn, clientConn) }()
		_, _ = io.Copy(clientConn, upstreamConn)
	}()

	proxyURL, errParse := url.Parse("http://" + proxyListener.Addr().String())
	if errParse != nil {
		t.Fatalf("url.Parse returned error: %v", errParse)
	}
	dialer := &httpConnectDialer{
		proxyURL:       proxyURL,
		dialer:         proxy.Direct,
		connectTimeout: connectTimeout,
	}

	conn, errDial := dialer.DialContext(context.Background(), "tcp", target.Addr().String())
	if errDial != nil {
		t.Fatalf("dialer.DialContext returned error: %v", errDial)
	}
	defer func() { _ = conn.Close() }()

	// Idle past the bound, then use the tunnel. A deadline left over from CONNECT
	// expires here and turns this into an i/o timeout.
	<-time.After(2 * connectTimeout)

	if _, errWrite := conn.Write([]byte("ping")); errWrite != nil {
		t.Fatalf("write after the CONNECT bound elapsed failed: %v", errWrite)
	}
	buf := make([]byte, 4)
	if _, errRead := io.ReadFull(conn, buf); errRead != nil {
		t.Fatalf("read after the CONNECT bound elapsed failed: %v", errRead)
	}
	if string(buf) != "ping" {
		t.Fatalf("tunnelled payload = %q, want ping", string(buf))
	}
}
