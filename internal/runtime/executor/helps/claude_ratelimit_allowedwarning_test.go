package helps

import (
	"net/http"
	"testing"
)

// Regression: an account past the surpassed threshold reports "allowed_warning" on the
// shared 7d window. A Fable-only (7d_oi) rejection must stay model-scoped in that state,
// otherwise the whole credential is benched until the Fable window resets.
func TestFableOnlyRejectionWithAllowedWarningSharedWindow(t *testing.T) {
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-Status", "rejected")
	h.Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
	h.Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed_warning")
	h.Set("Anthropic-Ratelimit-Unified-7d-Surpassed-Threshold", "0.75")
	h.Set("Anthropic-Ratelimit-Unified-7d_oi-Status", "rejected")

	if ClaudeHeadersIndicateUnifiedRateLimitRejection(h) {
		t.Fatal("Fable-only rejection was classified as a shared/credential-scoped rejection " +
			"while the 7d window was allowed_warning; the credential will be benched for all models")
	}
}

// A genuine shared-window rejection must still be credential-scoped.
func TestSharedWindowRejectionStillCredentialScoped(t *testing.T) {
	for _, w := range []string{"5h", "7d"} {
		h := http.Header{}
		h.Set("Anthropic-Ratelimit-Unified-Status", "rejected")
		h.Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
		h.Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed_warning")
		h.Set("Anthropic-Ratelimit-Unified-"+w+"-Status", "rejected")
		if !ClaudeHeadersIndicateUnifiedRateLimitRejection(h) {
			t.Fatalf("%s rejection must remain credential-scoped", w)
		}
	}
}
