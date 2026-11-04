package helps

import (
	"net/http"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
)

// GoogleQuotaExhaustion reports whether a Google error response is a 429
// RESOURCE_EXHAUSTED whose google.rpc.ErrorInfo reason is QUOTA_EXHAUSTED, and
// when the quota returns: the retry delay in the body, else a positive
// Retry-After header, else nil. RATE_LIMIT_EXCEEDED and keyword matches are
// not explicit quota exhaustion.
func GoogleQuotaExhaustion(statusCode int, headers http.Header, body []byte, now time.Time) (bool, *time.Duration) {
	if statusCode != http.StatusTooManyRequests || !strings.EqualFold(ojson.GetBytes(body, "error.status").String(), "RESOURCE_EXHAUSTED") {
		return false, nil
	}
	exhausted := false
	for _, detail := range ojson.GetBytes(body, "error.details").Array() {
		if detail.Get("@type").String() == "type.googleapis.com/google.rpc.ErrorInfo" && strings.EqualFold(detail.Get("reason").String(), "QUOTA_EXHAUSTED") {
			exhausted = true
		}
	}
	if !exhausted {
		return false, nil
	}
	if delay, err := ParseRetryDelay(body); err == nil && delay != nil && *delay > 0 {
		return true, delay
	}
	if retryAt, ok := parseRetryAfterHeader(getHeaderCaseInsensitive(headers, "Retry-After"), now); ok && retryAt.After(now) {
		reset := retryAt.Sub(now)
		return true, &reset
	}
	return true, nil
}
