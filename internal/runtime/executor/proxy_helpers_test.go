package executor

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestNewProxyAwareHTTPClientDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	client := newProxyAwareHTTPClient(
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

	client := newProxyAwareHTTPClient(
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

	first := newProxyAwareHTTPClient(context.Background(), cfg, nil, 0)
	second := newProxyAwareHTTPClient(context.Background(), cfg, nil, 0)

	if first.Transport == nil || second.Transport == nil {
		t.Fatalf("expected transports to be configured, got %T and %T", first.Transport, second.Transport)
	}
	if first.Transport != second.Transport {
		t.Fatal("expected global proxy transport to be reused")
	}
}

type roundTripperSpy struct{}

func (spy *roundTripperSpy) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, nil
}
