package helps

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ParseRetryAfterHeader accepts HTTP delay-seconds or an HTTP-date. Reject
// overflowing delays rather than allowing a conversion to wrap negative.
func ParseRetryAfterHeader(value string, now time.Time) *time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	digits := true
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			digits = false
			break
		}
	}
	if digits {
		seconds, err := strconv.ParseInt(value, 10, 64)
		if err != nil || seconds > int64((time.Duration(1<<63-1))/time.Second) {
			return nil
		}
		delay := time.Duration(seconds) * time.Second
		return &delay
	}
	date, err := http.ParseTime(value)
	if err != nil {
		return nil
	}
	delay := date.Sub(now)
	if delay < 0 {
		delay = 0
	}
	return &delay
}
