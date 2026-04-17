package helps

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestNewProxyAwareHTTPClientReusesDefaultClient(t *testing.T) {
	t.Parallel()

	first := NewProxyAwareHTTPClient(context.Background(), &config.Config{}, nil, 0)
	second := NewProxyAwareHTTPClient(context.Background(), &config.Config{}, nil, 0)

	if first != second {
		t.Fatal("expected default client to be reused")
	}
}

func TestNewProxyAwareHTTPClientUsesSharedDefaultTransportSettings(t *testing.T) {
	t.Parallel()

	client := NewProxyAwareHTTPClient(context.Background(), &config.Config{}, nil, 0)

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.MaxIdleConns != pooledTransportMaxIdleConns {
		t.Fatalf("MaxIdleConns = %d, want %d", transport.MaxIdleConns, pooledTransportMaxIdleConns)
	}
	if transport.MaxIdleConnsPerHost != pooledTransportMaxIdleConnsPerHost {
		t.Fatalf("MaxIdleConnsPerHost = %d, want %d", transport.MaxIdleConnsPerHost, pooledTransportMaxIdleConnsPerHost)
	}
	if transport.MaxConnsPerHost != 0 {
		t.Fatalf("MaxConnsPerHost = %d, want 0", transport.MaxConnsPerHost)
	}
	if transport.IdleConnTimeout != pooledTransportIdleConnTimeout {
		t.Fatalf("IdleConnTimeout = %s, want %s", transport.IdleConnTimeout, pooledTransportIdleConnTimeout)
	}
	if !transport.ForceAttemptHTTP2 {
		t.Fatal("expected ForceAttemptHTTP2 to be enabled")
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

func TestNewProxyAwareHTTPClientPrefersContextRoundTripperForAuthProxy(t *testing.T) {
	t.Parallel()

	expected := &roundTripperSpy{}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(expected))

	client := NewProxyAwareHTTPClient(
		ctx,
		&config.Config{},
		&cliproxyauth.Auth{ProxyURL: "http://auth-proxy.example.com:8080"},
		0,
	)

	if client.Transport != expected {
		t.Fatalf("transport = %T %v, want cached context round tripper", client.Transport, client.Transport)
	}
}

func TestNewProxyAwareHTTPClientCachesGlobalProxyTransport(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}}

	first := NewProxyAwareHTTPClient(context.Background(), cfg, nil, 0)
	second := NewProxyAwareHTTPClient(context.Background(), cfg, nil, 0)

	if first != second {
		t.Fatal("expected proxy-aware client to be reused")
	}
	if first.Transport == nil || second.Transport == nil {
		t.Fatalf("expected transports to be configured, got %T and %T", first.Transport, second.Transport)
	}
	if first.Transport != second.Transport {
		t.Fatal("expected global proxy transport to be reused")
	}
}

func TestNewProxyAwareHTTPClientSeparatesClientCacheByTimeout(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}}

	first := NewProxyAwareHTTPClient(context.Background(), cfg, nil, 0)
	second := NewProxyAwareHTTPClient(context.Background(), cfg, nil, 5*time.Second)

	if first == second {
		t.Fatal("expected timeout-specific clients to use separate cache entries")
	}
	if first.Transport != second.Transport {
		t.Fatal("expected timeout-specific clients to share transport")
	}
	if second.Timeout != 5*time.Second {
		t.Fatalf("Timeout = %s, want %s", second.Timeout, 5*time.Second)
	}
}

type roundTripperSpy struct{}

func (spy *roundTripperSpy) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, nil
}
