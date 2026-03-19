package util

import (
	"net/http"
	"testing"
)

func TestClientAddressResolverPrefersRightmostUntrustedForwardedIP(t *testing.T) {
	resolver, err := NewClientAddressResolver([]string{"127.0.0.1/32", "10.0.0.0/8"})
	if err != nil {
		t.Fatalf("NewClientAddressResolver: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, "http://localhost/test", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.RemoteAddr = "127.0.0.1:8080"
	req.Header.Set("X-Forwarded-For", "127.0.0.1, 198.51.100.10, 10.0.0.9")

	info := resolver.Resolve(req)
	if info.ClientIP != "198.51.100.10" {
		t.Fatalf("ClientIP = %q, want %q", info.ClientIP, "198.51.100.10")
	}
	if info.IsLoopbackClient() {
		t.Fatal("expected non-loopback client after trusted proxy chain resolution")
	}
}

func TestClientAddressResolverUsesLastUntrustedForwardedElement(t *testing.T) {
	resolver, err := NewClientAddressResolver([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatalf("NewClientAddressResolver: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, "http://localhost/test", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.RemoteAddr = "10.0.0.2:8080"
	req.Header.Set("Forwarded", "for=203.0.113.8;proto=https, for=10.0.0.1")

	info := resolver.Resolve(req)
	if info.ClientIP != "203.0.113.8" {
		t.Fatalf("ClientIP = %q, want %q", info.ClientIP, "203.0.113.8")
	}
}
