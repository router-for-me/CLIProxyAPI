package proxyutil

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
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

func TestBuildHTTPTransportHTTPSProxyInheritsDefaultTransportSettings(t *testing.T) {
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
	if transport.Proxy == nil {
		t.Fatal("expected HTTPS proxy transport to retain Proxy function")
	}
	if transport.DialTLSContext == nil {
		t.Fatal("expected HTTPS proxy transport to configure custom DialTLSContext")
	}

	req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errRequest != nil {
		t.Fatalf("http.NewRequest returned error: %v", errRequest)
	}
	proxyURL, errProxy := transport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("transport.Proxy returned error: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "https://proxy.example.com:8443" {
		t.Fatalf("proxy URL = %v, want https://proxy.example.com:8443", proxyURL)
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

func newTestCertificate(t *testing.T) tls.Certificate {
	t.Helper()

	key, errKey := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if errKey != nil {
		t.Fatalf("generate test key: %v", errKey)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
	}
	der, errCreate := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if errCreate != nil {
		t.Fatalf("create test certificate: %v", errCreate)
	}
	leaf, errParse := x509.ParseCertificate(der)
	if errParse != nil {
		t.Fatalf("parse test certificate: %v", errParse)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

func TestBuildHTTPTransportHTTPSProxyH2Negotiation(t *testing.T) {
	t.Parallel()

	targetServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "target ok")
	}))
	defer targetServer.Close()

	proxyCert := newTestCertificate(t)
	proxyListener, errListen := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{proxyCert},
		NextProtos:   []string{"h2", "http/1.1"},
	})
	if errListen != nil {
		t.Fatalf("tls.Listen returned error: %v", errListen)
	}
	defer func() {
		if errClose := proxyListener.Close(); errClose != nil {
			t.Errorf("proxyListener.Close returned error: %v", errClose)
		}
	}()

	proxyDone := make(chan error, 1)
	go func() {
		conn, errAccept := proxyListener.Accept()
		if errAccept != nil {
			proxyDone <- errAccept
			return
		}
		defer func() { _ = conn.Close() }()

		tlsConn, ok := conn.(*tls.Conn)
		if !ok {
			proxyDone <- errors.New("conn is not *tls.Conn")
			return
		}
		if errHandshake := tlsConn.Handshake(); errHandshake != nil {
			proxyDone <- fmt.Errorf("tls handshake failed: %w", errHandshake)
			return
		}

		// When ALPN negotiates h2, a proxy expecting HTTP/2 frames will reject
		// plain HTTP/1.1 CONNECT requests (reproducing unexpected EOF / bogus greeting).
		if tlsConn.ConnectionState().NegotiatedProtocol == "h2" {
			proxyDone <- fmt.Errorf("proxy negotiated h2 ALPN: client must negotiate http/1.1 for CONNECT proxy leg")
			return
		}

		req, errRead := http.ReadRequest(bufio.NewReader(tlsConn))
		if errRead != nil {
			proxyDone <- fmt.Errorf("read CONNECT request failed: %w", errRead)
			return
		}
		if req.Method != http.MethodConnect {
			proxyDone <- fmt.Errorf("method = %s, want CONNECT", req.Method)
			return
		}

		targetURL, _ := url.Parse(targetServer.URL)
		targetConn, errDial := net.Dial("tcp", targetURL.Host)
		if errDial != nil {
			proxyDone <- fmt.Errorf("dial target failed: %w", errDial)
			return
		}
		defer func() { _ = targetConn.Close() }()

		if _, errWrite := io.WriteString(tlsConn, "HTTP/1.1 200 Connection Established\r\n\r\n"); errWrite != nil {
			proxyDone <- fmt.Errorf("write CONNECT response failed: %w", errWrite)
			return
		}

		go func() {
			_, _ = io.Copy(targetConn, tlsConn)
			_ = targetConn.Close()
		}()
		_, _ = io.Copy(tlsConn, targetConn)
		_ = tlsConn.Close()
		proxyDone <- nil
	}()

	proxyURL := "https://" + proxyListener.Addr().String()
	transport, mode, errBuild := BuildHTTPTransport(proxyURL)
	if errBuild != nil {
		t.Fatalf("BuildHTTPTransport returned error: %v", errBuild)
	}
	if mode != ModeProxy {
		t.Fatalf("mode = %d, want %d", mode, ModeProxy)
	}

	// Trust proxy cert in DialTLSContext and target cert in TLSClientConfig
	certPool := x509.NewCertPool()
	certPool.AddCert(proxyCert.Leaf)
	parsedProxyURL, _ := url.Parse(proxyURL)
	transport.DialTLSContext = buildHTTPSProxyDialTLSContext(parsedProxyURL, &tls.Config{RootCAs: certPool}, transport.TLSHandshakeTimeout, transport.DialContext)

	targetCertPool := x509.NewCertPool()
	for _, cert := range targetServer.TLS.Certificates {
		if x509Cert, errParse := x509.ParseCertificate(cert.Certificate[0]); errParse == nil {
			targetCertPool.AddCert(x509Cert)
		}
	}
	transport.TLSClientConfig = &tls.Config{RootCAs: targetCertPool}

	client := &http.Client{
		Transport: transport,
		Timeout:   5 * time.Second,
	}

	req, errReq := http.NewRequest(http.MethodGet, targetServer.URL, nil)
	if errReq != nil {
		t.Fatalf("http.NewRequest returned error: %v", errReq)
	}
	req.Close = true

	resp, errGet := client.Do(req)
	if errGet != nil {
		t.Fatalf("client.Do returned error: %v", errGet)
	}
	defer func() { _ = resp.Body.Close() }()

	body, errBody := io.ReadAll(resp.Body)
	if errBody != nil {
		t.Fatalf("read response body failed: %v", errBody)
	}
	if string(body) != "target ok" {
		t.Fatalf("body = %q, want %q", string(body), "target ok")
	}

	transport.CloseIdleConnections()

	if errProxy := <-proxyDone; errProxy != nil {
		t.Fatalf("proxy returned error: %v", errProxy)
	}
}

