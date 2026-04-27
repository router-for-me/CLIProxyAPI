package helps

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

// TestNewProxyAwareHTTPClientDirectBypassesGlobalProxy tests that auth proxy takes precedence over config proxy
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

// TestNewProxyAwareHTTPClientFallbackToDefaultTransport tests that when no proxy or context transport is configured,
// the function falls back to a cloned default transport
func TestNewProxyAwareHTTPClientFallbackToDefaultTransport(t *testing.T) {
	t.Parallel()

	client := newProxyAwareHTTPClient(
		context.Background(),
		&config.Config{}, // No proxy configured
		nil,              // No auth
		0,                // No timeout
	)

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}

	// Verify it's not nil
	if transport == nil {
		t.Fatal("transport should not be nil")
	}

	// Verify it has reasonable default values (clone of DefaultTransport should have these)
	if transport.MaxIdleConns <= 0 {
		t.Fatalf("expected MaxIdleConns > 0, got %d", transport.MaxIdleConns)
	}
	if transport.IdleConnTimeout <= 0 {
		t.Fatalf("expected IdleConnTimeout > 0, got %d", transport.IdleConnTimeout)
	}
}

// TestNewProxyAwareHTTPClientUsesContextTransportWhenAvailable tests that context RoundTripper takes precedence
func TestNewProxyAwareHTTPClientUsesContextTransportWhenAvailable(t *testing.T) {
	t.Parallel()

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", &http.Transport{
		MaxIdleConns: 42,
	})

	client := newProxyAwareHTTPClient(
		ctx,
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://should-not-be-used.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "http://auth-proxy-should-not-be-used.example.com:8080"},
		0,
	)

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}

	// Should use the context transport, not the proxies
	if transport.MaxIdleConns != 42 {
		t.Fatalf("expected context transport MaxIdleConns = 42, got %d", transport.MaxIdleConns)
	}
}

// TestNewProxyAwareHTTPClientWithProxyUsesProxyTransport tests that when proxy is configured, it's used
func TestNewProxyAwareHTTPClientWithProxyUsesProxyTransport(t *testing.T) {
	t.Parallel()

	client := newProxyAwareHTTPClient(
		context.Background(),
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://test-proxy.example.com:8080"}},
		nil,
		0,
	)

	// Should have a transport set (not nil)
	if client.Transport == nil {
		t.Fatal("transport should not be nil when proxy is configured")
	}
}

// TestNewProxyAwareHTTPClientWithTimeoutSetsClientTimeout tests that timeout is properly set on the client
func TestNewProxyAwareHTTPClientWithTimeoutSetsClientTimeout(t *testing.T) {
	t.Parallel()

	timeout := 30 * time.Second
	client := newProxyAwareHTTPClient(
		context.Background(),
		&config.Config{},
		nil,
		timeout,
	)

	if client.Timeout != timeout {
		t.Fatalf("expected client timeout = %v, got %v", timeout, client.Timeout)
	}
}

// TestNewProxyAwareHTTPClientDirectModeInheritance tests that direct mode inherits default transport settings
func TestNewProxyAwareHTTPClientDirectModeInheritance(t *testing.T) {
	t.Parallel()

	client := newProxyAwareHTTPClient(
		context.Background(),
		&config.Config{}, // No proxy configured
		nil,              // No auth
		0,                // No timeout
	)

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}

	// Verify it's not nil
	if transport == nil {
		t.Fatal("transport should not be nil")
	}

	// Verify it has reasonable default values (clone of DefaultTransport should have these)
	if transport.MaxIdleConns <= 0 {
		t.Fatalf("expected MaxIdleConns > 0, got %d", transport.MaxIdleConns)
	}
	if transport.IdleConnTimeout <= 0 {
		t.Fatalf("expected IdleConnTimeout > 0, got %d", transport.IdleConnTimeout)
	}
	if transport.TLSHandshakeTimeout <= 0 {
		t.Fatalf("expected TLSHandshakeTimeout > 0, got %d", transport.TLSHandshakeTimeout)
	}
	if transport.ForceAttemptHTTP2 != true {
		t.Fatalf("expected ForceAttemptHTTP2 = true, got %v", transport.ForceAttemptHTTP2)
	}
}