package util

import (
	"net/http"
	"net/url"
	"testing"
)

func TestApplyCustomHeadersFromAttrs_PreservesCasing(t *testing.T) {
	req, err := http.NewRequest("GET", "http://example.com", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}

	attrs := map[string]string{
		"header:x-api-key":     "secret123",
		"header:x-custom-auth": "token",
	}

	ApplyCustomHeadersFromAttrs(req, attrs)

	// Go's net/http Header.Set canonicalizes keys via
	// textproto.CanonicalMIMEHeaderKey. We use direct map assignment so that
	// the user-supplied casing is preserved in the Header map and therefore
	// in the request actually written to the wire.
	if _, ok := req.Header["x-api-key"]; !ok {
		t.Errorf("expected exact-case header %q in Header map; got keys %v", "x-api-key", req.Header)
	}
	if _, ok := req.Header["x-custom-auth"]; !ok {
		t.Errorf("expected exact-case header %q in Header map; got keys %v", "x-custom-auth", req.Header)
	}

	// Confirm the values are correct via direct map access.
	if got := req.Header["x-api-key"]; len(got) != 1 || got[0] != "secret123" {
		t.Errorf("x-api-key = %v, want [secret123]", got)
	}
	if got := req.Header["x-custom-auth"]; len(got) != 1 || got[0] != "token" {
		t.Errorf("x-custom-auth = %v, want [token]", got)
	}
}

func TestApplyCustomHeadersFromAttrs_NilHeaderMap(t *testing.T) {
	// Synthetic requests may be created with a nil Header map. Direct
	// assignment must not panic and should initialize the map.
	req := &http.Request{Method: "GET", URL: &url.URL{Scheme: "http", Host: "example.com"}}
	if req.Header != nil {
		t.Fatalf("test setup: expected nil Header map")
	}

	ApplyCustomHeadersFromAttrs(req, map[string]string{
		"header:x-api-key": "secret123",
	})

	if req.Header == nil {
		t.Fatalf("ApplyCustomHeadersFromAttrs did not initialize nil Header map")
	}
	if got := req.Header["x-api-key"]; len(got) != 1 || got[0] != "secret123" {
		t.Errorf("x-api-key = %v, want [secret123]", got)
	}
}
