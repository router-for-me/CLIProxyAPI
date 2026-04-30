package helps

import (
	"net/http"
	"strconv"
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
			name: "Retry-After zero — retry immediately (RFC 9110 §10.2.3)",
			headers: http.Header{
				"Retry-After": {"0"},
			},
			want: durPtr(0),
		},
		{
			name: "Retry-After negative — treated as no hint",
			headers: http.Header{
				"Retry-After": {"-30"},
			},
			want: nil,
		},
		{
			name: "Retry-After exceeds 32-bit int — parsed as int64, clamped to max",
			headers: http.Header{
				// Larger than math.MaxInt32 (2147483647) so strconv.Atoi
				// would fail on 32-bit GOARCH; ParseInt(_, 10, 64) succeeds
				// and clamping kicks in before duration multiplication.
				"Retry-After": {"4294967296"},
			},
			want: durPtr(maxClaudeRetryAfter),
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
			got := ParseClaudeRetryAfter(tc.headers, now)
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

func TestParseClaudeRetryAfterAnchorsToServerDate(t *testing.T) {
	// Simulate a client clock that is 1 hour ahead of the server.
	serverNow := time.Unix(1777482352, 0).UTC()
	clientNow := serverNow.Add(1 * time.Hour)
	// Server says reset is 5 minutes after its own clock.
	resetEpoch := serverNow.Add(5 * time.Minute).Unix()

	headers := http.Header{
		"Date":                              {serverNow.Format(http.TimeFormat)},
		"Anthropic-Ratelimit-Unified-Reset": {strconv.FormatInt(resetEpoch, 10)},
	}

	got := ParseClaudeRetryAfter(headers, clientNow)
	if got == nil {
		t.Fatal("expected non-nil duration anchored to server Date")
	}
	// Without anchoring, clientNow would put the reset 55 minutes in the
	// past, returning nil. Anchoring to Date keeps the true 5-minute
	// cooldown — within HTTP-date 1s rounding tolerance.
	want := 5 * time.Minute
	diff := *got - want
	if diff < 0 {
		diff = -diff
	}
	if diff > time.Second {
		t.Fatalf("want=%v got=%v (diff=%v)", want, *got, diff)
	}

	// Sanity check: with the same headers minus Date, the stale fallback
	// puts the reset in the past and the function returns nil.
	headersWithoutDate := headers.Clone()
	headersWithoutDate.Del("Date")
	if got := ParseClaudeRetryAfter(headersWithoutDate, clientNow); got != nil {
		t.Fatalf("without Date anchor, expected nil (stale reset), got %v", *got)
	}
}

func durPtr(d time.Duration) *time.Duration {
	return &d
}
