package helps

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestClaudeRequestRateRetryKeepsQuotaEvidence(t *testing.T) {
	now := time.Date(2026, 10, 17, 14, 0, 0, 0, time.UTC)
	positive := "This request would exceed the requests per minute rate limit. Please try again later."
	// Synthetic fixtures, not captured upstream responses. The complete message
	// matters: a substring can also occur in a mixed or negated quota diagnosis.
	for _, tc := range []struct {
		name, message, retry, errorType       string
		headers                               http.Header
		short, malformed, noRetry, noDeadline bool
	}{
		{name: "requests", short: true},
		{name: "input_tokens", message: "This request would exceed the input tokens per minute rate limit. Please try again later.", short: true},
		{name: "output_tokens", message: "This request would exceed the output tokens per minute rate limit. Please try again later.", short: true},
		{name: "case_and_whitespace", message: "  " + strings.ToUpper(positive) + "\n", short: true},
		{name: "http_date", retry: "Sat, 17 Oct 2026 14:00:30 GMT", short: true},
		{name: "rfc3339", retry: "2026-10-17T14:00:30Z", short: true},
		{name: "negated", message: "Your weekly usage quota is exhausted. This is not a requests per minute rate limit."},
		{name: "mixed", message: "Your weekly usage quota and requests per minute rate limit are both exceeded."},
		{name: "appended_quota", message: positive + " Your weekly usage quota is exhausted."},
		{name: "unknown", message: "The requests per minute limit is mentioned for information only."},
		{name: "generic", message: "This request would exceed your account's rate limit"},
		{name: "model_credits", message: "Usage credits required for this model"},
		{name: "wrong_error_type", errorType: "api_error"},
		{name: "malformed_json", malformed: true},
		{name: "no_retry", noRetry: true},
		{name: "invalid_retry", retry: "invalid"},
		{name: "zero_retry", retry: "0"},
		{name: "negative_retry", retry: "-30"},
		{name: "past_date", retry: "Sat, 17 Oct 2026 13:59:30 GMT"},
		{name: "unified_rejected", headers: http.Header{"anthropic-ratelimit-unified-status": {"rejected"}}},
		{name: "shared_5h_rejected", headers: http.Header{"anthropic-ratelimit-unified-5h-status": {"rejected"}}},
		{name: "shared_7d_rejected", headers: http.Header{"anthropic-ratelimit-unified-7d-status": {"rejected"}}},
		{name: "model_rejected", headers: http.Header{"anthropic-ratelimit-unified-7d_oi-status": {"rejected"}}},
		{name: "overage_rejected", headers: http.Header{"anthropic-ratelimit-unified-overage-status": {"rejected"}}},
		{name: "unified_unknown", headers: http.Header{"anthropic-ratelimit-unified-status": {"unknown"}}, short: true},
		{name: "allowed_window", headers: http.Header{"anthropic-ratelimit-unified-5h-status": {"allowed"}}, short: true},
		{name: "overage_only", headers: http.Header{
			"Anthropic-Ratelimit-Unified-Status":         {"rejected"},
			"Anthropic-Ratelimit-Unified-5h-Status":      {"allowed"},
			"Anthropic-Ratelimit-Unified-7d-Status":      {"allowed"},
			"Anthropic-Ratelimit-Unified-Overage-Status": {"rejected"},
		}, noDeadline: true},
		{name: "overage_claim_without_shared_status", headers: http.Header{
			"Anthropic-Ratelimit-Unified-Status":               {"rejected"},
			"Anthropic-Ratelimit-Unified-Representative-Claim": {"overage"},
		}, noDeadline: true},
		{name: "overage_billing_boundary", headers: http.Header{
			"Anthropic-Ratelimit-Unified-Representative-Claim": {"overage"},
			"Anthropic-Ratelimit-Unified-Overage-Reset":        {"1792260000"},
		}, short: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reset := strconv.FormatInt(now.Add(4*time.Hour).Unix(), 10)
			h := http.Header{"anthropic-ratelimit-unified-reset": {reset}}
			// Reset metadata alone does not declare a rejected window.
			h.Set("Anthropic-Ratelimit-Unified-5h-Reset", reset)
			h.Set("Anthropic-Ratelimit-Unified-7d-Reset", reset)
			h.Set("Anthropic-Ratelimit-Unified-7d_oi-Reset", reset)
			retry := tc.retry
			if retry == "" {
				retry = "30"
			}
			if !tc.noRetry {
				h.Set("Retry-After", retry)
			}
			for key, values := range tc.headers {
				h[key] = values
			}
			before := h.Clone()
			message := tc.message
			if message == "" {
				message = positive
			}
			errorType := tc.errorType
			if errorType == "" {
				errorType = "rate_limit_error"
			}
			body, err := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": errorType, "message": message}})
			if err != nil {
				t.Fatal(err)
			}
			if tc.malformed {
				body = body[:len(body)-1]
			}
			got := ParseClaudeRateLimitResetForError(h, body, now)
			if !reflect.DeepEqual(h, before) {
				t.Fatal("mutated upstream headers")
			}
			if tc.noDeadline {
				if got != nil {
					t.Fatalf("overage-only rejection acquired cooldown: %v", *got)
				}
				return
			}
			want := 4 * time.Hour
			if tc.short {
				want = 30 * time.Second
			}
			if got == nil || *got < want+time.Second || *got > want+30*time.Second {
				t.Fatalf("want %v plus 1–30s grace, got %v", want, got)
			}
		})
	}
}

func TestClaudeRequestRateConservativeHeaderAndRetryBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 17, 14, 0, 0, 0, time.UTC)
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
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {

			h := tc.headers.Clone()
			if h == nil {
				h = http.Header{}
			}
			h.Set("Retry-After", tc.retry)
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
			before := h.Clone()
			got := ParseClaudeRateLimitResetForError(h, body, now)
			if !reflect.DeepEqual(h, before) {
				t.Fatal("mutated upstream headers")
			}
			if got == nil || *got < tc.want+time.Second || *got > tc.want+30*time.Second {
				t.Fatalf("want %v plus 1–30s grace, got %v", tc.want, got)
			}
		})
	}
}
