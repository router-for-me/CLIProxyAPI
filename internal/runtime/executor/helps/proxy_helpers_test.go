package helps

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
)

func TestNewProxyAwareHTTPClientRequestProxyOverridesAuthAndGlobal(t *testing.T) {
	t.Parallel()

	ctx := coreexecutor.WithRequestProxyURL(context.Background(), "http://request-proxy.example:8081")
	client := NewProxyAwareHTTPClient(
		ctx,
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "http://auth-proxy.example:8080"},
		0,
	)
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy == nil {
		t.Fatalf("transport = %#v, want request proxy", client.Transport)
	}
	req, errReq := http.NewRequest(http.MethodGet, "https://upstream.example/v1", nil)
	if errReq != nil {
		t.Fatalf("request: %v", errReq)
	}
	proxyURL, errProxy := transport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("proxy: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://request-proxy.example:8081" {
		t.Fatalf("proxy URL = %v, want request proxy", proxyURL)
	}

	refreshCtx := coreexecutor.WithoutRequestProxyURL(ctx)
	refreshClient := NewProxyAwareHTTPClient(
		refreshCtx,
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "http://auth-proxy.example:8080"},
		0,
	)
	refreshTransport, ok := refreshClient.Transport.(*http.Transport)
	if !ok || refreshTransport.Proxy == nil {
		t.Fatalf("refresh transport = %#v, want auth proxy", refreshClient.Transport)
	}
	refreshProxy, errRefresh := refreshTransport.Proxy(req)
	if errRefresh != nil {
		t.Fatalf("refresh proxy: %v", errRefresh)
	}
	if refreshProxy == nil || refreshProxy.String() != "http://auth-proxy.example:8080" {
		t.Fatalf("refresh proxy URL = %v, want auth proxy", refreshProxy)
	}
}

func TestNewProxyAwareHTTPClientDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "direct"},
		0,
	)

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestNewDevinHTTPClient_ReusesTransportFromContext(t *testing.T) {
	baseTransport := &http.Transport{}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", baseTransport)

	c1 := NewDevinHTTPClient(ctx, nil, nil, 0)
	c2 := NewDevinHTTPClient(ctx, nil, nil, 0)

	if c1.Transport != c2.Transport {
		t.Errorf("expected c1.Transport == c2.Transport across requests, got different pointers %p vs %p", c1.Transport, c2.Transport)
	}

	tr, ok := c1.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", c1.Transport)
	}
	if !tr.DisableCompression {
		t.Error("expected DisableCompression = true")
	}
}

func TestNewDevinHTTPClient_NonStandardRoundTripperDisablesGzip(t *testing.T) {
	var seenEncoding string
	customRT := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		seenEncoding = req.Header.Get("Accept-Encoding")
		return &http.Response{StatusCode: 200}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", customRT)

	c := NewDevinHTTPClient(ctx, nil, nil, 0)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.invalid", nil)
	_, _ = c.Transport.RoundTrip(req)

	if seenEncoding != "identity" {
		t.Errorf("expected Accept-Encoding: identity, got %q", seenEncoding)
	}
}

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestProxyConnectionPoolReuse(t *testing.T) {
	for _, tc := range []struct {
		name            string
		enabled         bool
		wantConnections int
	}{{"disabled", false, 2}, {"enabled", true, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			ResetProxyTransports()
			t.Cleanup(ResetProxyTransports)
			var connections atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
			server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateNew {
					connections.Add(1)
				}
			}
			server.Start()
			defer server.Close()
			cfg := testConfig()
			auth := &cliproxyauth.Auth{ProxyURL: "direct"}
			if tc.enabled {
				auth.ProxyConnectionPool = poolEnabled()
			}
			for i := 0; i < 2; i++ {
				client := NewProxyAwareHTTPClient(context.Background(), cfg, auth, 0)
				resp, err := client.Get(server.URL)
				if err != nil {
					t.Fatal(err)
				}
				_, err = io.Copy(io.Discard, resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				if err = resp.Body.Close(); err != nil {
					t.Fatal(err)
				}
				if !tc.enabled {
					client.CloseIdleConnections()
				}
			}
			if got := connections.Load(); got != int32(tc.wantConnections) {
				t.Fatalf("connections = %d, want %d", got, tc.wantConnections)
			}
		})
	}
}

