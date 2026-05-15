package helps

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

// ParseClaudeRetryAfter extracts a cooldown duration from Anthropic's
// rate-limit response headers, capped at maxClaudeRetryAfter. Priority:
//
//  1. anthropic-ratelimit-unified-reset (epoch seconds) — Anthropic's
//     authoritative reset moment, sent on every response.
//  2. Retry-After (RFC 7231 seconds-or-HTTP-date) — fallback for
//     intermediaries that inject it.
//
// fallbackNow is used as the reference point when the response carries no
// parseable Date header. When Date is present the server's clock anchors
// the calculation, so client clock skew can't distort cooldowns derived
// from absolute-epoch headers.
//
// Returns a non-nil duration when a header yields a non-negative future
// reset (zero is preserved as "retry immediately"). Past resets fall
// through; an absent or unparseable header pair returns nil.
func ParseClaudeRetryAfter(headers http.Header, fallbackNow time.Time) *time.Duration {
	now := fallbackNow
	if dateStr := headers.Get("Date"); dateStr != "" {
		if serverTime, errParse := http.ParseTime(dateStr); errParse == nil {
			now = serverTime
		}
	}
	if v := strings.TrimSpace(headers.Get("anthropic-ratelimit-unified-reset")); v != "" {
		if epoch, errParse := strconv.ParseInt(v, 10, 64); errParse == nil {
			if d := clampedRetryAfter(time.Unix(epoch, 0).Sub(now)); d != nil {
				return d
			}
		}
	}
	if v := strings.TrimSpace(headers.Get("Retry-After")); v != "" {
		if secs, errParse := strconv.ParseInt(v, 10, 64); errParse == nil && secs >= 0 {
			// Cap before multiplying to avoid int64 overflow into nanoseconds.
			if maxSecs := int64(maxClaudeRetryAfter / time.Second); secs > maxSecs {
				secs = maxSecs
			}
			d := time.Duration(secs) * time.Second
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

// clampedRetryAfter returns nil for past durations, preserves a zero
// duration as "retry immediately", and caps future durations at
// maxClaudeRetryAfter.
func clampedRetryAfter(d time.Duration) *time.Duration {
	if d < 0 {
		return nil
	}
	if d > maxClaudeRetryAfter {
		d = maxClaudeRetryAfter
	}
	return &d
}
