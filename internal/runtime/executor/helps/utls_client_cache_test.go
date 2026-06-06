package helps

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

// TestNewUtlsHTTPClientReusesClientForSameProxy verifies that two calls with
// timeout==0 and the same resolved proxyURL return the identical *http.Client
// pointer, confirming process-wide reuse instead of a fresh handshake per call.
func TestNewUtlsHTTPClientReusesClientForSameProxy(t *testing.T) {
	cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://reuse-proxy.example.com:8080"}}

	first := NewUtlsHTTPClient(cfg, nil, 0)
	second := NewUtlsHTTPClient(cfg, nil, 0)

	if first == nil || second == nil {
		t.Fatalf("expected non-nil clients, got first=%v second=%v", first, second)
	}
	if first != second {
		t.Fatalf("expected same *http.Client pointer for identical proxyURL, got %p and %p", first, second)
	}
}

// TestNewUtlsHTTPClientDistinctProxyDistinctClient verifies that different
// resolved proxyURLs map to different cached clients.
func TestNewUtlsHTTPClientDistinctProxyDistinctClient(t *testing.T) {
	cfgA := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://proxy-a.example.com:8080"}}
	cfgB := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://proxy-b.example.com:8080"}}

	clientA := NewUtlsHTTPClient(cfgA, nil, 0)
	clientB := NewUtlsHTTPClient(cfgB, nil, 0)

	if clientA == clientB {
		t.Fatalf("expected distinct clients for distinct proxyURLs, got same pointer %p", clientA)
	}
}

// TestNewUtlsHTTPClientTimeoutBypassesCache verifies that timeout>0 always
// builds a dedicated client (never cached, never shared) to preserve the
// per-request timeout semantics.
func TestNewUtlsHTTPClientTimeoutBypassesCache(t *testing.T) {
	cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://timeout-proxy.example.com:8080"}}

	cached := NewUtlsHTTPClient(cfg, nil, 0)
	timed := NewUtlsHTTPClient(cfg, nil, 5*time.Second)
	timedAgain := NewUtlsHTTPClient(cfg, nil, 5*time.Second)

	if timed == cached {
		t.Fatalf("expected timeout client to differ from cached client")
	}
	if timed == timedAgain {
		t.Fatalf("expected each timeout>0 call to build a fresh client")
	}
	if timed.Timeout != 5*time.Second {
		t.Fatalf("expected timeout to be honored, got %v", timed.Timeout)
	}
}

// TestNewUtlsHTTPClientAuthProxyOverridesConfig verifies the cache key follows
// the same proxy resolution priority (auth.ProxyURL over cfg.ProxyURL).
func TestNewUtlsHTTPClientAuthProxyOverridesConfig(t *testing.T) {
	cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://config-proxy.example.com:8080"}}
	auth := &cliproxyauth.Auth{ProxyURL: "http://auth-proxy.example.com:8080"}

	withAuth := NewUtlsHTTPClient(cfg, auth, 0)
	withAuthAgain := NewUtlsHTTPClient(cfg, auth, 0)
	configOnly := NewUtlsHTTPClient(cfg, nil, 0)

	if withAuth != withAuthAgain {
		t.Fatalf("expected same client for identical auth proxyURL")
	}
	if withAuth == configOnly {
		t.Fatalf("expected auth proxyURL to key a different client than config proxyURL")
	}
}
