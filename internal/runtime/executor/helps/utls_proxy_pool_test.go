package helps

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestUtlsFallbackProxyConnectionPool(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			ResetProxyTransports()
			t.Cleanup(ResetProxyTransports)
			var connections atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
			server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateNew {
					connections.Add(1)
				}
			}
			server.Start()
			defer server.Close()
			cfg := testConfig()
			auth := &cliproxyauth.Auth{ProxyURL: "direct"}
			if enabled {
				auth.ProxyConnectionPool = poolEnabled()
			}
			for i := 0; i < 2; i++ {
				client := NewUtlsHTTPClient(context.Background(), cfg, auth, 0)
				resp, err := client.Get(server.URL)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = io.Copy(io.Discard, resp.Body); err != nil {
					t.Fatal(err)
				}
				if err = resp.Body.Close(); err != nil {
					t.Fatal(err)
				}
			}
			want := int32(2)
			if enabled {
				want = 1
			}
			if got := connections.Load(); got != want {
				t.Fatalf("connections = %d, want %d", got, want)
			}
		})
	}
}

// TestUtlsAnthropicTransportPoolsPerCredential pins the Anthropic fingerprint path:
// it is built by its own pool kind, so the credential's settings apply and two keys
// never share one connection. Previously this transport was always the shared
// per-proxy cache entry, which mixed distinct identities on one TLS connection and
// ignored max-idle-conns-per-host.
func TestUtlsAnthropicTransportPoolsPerCredential(t *testing.T) {
	ResetProxyTransports()
	t.Cleanup(ResetProxyTransports)
	cfg := testConfig()
	anthropicTransport := func(id string, override *cliproxyauth.ProxyConnectionPoolOverride) *http.Transport {
		t.Helper()
		client := NewUtlsHTTPClient(context.Background(), cfg, poolAuth(id, override), 0)
		fallback, ok := client.Transport.(*fallbackRoundTripper)
		if !ok {
			t.Fatalf("client transport = %T, want *fallbackRoundTripper", client.Transport)
		}
		transport, ok := fallback.anthropic.(*http.Transport)
		if !ok {
			t.Fatalf("anthropic transport = %T, want *http.Transport", fallback.anthropic)
		}
		return transport
	}

	first := anthropicTransport("key-a", poolBlock())
	if first.IdleConnTimeout != proxyPoolDefaultIdleConnTimeout || first.MaxIdleConnsPerHost != proxyPoolDefaultMaxIdleConnsPerHost {
		t.Fatalf("pool defaults not applied to the Anthropic transport: %+v", first)
	}
	if got := anthropicTransport("key-a", poolBlock()); got != first {
		t.Fatal("credential did not reuse its own Anthropic pool")
	}
	if second := anthropicTransport("key-b", poolBlock()); second == first {
		t.Fatal("two credentials share one Anthropic transport")
	}

	// Zero bounds disable keep-alives on the fingerprint transport too.
	zero := 0
	if transport := anthropicTransport("key-a", &cliproxyauth.ProxyConnectionPoolOverride{MaxIdleConnsPerHost: &zero}); !transport.DisableKeepAlives {
		t.Fatal("max-idle-conns-per-host: 0 must disable keep-alives on the Anthropic transport")
	}

	// Credentials that did not opt in keep the historical shared transport.
	plain := func(id string) http.RoundTripper {
		t.Helper()
		client := NewUtlsHTTPClient(context.Background(), cfg, &cliproxyauth.Auth{ID: id}, 0)
		return client.Transport.(*fallbackRoundTripper).anthropic
	}
	if plain("key-a") != plain("key-b") {
		t.Fatal("unpooled credentials must keep sharing the cached Claude Code transport")
	}
}
