package pluginstore

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RateLimitError reports a GitHub API cooldown without exposing response bodies
// or credentials. RetryAt is also populated for requests blocked locally.
type RateLimitError struct {
	StatusCode int
	RetryAt    time.Time
}

func (err *RateLimitError) Error() string {
	return fmt.Sprintf("GitHub API rate limited; retry after %s", err.RetryAt.UTC().Format(time.RFC3339))
}

// RetryAfterSeconds rounds up so clients never retry before the reset time.
func (err *RateLimitError) RetryAfterSeconds(now time.Time) int64 {
	delay := err.RetryAt.Sub(now)
	if delay <= 0 {
		return 0
	}
	seconds := int64(delay / time.Second)
	if delay%time.Second != 0 {
		seconds++
	}
	return seconds
}

type githubCooldown struct {
	retryAt  time.Time
	status   int
	failures uint8
}

// GitHubRateLimiter shares cooldowns across repositories, release metadata and
// API asset downloads. Its zero value is ready for use. Clients without an
// explicit limiter share the process-wide default.
type GitHubRateLimiter struct {
	mu          sync.Mutex
	entries     map[string]githubCooldown
	nextPruneAt time.Time
	nowFunc     func() time.Time
}

var defaultGitHubRateLimiter GitHubRateLimiter

func (c Client) githubRateLimiter() *GitHubRateLimiter {
	if c.RateLimiter != nil {
		return c.RateLimiter
	}
	return &defaultGitHubRateLimiter
}

func (limiter *GitHubRateLimiter) now() time.Time {
	if limiter.nowFunc != nil {
		return limiter.nowFunc()
	}
	return time.Now()
}

func githubRateLimitKey(requestURL, networkScope string, headers http.Header, authenticated bool) string {
	parsed, errParse := url.Parse(requestURL)
	if errParse != nil || !strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Hostname(), "api.github.com") || (parsed.Port() != "" && parsed.Port() != "443") {
		return ""
	}
	return "api.github.com/" + requestIdentity(networkScope, headers, authenticated)
}

func (limiter *GitHubRateLimiter) check(key string) error {
	if key == "" {
		return nil
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	now := limiter.now()
	limiter.pruneLocked(now)
	entry := limiter.entries[key]
	if now.Before(entry.retryAt) {
		return &RateLimitError{StatusCode: entry.status, RetryAt: entry.retryAt}
	}
	return nil
}

// pruneLocked drops inactive identities after a grace period so credential and
// proxy rotation cannot leave cooldown state behind indefinitely. Retaining
// recently expired entries preserves secondary-limit backoff across retries.
func (limiter *GitHubRateLimiter) pruneLocked(now time.Time) {
	if now.Before(limiter.nextPruneAt) {
		return
	}
	for key, entry := range limiter.entries {
		if !now.Before(entry.retryAt.Add(time.Hour)) {
			delete(limiter.entries, key)
		}
	}
	limiter.nextPruneAt = now.Add(time.Hour)
}

// observe records even successful responses that consume the final request.
// A late success must not clear a cooldown established by another request.
func (limiter *GitHubRateLimiter) observe(key string, status int, headers http.Header, body []byte) error {
	if key == "" {
		return nil
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	now := limiter.now()
	limiter.pruneLocked(now)
	entry := limiter.entries[key]
	remainingZero := strings.TrimSpace(headers.Get("X-RateLimit-Remaining")) == "0"
	retryAt := githubRetryAfter(headers.Get("Retry-After"), now)
	rateLimited := status == http.StatusTooManyRequests || (status == http.StatusForbidden &&
		(remainingZero || !retryAt.IsZero() || githubRateLimitMessage(body)))
	if !rateLimited && !remainingZero {
		if status >= http.StatusOK && status < http.StatusMultipleChoices && !now.Before(entry.retryAt) {
			delete(limiter.entries, key)
		}
		return nil
	}
	if remainingZero {
		if reset, errReset := strconv.ParseInt(strings.TrimSpace(headers.Get("X-RateLimit-Reset")), 10, 64); errReset == nil && reset > 0 {
			if resetAt := time.Unix(reset, 0); resetAt.After(retryAt) {
				retryAt = resetAt
			}
		}
	}
	if !retryAt.After(now) {
		if !rateLimited {
			return nil
		}
		// Headerless secondary limits start at one minute and back off up to an
		// hour. Only an actual upstream rejection increments this counter.
		delay := time.Minute * time.Duration(1<<entry.failures)
		if delay > time.Hour {
			delay = time.Hour
		}
		retryAt = now.Add(delay)
		if entry.failures < 6 {
			entry.failures++
		}
	}
	if retryAt.After(entry.retryAt) {
		entry.retryAt = retryAt
	}
	entry.status = status
	if !rateLimited {
		entry.status = http.StatusTooManyRequests
	}
	if limiter.entries == nil {
		limiter.entries = make(map[string]githubCooldown)
	}
	limiter.entries[key] = entry
	if rateLimited {
		return &RateLimitError{StatusCode: status, RetryAt: entry.retryAt}
	}
	return nil
}

func githubRetryAfter(value string, now time.Time) time.Time {
	value = strings.TrimSpace(value)
	if seconds, errSeconds := strconv.ParseInt(value, 10, 64); errSeconds == nil && seconds >= 0 && seconds <= int64((1<<63-1)/time.Second) {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if retryAt, errDate := http.ParseTime(value); errDate == nil {
		return retryAt
	}
	return time.Time{}
}

func githubRateLimitMessage(body []byte) bool {
	var message struct {
		Message string `json:"message"`
	}
	if errDecode := json.Unmarshal(body, &message); errDecode != nil {
		return false
	}
	text := strings.ToLower(message.Message)
	return strings.Contains(text, "secondary rate limit") ||
		strings.Contains(text, "api rate limit exceeded") ||
		strings.Contains(text, "abuse detection")
}