func TestBuildHTTPTransportHTTPSProxyHTTPTarget(t *testing.T) {
	t.Parallel()

	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "http target ok")
	}))
	defer targetServer.Close()

	proxyCert := newTestCertificate(t)
	proxyListener, errListen := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{proxyCert},
		NextProtos:   []string{"h2", "http/1.1"},
	})
	if errListen != nil {
		t.Fatalf("tls.Listen returned error: %v", errListen)
	}
	defer func() {
		if errClose := proxyListener.Close(); errClose != nil {
			t.Errorf("proxyListener.Close returned error: %v", errClose)
		}
	}()

	proxyDone := make(chan error, 1)
	go func() {
		conn, errAccept := proxyListener.Accept()
		if errAccept != nil {
			proxyDone <- errAccept
			return
		}
		defer func() { _ = conn.Close() }()

		tlsConn, ok := conn.(*tls.Conn)
		if !ok {
			proxyDone <- errors.New("conn is not *tls.Conn")
			return
		}
		if errHandshake := tlsConn.Handshake(); errHandshake != nil {
			proxyDone <- fmt.Errorf("tls handshake failed: %w", errHandshake)
			return
		}

		if proto := tlsConn.ConnectionState().NegotiatedProtocol; proto != "http/1.1" {
			proxyDone <- fmt.Errorf("proxy negotiated %q, want http/1.1", proto)
			return
		}

		req, errRead := http.ReadRequest(bufio.NewReader(tlsConn))
		if errRead != nil {
			proxyDone <- fmt.Errorf("read request failed: %w", errRead)
			return
		}
		// Plain HTTP target via forward proxy sends standard GET http://host/path
		if req.Method != http.MethodGet {
			proxyDone <- fmt.Errorf("method = %s, want GET", req.Method)
			return
		}

		targetURL, _ := url.Parse(targetServer.URL)
		targetConn, errDial := net.Dial("tcp", targetURL.Host)
		if errDial != nil {
			proxyDone <- fmt.Errorf("dial target failed: %w", errDial)
			return
		}
		defer func() { _ = targetConn.Close() }()

		if errWrite := req.Write(targetConn); errWrite != nil {
			proxyDone <- fmt.Errorf("write to target failed: %w", errWrite)
			return
		}

		targetResp, errTargetResp := http.ReadResponse(bufio.NewReader(targetConn), req)
		if errTargetResp != nil {
			proxyDone <- fmt.Errorf("read target response failed: %w", errTargetResp)
			return
		}
		defer func() { _ = targetResp.Body.Close() }()

		if errWrite := targetResp.Write(tlsConn); errWrite != nil {
			proxyDone <- fmt.Errorf("write response to proxy client failed: %w", errWrite)
			return
		}

		proxyDone <- nil
	}()

	proxyURL := "https://" + proxyListener.Addr().String()
	transport, mode, errBuild := BuildHTTPTransport(proxyURL)
	if errBuild != nil {
		t.Fatalf("BuildHTTPTransport returned error: %v", errBuild)
	}
	if mode != ModeProxy {
		t.Fatalf("mode = %d, want %d", mode, ModeProxy)
	}

	certPool := x509.NewCertPool()
	certPool.AddCert(proxyCert.Leaf)
	parsedProxyURL, _ := url.Parse(proxyURL)
	transport.DialTLSContext = buildHTTPSProxyDialTLSContext(parsedProxyURL, &tls.Config{RootCAs: certPool}, transport.TLSHandshakeTimeout, transport.DialContext)

	client := &http.Client{
		Transport: transport,
		Timeout:   5 * time.Second,
	}

	req, errReq := http.NewRequest(http.MethodGet, targetServer.URL, nil)
	if errReq != nil {
		t.Fatalf("http.NewRequest returned error: %v", errReq)
	}
	req.Close = true

	resp, errGet := client.Do(req)
	if errGet != nil {
		t.Fatalf("client.Do returned error: %v", errGet)
	}
	defer func() { _ = resp.Body.Close() }()

	body, errBody := io.ReadAll(resp.Body)
	if errBody != nil {
		t.Fatalf("read response body failed: %v", errBody)
	}
	if string(body) != "http target ok" {
		t.Fatalf("body = %q, want %q", string(body), "http target ok")
	}

	transport.CloseIdleConnections()

	if errProxy := <-proxyDone; errProxy != nil {
		t.Fatalf("proxy returned error: %v", errProxy)
	}
}

