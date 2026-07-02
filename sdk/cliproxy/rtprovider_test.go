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

func TestRoundTripperForUsesImplicitProxy(t *testing.T) {
	t.Parallel()

	auth := &coreauth.Auth{}
	auth.SetImplicitProxyURL("http://implicit-proxy.example.com:8080")
	provider := newDefaultRoundTripperProvider()
	rt := provider.RoundTripperFor(auth)
	transport, ok := rt.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", rt)
	}
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	proxyURL, errProxy := transport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("transport.Proxy() error = %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://implicit-proxy.example.com:8080" {
		t.Fatalf("transport.Proxy() = %v, want implicit proxy", proxyURL)
	}
}
