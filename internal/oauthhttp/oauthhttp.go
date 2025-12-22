package oauthhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrBodyTooLarge is returned when the response body exceeds MaxBodyBytes.
var ErrBodyTooLarge = errors.New("oauthhttp: response body too large")

// RetryConfig controls retry/backoff behavior for OAuth HTTP calls.
type RetryConfig struct {
	// MaxAttempts is the total number of attempts (including the first).
	MaxAttempts int
	// InitialBackoff is the delay before the first retry.
	InitialBackoff time.Duration
	// BackoffFactor is the multiplier applied after each attempt.
	BackoffFactor float64
	// MaxBackoff caps exponential backoff.
	MaxBackoff time.Duration
	// RetryOnStatus controls which HTTP status codes are retried.
	RetryOnStatus map[int]struct{}
	// MaxBodyBytes caps how much of the response body is read into memory (0 = 1MB default).
	MaxBodyBytes int64
}

// DefaultRetryConfig uses conservative OAuth HTTP defaults:
// max_attempts=3, backoff=0.5s, factor=2, max_backoff=10s, retry on 429 + common 5xx.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:    3,
		InitialBackoff: 500 * time.Millisecond,
		BackoffFactor:  2.0,
		MaxBackoff:     10 * time.Second,
		RetryOnStatus: map[int]struct{}{
			http.StatusTooManyRequests:     {},
			http.StatusInternalServerError: {},
			http.StatusBadGateway:          {},
			http.StatusServiceUnavailable:  {},
			http.StatusGatewayTimeout:      {},
		},
		MaxBodyBytes: 1 << 20, // 1MB
	}
}

// Do executes buildReq+client.Do with retry/backoff for transient OAuth failures.
//
// Returns:
//   - status: HTTP status code (0 when no HTTP response was received)
//   - headers: response headers (nil when no HTTP response was received)
//   - body: response body (may be partial if ErrBodyTooLarge)
//   - err: network/build errors, ErrBodyTooLarge when body exceeds limit, or a retry exhaustion error when the final
//     response status was retryable (e.g. repeated 503s)
func Do(
	ctx context.Context,
	client *http.Client,
	buildReq func() (*http.Request, error),
	cfg RetryConfig,
) (status int, headers http.Header, body []byte, err error) {
	if client == nil {
		return 0, nil, nil, fmt.Errorf("oauthhttp: http client is nil")
	}
	if buildReq == nil {
		return 0, nil, nil, fmt.Errorf("oauthhttp: buildReq is nil")
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = DefaultRetryConfig().MaxAttempts
	}
	if cfg.InitialBackoff <= 0 {
		cfg.InitialBackoff = DefaultRetryConfig().InitialBackoff
	}
	if cfg.BackoffFactor <= 0 {
		cfg.BackoffFactor = DefaultRetryConfig().BackoffFactor
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = DefaultRetryConfig().MaxBackoff
	}
	if cfg.RetryOnStatus == nil {
		cfg.RetryOnStatus = DefaultRetryConfig().RetryOnStatus
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = DefaultRetryConfig().MaxBodyBytes
	}
	if ctx == nil {
		ctx = context.Background()
	}

	backoff := cfg.InitialBackoff

	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		req, errBuild := buildReq()
		if errBuild != nil {
			return 0, nil, nil, errBuild
		}
		if req == nil {
			return 0, nil, nil, fmt.Errorf("oauthhttp: buildReq returned nil request")
		}
		if req.Context() == nil {
			req = req.WithContext(ctx)
		}

		resp, errDo := client.Do(req)
		if errDo != nil {
			// Network/transport error.
			if attempt >= cfg.MaxAttempts {
				return 0, nil, nil, errDo
			}
			if errWait := wait(ctx, backoff); errWait != nil {
				return 0, nil, nil, errWait
			}
			backoff = nextBackoff(backoff, cfg.BackoffFactor, cfg.MaxBackoff)
			continue
		}

		status = resp.StatusCode
		headers = resp.Header.Clone()
		body, err = readLimitedAndClose(resp.Body, cfg.MaxBodyBytes)
		if err != nil {
			// Body read error or size cap. Don't retry; surface for visibility.
			return status, headers, body, err
		}

		if _, retryable := cfg.RetryOnStatus[status]; !retryable {
			return status, headers, body, nil
		}
		if attempt >= cfg.MaxAttempts {
			return status, headers, body, fmt.Errorf("oauthhttp: request failed with status %d after %d attempts", status, cfg.MaxAttempts)
		}

		delay := backoff
		if ra := retryAfter(headers.Get("Retry-After")); ra > delay {
			delay = ra
		}
		if errWait := wait(ctx, delay); errWait != nil {
			return status, headers, body, errWait
		}
		backoff = nextBackoff(backoff, cfg.BackoffFactor, cfg.MaxBackoff)
	}

	return status, headers, body, nil
}

func nextBackoff(current time.Duration, factor float64, max time.Duration) time.Duration {
	if current <= 0 {
		current = 500 * time.Millisecond
	}
	if factor <= 0 {
		factor = 2.0
	}
	next := time.Duration(float64(current) * factor)
	if max > 0 && next > max {
		return max
	}
	return next
}

func wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func readLimitedAndClose(rc io.ReadCloser, maxBytes int64) ([]byte, error) {
	if rc == nil {
		return nil, nil
	}
	defer func() { _ = rc.Close() }()

	limit := maxBytes
	if limit <= 0 {
		limit = 1 << 20
	}
	r := io.LimitReader(rc, limit+1)
	data, err := io.ReadAll(r)
	if err != nil {
		return data, err
	}
	if int64(len(data)) > limit {
		return data[:limit], ErrBodyTooLarge
	}
	return data, nil
}

func retryAfter(raw string) time.Duration {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0
	}
	if seconds, err := strconv.ParseFloat(s, 64); err == nil && seconds > 0 {
		return time.Duration(seconds * float64(time.Second))
	}
	if t, err := http.ParseTime(s); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d
		}
	}
	return 0
}
