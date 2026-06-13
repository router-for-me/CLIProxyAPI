package executor

import (
	"net/http"
	"testing"
)

// TestNewClaudeStatusErrPopulatesRetryAfterOnlyOn429 asserts the only-on-429
// gate in newClaudeStatusErr. Header-parsing correctness is covered by
// helps.TestParseClaudeRetryAfter in the helps package; this test only
// verifies the wrapping logic that lives alongside the executor.
func TestNewClaudeStatusErrPopulatesRetryAfterOnlyOn429(t *testing.T) {
	headers := http.Header{
		"Retry-After": {"60"},
	}

	if got := newClaudeStatusErr(429, headers, []byte("rate limited")); got.retryAfter == nil {
		t.Fatal("429: expected retryAfter to be set")
	}
	if got := newClaudeStatusErr(500, headers, []byte("server error")); got.retryAfter != nil {
		t.Fatalf("500: expected retryAfter to be nil, got %v", *got.retryAfter)
	}
	if got := newClaudeStatusErr(401, headers, []byte("unauthorized")); got.retryAfter != nil {
		t.Fatalf("401: expected retryAfter to be nil, got %v", *got.retryAfter)
	}
}
