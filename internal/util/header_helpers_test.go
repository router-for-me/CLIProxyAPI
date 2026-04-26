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

func BenchmarkApplyCustomHeadersFromAttrs(b *testing.B) {
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		b.Fatalf("NewRequest error: %v", err)
	}
	attrs := map[string]string{
		"header:Host":        "upstream.example.com",
		"header:User-Agent":  "custom-agent/1.0",
		"header:X-Test":      "value",
		"header:X-Trace":     "trace-value",
		"ignored_attribute":  "ignored",
		"header:Empty-Value": "   ",
	}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		req.Header = make(http.Header)
		req.Host = ""
		ApplyCustomHeadersFromAttrs(req, attrs)
	}
}
