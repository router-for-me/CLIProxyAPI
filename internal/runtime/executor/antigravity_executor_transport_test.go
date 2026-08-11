package executor

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func antigravityAuthWithProxy(proxyURL string) *cliproxyauth.Auth {
	return antigravityAuthWithIDAndProxy("antigravity-test", proxyURL)
}

func antigravityAuthWithIDAndProxy(id, proxyURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       id,
		ProxyURL: proxyURL,
		Metadata: map[string]any{
			"access_token": "test-access-token",
			"project_id":   "test-project",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
}

// TestNewAntigravityHTTPClientSharesTransport is the regression test for the bug where
// every proxied Antigravity request created a new transport, so no keep-alive connection
// was ever reused and every request paid a full TCP + TLS handshake.
func TestNewAntigravityHTTPClientSharesTransport(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.Config
		auth *cliproxyauth.Auth
	}{
		{"direct", &config.Config{}, antigravityAuthWithProxy("")},
		{"auth http proxy", &config.Config{}, antigravityAuthWithProxy("http://127.0.0.1:18080")},
		{"auth socks5 proxy", &config.Config{}, antigravityAuthWithProxy("socks5://127.0.0.1:18081")},
		{
			"config proxy",
			&config.Config{SDKConfig: config.SDKConfig{ProxyURL: "http://127.0.0.1:18082"}},
			antigravityAuthWithProxy(""),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first := newAntigravityHTTPClient(context.Background(), tc.cfg, tc.auth, 0)
			second := newAntigravityHTTPClient(context.Background(), tc.cfg, tc.auth, 0)
			if first.Transport == nil || second.Transport == nil {
				t.Fatal("expected a transport to be configured")
			}
			if first.Transport != second.Transport {
				t.Fatalf("expected a shared transport, got %p and %p", first.Transport, second.Transport)
			}
			transport, ok := first.Transport.(*http.Transport)
			if !ok {
				t.Fatalf("expected *http.Transport, got %T", first.Transport)
			}
			if transport.ForceAttemptHTTP2 {
				t.Fatal("Antigravity transport must not attempt HTTP/2")
			}
			if len(transport.TLSNextProto) != 0 {
				t.Fatal("Antigravity transport must not allow an implicit HTTP/2 upgrade")
			}
			if transport.TLSClientConfig == nil {
				t.Fatal("Antigravity transport must carry an explicit TLS config")
			}
			if len(transport.TLSClientConfig.NextProtos) != 0 {
				t.Fatalf("Antigravity must omit ALPN like the native client, got %v", transport.TLSClientConfig.NextProtos)
			}
			defaultTransport := http.DefaultTransport.(*http.Transport)
			if transport.MaxIdleConns != defaultTransport.MaxIdleConns ||
				transport.MaxIdleConnsPerHost != defaultTransport.MaxIdleConnsPerHost ||
				transport.IdleConnTimeout != defaultTransport.IdleConnTimeout {
				t.Fatal("Antigravity transport must preserve the standard Go pool settings")
			}
		})
	}
}

func TestNewAntigravityHTTPClientDistinctProxiesUseDistinctPools(t *testing.T) {
	cfg := &config.Config{}
	a := newAntigravityHTTPClient(context.Background(), cfg, antigravityAuthWithProxy("http://127.0.0.1:18090"), 0)
	b := newAntigravityHTTPClient(context.Background(), cfg, antigravityAuthWithProxy("http://127.0.0.1:18091"), 0)
	if a.Transport == b.Transport {
		t.Fatal("expected distinct proxies to use distinct connection pools")
	}
}

func TestNewAntigravityHTTPClientScopesPoolsByAuthIdentity(t *testing.T) {
	cfg := &config.Config{}
	const proxyURL = "http://127.0.0.1:18092"

	a1 := newAntigravityHTTPClient(context.Background(), cfg, antigravityAuthWithIDAndProxy("auth-a", proxyURL), 0)
	a2 := newAntigravityHTTPClient(context.Background(), cfg, antigravityAuthWithIDAndProxy("auth-a", proxyURL), 0)
	b := newAntigravityHTTPClient(context.Background(), cfg, antigravityAuthWithIDAndProxy("auth-b", proxyURL), 0)
	if a1.Transport != a2.Transport {
		t.Fatal("the same auth identity must share its connection pool across sessions")
	}
	if a1.Transport == b.Transport {
		t.Fatal("different auth identities must not share a proxied connection pool")
	}

	directA := newAntigravityHTTPClient(context.Background(), cfg, antigravityAuthWithIDAndProxy("direct-a", ""), 0)
	directB := newAntigravityHTTPClient(context.Background(), cfg, antigravityAuthWithIDAndProxy("direct-b", ""), 0)
	if directA.Transport == directB.Transport {
		t.Fatal("different auth identities must not share a direct connection pool")
	}
}