func TestBuildDialerHTTPSProxyCONNECT(t *testing.T) {
	t.Parallel()

	proxyCert := newTestCertificate(t)
	proxyListener, errListen := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{proxyCert},
		NextProtos:   []string{"h2", "http/1.1"},
	})
	if errListen != nil {
		t.Fatalf("tls.Listen returned error: %v", errListen)
	}
	defer func() {
		if errClose := proxyListener.Close(); errClose != nil {
			t.Errorf("proxyListener.Close returned error: %v", errClose)
		}
	}()

	done := make(chan error, 1)
	go func() {
		conn, errAccept := proxyListener.Accept()
		if errAccept != nil {
			done <- errAccept
			return
		}
		defer func() { _ = conn.Close() }()

		tlsConn, ok := conn.(*tls.Conn)
		if !ok {
			done <- errors.New("conn is not *tls.Conn")
			return
		}
		if errHandshake := tlsConn.Handshake(); errHandshake != nil {
			done <- fmt.Errorf("tls handshake failed: %w", errHandshake)
			return
		}

		if proto := tlsConn.ConnectionState().NegotiatedProtocol; proto != "http/1.1" {
			done <- fmt.Errorf("negotiated protocol = %q, want http/1.1", proto)
			return
		}

		req, errRead := http.ReadRequest(bufio.NewReader(tlsConn))
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

		if _, errWrite := io.WriteString(tlsConn, "HTTP/1.1 200 Connection Established\r\n\r\nok"); errWrite != nil {
			done <- fmt.Errorf("write CONNECT response failed: %w", errWrite)
			return
		}

		buf := make([]byte, 4)
		n, errReadTunnel := io.ReadFull(tlsConn, buf)
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

	dialer, mode, errBuild := BuildDialer("https://user:pass@" + proxyListener.Addr().String())
	if errBuild != nil {
		t.Fatalf("BuildDialer returned error: %v", errBuild)
	}
	if mode != ModeProxy {
		t.Fatalf("mode = %d, want %d", mode, ModeProxy)
	}
	httpDialer, ok := dialer.(*httpConnectDialer)
	if !ok {
		t.Fatalf("dialer type = %T, want *httpConnectDialer", dialer)
	}
	certPool := x509.NewCertPool()
	certPool.AddCert(proxyCert.Leaf)
	httpDialer.tlsConfig = &tls.Config{RootCAs: certPool}

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

func TestBuildHTTPTransportHTTPSProxyTLSHandshakeTimeout(t *testing.T) {
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
		defer func() { _ = conn.Close() }()
		// Stall indefinitely without performing TLS handshake
		time.Sleep(2 * time.Second)
	}()

	proxyURL := "https://" + listener.Addr().String()
	transport, mode, errBuild := BuildHTTPTransport(proxyURL)
	if errBuild != nil {
		t.Fatalf("BuildHTTPTransport returned error: %v", errBuild)
	}
	if mode != ModeProxy {
		t.Fatalf("mode = %d, want %d", mode, ModeProxy)
	}

	transport.TLSHandshakeTimeout = 50 * time.Millisecond
	parsedProxyURL, _ := url.Parse(proxyURL)
	transport.DialTLSContext = buildHTTPSProxyDialTLSContext(parsedProxyURL, nil, transport.TLSHandshakeTimeout, transport.DialContext)

	client := &http.Client{
		Transport: transport,
		Timeout:   2 * time.Second,
	}

	start := time.Now()
	_, errGet := client.Get("https://example.com/test")
	duration := time.Since(start)

	if errGet == nil {
		t.Fatal("expected error due to TLS handshake timeout, got nil")
	}
	if duration >= time.Second {
		t.Fatalf("request took %v, expected TLSHandshakeTimeout of 50ms to abort sooner", duration)
	}
}

