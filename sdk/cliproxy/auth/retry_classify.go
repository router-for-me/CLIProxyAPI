package auth

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// DefaultRetryStatuses are the HTTP status codes that trigger a per-entry
// retry before pool failover. Hard-failover classes (400, 401, 403, 404, 422)
// are intentionally excluded.
var DefaultRetryStatuses = map[int]struct{}{
	408: {}, 429: {}, 500: {}, 502: {}, 503: {}, 504: {},
}

// StatusResponder is the minimal surface needed from an HTTP response for
// retry classification. *http.Response satisfies it via the provided
// adapter helpers.
type StatusResponder interface {
	StatusCode() int
	HeaderGet(string) string
}

type httpRespAdapter struct{ r *http.Response }

func (a *httpRespAdapter) StatusCode() int           { return a.r.StatusCode }
func (a *httpRespAdapter) HeaderGet(k string) string { return a.r.Header.Get(k) }

// AdaptHTTPResponse wraps an *http.Response so it satisfies StatusResponder.
// Returns nil if r is nil.
func AdaptHTTPResponse(r *http.Response) StatusResponder {
	if r == nil {
		return nil
	}
	return &httpRespAdapter{r: r}
}

// IsRetryableStatus reports whether the status code is in
// DefaultRetryStatuses.
func IsRetryableStatus(code int) bool {
	_, ok := DefaultRetryStatuses[code]
	return ok
}

// IsRetryableError reports whether err is a transient network error
// (connection refused, DNS, TLS handshake, EOF/UnexpectedEOF, wrapped
// url.Error or net.OpError). Application-level errors are NOT retried.
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		// url.Error wraps connection refused, DNS failures, TLS
		// handshakes, and EOF during body read — all transient.
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	return false
}

// IsRetryable is the unified predicate: retry when either the response
// status is transient OR the error is a transient network error.
// A nil response with a nil error is NOT retryable (caller treats it as
// a non-event; the attempt didn't actually run).
func IsRetryable(resp StatusResponder, err error) bool {
	if err != nil && IsRetryableError(err) {
		return true
	}
	if resp != nil && IsRetryableStatus(resp.StatusCode()) {
		return true
	}
	return false
}

// RetryAfterDuration parses a Retry-After header (delta-seconds or
// HTTP-date) and returns the duration to wait, capped to time.Until(deadline).
// Returns 0 when the header is missing or unparseable.
func RetryAfterDuration(r StatusResponder, deadline time.Time) time.Duration {
	if r == nil {
		return 0
	}
	v := strings.TrimSpace(r.HeaderGet("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		d := time.Duration(secs) * time.Second
		if remaining := time.Until(deadline); remaining > 0 && d > remaining {
			return remaining
		}
		return d
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		if remaining := time.Until(deadline); remaining > 0 && d > remaining {
			return remaining
		}
		return d
	}
	logrus.WithField("retry_after", v).Debug("retry_classify: unparseable Retry-After header")
	return 0
}
