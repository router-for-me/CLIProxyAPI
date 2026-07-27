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

func TestNewProxyAwareHTTPClientReusesPooledClientsAndTransport(t *testing.T) {
	cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://pool-proxy.example.com:8080"}}
	auth := &cliproxyauth.Auth{}

	first := NewProxyAwareHTTPClient(context.Background(), cfg, auth, 0)
	seen := map[*http.Client]struct{}{first: {}}
	var sharedTransport http.RoundTripper = first.Transport

	for i := 0; i < proxyHTTPClientPoolSize*3; i++ {
		client := NewProxyAwareHTTPClient(context.Background(), cfg, auth, 0)
		seen[client] = struct{}{}
		if client.Transport != sharedTransport {
			t.Fatalf("iteration %d: transport was not shared across pooled clients", i)
		}
	}

	if len(seen) == 0 {
		t.Fatal("expected pooled clients")
	}
	if len(seen) > proxyHTTPClientPoolSize {
		t.Fatalf("unique clients = %d, want <= pool size %d", len(seen), proxyHTTPClientPoolSize)
	}

	transport, ok := sharedTransport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", sharedTransport)
	}
	if transport.IdleConnTimeout != 30*time.Second {
		t.Fatalf("IdleConnTimeout = %v, want 30s", transport.IdleConnTimeout)
	}
	if transport.MaxIdleConnsPerHost < 32 {
		t.Fatalf("MaxIdleConnsPerHost = %d, want >= 32", transport.MaxIdleConnsPerHost)
	}
}

func TestNewProxyAwareHTTPClientPoolKeyIncludesTimeout(t *testing.T) {
	cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://timeout-proxy.example.com:8080"}}
	auth := &cliproxyauth.Auth{}

	noTimeout := NewProxyAwareHTTPClient(context.Background(), cfg, auth, 0)
	withTimeout := NewProxyAwareHTTPClient(context.Background(), cfg, auth, 15*time.Second)

	if noTimeout == withTimeout {
		t.Fatal("clients with different timeouts should not be identical pool entries")
	}
	if withTimeout.Timeout != 15*time.Second {
		t.Fatalf("Timeout = %v, want 15s", withTimeout.Timeout)
	}
	if noTimeout.Timeout != 0 {
		t.Fatalf("Timeout = %v, want 0", noTimeout.Timeout)
	}
}
