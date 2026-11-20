package executor

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	"golang.org/x/net/http2"
)

type capturedOAuthRefreshRequest struct {
	proto              string
	negotiatedProtocol string
	clientHelloProtos  []string
	userAgent          string
	host               string
	contentType        string
	connectionHeader   string
	remoteAddr         string
}

func startAntigravityOAuthRefreshTLSServer(t *testing.T, enableHTTP2 bool) (*httptest.Server, *[]capturedOAuthRefreshRequest, *sync.Mutex, <-chan string) {
	t.Helper()

	var mu sync.Mutex
	var captured []capturedOAuthRefreshRequest
	helloProtosByAddr := make(map[string][]string)
	closedCh := make(chan string, 16)

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		rec := capturedOAuthRefreshRequest{
			proto:            r.Proto,
			userAgent:        r.Header.Get("User-Agent"),
			host:             r.Host,
			contentType:      r.Header.Get("Content-Type"),
			connectionHeader: r.Header.Get("Connection"),
			remoteAddr:       r.RemoteAddr,
		}
		if r.TLS != nil {
			rec.negotiatedProtocol = r.TLS.NegotiatedProtocol
		}
		rec.clientHelloProtos = append([]string(nil), helloProtosByAddr[r.RemoteAddr]...)
		captured = append(captured, rec)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"access_token":"refreshed-token","expires_in":3600}`))
	}))
	server.EnableHTTP2 = enableHTTP2
	server.Config.ConnState = func(conn net.Conn, state http.ConnState) {
		if state == http.StateClosed && conn != nil && conn.RemoteAddr() != nil {
			select {
			case closedCh <- conn.RemoteAddr().String():
			default:
			}
		}
	}
	server.TLS = &tls.Config{
		MinVersion: tls.VersionTLS13,
		MaxVersion: tls.VersionTLS13,
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			if hello != nil && hello.Conn != nil && hello.Conn.RemoteAddr() != nil {
				mu.Lock()
				helloProtosByAddr[hello.Conn.RemoteAddr().String()] = append([]string(nil), hello.SupportedProtos...)
				mu.Unlock()
			}
			return nil, nil
		},
	}
	server.StartTLS()
	t.Cleanup(server.Close)

	return server, &captured, &mu, closedCh
}

func newLocalDialTransport(targetAddr string) *http.Transport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil
	base.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, targetAddr)
	}
	base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	return base
}

func startLocalHTTPConnectProxy(t *testing.T, forwardAddr string) (*httptest.Server, *[]string, *sync.Mutex) {
	t.Helper()

	var mu sync.Mutex
	var targets []string

	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "expected CONNECT", http.StatusMethodNotAllowed)
			return
		}
		mu.Lock()
		targets = append(targets, r.Host)
		mu.Unlock()

		upstreamConn, errDial := net.DialTimeout("tcp", forwardAddr, 5*time.Second)
		if errDial != nil {
			http.Error(w, errDial.Error(), http.StatusBadGateway)
			return
		}

		hijacker, ok := w.(http.Hijacker)
		if !ok {
			_ = upstreamConn.Close()
			http.Error(w, "hijack unsupported", http.StatusInternalServerError)
			return
		}
		clientConn, bufRW, errHijack := hijacker.Hijack()
		if errHijack != nil {
			_ = upstreamConn.Close()
			return
		}

		if _, errWrite := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); errWrite != nil {
			_ = upstreamConn.Close()
			_ = clientConn.Close()
			return
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = io.Copy(upstreamConn, bufRW)
			if tcpConn, okTCP := upstreamConn.(*net.TCPConn); okTCP {
				_ = tcpConn.CloseWrite()
			}
		}()
		go func() {
			defer wg.Done()
			_, _ = io.Copy(clientConn, upstreamConn)
			if tcpConn, okTCP := clientConn.(*net.TCPConn); okTCP {
				_ = tcpConn.CloseWrite()
			}
		}()
		wg.Wait()
		_ = upstreamConn.Close()
		_ = clientConn.Close()
	}))
	t.Cleanup(proxyServer.Close)

	return proxyServer, &targets, &mu
}

func TestAntigravityRefreshTokenSingleFlightNegotiatesHTTP2WithMatchingUserAgent(t *testing.T) {
	server, captured, mu, _ := startAntigravityOAuthRefreshTLSServer(t, true)
	baseTransport := newLocalDialTransport(server.Listener.Addr().String())
	defer baseTransport.CloseIdleConnections()

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(baseTransport))
	exec := NewAntigravityExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "oauth-refresh-h2"}

	tokenResp, errRefresh := exec.refreshTokenSingleFlight(ctx, auth, "refresh-token-h2")
	if errRefresh != nil {
		t.Fatalf("refreshTokenSingleFlight() error = %v", errRefresh)
	}
	if tokenResp == nil || tokenResp.AccessToken != "refreshed-token" {
		t.Fatalf("tokenResp = %#v, want AccessToken=refreshed-token", tokenResp)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(*captured) != 1 {
		t.Fatalf("captured requests = %d, want 1", len(*captured))
	}
	got := (*captured)[0]
	if !slices.Contains(got.clientHelloProtos, "h2") {
		t.Fatalf("ClientHello ALPN = %v, want h2 advertised", got.clientHelloProtos)
	}
	if got.proto != "HTTP/2.0" {
		t.Fatalf("request proto = %q, want HTTP/2.0", got.proto)
	}
	if got.negotiatedProtocol != "h2" {
		t.Fatalf("negotiated ALPN = %q, want h2", got.negotiatedProtocol)
	}
	if got.userAgent != "Go-http-client/2.0" {
		t.Fatalf("User-Agent = %q, want Go-http-client/2.0", got.userAgent)
	}
	if got.host != "oauth2.googleapis.com" {
		t.Fatalf("Host = %q, want oauth2.googleapis.com", got.host)
	}
	if got.contentType != "application/x-www-form-urlencoded" {
		t.Fatalf("Content-Type = %q, want application/x-www-form-urlencoded", got.contentType)
	}
}

func TestAntigravityRefreshTokenSingleFlightAlignsUserAgentOnHTTP11Fallback(t *testing.T) {
	server, captured, mu, _ := startAntigravityOAuthRefreshTLSServer(t, false)
	baseTransport := newLocalDialTransport(server.Listener.Addr().String())
	defer baseTransport.CloseIdleConnections()

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(baseTransport))
	exec := NewAntigravityExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "oauth-refresh-h1-fallback"}

	if _, errRefresh := exec.refreshTokenSingleFlight(ctx, auth, "refresh-token-h1"); errRefresh != nil {
		t.Fatalf("refreshTokenSingleFlight() error = %v", errRefresh)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(*captured) != 1 {
		t.Fatalf("captured requests = %d, want 1", len(*captured))
	}
	got := (*captured)[0]
	if got.proto != "HTTP/1.1" {
		t.Fatalf("request proto = %q, want HTTP/1.1", got.proto)
	}
	if got.userAgent != "Go-http-client/1.1" {
		t.Fatalf("User-Agent = %q, want Go-http-client/1.1 on HTTP/1.1 fallback", got.userAgent)
	}
	if got.connectionHeader != "" {
		t.Fatalf("Connection header = %q, want empty (no synthetic Connection: close)", got.connectionHeader)
	}
}

func TestAntigravityRefreshTokenSingleFlightHTTP2ViaCONNECTProxy(t *testing.T) {
	server, captured, mu, _ := startAntigravityOAuthRefreshTLSServer(t, true)
	proxySrv, targets, targetsMu := startLocalHTTPConnectProxy(t, server.Listener.Addr().String())

	origDefault := http.DefaultTransport
	customDefault := origDefault.(*http.Transport).Clone()
	customDefault.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	http.DefaultTransport = customDefault
	t.Cleanup(func() {
		http.DefaultTransport = origDefault
		customDefault.CloseIdleConnections()
		ResetAntigravityTransports()
	})

	exec := NewAntigravityExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "oauth-refresh-connect-proxy",
		ProxyURL: proxySrv.URL,
	}

	if _, errRefresh := exec.refreshTokenSingleFlight(context.Background(), auth, "refresh-token-proxy"); errRefresh != nil {
		t.Fatalf("refreshTokenSingleFlight() error = %v", errRefresh)
	}

	targetsMu.Lock()
	gotTargets := append([]string(nil), (*targets)...)
	targetsMu.Unlock()
	if len(gotTargets) != 1 || gotTargets[0] != "oauth2.googleapis.com:443" {
		t.Fatalf("CONNECT targets = %v, want [oauth2.googleapis.com:443]", gotTargets)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(*captured) != 1 {
		t.Fatalf("captured requests = %d, want 1", len(*captured))
	}
	got := (*captured)[0]
	if !slices.Contains(got.clientHelloProtos, "h2") {
		t.Fatalf("ClientHello ALPN = %v, want h2 advertised through proxy", got.clientHelloProtos)
	}
	if got.proto != "HTTP/2.0" {
		t.Fatalf("request proto = %q, want HTTP/2.0", got.proto)
	}
	if got.negotiatedProtocol != "h2" {
		t.Fatalf("negotiated ALPN = %q, want h2", got.negotiatedProtocol)
	}
	if got.userAgent != "Go-http-client/2.0" {
		t.Fatalf("User-Agent = %q, want Go-http-client/2.0", got.userAgent)
	}
}

func TestAntigravityRefreshTokenSingleFlightIsolatesDirectCredentialsAndClosesIdleConn(t *testing.T) {
	server, captured, mu, closedCh := startAntigravityOAuthRefreshTLSServer(t, true)

	origDefault := http.DefaultTransport
	origAntigravityBase := antigravityBaseTransport
	customDefault := newLocalDialTransport(server.Listener.Addr().String())
	http.DefaultTransport = customDefault
	antigravityBaseTransport = customDefault
	t.Cleanup(func() {
		http.DefaultTransport = origDefault
		antigravityBaseTransport = origAntigravityBase
		customDefault.CloseIdleConnections()
		ResetAntigravityTransports()
	})

	exec := NewAntigravityExecutor(&config.Config{})
	authA := &cliproxyauth.Auth{ID: "oauth-direct-a"}
	authB := &cliproxyauth.Auth{ID: "oauth-direct-b"}

	if _, errA := exec.refreshTokenSingleFlight(context.Background(), authA, "refresh-a"); errA != nil {
		t.Fatalf("refreshTokenSingleFlight(authA) error = %v", errA)
	}
	if _, errB := exec.refreshTokenSingleFlight(context.Background(), authB, "refresh-b"); errB != nil {
		t.Fatalf("refreshTokenSingleFlight(authB) error = %v", errB)
	}

	mu.Lock()
	if len(*captured) != 2 {
		mu.Unlock()
		t.Fatalf("captured requests = %d, want 2", len(*captured))
	}
	recA := (*captured)[0]
	recB := (*captured)[1]
	mu.Unlock()

	for idx, rec := range []capturedOAuthRefreshRequest{recA, recB} {
		if rec.proto != "HTTP/2.0" {
			t.Fatalf("request %d proto = %q, want HTTP/2.0", idx, rec.proto)
		}
		if rec.userAgent != "Go-http-client/2.0" {
			t.Fatalf("request %d User-Agent = %q, want Go-http-client/2.0", idx, rec.userAgent)
		}
	}
	if recA.remoteAddr == recB.remoteAddr {
		t.Fatalf("distinct credentials shared TCP/TLS connection %q on direct OAuth refresh", recA.remoteAddr)
	}

	closedAddrs := map[string]bool{}
	deadline := time.After(3 * time.Second)
	for !closedAddrs[recA.remoteAddr] || !closedAddrs[recB.remoteAddr] {
		select {
		case addr := <-closedCh:
			closedAddrs[addr] = true
		case <-deadline:
			t.Fatalf("timed out waiting for idle OAuth refresh connections to close; closed=%v want %q and %q", closedAddrs, recA.remoteAddr, recB.remoteAddr)
		}
	}
}

func TestAntigravityRefreshTokenSingleFlightDecoratorSeesHTTP2AndCloudCodeStaysHTTP11(t *testing.T) {
	server, captured, mu, _ := startAntigravityOAuthRefreshTLSServer(t, true)
	baseTransport := newLocalDialTransport(server.Listener.Addr().String())
	defer baseTransport.CloseIdleConnections()

	decoratorCalls := 0
	sawTransport := false
	forceAttemptHTTP2 := false
	tlsNextProtoWasNil := false
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(baseTransport))
	ctx = httptransport.WithDecorator(ctx, func(next http.RoundTripper) http.RoundTripper {
		decoratorCalls++
		if tr, ok := next.(*http.Transport); ok && tr != nil {
			sawTransport = true
			forceAttemptHTTP2 = tr.ForceAttemptHTTP2
			tlsNextProtoWasNil = tr.TLSNextProto == nil
		}
		return &auditTestTransport{RoundTripper: next}
	})

	exec := NewAntigravityExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "oauth-decorator-check"}
	if _, errRefresh := exec.refreshTokenSingleFlight(ctx, auth, "refresh-decorator"); errRefresh != nil {
		t.Fatalf("refreshTokenSingleFlight() error = %v", errRefresh)
	}

	if decoratorCalls != 1 {
		t.Fatalf("decorator calls = %d, want 1", decoratorCalls)
	}
	if !sawTransport {
		t.Fatal("decorator did not receive an *http.Transport")
	}
	if !forceAttemptHTTP2 || !tlsNextProtoWasNil {
		t.Fatalf("OAuth refresh transport disabled HTTP/2: ForceAttemptHTTP2=%v TLSNextProto==nil:%v", forceAttemptHTTP2, tlsNextProtoWasNil)
	}

	mu.Lock()
	if len(*captured) != 1 || (*captured)[0].proto != "HTTP/2.0" || (*captured)[0].userAgent != "Go-http-client/2.0" {
		got := append([]capturedOAuthRefreshRequest(nil), (*captured)...)
		mu.Unlock()
		t.Fatalf("captured = %+v, want HTTP/2.0 with Go-http-client/2.0", got)
	}
	mu.Unlock()

	// Verify Cloud Code PA client remains strictly HTTP/1.1 without ALPN.
	cloudCodeClient := newAntigravityHTTPClient(context.Background(), &config.Config{}, auth, 0)
	cloudCodeTransport, ok := cloudCodeClient.Transport.(*http.Transport)
	if !ok || cloudCodeTransport == nil {
		t.Fatalf("Cloud Code client transport = %T, want *http.Transport", cloudCodeClient.Transport)
	}
	if cloudCodeTransport.ForceAttemptHTTP2 || cloudCodeTransport.TLSNextProto == nil || len(cloudCodeTransport.TLSNextProto) != 0 {
		t.Fatalf("Cloud Code transport must keep HTTP/1.1 disabled-H2 settings: ForceAttemptHTTP2=%v TLSNextProto=%#v", cloudCodeTransport.ForceAttemptHTTP2, cloudCodeTransport.TLSNextProto)
	}
	if cloudCodeTransport.TLSClientConfig == nil || len(cloudCodeTransport.TLSClientConfig.NextProtos) != 0 {
		t.Fatalf("Cloud Code transport must omit ALPN NextProtos, got %#v", cloudCodeTransport.TLSClientConfig)
	}
}

func TestNewAntigravityOAuthHTTPClientFallbackAndCustomRoundTripper(t *testing.T) {
	t.Run("typed nil context transport falls back to isolated default clone", func(t *testing.T) {
		var typedNil *http.Transport
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(typedNil))
		client, cleanup := newAntigravityOAuthHTTPClient(ctx, &config.Config{}, &cliproxyauth.Auth{ID: "typed-nil"}, 0)
		defer cleanup()

		tr, ok := client.Transport.(*http.Transport)
		if !ok || tr == nil {
			t.Fatalf("Transport = %#v, want non-nil *http.Transport", client.Transport)
		}
		if tr == http.DefaultTransport {
			t.Fatal("Transport must be an isolated clone, not http.DefaultTransport")
		}
		if !tr.ForceAttemptHTTP2 {
			t.Fatal("fallback OAuth transport must attempt HTTP/2")
		}
	})

	t.Run("custom non-Transport RoundTripper is preserved", func(t *testing.T) {
		customRT := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return nil, io.EOF
		})
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(customRT))
		client, cleanup := newAntigravityOAuthHTTPClient(ctx, &config.Config{}, &cliproxyauth.Auth{ID: "custom-rt"}, 0)
		defer cleanup()

		if _, isTransport := client.Transport.(*http.Transport); isTransport {
			t.Fatal("custom non-*http.Transport RoundTripper must be preserved as-is")
		}
	})

	t.Run("custom non-Transport http.DefaultTransport is preserved", func(t *testing.T) {
		origDefault := http.DefaultTransport
		http.DefaultTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return nil, io.EOF
		})
		t.Cleanup(func() {
			http.DefaultTransport = origDefault
		})

		client, cleanup := newAntigravityOAuthHTTPClient(context.Background(), &config.Config{}, &cliproxyauth.Auth{ID: "custom-default"}, 0)
		defer cleanup()

		if client.Transport != nil {
			t.Fatalf("Transport = %#v, want nil to preserve custom http.DefaultTransport", client.Transport)
		}
	})
}

func TestAntigravityRefreshTokenSingleFlightIsolatesConfiguredHTTP2BaseTransport(t *testing.T) {
	server, captured, mu, closedCh := startAntigravityOAuthRefreshTLSServer(t, true)

	baseTransport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	if errConfigure := http2.ConfigureTransport(baseTransport); errConfigure != nil {
		t.Fatalf("http2.ConfigureTransport() error = %v", errConfigure)
	}
	defer baseTransport.CloseIdleConnections()

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(baseTransport))
	exec := NewAntigravityExecutor(&config.Config{})
	authA := &cliproxyauth.Auth{ID: "oauth-xnet-h2-a"}
	authB := &cliproxyauth.Auth{ID: "oauth-xnet-h2-b"}

	if _, errA := exec.refreshTokenSingleFlight(ctx, authA, "refresh-xnet-a"); errA != nil {
		t.Fatalf("refreshTokenSingleFlight(authA) error = %v", errA)
	}
	if _, errB := exec.refreshTokenSingleFlight(ctx, authB, "refresh-xnet-b"); errB != nil {
		t.Fatalf("refreshTokenSingleFlight(authB) error = %v", errB)
	}

	mu.Lock()
	if len(*captured) != 2 {
		mu.Unlock()
		t.Fatalf("captured requests = %d, want 2", len(*captured))
	}
	recA := (*captured)[0]
	recB := (*captured)[1]
	mu.Unlock()

	for idx, rec := range []capturedOAuthRefreshRequest{recA, recB} {
		if rec.proto != "HTTP/2.0" {
			t.Fatalf("request %d proto = %q, want HTTP/2.0", idx, rec.proto)
		}
		if rec.userAgent != "Go-http-client/2.0" {
			t.Fatalf("request %d User-Agent = %q, want Go-http-client/2.0", idx, rec.userAgent)
		}
	}
	if recA.remoteAddr == recB.remoteAddr {
		t.Fatalf("distinct credentials shared x/net/http2 connection %q across cloned transports", recA.remoteAddr)
	}

	closedAddrs := map[string]bool{}
	deadline := time.After(3 * time.Second)
	for !closedAddrs[recA.remoteAddr] || !closedAddrs[recB.remoteAddr] {
		select {
		case addr := <-closedCh:
			closedAddrs[addr] = true
		case <-deadline:
			t.Fatalf("timed out waiting for isolated HTTP/2 connections to close; closed=%v want %q and %q", closedAddrs, recA.remoteAddr, recB.remoteAddr)
		}
	}
}
