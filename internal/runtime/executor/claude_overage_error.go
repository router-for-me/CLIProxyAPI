package executor

import (
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
)

// claudeOverageError marks an upstream rejection caused by the account's
// overage/spend-cap state rather than ordinary throttling. It wraps the
// classified status error so consumers can distinguish "wait" (throttle) from
// "cap reached" (overage) when deciding credential cooldown policy.
type claudeOverageError struct {
	statusErr
}

// OverageRejected reports that the rejection is an overage violation, so the
// credential should enter its long model-scoped overage cooldown instead of
// the short throttle ladder. The nil-receiver form keeps interface assertions
// on boxed nil pointers honest.
func (e *claudeOverageError) OverageRejected() bool { return e != nil }

// Unwrap exposes the embedded status error so errors.As consumers reach
// StatusCode and RetryAfter without knowing about this wrapper.
func (e *claudeOverageError) Unwrap() error { return &e.statusErr }

// claudeOverageBodyKeywords are the ASCII, lowercase-substring markers of an
// overage/spend-cap refusal in an upstream error body.
var claudeOverageBodyKeywords = []string{"usage limit", "spend cap", "credit balance"}

// overageIndicated reports whether the response marks an overage rejection:
// either the rate-limit headers say the overage window alone is rejected, or
// (for 403/429) the body carries an overage keyword while no usable Retry-After
// exists — a usable hint means the upstream is throttling, which wins.
func overageIndicated(statusCode int, headers http.Header, body []byte) bool {
	if helps.IsClaudeOverageOnlyRejection(headers) {
		return true
	}
	if statusCode != http.StatusForbidden && statusCode != http.StatusTooManyRequests {
		return false
	}
	if helps.ParseClaudeRetryAfterHeaders(headers, time.Now()) != nil {
		return false
	}
	message := strings.ToLower(string(body))
	for _, keyword := range claudeOverageBodyKeywords {
		if strings.Contains(message, keyword) {
			return true
		}
	}
	return false
}
