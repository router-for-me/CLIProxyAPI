package executor

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

// The classifier must surface Anthropic's retry-at hints (unified rate-limit
// headers plus Retry-After) on the status error it returns, so the conductor's
// cooldown path can honor the upstream hint instead of the default ladder.
func TestClassifyClaudeUpstreamError_RetryAfterHeader(t *testing.T) {
	headers := http.Header{}
	headers.Set("Retry-After", "30")
	err := classifyClaudeUpstreamError(http.StatusTooManyRequests, headers, []byte("{}"))

	want := 30 * time.Second
	var status interface{ RetryAfter() *time.Duration }
	if !errors.As(err, &status) {
		t.Fatalf("classifier returned %T, want an error with RetryAfter()", err)
	}
	if got := status.RetryAfter(); got == nil || *got != want {
		t.Fatalf("RetryAfter() = %v, want %s", got, want)
	}
}

// Overage-only rejections are account caps, not throttles: Task 1's parser
// returns no retry hint for them and the end-to-end wiring must not invent one.
func TestClassifyClaudeUpstreamError_OverageOnlyRejectionHasNoHint(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-status", "rejected")
	headers.Set("anthropic-ratelimit-unified-overage-status", "rejected")
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	err := classifyClaudeUpstreamError(http.StatusTooManyRequests, headers, []byte("{}"))

	var status interface{ RetryAfter() *time.Duration }
	if !errors.As(err, &status) {
		t.Fatalf("classifier returned %T, want an error with RetryAfter()", err)
	}
	if got := status.RetryAfter(); got != nil {
		t.Fatalf("RetryAfter() = %s, want nil for an overage-only rejection", got)
	}
}

// Without any rate-limit headers the default cooldown ladder stays in charge.
func TestClassifyClaudeUpstreamError_NoHeadersNoHint(t *testing.T) {
	err := classifyClaudeUpstreamError(http.StatusTooManyRequests, http.Header{}, []byte("{}"))

	var status interface{ RetryAfter() *time.Duration }
	if !errors.As(err, &status) {
		t.Fatalf("classifier returned %T, want an error with RetryAfter()", err)
	}
	if got := status.RetryAfter(); got != nil {
		t.Fatalf("RetryAfter() = %s, want nil when no rate-limit headers are present", got)
	}
}

// Retry-After is parsed for any non-2xx status; the conductor only consumes the
// hint where it is meaningful, so a 500 with a header is harmless to surface.
func TestClassifyClaudeUpstreamError_RetryAfterOnNon429(t *testing.T) {
	headers := http.Header{}
	headers.Set("Retry-After", "5")
	err := classifyClaudeUpstreamError(http.StatusInternalServerError, headers, []byte("{}"))

	var status interface{ RetryAfter() *time.Duration }
	if !errors.As(err, &status) {
		t.Fatalf("classifier returned %T, want an error with RetryAfter()", err)
	}
	if got := status.RetryAfter(); got == nil || *got != 5*time.Second {
		t.Fatalf("RetryAfter() = %v, want 5s", got)
	}
}
