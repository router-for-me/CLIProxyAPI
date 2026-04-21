package util

import (
	"net/http"
	"testing"
)

func TestApplyCustomHeadersFromAttrsSetsHostOnRequest(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatalf("NewRequest error: %v", err)
	}

	ApplyCustomHeadersFromAttrs(req, map[string]string{
		"header:Host": "upstream.example.com",
	})

	if req.Host != "upstream.example.com" {
		t.Fatalf("req.Host = %q, want %q", req.Host, "upstream.example.com")
	}
	if req.Header.Get("Host") != "upstream.example.com" {
		t.Fatalf("req.Header.Get(\"Host\") = %q, want %q", req.Header.Get("Host"), "upstream.example.com")
	}
}
