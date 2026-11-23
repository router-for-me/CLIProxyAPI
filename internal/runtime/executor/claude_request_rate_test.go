package executor

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// Issue #6720: an explicit per-minute rate-limit error carrying a short
// Retry-After must not inherit a later unclassified unified reset when no
// status header declares a quota window. Fixtures are synthetic.

var claudePerMinuteMessages = []struct {
	name     string
	resource string
}{
	{name: "requests", resource: "requests"},
	{name: "input_tokens", resource: "input tokens"},
	{name: "output_tokens", resource: "output tokens"},
}

func claudePerMinuteBody(resource string) []byte {
	return []byte(fmt.Sprintf(
		`{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed the %s per minute rate limit. Please try again later."}}`,
		resource,
	))
}

func classifyClaudeRateTestError(t *testing.T, fast bool, headers http.Header, body []byte) time.Duration {
	t.Helper()
	var err error
	if fast {
		err = newClaudeFastDirectResponseError(&http.Response{StatusCode: http.StatusTooManyRequests, Header: headers}, body)
	} else {
		err = classifyClaudeUpstreamError(http.StatusTooManyRequests, headers, body)
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var rap retryAfterProvider
	if !errors.As(err, &rap) || rap.RetryAfter() == nil {
		t.Fatalf("expected non-nil RetryAfter, got %v", err)
	}
	return *rap.RetryAfter()
}

func TestClaudePerMinuteRateLimitHonorsShortRetryAfter_Issue6720(t *testing.T) {
	for _, msg := range claudePerMinuteMessages {
		for _, fast := range []bool{false, true} {
			path := "standard"
			if fast {
				path = "fast_direct"
			}
			t.Run(msg.name+"/"+path, func(t *testing.T) {
				headers := http.Header{}
				headers.Set("Retry-After", "30")
				headers.Set("Anthropic-Ratelimit-Unified-Reset", strconv.FormatInt(time.Now().Add(4*time.Hour).Unix(), 10))

				got := classifyClaudeRateTestError(t, fast, headers, claudePerMinuteBody(msg.resource))
				// 30s hint plus the existing 1-30s bounded grace; allow clock slack.
				if got < 29*time.Second || got > 60*time.Second {
					t.Fatalf("RetryAfter = %v, want between 29s and 60s (must not inherit the 4h unified reset)", got)
				}
			})
		}
	}
}

func TestClaudePerMinuteRateLimitKeepsConservativeQuota_Issue6720(t *testing.T) {
	tests := []struct {
		name   string
		body   []byte
		mutate func(h http.Header)
	}{
		{
			name: "explicit unified status",
			body: claudePerMinuteBody("requests"),
			mutate: func(h http.Header) {
				h.Set("Anthropic-Ratelimit-Unified-Status", "rejected")
			},
		},
		{
			name: "unknown message wording",
			body: []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"The requests per minute limit is mentioned for information only."}}`),
		},
		{
			name: "mixed quota message",
			body: []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed the requests per minute rate limit. Please try again later. Your weekly usage quota is exhausted."}}`),
		},
		{
			name: "repeated retry-after values",
			body: claudePerMinuteBody("requests"),
			mutate: func(h http.Header) {
				h["Retry-After"] = []string{"30", "14400"}
			},
		},
		{
			name: "missing retry-after",
			body: claudePerMinuteBody("requests"),
			mutate: func(h http.Header) {
				h.Del("Retry-After")
			},
		},
		{
			name: "malformed body",
			body: []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed the requests per minute rate limit. Please try again later."}`),
		},
	}

	for _, tt := range tests {
		for _, fast := range []bool{false, true} {
			path := "standard"
			if fast {
				path = "fast_direct"
			}
			t.Run(tt.name+"/"+path, func(t *testing.T) {
				headers := http.Header{}
				headers.Set("Retry-After", "30")
				headers.Set("Anthropic-Ratelimit-Unified-Reset", strconv.FormatInt(time.Now().Add(4*time.Hour).Unix(), 10))
				if tt.mutate != nil {
					tt.mutate(headers)
				}

				got := classifyClaudeRateTestError(t, fast, headers, tt.body)
				if got < 4*time.Hour-2*time.Second || got > 4*time.Hour+30*time.Second {
					t.Fatalf("RetryAfter = %v, want ~4h conservative unified cooldown", got)
				}
			})
		}
	}
}
