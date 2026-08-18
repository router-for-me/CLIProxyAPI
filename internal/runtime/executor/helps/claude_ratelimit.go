package helps

import (
	cryptorand "crypto/rand"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	defaultClaudeRateLimitFuzzMinSeconds = 1
	defaultClaudeRateLimitFuzzMaxSeconds = 30
)

// ClaudeHeadersIndicateUnifiedRateLimitRejection reports whether response headers explicitly
// declare an Anthropic unified 5h or 7d rate-limit rejection.
func ClaudeHeadersIndicateUnifiedRateLimitRejection(headers http.Header) bool {
	if headers == nil {
		return false
	}
	unifiedStatus := strings.ToLower(strings.TrimSpace(getHeaderCaseInsensitive(headers, "Anthropic-Ratelimit-Unified-Status")))
	if unifiedStatus == "rejected" {
		return true
	}
	status5h := strings.ToLower(strings.TrimSpace(getHeaderCaseInsensitive(headers, "Anthropic-Ratelimit-Unified-5h-Status")))
	if status5h == "rejected" {
		return true
	}
	status7d := strings.ToLower(strings.TrimSpace(getHeaderCaseInsensitive(headers, "Anthropic-Ratelimit-Unified-7d-Status")))
	if status7d == "rejected" {
		return true
	}
	return false
}

// ParseClaudeRateLimitReset inspects Anthropic response headers for unified rate-limit
// and standard Retry-After reset information, returning the conservative cooldown
// duration including a bounded non-negative random grace period.
// If no valid future reset information is present, it returns nil.
func ParseClaudeRateLimitReset(headers http.Header, now time.Time) *time.Duration {
	return parseClaudeRateLimitResetWithFuzz(headers, now, defaultClaudeRateLimitFuzzMinSeconds, defaultClaudeRateLimitFuzzMaxSeconds)
}

func parseClaudeRateLimitResetWithFuzz(headers http.Header, now time.Time, minFuzzSec, maxFuzzSec int) *time.Duration {
	if headers == nil {
		return nil
	}

	unifiedStatus := strings.ToLower(strings.TrimSpace(getHeaderCaseInsensitive(headers, "Anthropic-Ratelimit-Unified-Status")))
	status5h := strings.ToLower(strings.TrimSpace(getHeaderCaseInsensitive(headers, "Anthropic-Ratelimit-Unified-5h-Status")))
	status7d := strings.ToLower(strings.TrimSpace(getHeaderCaseInsensitive(headers, "Anthropic-Ratelimit-Unified-7d-Status")))

	var candidateDeadlines []time.Time
	var rejectedWindows []string

	if unifiedStatus == "rejected" {
		rejectedWindows = append(rejectedWindows, "unified")
	}
	if status5h == "rejected" {
		rejectedWindows = append(rejectedWindows, "5h")
	}
	if status7d == "rejected" {
		rejectedWindows = append(rejectedWindows, "7d")
	}

	// 1. Retry-After header
	if rawRetryAfter := getHeaderCaseInsensitive(headers, "Retry-After"); rawRetryAfter != "" {
		if !containsString(rejectedWindows, "retry-after") {
			rejectedWindows = append(rejectedWindows, "retry-after")
		}
		if t, ok := parseRetryAfterHeader(rawRetryAfter, now); ok && t.After(now) {
			candidateDeadlines = append(candidateDeadlines, t)
		}
	}

	// 2. 5-hour window reset (only when rejected)
	if status5h == "rejected" {
		if raw := getHeaderCaseInsensitive(headers, "Anthropic-Ratelimit-Unified-5h-Reset"); raw != "" {
			if t, ok := parseUnixOrTimestamp(raw); ok && t.After(now) {
				candidateDeadlines = append(candidateDeadlines, t)
			}
		}
	}

	// 3. 7-day window reset (only when rejected)
	if status7d == "rejected" {
		if raw := getHeaderCaseInsensitive(headers, "Anthropic-Ratelimit-Unified-7d-Reset"); raw != "" {
			if t, ok := parseUnixOrTimestamp(raw); ok && t.After(now) {
				candidateDeadlines = append(candidateDeadlines, t)
			}
		}
	}

	// 4. Unified reset header:
	unifiedRejected := unifiedStatus == "rejected" || status5h == "rejected" || status7d == "rejected" ||
		(unifiedStatus == "" && status5h != "allowed" && status7d != "allowed")

	if unifiedRejected {
		if raw := getHeaderCaseInsensitive(headers, "Anthropic-Ratelimit-Unified-Reset"); raw != "" {
			if !containsString(rejectedWindows, "unified") {
				rejectedWindows = append(rejectedWindows, "unified")
			}
			if t, ok := parseUnixOrTimestamp(raw); ok && t.After(now) {
				candidateDeadlines = append(candidateDeadlines, t)
			}
		}
	}

	if len(candidateDeadlines) == 0 {
		if len(rejectedWindows) > 0 {
			log.WithFields(log.Fields{
				"rejected_windows": strings.Join(rejectedWindows, ","),
				"status":           "fallback_exponential_backoff",
			}).Info("Anthropic rate limit window rejected; falling back to generic exponential backoff")
		}
		return nil
	}

	// Pick the latest applicable deadline across rejected windows
	var latestDeadline time.Time
	for _, deadline := range candidateDeadlines {
		if deadline.After(latestDeadline) {
			latestDeadline = deadline
		}
	}

	if latestDeadline.IsZero() || !latestDeadline.After(now) {
		if len(rejectedWindows) > 0 {
			log.WithFields(log.Fields{
				"rejected_windows": strings.Join(rejectedWindows, ","),
				"status":           "fallback_exponential_backoff",
			}).Info("Anthropic rate limit window rejected; falling back to generic exponential backoff")
		}
		return nil
	}

	baseDuration := latestDeadline.Sub(now)
	fuzz := randomClaudeFuzzDuration(minFuzzSec, maxFuzzSec)
	effectiveDuration := baseDuration + fuzz

	log.WithFields(log.Fields{
		"rejected_windows":   strings.Join(rejectedWindows, ","),
		"effective_cooldown": effectiveDuration.String(),
		"base_cooldown":      baseDuration.String(),
		"fuzz":               fuzz.String(),
		"deadline":           latestDeadline.Format(time.RFC3339),
	}).Info("parsed Anthropic rate limit reset headers")

	return &effectiveDuration
}

func containsString(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}

func getHeaderCaseInsensitive(h http.Header, target string) string {
	if h == nil {
		return ""
	}
	if val := h.Get(target); val != "" {
		return val
	}
	for k, v := range h {
		if strings.EqualFold(k, target) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

func parseUnixOrTimestamp(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if sec, err := strconv.ParseFloat(raw, 64); err == nil && sec > 0 {
		secInt := int64(sec)
		nsec := int64((sec - float64(secInt)) * 1e9)
		return time.Unix(secInt, nsec), true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, true
	}
	if t, err := http.ParseTime(raw); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func parseRetryAfterHeader(raw string, now time.Time) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if sec, err := strconv.ParseFloat(raw, 64); err == nil && sec > 0 {
		d := time.Duration(sec * float64(time.Second))
		return now.Add(d), true
	}
	if t, err := http.ParseTime(raw); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func randomClaudeFuzzDuration(minSec, maxSec int) time.Duration {
	if maxSec <= minSec {
		if minSec < 0 {
			return 0
		}
		return time.Duration(minSec) * time.Second
	}
	nBig, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(maxSec-minSec+1)))
	if err != nil {
		return time.Duration(minSec) * time.Second
	}
	return time.Duration(minSec+int(nBig.Int64())) * time.Second
}
