package executor

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCodexQuotaErrorCredentialScope(t *testing.T) {
	quota := `{"type":"usage_limit_reached","message":"You've hit your usage limit.","resets_in_seconds":3600}`
	for _, test := range []struct {
		name string
		path string
		body string
		want bool
	}{
		{name: "http usage limit", path: "http", body: `{"error":` + quota + `}`, want: true},
		{name: "http top-level usage limit", path: "http", body: quota, want: true},
		{name: "http usage limit without reset", path: "http", body: `{"error":{"type":"usage_limit_reached"}}`, want: true},
		{name: "sse error", path: "terminal", body: `{"type":"error","error":` + quota + `}`, want: true},
		{name: "sse response failed", path: "terminal", body: `{"type":"response.failed","response":{"error":` + quota + `}}`, want: true},
		{name: "websocket error", path: "websocket", body: `{"type":"error","status":429,"error":` + quota + `}`, want: true},
		{name: "websocket body error", path: "websocket", body: `{"type":"error","status":429,"body":{"error":` + quota + `}}`, want: true},
		{name: "model capacity", path: "http", body: `{"error":{"message":"Selected model is at capacity. Please try a different model."}}`},
		{name: "transient rate limit", path: "http", body: `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded"}}`},
		{name: "websocket connection limit", path: "websocket", body: `{"type":"error","status":429,"error":{"code":"websocket_connection_limit_reached"}}`},
		{name: "generic provider error", path: "generic", body: `{"error":` + quota + `}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var err error
			switch test.path {
			case "http":
				err = newCodexStatusErr(http.StatusTooManyRequests, []byte(test.body))
			case "terminal":
				terminalErr, _, ok := codexTerminalStreamErr([]byte(test.body))
				if !ok {
					t.Fatal("terminal error was not recognized")
				}
				err = terminalErr
			case "websocket":
				var ok bool
				err, ok = parseCodexWebsocketError([]byte(test.body))
				if !ok {
					t.Fatal("websocket error was not recognized")
				}
			default:
				err = statusErr{code: http.StatusTooManyRequests, msg: test.body}
			}
			var scoped interface{ IsCredentialScoped() bool }
			got := errors.As(err, &scoped) && scoped.IsCredentialScoped()
			if got != test.want {
				t.Errorf("credential scope = %t, want %t; actual error type %T", got, test.want, err)
			}
			if test.want && strings.Contains(test.body, "resets_in_seconds") {
				var retry interface{ RetryAfter() *time.Duration }
				if !errors.As(err, &retry) || retry.RetryAfter() == nil || *retry.RetryAfter() != time.Hour {
					t.Errorf("real Codex quota error must preserve its one-hour reset: %v", err)
				}
			}
		})
	}
}

func TestParseCodexRetryAfterQuotaLayouts(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for _, layout := range []string{"nested", "top-level"} {
		for _, test := range []struct {
			name string
			body string
			want time.Duration
		}{
			{name: "relative reset", body: `{"type":"usage_limit_reached","resets_in_seconds":3600}`, want: time.Hour},
			{name: "absolute reset wins", body: `{"type":"usage_limit_reached","resets_at":1700000300,"resets_in_seconds":1}`, want: 5 * time.Minute},
			{name: "expired absolute reset falls back", body: `{"type":"usage_limit_reached","resets_at":1699999940,"resets_in_seconds":77}`, want: 77 * time.Second},
			{name: "type matching agrees with quota classification", body: `{"type":" USAGE_LIMIT_REACHED ","resets_in_seconds":30}`, want: 30 * time.Second},
			{name: "missing reset", body: `{"type":"usage_limit_reached"}`},
			{name: "expired reset", body: `{"type":"usage_limit_reached","resets_at":1699999940}`},
			{name: "nonpositive reset", body: `{"type":"usage_limit_reached","resets_at":0,"resets_in_seconds":-1}`},
			{name: "transient limit", body: `{"type":"rate_limit_error","resets_in_seconds":30}`},
		} {
			t.Run(layout+"/"+test.name, func(t *testing.T) {
				body := []byte(test.body)
				if layout == "nested" {
					body = []byte(`{"error":` + test.body + `}`)
				}
				got := parseCodexRetryAfter(http.StatusTooManyRequests, body, now)
				if test.want == 0 {
					if got != nil {
						t.Fatalf("retryAfter = %v, want nil", *got)
					}
					return
				}
				if got == nil || *got != test.want {
					t.Fatalf("retryAfter = %v, want %v", got, test.want)
				}
			})
		}
	}
}

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
