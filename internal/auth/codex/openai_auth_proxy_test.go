package codex

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestNewCodexAuthWithProxy_UsesOverride(t *testing.T) {
	t.Parallel()

	auth := NewCodexAuthWithProxy(&config.Config{
		SDKConfig: config.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
	}, "direct")

	transport, ok := auth.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", auth.httpClient.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("expected direct override to disable proxy function")
	}
}

func TestNewCodexAuthWithProxy_NilConfigHonorsOverride(t *testing.T) {
	t.Parallel()

	// When cfg is nil, proxyURL override should still be applied
	auth := NewCodexAuthWithProxy(nil, "direct")

	transport, ok := auth.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", auth.httpClient.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("expected direct override to disable proxy function even when cfg is nil")
	}
}

func TestNewCodexAuthWithProxy_NilConfigEmptyOverride(t *testing.T) {
	t.Parallel()

	// When cfg is nil and proxyURL is empty, transport should be nil (default client behavior)
	auth := NewCodexAuthWithProxy(nil, "")

	if auth.httpClient.Transport != nil {
		t.Fatalf("expected nil transport when no proxy configured, got %T", auth.httpClient.Transport)
	}
}
