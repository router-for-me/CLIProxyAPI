package claudemaster

import (
	"net/http"
	"reflect"
	"testing"
)

// Protocol values are from the credential-free 2.1.269/270 native capture.
// OAuth beta is explicit because the isolated executable capture used a fake
// API key; this fixture must not imply a live OAuth-header capture was made.
func nativeProtocolFixture() http.Header {
	return http.Header{
		"Content-Type": {"application/json"}, "Accept": {"application/json"},
		"Accept-Encoding":   {"gzip, deflate, br, zstd"},
		"User-Agent":        {"claude-cli/2.1.269 (external, sdk-cli)"},
		"Anthropic-Version": {"2023-06-01"},
		"Anthropic-Beta":    {"claude-code-20250219,oauth-2025-04-20", "native-feature-order-preserved"},
		"Anthropic-Dangerous-Direct-Browser-Access": {"true"}, "X-App": {"cli"},
		"X-Stainless-Lang": {"js"}, "X-Stainless-Package-Version": {"0.112.1"},
		"X-Stainless-Os": {"Linux"}, "X-Stainless-Arch": {"arm64"},
		"X-Stainless-Runtime": {"node"}, "X-Stainless-Runtime-Version": {"v26.3.0"},
		"X-Stainless-Timeout": {"600"}, "X-Stainless-Retry-Count": {"0"},
		"X-Client-Request-Id": {"11111111-1111-4111-8111-111111111111"},
	}
}

func TestBackendResponseHeaderBoundary(t *testing.T) {
	dst := make(http.Header)
	writeBackendProtocolHeaders(dst, http.Header{
		"Request-Id": {"req-123"}, "Retry-After": {"3"}, "Retry-After-Ms": {"250"},
		"X-Should-Retry": {"false"}, "Anthropic-Ratelimit-Unified-Status": {"allowed"},
		"Connection": {"X-Request-Id"}, "X-Request-Id": {"scoped-secret"},
		"Set-Cookie": {"secret"}, "Authorization": {"secret"}, "X-Account-Id": {"secret"},
		"Content-Encoding": {"gzip"}, "Content-Length": {"1234"},
	})
	want := http.Header{
		"Request-Id": {"req-123"}, "Retry-After": {"3"}, "Retry-After-Ms": {"250"},
		"X-Should-Retry": {"false"}, "Anthropic-Ratelimit-Unified-Status": {"allowed"},
	}
	if !reflect.DeepEqual(dst, want) {
		t.Fatalf("response protocol boundary mismatch: %v", dst)
	}
}
