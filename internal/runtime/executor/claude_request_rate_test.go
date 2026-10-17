package executor

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These error messages and headers are synthetic regression fixtures.
func TestClaudeRequestRateResetOnlyRetryAfter(t *testing.T) {
	for _, resource := range []string{"requests", "input tokens", "output tokens"} {
		for _, format := range []string{"seconds", "http_date", "lowercase", "http_roundtrip"} {
			for _, fast := range []bool{false, true} {
				path := "standard"
				if fast {
					path = "fast_direct"
				}
				t.Run(resource+"/"+format+"/"+path, func(t *testing.T) {
					h := http.Header{}
					h.Set("Retry-After", "30")
					if format == "http_date" {
						h.Set("Retry-After", time.Now().Add(30*time.Second).UTC().Format(http.TimeFormat))
					}
					h.Set("Anthropic-Ratelimit-Unified-Reset", strconv.FormatInt(time.Now().Add(4*time.Hour).Unix(), 10))
					body := []byte(fmt.Sprintf(`{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed the %s per minute rate limit. Please try again later."}}`, resource))
					if format == "lowercase" {
						lower := http.Header{}
						for key, value := range h {
							lower[strings.ToLower(key)] = value
						}
						h = lower
					}
					if format == "http_roundtrip" {
						wire := &http.Response{StatusCode: 429, ProtoMajor: 1, ProtoMinor: 1, Header: h, Body: http.NoBody}
						var encoded bytes.Buffer
						if err := wire.Write(&encoded); err != nil {
							t.Fatal(err)
						}
						decoded, err := http.ReadResponse(bufio.NewReader(&encoded), nil)
						if err != nil {
							t.Fatal(err)
						}
						h = decoded.Header
						if err := decoded.Body.Close(); err != nil {
							t.Fatal(err)
						}
					}
					var err error
					if fast {
						err = newClaudeFastDirectResponseError(&http.Response{StatusCode: 429, Header: h}, body)
					} else {
						err = classifyClaudeUpstreamError(429, h, body)
					}
					var retry interface{ RetryAfter() *time.Duration }
					if !errors.As(err, &retry) || retry.RetryAfter() == nil {
						t.Fatalf("missing retry: %v", err)
					}
					// HTTP dates have second precision; the existing grace is 1–30 seconds.
					if got := *retry.RetryAfter(); got < 29*time.Second || got > 60*time.Second {
						t.Fatalf("request-rate rejection inherited unrelated reset: %v", got)
					}
					var scoped interface{ IsCredentialScoped() bool }
					if errors.As(err, &scoped) && scoped.IsCredentialScoped() {
						t.Fatal("request-rate failure became shared credential quota")
					}
				})
			}
		}
	}
}

func TestClaudeRequestRateRetainsMixedAndNegatedQuota(t *testing.T) {
	for _, fast := range []bool{false, true} {
		path := "standard"
		if fast {
			path = "fast_direct"
		}
		for _, tc := range []struct{ name, message string }{
			{"negated", "Your weekly usage quota is exhausted. This is not a requests per minute rate limit."},
			{"mixed", "Your weekly usage quota and requests per minute rate limit are both exceeded."},
			{"positive_then_quota", "This request would exceed the requests per minute rate limit. Please try again later. Your weekly usage quota is exhausted."},
			{"unknown", "The requests per minute limit is mentioned for information only."},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				h := http.Header{}
				h.Set("Retry-After", "30")
				h.Set("Anthropic-Ratelimit-Unified-Reset", strconv.FormatInt(time.Now().Add(4*time.Hour).Unix(), 10))
				body := []byte(fmt.Sprintf(`{"type":"error","error":{"type":"rate_limit_error","message":%q}}`, tc.message))
				var err error
				if fast {
					err = newClaudeFastDirectResponseError(&http.Response{StatusCode: 429, Header: h}, body)
				} else {
					err = classifyClaudeUpstreamError(429, h, body)
				}
				var retry interface{ RetryAfter() *time.Duration }
				if !errors.As(err, &retry) || retry.RetryAfter() == nil {
					t.Fatalf("missing conservative quota retry: %v", err)
				}
				if got := *retry.RetryAfter(); got < 4*time.Hour-time.Second || got > 4*time.Hour+30*time.Second {
					t.Fatalf("ambiguous quota shortened to %v", got)
				}
			})
		}
	}
}

func TestClaudeRequestRateConservativeBoundaries(t *testing.T) {
	for _, fast := range []bool{false, true} {
		path := "standard"
		if fast {
			path = "fast_direct"
		}
		for _, tc := range []struct {
			name, statusHeader string
			noHint, malformed  bool
		}{
			{name: "no_retry_hint", noHint: true},
			{name: "malformed_body", malformed: true},
			{name: "explicit_shared_quota", statusHeader: "Anthropic-Ratelimit-Unified-5h-Status"},
			{name: "explicit_model_quota", statusHeader: "Anthropic-Ratelimit-Unified-7d_oi-Status"},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				h := http.Header{}
				if !tc.noHint {
					h.Set("Retry-After", "30")
				}
				reset := strconv.FormatInt(time.Now().Add(4*time.Hour).Unix(), 10)
				h.Set("Anthropic-Ratelimit-Unified-Reset", reset)
				h.Set("Anthropic-Ratelimit-Unified-5h-Reset", reset)
				h.Set("Anthropic-Ratelimit-Unified-7d_oi-Reset", reset)
				if tc.statusHeader != "" {
					h.Set(tc.statusHeader, "rejected")
				}
				body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed the requests per minute rate limit. Please try again later."}}`)
				if tc.malformed {
					body = body[:len(body)-1]
				}
				var err error
				if fast {
					err = newClaudeFastDirectResponseError(&http.Response{StatusCode: 429, Header: h}, body)
				} else {
					err = classifyClaudeUpstreamError(429, h, body)
				}
				var retry interface{ RetryAfter() *time.Duration }
				if !errors.As(err, &retry) || retry.RetryAfter() == nil {
					t.Fatalf("missing conservative retry: %v", err)
				}
				if got := *retry.RetryAfter(); got < 4*time.Hour-time.Second || got > 4*time.Hour+30*time.Second {
					t.Fatalf("quota boundary shortened to %v", got)
				}
			})
		}
	}
}