// TestAntigravityHTTP11TransportNeverSharesWithoutStableIdentity guards the pool
// cache against auths that carry no ID. Keying such auths by pointer identity is
// unsafe: the address is reused once the previous auth is collected, which would
// silently place two unrelated OAuth identities on the same TCP/TLS connections.
func TestAntigravityHTTP11TransportNeverSharesWithoutStableIdentity(t *testing.T) {
	base := http.DefaultTransport.(*http.Transport)

	anonymous := &cliproxyauth.Auth{}
	first := antigravityHTTP11Transport(anonymous, base)
	second := antigravityHTTP11Transport(anonymous, base)
	if first == nil || second == nil {
		t.Fatal("expected a transport for an auth without an ID")
	}
	if first == second {
		t.Fatal("an auth without a stable ID must not be cached, otherwise a reused address grants another credential its pool")
	}

	blankID := antigravityHTTP11Transport(&cliproxyauth.Auth{ID: "   "}, base)
	if blankID == first || blankID == second {
		t.Fatal("a blank auth ID must not resolve to an existing pool")
	}
	if nilAuth := antigravityHTTP11Transport(nil, base); nilAuth == nil || nilAuth == first || nilAuth == blankID {
		t.Fatal("a nil auth must receive its own private pool")
	}

	// Identified auths keep sharing their pool.
	identified := &cliproxyauth.Auth{ID: "stable-identity"}
	if antigravityHTTP11Transport(identified, base) != antigravityHTTP11Transport(identified, base) {
		t.Fatal("an auth with a stable ID must reuse its cached pool")
	}
}

func TestAntigravityTransportScopeRequiresStableID(t *testing.T) {
	cases := []struct {
		name      string
		auth      *cliproxyauth.Auth
		wantScope string
		wantOK    bool
	}{
		{"nil auth", nil, "", false},
		{"missing id", &cliproxyauth.Auth{}, "", false},
		{"blank id", &cliproxyauth.Auth{ID: " \t "}, "", false},
		{"stable id", &cliproxyauth.Auth{ID: " auth-1 "}, "id:auth-1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scope, ok := antigravityTransportScope(tc.auth)
			if scope != tc.wantScope || ok != tc.wantOK {
				t.Fatalf("antigravityTransportScope() = (%q, %v), want (%q, %v)", scope, ok, tc.wantScope, tc.wantOK)
			}
		})
	}
}

func TestAntigravityTransportMatchesNativeTLSProfile(t *testing.T) {
	var clientHelloProtos []string
	var requestProto string
	var tlsVersion uint16
	var negotiatedProtocol string

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestProto = r.Proto
		if r.TLS != nil {
			tlsVersion = r.TLS.Version
			negotiatedProtocol = r.TLS.NegotiatedProtocol
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = &tls.Config{
		MinVersion: tls.VersionTLS13,
		MaxVersion: tls.VersionTLS13,
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			clientHelloProtos = append([]string(nil), hello.SupportedProtos...)
			return nil, nil
		},
	}
	server.StartTLS()
	defer server.Close()

	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	transport := antigravityHTTP11Transport(antigravityAuthWithIDAndProxy("native-tls-profile", ""), base)
	resp, errDo := (&http.Client{Transport: transport}).Get(server.URL)
	if errDo != nil {
		t.Fatalf("GET() error = %v", errDo)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatalf("close response body: %v", errClose)
	}

	if len(clientHelloProtos) != 0 {
		t.Fatalf("ClientHello ALPN = %v, want no ALPN extension", clientHelloProtos)
	}
	if requestProto != "HTTP/1.1" {
		t.Fatalf("request protocol = %q, want HTTP/1.1", requestProto)
	}
	if tlsVersion != tls.VersionTLS13 {
		t.Fatalf("TLS version = %#x, want TLS 1.3", tlsVersion)
	}
	if negotiatedProtocol != "" {
		t.Fatalf("negotiated ALPN = %q, want empty", negotiatedProtocol)
	}
}

// TestAntigravityProxiedRequestsReuseOneConnection proves the end-to-end effect:
// repeated Antigravity clients built for the same auth send every request over a
// single pooled connection.
func TestAntigravityProxiedRequestsReuseOneConnection(t *testing.T) {
	var mu sync.Mutex
	remotes := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		remotes[r.RemoteAddr]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &config.Config{}
	auth := antigravityAuthWithProxy(srv.URL)
	const requests = 8
	for i := 0; i < requests; i++ {
		client := newAntigravityHTTPClient(context.Background(), cfg, auth, 0)
		req, errReq := http.NewRequest(http.MethodGet, "http://antigravity.invalid/v1internal:streamGenerateContent", nil)
		if errReq != nil {
			t.Fatalf("NewRequest() error = %v", errReq)
		}
		resp, errDo := client.Do(req)
		if errDo != nil {
			t.Fatalf("request %d error = %v", i, errDo)
		}
		_ = resp.Body.Close()
	}

	mu.Lock()
	distinct := len(remotes)
	mu.Unlock()
	if distinct != 1 {
		t.Fatalf("expected %d requests to share one connection, got %d connections", requests, distinct)
	}
}
