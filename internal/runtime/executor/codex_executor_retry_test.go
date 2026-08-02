package executor

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestParseCodexRetryAfter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	t.Run("resets_in_seconds", func(t *testing.T) {
		body := []byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":123}}`)
		retryAfter := parseCodexRetryAfter(http.StatusTooManyRequests, body, now)
		if retryAfter == nil {
			t.Fatalf("expected retryAfter, got nil")
		}
		if *retryAfter != 123*time.Second {
			t.Fatalf("retryAfter = %v, want %v", *retryAfter, 123*time.Second)
		}
	})

	t.Run("prefers resets_at", func(t *testing.T) {
		resetAt := now.Add(5 * time.Minute).Unix()
		body := []byte(`{"error":{"type":"usage_limit_reached","resets_at":` + itoa(resetAt) + `,"resets_in_seconds":1}}`)
		retryAfter := parseCodexRetryAfter(http.StatusTooManyRequests, body, now)
		if retryAfter == nil {
			t.Fatalf("expected retryAfter, got nil")
		}
		if *retryAfter != 5*time.Minute {
			t.Fatalf("retryAfter = %v, want %v", *retryAfter, 5*time.Minute)
		}
	})

	t.Run("fallback when resets_at is past", func(t *testing.T) {
		resetAt := now.Add(-1 * time.Minute).Unix()
		body := []byte(`{"error":{"type":"usage_limit_reached","resets_at":` + itoa(resetAt) + `,"resets_in_seconds":77}}`)
		retryAfter := parseCodexRetryAfter(http.StatusTooManyRequests, body, now)
		if retryAfter == nil {
			t.Fatalf("expected retryAfter, got nil")
		}
		if *retryAfter != 77*time.Second {
			t.Fatalf("retryAfter = %v, want %v", *retryAfter, 77*time.Second)
		}
	})

	t.Run("non-429 status code", func(t *testing.T) {
		body := []byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":30}}`)
		if got := parseCodexRetryAfter(http.StatusBadRequest, body, now); got != nil {
			t.Fatalf("expected nil for non-429, got %v", *got)
		}
	})

	t.Run("non usage_limit_reached error type", func(t *testing.T) {
		body := []byte(`{"error":{"type":"server_error","resets_in_seconds":30}}`)
		if got := parseCodexRetryAfter(http.StatusTooManyRequests, body, now); got != nil {
			t.Fatalf("expected nil for non-usage_limit_reached, got %v", *got)
		}
	})
}

func TestNewCodexStatusErrWithHeadersPreservesRetryMetadata(t *testing.T) {
	t.Run("delta seconds", func(t *testing.T) {
		headers := http.Header{
			"Retry-After":  {"7"},
			"X-Request-Id": {"req-test"},
		}
		err := newCodexStatusErrWithHeaders(
			http.StatusServiceUnavailable,
			[]byte(`{"error":{"type":"server_error","message":"overloaded"}}`),
			headers,
		)

		if retryAfter := err.RetryAfter(); retryAfter == nil || *retryAfter != 7*time.Second {
			t.Fatalf("RetryAfter() = %v, want 7s", retryAfter)
		}
		if got := err.Headers().Get("X-Request-Id"); got != "req-test" {
			t.Fatalf("Headers().Get(X-Request-Id) = %q, want req-test", got)
		}

		headers.Set("X-Request-Id", "mutated")
		if got := err.Headers().Get("X-Request-Id"); got != "req-test" {
			t.Fatalf("stored headers changed with source mutation: %q", got)
		}
	})

	t.Run("http date", func(t *testing.T) {
		retryAt := time.Now().Add(10 * time.Second).UTC().Truncate(time.Second)
		err := newCodexStatusErrWithHeaders(
			http.StatusServiceUnavailable,
			[]byte(`{"error":{"type":"server_error"}}`),
			http.Header{"Retry-After": {retryAt.Format(http.TimeFormat)}},
		)

		retryAfter := err.RetryAfter()
		if retryAfter == nil || *retryAfter < 8*time.Second || *retryAfter > 10*time.Second {
			t.Fatalf("RetryAfter() = %v, want approximately 10s", retryAfter)
		}
	})

	t.Run("overflowing delta seconds", func(t *testing.T) {
		err := newCodexStatusErrWithHeaders(
			http.StatusServiceUnavailable,
			[]byte(`{"error":{"type":"server_error"}}`),
			http.Header{"Retry-After": {"9999999999"}},
		)

		if retryAfter := err.RetryAfter(); retryAfter != nil {
			t.Fatalf("RetryAfter() = %v, want nil for overflowing delta seconds", *retryAfter)
		}
	})

	t.Run("body retry metadata wins", func(t *testing.T) {
		err := newCodexStatusErrWithHeaders(
			http.StatusTooManyRequests,
			[]byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":120}}`),
			http.Header{"Retry-After": {"7"}},
		)

		if retryAfter := err.RetryAfter(); retryAfter == nil || *retryAfter != 120*time.Second {
			t.Fatalf("RetryAfter() = %v, want body-derived 2m", retryAfter)
		}
	})
}

