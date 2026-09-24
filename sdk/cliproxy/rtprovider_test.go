package cliproxy

import (
	"net/http"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestRoundTripperForDirectBypassesProxy(t *testing.T) {
	t.Parallel()

	provider := newDefaultRoundTripperProvider()
	rt := provider.RoundTripperFor(&coreauth.Auth{ProxyURL: "direct"})
	transport, ok := rt.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", rt)
	}
	if transport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestRoundTripperForEmptyProxyURLSharesPool(t *testing.T) {
	t.Parallel()
	provider := newDefaultRoundTripperProvider()
	a := provider.RoundTripperFor(&coreauth.Auth{})
	b := provider.RoundTripperFor(&coreauth.Auth{ProxyURL: "  "})
	if a == nil || b == nil {
		t.Fatal("expected shared pooled transport for empty ProxyURL")
	}
	if a != b {
		t.Fatal("empty ProxyURL must reuse one RoundTripper")
	}
}