func TestClaudeRequestRateConservativeHeaderAndRetryBoundaries(t *testing.T) {
	now := time.Now()
	body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed the requests per minute rate limit. Please try again later."}}`)

	type resetCase struct {
		name, retry string
		headers     http.Header
		roundtrip   bool
		want        time.Duration
	}
	cases := []resetCase{}
	for _, name := range []string{"Anthropic-Ratelimit-Unified-Status", "Anthropic-Ratelimit-Unified-5h-Status", "Anthropic-Ratelimit-Unified-7d-Status", "Anthropic-Ratelimit-Unified-7d_oi-Status", "Anthropic-Ratelimit-Unified-Overage-Status"} {
		for _, variant := range []string{"canonical", "lowercase", "case_aliases", "http_roundtrip"} {
			key := name
			if variant == "lowercase" {
				key = strings.ToLower(name)
			}
			h := http.Header{key: {"", "rejected"}}
			if variant == "case_aliases" {
				h = http.Header{name: {""}, strings.ToLower(name): {"", "rejected"}}
			}
			cases = append(cases, resetCase{name + "/" + variant, "30", h, variant == "http_roundtrip", 4 * time.Hour})
		}
	}
	for _, retry := range []string{"691200", "608401", now.Add(8 * 24 * time.Hour).UTC().Format(http.TimeFormat), now.Add(8 * 24 * time.Hour).UTC().Format(time.RFC3339)} {
		cases = append(cases, resetCase{"unusable_retry/" + retry, retry, nil, false, 4 * time.Hour})
	}
	// The longest supported deadline remains usable; do not tighten this boundary.
	cases = append(cases, resetCase{"maximum_usable_retry", "608400", nil, false, 7*24*time.Hour + time.Hour})

	// Any repeated value or case alias is ambiguous, even if values agree.
	for _, tc := range []struct {
		name    string
		headers http.Header
	}{
		{"aliases_both_usable", http.Header{"retry-after": {"30"}, "RETRY-AFTER": {"60"}}},
		{"aliases_short_long", http.Header{"retry-after": {"30"}, "RETRY-AFTER": {"691200"}}},
		{"aliases_long_short", http.Header{"retry-after": {"691200"}, "RETRY-AFTER": {"30"}}},
		{"canonical_short_alias_long", http.Header{"Retry-After": {"30"}, "retry-after": {"691200"}}},
		{"canonical_long_alias_short", http.Header{"Retry-After": {"691200"}, "RETRY-AFTER": {"30"}}},
		{"canonical_alias_same", http.Header{"Retry-After": {"30"}, "retry-after": {"30"}}},
		{"canonical_alias_empty", http.Header{"Retry-After": {"30"}, "retry-after": {""}}},
		{"canonical_alias_nil", http.Header{"Retry-After": {"30"}, "retry-after": nil}},
		{"repeated_short_long", http.Header{"Retry-After": {"30", "691200"}}},
		{"repeated_long_short", http.Header{"Retry-After": {"691200", "30"}}},
		{"repeated_same", http.Header{"Retry-After": {"30", "30"}}},
		{"lowercase_repeated_short_long", http.Header{"retry-after": {"30", "691200"}}},
		{"lowercase_repeated_long_short", http.Header{"retry-after": {"691200", "30"}}},
	} {
		cases = append(cases, resetCase{"ambiguous_retry/" + tc.name, "", tc.headers, false, 4 * time.Hour})
	}
	cases = append(cases, resetCase{"ambiguous_retry/http_roundtrip", "", http.Header{"Retry-After": {"30", "691200"}}, true, 4 * time.Hour})

	for _, tc := range cases {
		for _, fast := range []bool{false, true} {
			path := "standard"
			if fast {
				path = "fast_direct"
			}
			t.Run(tc.name+"/"+path, func(t *testing.T) {

				h := tc.headers.Clone()
				if h == nil {
					h = http.Header{}
				}
				if tc.retry != "" {
					h.Set("Retry-After", tc.retry)
				}
				h.Set("Anthropic-Ratelimit-Unified-Reset", strconv.FormatInt(now.Add(4*time.Hour).Unix(), 10))
				if tc.roundtrip {
					var wire bytes.Buffer
					resp := &http.Response{StatusCode: 429, ProtoMajor: 1, ProtoMinor: 1, Header: h, Body: http.NoBody}
					if err := resp.Write(&wire); err != nil {
						t.Fatal(err)
					}
					parsed, err := http.ReadResponse(bufio.NewReader(&wire), nil)
					if err != nil {
						t.Fatal(err)
					}
					h = parsed.Header
					if err := parsed.Body.Close(); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				if fast {
					err = newClaudeFastDirectResponseError(&http.Response{StatusCode: 429, Header: h}, body)
				} else {
					err = classifyClaudeUpstreamError(429, h, body)
				}
				var retry interface{ RetryAfter() *time.Duration }
				if !errors.As(err, &retry) || retry.RetryAfter() == nil {
					t.Fatalf("lost conservative reset: %v", err)
				}
				if got := *retry.RetryAfter(); got < tc.want-time.Second || got > tc.want+30*time.Second {
					t.Fatalf("want %v plus bounded grace, got %v", tc.want, got)
				}
			})
		}
	}
}
