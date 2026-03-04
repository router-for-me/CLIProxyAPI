package executor

import (
	"testing"
)

func TestBuildProxyTransport_CachesByURL(t *testing.T) {
	proxyURL := "http://127.0.0.1:8080"

	first := buildProxyTransport(proxyURL)
	if first == nil {
		t.Fatal("expected first transport, got nil")
	}
	second := buildProxyTransport(proxyURL)
	if second == nil {
		t.Fatal("expected second transport, got nil")
	}
	if first != second {
		t.Fatal("expected cached transport pointer to be reused")
	}
}

func TestBuildProxyTransport_InvalidURLCachedAsFailure(t *testing.T) {
	proxyURL := "://bad url"
	if got := buildProxyTransport(proxyURL); got != nil {
		t.Fatalf("expected nil transport for invalid proxy, got %#v", got)
	}
	if got := buildProxyTransport(proxyURL); got != nil {
		t.Fatalf("expected nil transport for cached invalid proxy, got %#v", got)
	}
}
