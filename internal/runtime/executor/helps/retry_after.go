package helps

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RetryAfter parses an upstream Retry-After header as either delta-seconds or
// an HTTP date. Invalid, absent, and already-expired values return nil.
func RetryAfter(headers http.Header, now time.Time) *time.Duration {
	if headers == nil {
		return nil
	}
	value := strings.TrimSpace(headers.Get("Retry-After"))
	if value == "" {
		return nil
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return nil
		}
		duration := time.Duration(seconds) * time.Second
		return &duration
	}
	retryAt, err := http.ParseTime(value)
	if err != nil {
		return nil
	}
	duration := retryAt.Sub(now)
	if duration <= 0 {
		return nil
	}
	return &duration
}
