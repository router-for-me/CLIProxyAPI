// Package misc provides miscellaneous utility functions for the CLI Proxy API server.
// It includes helper functions for HTTP header manipulation and other common operations
// that don't fit into more specific packages.
package misc

import (
	"net/http"
	"strings"
)

// EnsureHeader ensures that a header exists in the target header map by checking
// multiple sources in order of priority: source headers, existing target headers,
// and finally the default value. It only sets the header if it's not already present
// and the value is not empty after trimming whitespace.
//
// Parameters:
//   - target: The target header map to modify
//   - source: The source header map to check first (can be nil)
//   - key: The header key to ensure
//   - defaultValue: The default value to use if no other source provides a value
func EnsureHeader(target http.Header, source http.Header, key, defaultValue string) {
	if target == nil {
		return
	}
	if source != nil {
		if val := strings.TrimSpace(source.Get(key)); val != "" {
			target.Set(key, val)
			return
		}
	}
	if strings.TrimSpace(target.Get(key)) != "" {
		return
	}
	if val := strings.TrimSpace(defaultValue); val != "" {
		target.Set(key, val)
	}
}

// ScrubProxyAndFingerprintHeaders removes or normalizes proxy and fingerprint-related
// headers from an HTTP request to prevent information leakage.
func ScrubProxyAndFingerprintHeaders(req *http.Request) {
	if req == nil {
		return
	}
	// Remove proxy-related headers that might leak internal information
	headersToRemove := []string{
		"X-Forwarded-For",
		"X-Forwarded-Host",
		"X-Forwarded-Proto",
		"X-Real-IP",
		"Via",
		"Proxy-Connection",
	}
	for _, h := range headersToRemove {
		req.Header.Del(h)
	}
}

// GeminiCLIUserAgent returns the User-Agent header value for Gemini CLI requests.
func GeminiCLIUserAgent(model string) string {
	if model != "" {
		return "genai-cli/" + strings.TrimPrefix(model, "models/")
	}
	return "genai-cli/1.0"
}

// GeminiCLIApiClientHeader returns the X-Goog-Api-Client header value for Gemini CLI requests.
func GeminiCLIApiClientHeader() string {
	return "genai-cli/1.0 gl-go/1.1"
}
