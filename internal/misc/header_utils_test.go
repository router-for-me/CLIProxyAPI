package misc

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestScrubProxyAndFingerprintHeaders(t *testing.T) {
	t.Run("nil request is safe", func(t *testing.T) {
		ScrubProxyAndFingerprintHeaders(nil)
	})

	t.Run("strips stainless wildcard and fingerprint headers while keeping safe headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "https://example.com/v1/messages", nil)
		req.Header.Set("Authorization", "Bearer test-token")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "antigravity/1.0")

		// Proxy and client identity headers
		req.Header.Set("X-Forwarded-For", "1.2.3.4")
		req.Header.Set("X-Title", "SomeClient")
		req.Header.Set("X-Fingerprint", "fp-123")
		req.Header.Set("X-Goog-Safety-Level", "high")
		req.Header.Set("X-Client-Trace-Id", "trace-xyz")
		req.Header.Set("Referer", "https://app.example.com")

		// Dynamic X-Stainless-* headers (standard and custom suffixes, mixed case)
		req.Header.Set("X-Stainless-Lang", "js")
		req.Header.Set("X-Stainless-Retry-Count", "2")
		req.Header["x-stainless-custom-header"] = []string{"custom-val"}

		// Browser fingerprint headers
		req.Header.Set("Sec-Ch-Ua", `"Chromium";v="120"`)
		req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")

		ScrubProxyAndFingerprintHeaders(req)

		removed := []string{
			"X-Forwarded-For",
			"X-Title",
			"X-Fingerprint",
			"X-Goog-Safety-Level",
			"X-Client-Trace-Id",
			"Referer",
			"X-Stainless-Lang",
			"X-Stainless-Retry-Count",
			"x-stainless-custom-header",
			"Sec-Ch-Ua",
			"Accept-Encoding",
		}
		for _, h := range removed {
			if got := req.Header.Get(h); got != "" {
				t.Errorf("expected header %q to be removed, got %q", h, got)
			}
		}

		preserved := map[string]string{
			"Authorization": "Bearer test-token",
			"Content-Type":  "application/json",
			"User-Agent":    "antigravity/1.0",
		}
		for h, want := range preserved {
			if got := req.Header.Get(h); got != want {
				t.Errorf("header %q = %q, want %q", h, got, want)
			}
		}
	})
}
