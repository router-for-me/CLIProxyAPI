package executor

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxClaudeRetryAfter caps cooldown durations parsed from upstream rate-limit
// headers at Anthropic's longest enforced quota window (7 days). A buggy or
// malformed upstream value (e.g. an off-by-1000 ms-vs-s mistake producing a
// year-5138 epoch) would otherwise lock out a credential effectively forever.
const maxClaudeRetryAfter = 7 * 24 * time.Hour

// newClaudeStatusErr builds a statusErr for an Anthropic Claude error
// response, populating retryAfter on 429 from the response headers.
func newClaudeStatusErr(statusCode int, headers http.Header, body []byte) statusErr {
	err := statusErr{code: statusCode, msg: string(body)}
	if statusCode == http.StatusTooManyRequests {
		if d := parseClaudeRetryAfter(headers, time.Now()); d != nil {
			err.retryAfter = d
		}
	}
	return err
}

// parseClaudeRetryAfter extracts a cooldown duration from Anthropic's
// rate-limit response headers, capped at maxClaudeRetryAfter. Priority:
//
//  1. anthropic-ratelimit-unified-reset (epoch seconds) — Anthropic's
//     authoritative reset moment, sent on every response.
//  2. Retry-After (RFC 7231 seconds-or-HTTP-date) — fallback for
//     intermediaries that inject it.
//
// Returns nil when neither header yields a positive future duration.
func parseClaudeRetryAfter(headers http.Header, now time.Time) *time.Duration {
	if v := strings.TrimSpace(headers.Get("anthropic-ratelimit-unified-reset")); v != "" {
		if epoch, errParse := strconv.ParseInt(v, 10, 64); errParse == nil {
			if d := clampedRetryAfter(time.Unix(epoch, 0).Sub(now)); d != nil {
				return d
			}
		}
	}
	if v := strings.TrimSpace(headers.Get("Retry-After")); v != "" {
		if secs, errParse := strconv.Atoi(v); errParse == nil && secs > 0 {
			// Cap before multiplying to avoid int64 overflow into nanoseconds.
			s := int64(secs)
			if maxSecs := int64(maxClaudeRetryAfter / time.Second); s > maxSecs {
				s = maxSecs
			}
			d := time.Duration(s) * time.Second
			return &d
		}
		if t, errParse := http.ParseTime(v); errParse == nil {
			if d := clampedRetryAfter(t.Sub(now)); d != nil {
				return d
			}
		}
	}
	return nil
}

// clampedRetryAfter returns nil for non-positive durations and caps positive
// durations at maxClaudeRetryAfter.
func clampedRetryAfter(d time.Duration) *time.Duration {
	if d <= 0 {
		return nil
	}
	if d > maxClaudeRetryAfter {
		d = maxClaudeRetryAfter
	}
	return &d
}