func TestNewCodexStatusErrTreatsCapacityAsRetryableRateLimit(t *testing.T) {
	body := []byte(`{"error":{"message":"Selected model is at capacity. Please try a different model."}}`)

	err := newCodexStatusErr(http.StatusBadRequest, body)

	if got := err.StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", got, http.StatusTooManyRequests)
	}
	if err.RetryAfter() != nil {
		t.Fatalf("expected nil explicit retryAfter for capacity fallback, got %v", *err.RetryAfter())
	}
}

func TestNewCodexStatusErrTreatsUsageLimitAsRetryableRateLimit(t *testing.T) {
	body := []byte(`{"error":{"type":"usage_limit_reached","message":"You've hit your usage limit.","resets_in_seconds":120}}`)

	err := newCodexStatusErr(http.StatusBadRequest, body)

	if got := err.StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", got, http.StatusTooManyRequests)
	}
	retryAfter := err.RetryAfter()
	if retryAfter == nil {
		t.Fatalf("expected retryAfter from usage_limit_reached, got nil")
	}
	if *retryAfter != 120*time.Second {
		t.Fatalf("retryAfter = %v, want %v", *retryAfter, 120*time.Second)
	}
}

func TestIsCodexUsageLimitError(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want bool
	}{
		{
			name: "nested usage_limit_reached",
			body: []byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":30}}`),
			want: true,
		},
		{
			name: "top-level usage_limit_reached",
			body: []byte(`{"type":"usage_limit_reached"}`),
			want: true,
		},
		{
			name: "transient rate limit is excluded",
			body: []byte(`{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded"}}`),
			want: false,
		},
		{
			name: "empty body",
			body: nil,
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCodexUsageLimitError(tc.body); got != tc.want {
				t.Fatalf("isCodexUsageLimitError = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNewCodexStatusErrClassifiesKnownCodexFailures(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       []byte
		wantStatus int
		wantType   string
		wantCode   string
	}{
		{
			name:       "context length status",
			statusCode: http.StatusRequestEntityTooLarge,
			body:       []byte(`{"error":{"message":"context length exceeded","type":"invalid_request_error","code":"context_length_exceeded"}}`),
			wantStatus: http.StatusRequestEntityTooLarge,
			wantType:   "invalid_request_error",
			wantCode:   "context_too_large",
		},
		{
			name:       "thinking signature",
			statusCode: http.StatusBadRequest,
			body:       []byte(`{"error":{"message":"Invalid signature in thinking block","type":"invalid_request_error","code":"invalid_request_error"}}`),
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_request_error",
			wantCode:   "thinking_signature_invalid",
		},
		{
			name:       "previous response missing",
			statusCode: http.StatusBadRequest,
			body:       []byte(`{"error":{"message":"No response found for previous_response_id resp_123","type":"invalid_request_error","code":"previous_response_not_found"}}`),
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_request_error",
			wantCode:   "previous_response_not_found",
		},
		{
			name:       "auth unavailable",
			statusCode: http.StatusUnauthorized,
			body:       []byte(`{"error":{"message":"invalid or expired token","type":"authentication_error","code":"invalid_api_key"}}`),
			wantStatus: http.StatusUnauthorized,
			wantType:   "authentication_error",
			wantCode:   "auth_unavailable",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := newCodexStatusErr(tc.statusCode, tc.body)

			if got := err.StatusCode(); got != tc.wantStatus {
				t.Fatalf("status code = %d, want %d", got, tc.wantStatus)
			}
			assertCodexErrorCode(t, err.Error(), tc.wantType, tc.wantCode)
		})
	}
}

func TestNewCodexStatusErrPreservesUnclassifiedErrors(t *testing.T) {
	body := []byte(`{"error":{"message":"documentation mentions too many tokens, but this is a billing configuration failure","type":"server_error","code":"billing_config_error"}}`)

	err := newCodexStatusErr(http.StatusBadGateway, body)

	if got := err.StatusCode(); got != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d", got, http.StatusBadGateway)
	}
	if got := err.Error(); got != string(body) {
		t.Fatalf("error body = %s, want original %s", got, string(body))
	}
}

func assertCodexErrorCode(t *testing.T, raw string, wantType string, wantCode string) {
	t.Helper()

	var payload struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("error body is not valid JSON: %v; body=%s", err, raw)
	}
	if payload.Error.Type != wantType {
		t.Fatalf("error.type = %q, want %q; body=%s", payload.Error.Type, wantType, raw)
	}
	if payload.Error.Code != wantCode {
		t.Fatalf("error.code = %q, want %q; body=%s", payload.Error.Code, wantCode, raw)
	}
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}