func TestProxyConnectionPoolSettingsIsolation(t *testing.T) {
	enabled := true
	maxIdle := 4
	cfg := testConfig()
	auth := poolAuth("settings", &cliproxyauth.ProxyConnectionPoolOverride{Enabled: &enabled, IdleConnTimeout: "15s", MaxIdleConnsPerHost: &maxIdle})
	defer ResetProxyTransports()
	first := pooledProxyTransport("direct", cfg, auth)
	if first == nil || first.IdleConnTimeout != 15*time.Second || first.MaxIdleConnsPerHost != 4 {
		t.Fatalf("unexpected transport: %+v", first)
	}
	if pooledProxyTransport("direct", cfg, auth) != first {
		t.Fatal("transport not reused")
	}
	auth.ProxyConnectionPool.IdleConnTimeout = "20s"
	if pooledProxyTransport("direct", cfg, auth) == first {
		t.Fatal("different settings reused transport")
	}
	if pooledProxyTransport("http://localhost:1234", cfg, auth) == first {
		t.Fatal("different proxies reused transport")
	}
	ResetProxyTransports()
	if pooledProxyTransport("direct", cfg, auth) == first {
		t.Fatal("reset did not remove transport")
	}
}

func TestProxyConnectionPoolAuthenticatedProxyIsolation(t *testing.T) {
	ResetProxyTransports()
	t.Cleanup(ResetProxyTransports)
	cfg := testConfig()
	auth := poolOptInAuth("credential-url")

	first := pooledProxyTransport("http://user:old-secret@localhost:1234", cfg, auth)
	second := pooledProxyTransport("http://user:old-secret@localhost:1234", cfg, auth)
	if first == nil || second != first {
		t.Fatal("identical authenticated proxy must reuse its pool")
	}
	rotated := pooledProxyTransport("http://user:new-secret@localhost:1234", cfg, auth)
	if rotated == nil || rotated == first {
		t.Fatal("different proxy credentials on the same host must not share a pool")
	}
	socks := pooledProxyTransport("socks5://user:old-secret@localhost:1234", cfg, auth)
	if socks == nil || socks == first || pooledProxyTransport("socks5://user:old-secret@localhost:1234", cfg, auth) != socks {
		t.Fatal("authenticated socks5 proxy must pool per scheme and credential")
	}
	setting, err := proxyutil.Parse("http://user:old-secret@localhost:1234")
	if err != nil {
		t.Fatal(err)
	}
	cacheKey, label := proxyPoolCacheTarget(setting)
	if strings.Contains(cacheKey, "old-secret") || strings.Contains(label, "old-secret") {
		t.Fatalf("cache target leaked proxy secret: key=%q label=%q", cacheKey, label)
	}
}
func TestProxyConnectionPoolCustomDefaultTransportDeclinesPooling(t *testing.T) {
	ResetProxyTransports()
	t.Cleanup(ResetProxyTransports)

	oldDefault := http.DefaultTransport
	defer func() { http.DefaultTransport = oldDefault }()

	customRT := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200}, nil
	})
	http.DefaultTransport = customRT

	cfg := testConfig()
	auth := poolOptInAuth("custom-default")

	// With empty proxy-url, pooling must be declined because http.DefaultTransport
	// cannot be cloned into *http.Transport.
	client := NewProxyAwareHTTPClient(context.Background(), cfg, auth, 0)
	if client.Transport != nil {
		t.Fatalf("client.Transport = %T, want nil so client uses http.DefaultTransport", client.Transport)
	}
}
