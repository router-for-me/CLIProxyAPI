package helps

import (
	"context"
	gotls "crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestUtlsFallbackProxyConnectionPool(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			ResetProxyTransports()
			t.Cleanup(ResetProxyTransports)
			var connections atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
			server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateNew {
					connections.Add(1)
				}
			}
			server.Start()
			defer server.Close()
			cfg := testConfig()
			auth := &cliproxyauth.Auth{ProxyURL: "direct"}
			if enabled {
				auth.ProxyConnectionPool = poolEnabled()
			}
			for i := 0; i < 2; i++ {
				client := NewUtlsHTTPClient(context.Background(), cfg, auth, 0)
				resp, err := client.Get(server.URL)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = io.Copy(io.Discard, resp.Body); err != nil {
					t.Fatal(err)
				}
				if err = resp.Body.Close(); err != nil {
					t.Fatal(err)
				}
			}
			want := int32(2)
			if enabled {
				want = 1
			}
			if got := connections.Load(); got != want {
				t.Fatalf("connections = %d, want %d", got, want)
			}
		})
	}
}

// TestUtlsAnthropicTransportPoolsPerCredential pins the Anthropic fingerprint path:
// it is built by its own pool kind, so the credential's settings apply and two keys
// never share one connection. Previously this transport was always the shared
// per-proxy cache entry, which mixed distinct identities on one TLS connection and
// ignored max-idle-conns-per-host.
func TestUtlsAnthropicTransportPoolsPerCredential(t *testing.T) {
	ResetProxyTransports()
	t.Cleanup(ResetProxyTransports)
	cfg := testConfig()
	anthropicTransport := func(id string, override *cliproxyauth.ProxyConnectionPoolOverride) *http.Transport {
		t.Helper()
		client := NewUtlsHTTPClient(context.Background(), cfg, poolAuth(id, override), 0)
		fallback, ok := client.Transport.(*fallbackRoundTripper)
		if !ok {
			t.Fatalf("client transport = %T, want *fallbackRoundTripper", client.Transport)
		}
		transport, ok := fallback.anthropicRoundTripper().(*http.Transport)
		if !ok {
			t.Fatalf("anthropic transport = %T, want *http.Transport", fallback.anthropicRoundTripper())
		}
		return transport
	}

	first := anthropicTransport("key-a", poolBlock())
	if first.IdleConnTimeout != proxyPoolDefaultIdleConnTimeout || first.MaxIdleConnsPerHost != proxyPoolDefaultMaxIdleConnsPerHost {
		t.Fatalf("pool defaults not applied to the Anthropic transport: %+v", first)
	}
	if got := anthropicTransport("key-a", poolBlock()); got != first {
		t.Fatal("credential did not reuse its own Anthropic pool")
	}
	if second := anthropicTransport("key-b", poolBlock()); second == first {
		t.Fatal("two credentials share one Anthropic transport")
	}

	// Zero bounds disable keep-alives on the fingerprint transport too.
	zero := 0
	if transport := anthropicTransport("key-a", &cliproxyauth.ProxyConnectionPoolOverride{MaxIdleConnsPerHost: &zero}); !transport.DisableKeepAlives {
		t.Fatal("max-idle-conns-per-host: 0 must disable keep-alives on the Anthropic transport")
	}

	// Credentials that did not opt in keep the shared Claude Code transport with
	// idle connection pooling disabled, while opted-in credentials enable pooling.
	plain := func(id string) *http.Transport {
		t.Helper()
		client := NewUtlsHTTPClient(context.Background(), cfg, &cliproxyauth.Auth{ID: id}, 0)
		tr, ok := client.Transport.(*fallbackRoundTripper).anthropicRoundTripper().(*http.Transport)
		if !ok {
			t.Fatalf("unpooled anthropic transport = %T, want *http.Transport", client.Transport.(*fallbackRoundTripper).anthropicRoundTripper())
		}
		return tr
	}
	if plain("key-a") != plain("key-b") {
		t.Fatal("unpooled credentials must keep sharing the cached Claude Code transport")
	}
	if plain("key-a").MaxIdleConnsPerHost >= 0 {
		t.Fatalf("unpooled Claude Code transport MaxIdleConnsPerHost = %d, want < 0 to disable idle connection reuse", plain("key-a").MaxIdleConnsPerHost)
	}
}

