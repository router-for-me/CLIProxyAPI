package helps

import (
	"bufio"
	"bytes"
	"net/http"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// Issue #6720: ParseClaudeRateLimitResetForError narrows the cooldown only for an
// exact explicit per-minute error with one usable Retry-After and no quota status
// evidence. All other inputs keep the conservative header-only result.

var claudePerMinuteBodyRequests = []byte(
	`{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed the requests per minute rate limit. Please try again later."}}`,
)

func TestParseClaudeRateLimitResetForError_Issue6720(t *testing.T) {
	now := time.Date(2026, 10, 17, 14, 0, 0, 0, time.UTC)
	unifiedReset := strconv.FormatInt(now.Add(4*time.Hour).Unix(), 10)

	baseHeaders := func() http.Header {
		h := http.Header{}
		h.Set("Retry-After", "30")
		h.Set("Anthropic-Ratelimit-Unified-Reset", unifiedReset)
		return h
	}

	tests := []struct {
		name      string
		body      []byte
		mutate    func(h http.Header)
		roundtrip bool
		wantMin   time.Duration
		wantMax   time.Duration
		wantNil   bool
	}{
		{name: "nil headers", body: claudePerMinuteBodyRequests, mutate: func(h http.Header) {}, wantNil: true},
		{name: "short retry honored", body: claudePerMinuteBodyRequests, wantMin: 31 * time.Second, wantMax: 60 * time.Second},
		{name: "http date retry honored", body: claudePerMinuteBodyRequests, mutate: func(h http.Header) {
			h.Set("Retry-After", now.Add(30*time.Second).UTC().Format(http.TimeFormat))
		}, wantMin: 31 * time.Second, wantMax: 60 * time.Second},
		{name: "message case and whitespace ignored", body: []byte(
			`{"type":"error","error":{"type":"rate_limit_error","message":"  THIS REQUEST WOULD EXCEED THE REQUESTS PER MINUTE RATE LIMIT. PLEASE TRY AGAIN LATER.\n"}}`,
		), wantMin: 31 * time.Second, wantMax: 60 * time.Second},
		{name: "empty alias status values do not block", body: claudePerMinuteBodyRequests, mutate: func(h http.Header) {
			h["anthropic-ratelimit-unified-status"] = []string{""}
		}, wantMin: 31 * time.Second, wantMax: 60 * time.Second},
		{name: "maximum usable retry honored", body: claudePerMinuteBodyRequests, mutate: func(h http.Header) {
			h.Set("Retry-After", strconv.FormatInt(int64(maxClaudeRateLimitWindow/time.Second), 10))
		}, wantMin: maxClaudeRateLimitWindow + time.Second, wantMax: maxClaudeRateLimitWindow + 30*time.Second},
		{name: "nonempty alias status blocks", body: claudePerMinuteBodyRequests, mutate: func(h http.Header) {
			h["Anthropic-Ratelimit-Unified-Status"] = []string{""}
			h["anthropic-ratelimit-unified-status"] = []string{"", "rejected"}
		}, wantMin: 4*time.Hour + time.Second, wantMax: 4*time.Hour + 30*time.Second},
		// An allowed shared window already excludes the unified reset in the
		// conservative parser, so the short retry wins with or without the exception.
		{name: "allowed shared window already excludes unified reset", body: claudePerMinuteBodyRequests, mutate: func(h http.Header) {
			h.Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
		}, wantMin: 31 * time.Second, wantMax: 60 * time.Second},
		{name: "aliased retry-after keys are ambiguous", body: claudePerMinuteBodyRequests, mutate: func(h http.Header) {
			h.Del("Retry-After")
			h["retry-after"] = []string{"30"}
			h["RETRY-AFTER"] = []string{"30"}
		}, wantMin: 4*time.Hour + time.Second, wantMax: 4*time.Hour + 30*time.Second},
		{name: "unusable retry beyond recoverable window", body: claudePerMinuteBodyRequests, mutate: func(h http.Header) {
			h.Set("Retry-After", strconv.FormatInt(int64(maxClaudeRateLimitWindow/time.Second)+1, 10))
		}, wantMin: 4*time.Hour + time.Second, wantMax: 4*time.Hour + 30*time.Second},
		{name: "lowercase headers honored", body: claudePerMinuteBodyRequests, mutate: func(h http.Header) {
			h.Del("Retry-After")
			h.Del("Anthropic-Ratelimit-Unified-Reset")
			h["retry-after"] = []string{"30"}
			h["anthropic-ratelimit-unified-reset"] = []string{unifiedReset}
		}, wantMin: 31 * time.Second, wantMax: 60 * time.Second},
		// The latest applicable deadline wins in the conservative parser: if a differently cased
		// unified reset survived, its 2h deadline would beat the 30s retry.
		{name: "differently cased unified resets all excluded", body: claudePerMinuteBodyRequests, mutate: func(h http.Header) {
			h["anthropic-ratelimit-unified-reset"] = []string{strconv.FormatInt(now.Add(2*time.Hour).Unix(), 10)}
		}, wantMin: 31 * time.Second, wantMax: 60 * time.Second},
		{name: "http header round trip honored", body: claudePerMinuteBodyRequests, roundtrip: true, wantMin: 31 * time.Second, wantMax: 60 * time.Second},
		{name: "nil body keeps conservative result", body: nil, wantMin: 4*time.Hour + time.Second, wantMax: 4*time.Hour + 30*time.Second},
		{name: "invalid retry keeps conservative result", body: claudePerMinuteBodyRequests, mutate: func(h http.Header) {
			h.Set("Retry-After", "invalid")
		}, wantMin: 4*time.Hour + time.Second, wantMax: 4*time.Hour + 30*time.Second},
		{name: "zero retry keeps conservative result", body: claudePerMinuteBodyRequests, mutate: func(h http.Header) {
			h.Set("Retry-After", "0")
		}, wantMin: 4*time.Hour + time.Second, wantMax: 4*time.Hour + 30*time.Second},
		{name: "negative retry keeps conservative result", body: claudePerMinuteBodyRequests, mutate: func(h http.Header) {
			h.Set("Retry-After", "-30")
		}, wantMin: 4*time.Hour + time.Second, wantMax: 4*time.Hour + 30*time.Second},
		{name: "overflow retry keeps conservative result", body: claudePerMinuteBodyRequests, mutate: func(h http.Header) {
			h.Set("Retry-After", "1e300")
		}, wantMin: 4*time.Hour + time.Second, wantMax: 4*time.Hour + 30*time.Second},
		{name: "wrong error type", body: []byte(
			`{"type":"error","error":{"type":"api_error","message":"This request would exceed the requests per minute rate limit. Please try again later."}}`,
		), wantMin: 4*time.Hour + time.Second, wantMax: 4*time.Hour + 30*time.Second},
		{name: "overage-only rejection keeps no credential cooldown", body: claudePerMinuteBodyRequests, mutate: func(h http.Header) {
			h.Set("Anthropic-Ratelimit-Unified-Status", "rejected")
			h.Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
			h.Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed")
			h.Set("Anthropic-Ratelimit-Unified-Overage-Status", "rejected")
		}, wantNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var headers http.Header
			if tt.name != "nil headers" {
				headers = baseHeaders()
				if tt.mutate != nil {
					tt.mutate(headers)
				}
			}
			if tt.roundtrip && headers != nil {
				wire := &http.Response{StatusCode: http.StatusTooManyRequests, ProtoMajor: 1, ProtoMinor: 1, Header: headers, Body: http.NoBody}
				var encoded bytes.Buffer
				if errWrite := wire.Write(&encoded); errWrite != nil {
					t.Fatal(errWrite)
				}
				decoded, errRead := http.ReadResponse(bufio.NewReader(&encoded), nil)
				if errRead != nil {
					t.Fatal(errRead)
				}
				if errClose := decoded.Body.Close(); errClose != nil {
					t.Fatal(errClose)
				}
				headers = decoded.Header
			}
			before := headers.Clone()

			got := ParseClaudeRateLimitResetForError(headers, tt.body, now)

			if !reflect.DeepEqual(headers, before) {
				t.Fatalf("headers mutated: %v -> %v", before, headers)
			}
			if tt.wantNil {
				if got != nil {
					t.Fatalf("expected nil cooldown, got %v", *got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil cooldown, got nil")
			}
			if *got < tt.wantMin || *got > tt.wantMax {
				t.Fatalf("cooldown = %v, want between %v and %v", *got, tt.wantMin, tt.wantMax)
			}
		})
	}
}
