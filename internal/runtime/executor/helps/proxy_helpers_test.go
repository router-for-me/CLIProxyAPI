package helps

import (
	"context"
	"net/http"
	"sync"
	"testing"

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

func TestSharedProxyTransportReusesTransportPerURL(t *testing.T) {
	first := sharedProxyTransport("http://127.0.0.1:18080")
	if first == nil {
		t.Fatal("expected transport, got nil")
	}
	second := sharedProxyTransport("http://127.0.0.1:18080")
	if first != second {
		t.Fatal("expected cached transport to be reused")
	}
	other := sharedProxyTransport("http://127.0.0.1:18081")
	if other == nil {
		t.Fatal("expected transport for second proxy URL, got nil")
	}
	if other == first {
		t.Fatal("expected distinct transports for distinct proxy URLs")
	}
}

func TestSharedProxyTransportInvalidURLReturnsNil(t *testing.T) {
	if transport := sharedProxyTransport("ftp://invalid-scheme"); transport != nil {
		t.Fatalf("expected nil transport for unsupported scheme, got %v", transport)
	}
}

func TestNewProxyAwareHTTPClientSharesTransportAcrossCalls(t *testing.T) {
	cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://127.0.0.1:18082"}}

	clientA := NewProxyAwareHTTPClient(context.Background(), cfg, nil, 0)
	clientB := NewProxyAwareHTTPClient(context.Background(), cfg, nil, 0)
	if clientA.Transport == nil || clientB.Transport == nil {
		t.Fatal("expected proxy transports to be configured")
	}
	if clientA.Transport != clientB.Transport {
		t.Fatal("expected both clients to share one pooled transport")
	}
}

func TestSharedUtlsRoundTripperReusedPerProxyURL(t *testing.T) {
	first := sharedUtlsRoundTripper("")
	second := sharedUtlsRoundTripper("")
	if first != second {
		t.Fatal("expected cached utls round tripper to be reused")
	}
	proxied := sharedUtlsRoundTripper("socks5://127.0.0.1:1080")
	if proxied == first {
		t.Fatal("expected distinct utls round trippers for distinct proxy URLs")
	}
}

func TestSharedUtlsRoundTripperConcurrentAccess(t *testing.T) {
	var wg sync.WaitGroup
	results := make([]*utlsRoundTripper, 16)
	for i := range results {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = sharedUtlsRoundTripper("http://127.0.0.1:18083")
		}(i)
	}
	wg.Wait()
	for i := 1; i < len(results); i++ {
		if results[i] != results[0] {
			t.Fatal("expected all goroutines to receive the same round tripper")
		}
	}
}
