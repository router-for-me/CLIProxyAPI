package helps

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

func TestNewProxyAwareHTTPClientReusesAuthProxyTransport(t *testing.T) {
	t.Parallel()

	auth := &cliproxyauth.Auth{ProxyURL: "socks5://auth-proxy-cache.example.com:1080"}
	clientOne := NewProxyAwareHTTPClient(context.Background(), nil, auth, 0)
	clientTwo := NewProxyAwareHTTPClient(context.Background(), nil, auth, 0)

	if clientOne.Transport == nil {
		t.Fatal("expected first client transport to be set")
	}
	if clientTwo.Transport == nil {
		t.Fatal("expected second client transport to be set")
	}
	if clientOne.Transport != clientTwo.Transport {
		t.Fatal("expected auth proxy transport to be reused")
	}
}

func TestNewProxyAwareHTTPClientReusesGlobalProxyTransport(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy-cache.example.com:8080"},
	}
	clientOne := NewProxyAwareHTTPClient(context.Background(), cfg, nil, 0)
	clientTwo := NewProxyAwareHTTPClient(context.Background(), cfg, nil, 0)

	if clientOne.Transport == nil {
		t.Fatal("expected first client transport to be set")
	}
	if clientTwo.Transport == nil {
		t.Fatal("expected second client transport to be set")
	}
	if clientOne.Transport != clientTwo.Transport {
		t.Fatal("expected global proxy transport to be reused")
	}
}