func TestBuildHTTPTransportHTTPSProxyClonedTransport(t *testing.T) {
	t.Parallel()

	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "cloned target ok")
	}))
	defer targetServer.Close()

	proxyCert := newTestCertificate(t)
	proxyListener, errListen := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{proxyCert},
		NextProtos:   []string{"h2", "http/1.1"},
	})
	if errListen != nil {
		t.Fatalf("tls.Listen returned error: %v", errListen)
	}
	defer func() {
		if errClose := proxyListener.Close(); errClose != nil {
			t.Errorf("proxyListener.Close returned error: %v", errClose)
		}
	}()

	proxyDone := make(chan error, 1)
	go func() {
		conn, errAccept := proxyListener.Accept()
		if errAccept != nil {
			proxyDone <- errAccept
			return
		}
		defer func() { _ = conn.Close() }()

		tlsConn, ok := conn.(*tls.Conn)
		if !ok {
			proxyDone <- errors.New("conn is not *tls.Conn")
			return
		}
		if errHandshake := tlsConn.Handshake(); errHandshake != nil {
			proxyDone <- fmt.Errorf("tls handshake failed: %w", errHandshake)
			return
		}

		if proto := tlsConn.ConnectionState().NegotiatedProtocol; proto != "http/1.1" {
			proxyDone <- fmt.Errorf("proxy negotiated %q, want http/1.1", proto)
			return
		}

		req, errRead := http.ReadRequest(bufio.NewReader(tlsConn))
		if errRead != nil {
			proxyDone <- fmt.Errorf("read request failed: %w", errRead)
			return
		}

		targetURL, _ := url.Parse(targetServer.URL)
		targetConn, errDial := net.Dial("tcp", targetURL.Host)
		if errDial != nil {
			proxyDone <- fmt.Errorf("dial target failed: %w", errDial)
			return
		}
		defer func() { _ = targetConn.Close() }()

		if errWrite := req.Write(targetConn); errWrite != nil {
			proxyDone <- fmt.Errorf("write to target failed: %w", errWrite)
			return
		}

		targetResp, errTargetResp := http.ReadResponse(bufio.NewReader(targetConn), req)
		if errTargetResp != nil {
			proxyDone <- fmt.Errorf("read target response failed: %w", errTargetResp)
			return
		}
		defer func() { _ = targetResp.Body.Close() }()

		if errWrite := targetResp.Write(tlsConn); errWrite != nil {
			proxyDone <- fmt.Errorf("write response to proxy client failed: %w", errWrite)
			return
		}

		proxyDone <- nil
	}()

	proxyURL := "https://" + proxyListener.Addr().String()
	transport, mode, errBuild := BuildHTTPTransport(proxyURL)
	if errBuild != nil {
		t.Fatalf("BuildHTTPTransport returned error: %v", errBuild)
	}
	if mode != ModeProxy {
		t.Fatalf("mode = %d, want %d", mode, ModeProxy)
	}

	certPool := x509.NewCertPool()
	certPool.AddCert(proxyCert.Leaf)
	parsedProxyURL, _ := url.Parse(proxyURL)
	transport.DialTLSContext = buildHTTPSProxyDialTLSContext(parsedProxyURL, &tls.Config{RootCAs: certPool}, transport.TLSHandshakeTimeout, transport.DialContext)

	clonedTransport := transport.Clone()

	client := &http.Client{
		Transport: clonedTransport,
		Timeout:   5 * time.Second,
	}

	req, errReq := http.NewRequest(http.MethodGet, targetServer.URL, nil)
	if errReq != nil {
		t.Fatalf("http.NewRequest returned error: %v", errReq)
	}
	req.Close = true

	resp, errGet := client.Do(req)
	if errGet != nil {
		t.Fatalf("client.Do returned error: %v", errGet)
	}
	defer func() { _ = resp.Body.Close() }()

	body, errBody := io.ReadAll(resp.Body)
	if errBody != nil {
		t.Fatalf("read response body failed: %v", errBody)
	}
	if string(body) != "cloned target ok" {
		t.Fatalf("body = %q, want %q", string(body), "cloned target ok")
	}

	clonedTransport.CloseIdleConnections()

	if errProxy := <-proxyDone; errProxy != nil {
		t.Fatalf("proxy returned error: %v", errProxy)
	}
}
