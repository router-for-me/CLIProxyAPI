package executor

import (
	"net/http"
	"testing"
	"time"
)

func TestParseClaudeRetryAfter(t *testing.T) {
	// Pinned to the moment of a real captured 429 (epoch 1777482352)
	// against api.anthropic.com, with reset at 1777500000 — exactly 17648s
	// (4h54m08s) later. Using time.Unix avoids any timezone ambiguity that
	// time.Date would introduce.
	now := time.Unix(1777482352, 0).UTC()

	tests := []struct {
		name    string
		headers http.Header
		want    *time.Duration
	}{
		{
			name:    "nil headers",
			headers: nil,
			want:    nil,
		},
		{
			name:    "empty headers",
			headers: http.Header{},
			want:    nil,
		},
		{
			name: "anthropic-ratelimit-unified-reset future epoch — wins over Retry-After",
			headers: http.Header{
				"Anthropic-Ratelimit-Unified-Reset": {"1777500000"},
				"Retry-After":                       {"30"},
			},
			want: durPtr(17648 * time.Second),
		},
		{
			name: "anthropic-ratelimit-unified-reset past epoch — falls through to Retry-After",
			headers: http.Header{
				"Anthropic-Ratelimit-Unified-Reset": {"1777400000"},
				"Retry-After":                       {"30"},
			},
			want: durPtr(30 * time.Second),
		},
		{
			name: "anthropic-ratelimit-unified-reset year 5138 — clamped to max",
			headers: http.Header{
				"Anthropic-Ratelimit-Unified-Reset": {"99999999999"},
			},
			want: durPtr(maxClaudeRetryAfter),
		},
		{
			name: "anthropic-ratelimit-unified-reset unparseable — falls through",
			headers: http.Header{
				"Anthropic-Ratelimit-Unified-Reset": {"not-a-number"},
				"Retry-After":                       {"30"},
			},
			want: durPtr(30 * time.Second),
		},
		{
			name: "Retry-After seconds",
			headers: http.Header{
				"Retry-After": {"60"},
			},
			want: durPtr(60 * time.Second),
		},
		{
			name: "Retry-After zero — treated as no hint",
			headers: http.Header{
				"Retry-After": {"0"},
			},
			want: nil,
		},
		{
			name: "Retry-After absurdly large seconds — clamped to max",
			headers: http.Header{
				"Retry-After": {"99999999999"},
			},
			want: durPtr(maxClaudeRetryAfter),
		},
		{
			name: "Retry-After HTTP date in future",
			headers: http.Header{
				"Retry-After": {now.Add(5 * time.Minute).In(time.UTC).Format(http.TimeFormat)},
			},
			want: durPtr(5 * time.Minute),
		},
		{
			name: "Retry-After HTTP date 10 years out — clamped to max",
			headers: http.Header{
				"Retry-After": {now.Add(10 * 365 * 24 * time.Hour).In(time.UTC).Format(http.TimeFormat)},
			},
			want: durPtr(maxClaudeRetryAfter),
		},
		{
			name: "Retry-After HTTP date in past — nil",
			headers: http.Header{
				"Retry-After": {now.Add(-1 * time.Hour).In(time.UTC).Format(http.TimeFormat)},
			},
			want: nil,
		},
		{
			name: "whitespace tolerated",
			headers: http.Header{
				"Anthropic-Ratelimit-Unified-Reset": {"  1777500000  "},
			},
			want: durPtr(17648 * time.Second),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseClaudeRetryAfter(tc.headers, now)
			if (tc.want == nil) != (got == nil) {
				t.Fatalf("want=%v got=%v", tc.want, got)
			}
			if tc.want != nil {
				// 1s tolerance for HTTP-date roundtrips; everything else exact.
				diff := *got - *tc.want
				if diff < 0 {
					diff = -diff
				}
				if diff > time.Second {
					t.Fatalf("want=%v got=%v (diff=%v)", *tc.want, *got, diff)
				}
			}
		})
	}
}

func TestNewClaudeStatusErrPopulatesRetryAfterOnlyOn429(t *testing.T) {
	headers := http.Header{
		"Retry-After": {"60"},
	}

	if got := newClaudeStatusErr(429, headers, []byte("rate limited")); got.retryAfter == nil {
		t.Fatal("429: expected retryAfter to be set")
	}
	if got := newClaudeStatusErr(500, headers, []byte("server error")); got.retryAfter != nil {
		t.Fatalf("500: expected retryAfter to be nil, got %v", *got.retryAfter)
	}
	if got := newClaudeStatusErr(401, headers, []byte("unauthorized")); got.retryAfter != nil {
		t.Fatalf("401: expected retryAfter to be nil, got %v", *got.retryAfter)
	}
}

func durPtr(d time.Duration) *time.Duration {
	return &d
}
