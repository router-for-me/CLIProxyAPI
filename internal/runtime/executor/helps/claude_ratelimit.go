package helps

import (
	"net/http"
	"strconv"
	"time"
)

// Claude unified rate-limit status headers. Values are
// "allowed", "allowed_warning" or "rejected".
const (
	claudeUnifiedStatus   = "anthropic-ratelimit-unified-status"
	claude5hStatus        = "anthropic-ratelimit-unified-5h-status"
	claude7dStatus        = "anthropic-ratelimit-unified-7d-status"
	claudeOverageStatus   = "anthropic-ratelimit-unified-overage-status"
	claudeOverageDisabled = "anthropic-ratelimit-unified-overage-disabled-reason"
	claudeUnifiedUtil     = "anthropic-ratelimit-unified-utilization"
	claude5hUtil          = "anthropic-ratelimit-unified-5h-utilization"
	claude7dUtil          = "anthropic-ratelimit-unified-7d-utilization"
)

// getLower looks up a header value case-insensitively, including raw map
// writes that bypass http.Header.Set's canonical key casing.
func getLower(h http.Header, name string) string {
	lower := []byte(name)
	for k, vs := range h {
		if len(k) != len(lower) {
			continue
		}
		match := true
		for i := 0; i < len(k); i++ {
			if lowerByte(k[i]) != lower[i] {
				match = false
				break
			}
		}
		if match && len(vs) > 0 {
			return vs[0]
		}
	}
	return ""
}

func lowerByte(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + 'a' - 'A'
	}
	return b
}

// ClaudeSharedWindowRejected reports whether any of the unified, 5h or 7d
// rate-limit windows is "rejected". A shared-window rejection is a real
// throttle; Retry-After applies to it.
func ClaudeSharedWindowRejected(h http.Header) bool {
	status := func(name string) bool { return getLower(h, name) == "rejected" }
	return status(claudeUnifiedStatus) || status(claude5hStatus) || status(claude7dStatus)
}

// IsClaudeOverageOnlyRejection reports whether the account's overage/spend-cap
// window is rejected while all regular windows (unified/5h/7d) are healthy.
// This is a billing cap, not a throttle: no Retry-After wait applies. The
// check is conservative — unknown or ambiguous header states report false.
func IsClaudeOverageOnlyRejection(h http.Header) bool {
	if ClaudeSharedWindowRejected(h) {
		return false
	}
	if getLower(h, claudeOverageStatus) == "rejected" {
		return true
	}
	// No explicit overage status; the disabled-reason header signals the
	// overage cap was hit. Only accept it when every utilization header that
	// is present shows a healthy window (we cannot rule out a shared
	// rejection otherwise).
	if getLower(h, claudeOverageDisabled) == "" {
		return false
	}
	for _, name := range []string{claudeUnifiedUtil, claude5hUtil, claude7dUtil} {
		v := getLower(h, name)
		if v == "" {
			continue
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f >= 1.0 {
			return false
		}
	}
	return true
}

// ParseClaudeRetryAfterHeaders extracts the effective retry delay from
// Claude rate-limit response headers, or nil when none applies.
//
// It returns nil immediately when IsClaudeOverageOnlyRejection is true: an
// overage-only rejection is an account cap, not a throttle, so Retry-After
// there does not describe a useful wait and consuming it would mislead the
// per-credential cooldown ladder.
//
// Precedence: Retry-After (seconds, then HTTP-date), then Retry-After-Ms.
// Non-positive or unparseable values yield no hint; values are not capped.
func ParseClaudeRetryAfterHeaders(h http.Header, now time.Time) *time.Duration {
	if IsClaudeOverageOnlyRejection(h) {
		return nil
	}
	if v := getLower(h, "retry-after"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil {
			if secs > 0 {
				d := time.Duration(secs) * time.Second
				return &d
			}
			return nil
		}
		if until, err := time.Parse(http.TimeFormat, v); err == nil {
			d := until.Sub(now)
			if d > 0 {
				return &d
			}
			return nil
		}
	}
	if v := getLower(h, "retry-after-ms"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			d := time.Duration(ms) * time.Millisecond
			return &d
		}
	}
	return nil
}
