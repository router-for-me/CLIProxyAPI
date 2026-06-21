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

// TestNewProxyAwareHTTPClientFallsBackToConfigHeaderTimeout verifies that when a
// caller passes timeout 0 (the legacy no-timeout value), the constructed
// transport picks up cfg.RequestTimeoutSeconds as ResponseHeaderTimeout. This
// bounds the connect + TLS + first-response-byte phase so a single request
// cannot hang indefinitely on a flaky upstream connection, while leaving the
// response body (including streaming) unbounded.
func TestNewProxyAwareHTTPClientFallsBackToConfigHeaderTimeout(t *testing.T) {
	t.Parallel()

	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{RequestTimeoutSeconds: 42},
		&cliproxyauth.Auth{ProxyURL: "http://proxy.example.com:8080"},
		0,
	)

	if client.Timeout != 0 {
		t.Fatalf("client.Timeout = %v, want 0 (must not set whole-request timeout; it cuts off streaming bodies)", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.ResponseHeaderTimeout != 42*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %v, want %v", transport.ResponseHeaderTimeout, 42*time.Second)
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
	if transport.ResponseHeaderTimeout != 7*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %v, want %v (explicit caller timeout must win)", transport.ResponseHeaderTimeout, 7*time.Second)
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
	if transport.ResponseHeaderTimeout != 0 {
		t.Fatalf("ResponseHeaderTimeout = %v, want 0 (legacy no-timeout when unconfigured)", transport.ResponseHeaderTimeout)
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
