package helps

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

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

// TestNewProxyAwareHTTPClientFallsBackToConfigConnectTimeout verifies that when
// a caller passes timeout 0 (the legacy value), the constructed transport picks
// up cfg.RequestTimeoutSeconds as a dial + TLS handshake timeout. This bounds
// the connect phase where the process wedge in issue #3944 actually occurs,
// without limiting how long an active streaming (SSE) response body can run.
func TestNewProxyAwareHTTPClientFallsBackToConfigConnectTimeout(t *testing.T) {
	t.Parallel()

	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{RequestTimeoutSeconds: 42},
		&cliproxyauth.Auth{ProxyURL: "http://proxy.example.com:8080"},
		0,
	)

	if client.Timeout != 0 {
		t.Fatalf("client.Timeout = %v, want 0 (must never set whole-request timeout; it cuts off streaming bodies)", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.TLSHandshakeTimeout != 42*time.Second {
		t.Fatalf("TLSHandshakeTimeout = %v, want %v", transport.TLSHandshakeTimeout, 42*time.Second)
	}
	if transport.DialContext == nil {
		t.Fatal("DialContext is nil, expected a deadline-wrapped dialer")
	}
}

// TestNewProxyAwareHTTPClientExplicitTimeoutWinsOverConfig ensures a caller
// that passes a positive timeout still takes precedence over the config value.
func TestNewProxyAwareHTTPClientExplicitTimeoutWinsOverConfig(t *testing.T) {
	t.Parallel()

	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{RequestTimeoutSeconds: 42},
		&cliproxyauth.Auth{ProxyURL: "http://proxy.example.com:8080"},
		7*time.Second,
	)

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.TLSHandshakeTimeout != 7*time.Second {
		t.Fatalf("TLSHandshakeTimeout = %v, want %v (explicit caller timeout must win)", transport.TLSHandshakeTimeout, 7*time.Second)
	}
}

// TestNewProxyAwareHTTPClientZeroTimeoutNoConfigKeepsLegacy ensures the legacy
// no-timeout behavior is preserved when neither the caller nor the config set a
// timeout (backward compatible default).
func TestNewProxyAwareHTTPClientZeroTimeoutNoConfigKeepsLegacy(t *testing.T) {
	t.Parallel()

	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{},
		&cliproxyauth.Auth{ProxyURL: "http://proxy.example.com:8080"},
		0,
	)

	if client.Timeout != 0 {
		t.Fatalf("client.Timeout = %v, want 0", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	// In legacy mode applyConnectTimeout is never called, so TLSHandshakeTimeout
	// keeps the http.DefaultTransport default; verify it was not overridden by
	// this helper.
	defaultTLS := time.Duration(0)
	if dt, ok := http.DefaultTransport.(*http.Transport); ok && dt != nil {
		defaultTLS = dt.TLSHandshakeTimeout
	}
	if transport.TLSHandshakeTimeout != defaultTLS {
		t.Fatalf("TLSHandshakeTimeout = %v, want %v (default, untouched by helper in legacy mode)", transport.TLSHandshakeTimeout, defaultTLS)
	}
}

// TestNewProxyAwareHTTPClientZeroTimeoutNoProxyLeavesTransportNil verifies that
// when no timeout is configured and there is no proxy and no context round
// tripper, Transport stays nil. Callers such as antigravity_executor treat a nil
// transport as a signal to reuse a shared singleton transport, so installing one
// here would regress connection reuse for them.
func TestNewProxyAwareHTTPClientZeroTimeoutNoProxyLeavesTransportNil(t *testing.T) {
	t.Parallel()

	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{}, // no RequestTimeoutSeconds
		nil,              // no auth proxy
		0,
	)

	if client.Timeout != 0 {
		t.Fatalf("client.Timeout = %v, want 0", client.Timeout)
	}
	if client.Transport != nil {
		t.Fatalf("Transport = %T, want nil (legacy no-proxy path must leave Transport nil)", client.Transport)
	}
}

// TestNewProxyAwareHTTPClientNeverSetsWholeRequestTimeout is a regression guard:
// http.Client.Timeout must never be set by this helper, because it covers the
// entire request-response lifecycle including reading the response body, which
// would abort healthy streaming (SSE) responses mid-stream.
func TestNewProxyAwareHTTPClientNeverSetsWholeRequestTimeout(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{RequestTimeoutSeconds: 42}
	cases := []struct {
		name    string
		auth    *cliproxyauth.Auth
		timeout time.Duration
	}{
		{"config fallback proxy", &cliproxyauth.Auth{ProxyURL: "http://proxy.example.com:8080"}, 0},
		{"explicit timeout proxy", &cliproxyauth.Auth{ProxyURL: "http://proxy.example.com:8080"}, 10 * time.Second},
	}
	for _, c := range cases {
		client := NewProxyAwareHTTPClient(context.Background(), cfg, c.auth, c.timeout)
		if client.Timeout != 0 {
			t.Errorf("%s: client.Timeout = %v, want 0 (whole-request timeout would cut streaming bodies)", c.name, client.Timeout)
		}
	}
}

// TestSharedDefaultTransportCacheReused ensures the default-transport path
// returns the same cached transport for a given timeout instead of cloning a
// fresh one every call (which would disable connection reuse and leak idle
// sockets).
func TestSharedDefaultTransportCacheReused(t *testing.T) {
	t.Parallel()

	a := sharedDefaultTransportWithConnectTimeout(30 * time.Second)
	b := sharedDefaultTransportWithConnectTimeout(30 * time.Second)
	if a != b {
		t.Fatal("expected the same cached transport for the same timeout value")
	}
	c := sharedDefaultTransportWithConnectTimeout(60 * time.Second)
	if a == c {
		t.Fatal("expected a different transport for a different timeout value")
	}
}
