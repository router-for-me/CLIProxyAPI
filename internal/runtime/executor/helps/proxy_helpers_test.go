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

func assertTransportTuned(t *testing.T, transport *http.Transport, label string) {
	t.Helper()
	if transport.MaxIdleConns != 200 {
		t.Errorf("%s: MaxIdleConns = %d, want 200", label, transport.MaxIdleConns)
	}
	if transport.MaxIdleConnsPerHost != 20 {
		t.Errorf("%s: MaxIdleConnsPerHost = %d, want 20", label, transport.MaxIdleConnsPerHost)
	}
	if transport.IdleConnTimeout != 90*time.Second {
		t.Errorf("%s: IdleConnTimeout = %v, want 90s", label, transport.IdleConnTimeout)
	}
	if !transport.ForceAttemptHTTP2 {
		t.Errorf("%s: ForceAttemptHTTP2 = false, want true", label)
	}
}

func TestNewProxyAwareHTTPClient_DefaultTransportTuned(t *testing.T) {
	t.Parallel()

	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Skip("http.DefaultTransport is not *http.Transport")
	}

	assertTransportTuned(t, transport, "global tuned transport")

	client := NewProxyAwareHTTPClient(context.Background(), nil, nil, 0)
	if client.Transport != nil {
		t.Error("expected NewProxyAwareHTTPClient to return nil transport when no proxy is configured to use http.DefaultTransport")
	}
}

func TestNewProxyAwareHTTPClient_ContextRoundTripperReused(t *testing.T) {
	t.Parallel()

	original := &http.Transport{
		MaxIdleConns:        1,
		MaxIdleConnsPerHost: 1,
		IdleConnTimeout:     time.Second,
		ForceAttemptHTTP2:   false,
	}

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", original)
	client := NewProxyAwareHTTPClient(ctx, nil, nil, 0)

	if client.Transport != original {
		t.Error("expected original context RoundTripper to be reused without cloning")
	}

	// Verify the original transport was NOT mutated by NewProxyAwareHTTPClient
	if original.MaxIdleConns != 1 || original.MaxIdleConnsPerHost != 1 {
		t.Error("original transport was mutated, but it should be preserved as-is when provided via context")
	}
}
