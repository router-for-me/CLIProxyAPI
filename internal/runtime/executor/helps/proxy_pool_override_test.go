package helps

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// These tests share the process-wide pool and must not run in parallel.

func boolPtr(v bool) *bool { return &v }

func intPtr(v int) *int { return &v }

// TestProxyPoolRequiresCredentialOptIn pins the per-api-key switch: there is no global
// switch, so only a credential carrying a connection-pool block is pooled.
func TestProxyPoolRequiresCredentialOptIn(t *testing.T) {
	ResetProxyTransports()
	t.Cleanup(ResetProxyTransports)

	cfg := testConfig()
	optedIn := poolOptInAuth("ollama-key")
	plain := &cliproxyauth.Auth{ID: "other-key"}

	pooled := pooledProxyTransport("direct", cfg, optedIn)
	if pooled == nil {
		t.Fatal("opted-in credential got no transport")
	}
	if pooled.IdleConnTimeout != proxyPoolDefaultIdleConnTimeout {
		t.Fatalf("IdleConnTimeout = %s, want default %s", pooled.IdleConnTimeout, proxyPoolDefaultIdleConnTimeout)
	}
	if pooledProxyTransport("direct", cfg, optedIn) != pooled {
		t.Fatal("opted-in credential does not reuse its pool")
	}
	if pooledProxyTransport("direct", cfg, plain) == pooled {
		t.Fatal("credential without the block shared the pool")
	}
	first := pooledProxyTransport("direct", cfg, plain)
	second := pooledProxyTransport("direct", cfg, plain)
	if first == second {
		t.Fatal("credential without the block must not be pooled")
	}
	// A nil config still honours the credential block: pooling no longer needs a config.
	if tr := pooledProxyTransport("direct", nil, optedIn); tr == nil || tr != pooled {
		t.Fatal("nil config must still honour the credential block")
	}
}

// TestProxyPoolExplicitDisable covers a key opting out of its group block.
func TestProxyPoolExplicitDisable(t *testing.T) {
	ResetProxyTransports()
	t.Cleanup(ResetProxyTransports)

	optedOut := poolAuth("no-pool", &cliproxyauth.ProxyConnectionPoolOverride{Enabled: boolPtr(false)})
	first := pooledProxyTransport("direct", testConfig(), optedOut)
	second := pooledProxyTransport("direct", testConfig(), optedOut)
	if first == nil || second == nil {
		t.Fatal("transport missing")
	}
	if first == second {
		t.Fatal("credential that disabled pooling was still pooled")
	}
	if proxyTransportCache.Len() != 0 {
		t.Fatalf("opted-out credential polluted the cache: %d entries", proxyTransportCache.Len())
	}
}

// TestProxyPoolPartialSettings covers partial blocks: omitted fields use the defaults.
func TestProxyPoolPartialSettings(t *testing.T) {
	ResetProxyTransports()
	t.Cleanup(ResetProxyTransports)

	partial := poolAuth("partial", &cliproxyauth.ProxyConnectionPoolOverride{IdleConnTimeout: "45s"})
	tr := pooledProxyTransport("direct", testConfig(), partial)
	if tr == nil {
		t.Fatal("pooled transport missing")
	}
	if tr.IdleConnTimeout != 45*time.Second {
		t.Fatalf("IdleConnTimeout = %s, want 45s from the block", tr.IdleConnTimeout)
	}
	if tr.MaxIdleConnsPerHost != proxyPoolDefaultMaxIdleConnsPerHost {
		t.Fatalf("MaxIdleConnsPerHost = %d, want the default %d", tr.MaxIdleConnsPerHost, proxyPoolDefaultMaxIdleConnsPerHost)
	}

	other := poolAuth("partial-2", &cliproxyauth.ProxyConnectionPoolOverride{IdleConnTimeout: "60s"})
	if pooledProxyTransport("direct", testConfig(), other) == tr {
		t.Fatal("different settings shared one pool")
	}
}

// TestProxyPoolCredentialOptInWithoutProxyURL pins that pooling does not require a
// proxy-url: the inherited environment-proxy behavior is preserved.
func TestProxyPoolCredentialOptInWithoutProxyURL(t *testing.T) {
	ResetProxyTransports()
	t.Cleanup(ResetProxyTransports)

	cfg := testConfig()
	auth := poolOptInAuth("no-proxy-url")
	pooled := pooledProxyTransport("", cfg, auth)
	if pooled == nil {
		t.Fatal("opted-in credential without proxy-url got no transport")
	}
	if pooled.Proxy == nil {
		t.Fatal("inherited environment proxy behavior must be preserved")
	}
	if pooledProxyTransport("", cfg, auth) != pooled {
		t.Fatal("inherited pool not reused")
	}
	// A credential without the block keeps the shared default transport.
	if tr := pooledProxyTransport("", cfg, &cliproxyauth.Auth{ID: "plain"}); tr != nil {
		t.Fatal("unopted credential must keep using http.DefaultTransport")
	}
	// The generic client must wire the pooled transport in.
	client := NewProxyAwareHTTPClient(context.Background(), cfg, auth, 0)
	if client.Transport != pooled {
		t.Fatalf("client transport = %T, want the pooled transport", client.Transport)
	}
	// When proxy-url is empty, an explicitly injected context RoundTripper must keep
	// priority over the implicit credential pool, matching NewUtlsHTTPClient.
	injected := roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, nil })
	ctxWithRT := context.WithValue(context.Background(), "cliproxy.roundtripper", injected)
	injectedClient := NewProxyAwareHTTPClient(ctxWithRT, cfg, auth, 0)
	if injectedClient.Transport == pooled {
		t.Fatal("context RoundTripper must take priority when proxy-url is empty")
	}
	// Zero or negative pool bounds with an empty proxy-url must still return a
	// transport with DisableKeepAlives=true rather than nil (which would fall back
	// to http.DefaultTransport and keep pooling).
	for _, raw := range []string{"0s", "-1s"} {
		zeroIdle := poolAuth("zero-no-proxy", &cliproxyauth.ProxyConnectionPoolOverride{IdleConnTimeout: raw})
		if got := pooledProxyTransport("", cfg, zeroIdle); got == nil || !got.DisableKeepAlives {
			t.Fatalf("empty proxy-url with idle %s must disable keep-alives, got %+v", raw, got)
		}
	}
	for _, max := range []int{0, -1} {
		val := max
		zeroMax := poolAuth("max-no-proxy", &cliproxyauth.ProxyConnectionPoolOverride{MaxIdleConnsPerHost: &val})
		if got := pooledProxyTransport("", cfg, zeroMax); got == nil || !got.DisableKeepAlives {
			t.Fatalf("empty proxy-url with max %d must disable keep-alives, got %+v", max, got)
		}
	}
}