// TestUtlsClaudeCodeDefaultDisablesConnectionReusePreservingExplicitPool verifies
// that by default (including OAuth and unpooled API keys) the Claude Code uTLS
// transport closes connections after each request instead of leaving them in an
// idle pool, while an API key that explicitly configures connection-pool still
// reuses its pooled connection.
func TestUtlsClaudeCodeDefaultDisablesConnectionReusePreservingExplicitPool(t *testing.T) {
	ResetProxyTransports()
	t.Cleanup(ResetProxyTransports)

	certificate := newResumptionTestCertificate(t)
	roots := x509.NewCertPool()
	roots.AddCert(certificate.Leaf)

	var (
		newConns   atomic.Int32
		headerMu   sync.Mutex
		connHeader []string
	)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headerMu.Lock()
		connHeader = append(connHeader, r.Header.Get("Connection"))
		headerMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	server.TLS = &gotls.Config{
		Certificates: []gotls.Certificate{certificate},
		MinVersion:   gotls.VersionTLS13,
		NextProtos:   []string{"http/1.1"},
	}
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConns.Add(1)
		}
	}
	server.StartTLS()
	defer server.Close()

	serverAddr := server.Listener.Addr().String()
	dialer := contextDialerFunc(func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, serverAddr)
	})

	doTwoRequests := func(t *testing.T, tr *http.Transport) []bool {
		t.Helper()
		t.Cleanup(tr.CloseIdleConnections)
		var (
			mu     sync.Mutex
			reused []bool
		)
		trace := &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) {
				mu.Lock()
				reused = append(reused, info.Reused)
				mu.Unlock()
			},
		}
		client := &http.Client{Transport: tr}
		for i := 0; i < 2; i++ {
			req, errReq := http.NewRequestWithContext(
				httptrace.WithClientTrace(context.Background(), trace),
				http.MethodPost,
				"https://api.anthropic.com/v1/messages?beta=true",
				nil,
			)
			if errReq != nil {
				t.Fatalf("NewRequest: %v", errReq)
			}
			req.Header.Set("Connection", "keep-alive")
			resp, errDo := client.Do(req)
			if errDo != nil {
				t.Fatalf("round %d Do: %v", i+1, errDo)
			}
			if _, errCopy := io.Copy(io.Discard, resp.Body); errCopy != nil {
				t.Fatalf("round %d read body: %v", i+1, errCopy)
			}
			if errClose := resp.Body.Close(); errClose != nil {
				t.Fatalf("round %d close body: %v", i+1, errClose)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		return append([]bool(nil), reused...)
	}

	// 1. Default / OAuth transport: must NOT reuse connections across requests,
	// while keeping the wire Connection: keep-alive header intact.
	defaultTransport := newClaudeCodeRoundTripperForDialerAndRoots(dialer, roots)
	reusedDefault := doTwoRequests(t, defaultTransport)
	if got := newConns.Load(); got != 2 {
		t.Fatalf("default transport connections = %d, want 2 (reused = %v)", got, reusedDefault)
	}
	if len(reusedDefault) != 2 || reusedDefault[0] || reusedDefault[1] {
		t.Fatalf("default transport GotConn reused = %v, want [false false]", reusedDefault)
	}
	headerMu.Lock()
	gotHeaders := append([]string(nil), connHeader...)
	headerMu.Unlock()
	for i, h := range gotHeaders {
		if h != "keep-alive" {
			t.Fatalf("request %d Connection header = %q, want \"keep-alive\"", i+1, h)
		}
	}

	// 2. Explicit connection-pool opt-in on claude-api-key: MUST still reuse
	// connections across requests.
	newConns.Store(0)
	cfg := testConfig()
	pooled, ok := pooledTransport(proxyPoolKindClaudeCode, "direct", cfg, poolOptInAuth("claude-key-pooled"), func() (*http.Transport, error) {
		return newClaudeCodeRoundTripperForDialerAndRoots(dialer, roots), nil
	})
	if !ok || pooled == nil {
		t.Fatal("expected pooled Claude Code transport for opted-in credential")
	}
	reusedPooled := doTwoRequests(t, pooled)
	if got := newConns.Load(); got != 1 {
		t.Fatalf("pooled transport connections = %d, want 1 (reused = %v)", got, reusedPooled)
	}
	if len(reusedPooled) != 2 || reusedPooled[0] || !reusedPooled[1] {
		t.Fatalf("pooled transport GotConn reused = %v, want [false true]", reusedPooled)
	}
}

// TestUtlsLazyPoolsDoNotEvictUsedPool pins that a request only populates the pool
// kind it actually uses. Eagerly populating both the standard and Anthropic pools
// consumed two cache slots per key, so 300 non-Anthropic keys exceeded the 512-slot
// cache and evicted their own live pools on every second pass.
func TestUtlsLazyPoolsDoNotEvictUsedPool(t *testing.T) {
	ResetProxyTransports()
	t.Cleanup(ResetProxyTransports)

	var opened atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			opened.Add(1)
		}
	}
	server.Start()
	defer server.Close()

	cfg := testConfig()
	const keys = 300
	for round := 0; round < 2; round++ {
		for i := 0; i < keys; i++ {
			auth := poolOptInAuth("key-" + strconv.Itoa(i))
			auth.ProxyURL = "direct"
			client := NewUtlsHTTPClient(context.Background(), cfg, auth, 0)
			resp, err := client.Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = io.Copy(io.Discard, resp.Body); err != nil {
				t.Fatal(err)
			}
			if err = resp.Body.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got := proxyTransportCache.Len(); got != keys {
		t.Fatalf("cache holds %d entries, want %d (one per key, no unused Anthropic pools)", got, keys)
	}
	if got := opened.Load(); got != keys {
		t.Fatalf("connections = %d, want %d (second round must reuse every key's pool)", got, keys)
	}
}
