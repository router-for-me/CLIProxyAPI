package executor

import (
	"net/http"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func TestCodexWebsocketStatuslessErrorEventPreservesTopLevelError(t *testing.T) {
	tests := []struct {
		name           string
		payload        []byte
		wantStatus     int
		wantCode       string
		wantMessage    string
		wantRetryAfter bool
	}{
		{
			name:        "previous_response_not_found",
			payload:     []byte(`{"type":"error","code":"previous_response_not_found","message":"Previous response with id 'resp-1' not found.","param":"previous_response_id"}`),
			wantStatus:  http.StatusBadRequest,
			wantCode:    "previous_response_not_found",
			wantMessage: "Previous response with id 'resp-1' not found.",
		},
		{
			name:           "websocket_connection_limit_reached",
			payload:        []byte(`{"type":"error","code":"websocket_connection_limit_reached","message":"too many websocket connections"}`),
			wantStatus:     http.StatusTooManyRequests,
			wantCode:       "websocket_connection_limit_reached",
			wantMessage:    "too many websocket connections",
			wantRetryAfter: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err, ok := codexWebsocketStatuslessErrorEvent(tc.payload)
			if !ok {
				t.Fatal("expected statusless websocket error")
			}

			statusErr, ok := err.(interface{ StatusCode() int })
			if !ok || statusErr.StatusCode() != tc.wantStatus {
				t.Fatalf("status = %#v, want %d", err, tc.wantStatus)
			}

			parsed := gjson.Parse(err.Error())
			if got := parsed.Get("status").Int(); got != int64(tc.wantStatus) {
				t.Fatalf("payload status = %d, want %d; payload=%s", got, tc.wantStatus, err.Error())
			}
			if got := parsed.Get("error.code").String(); got != tc.wantCode {
				t.Fatalf("error code = %s, want %s; payload=%s", got, tc.wantCode, err.Error())
			}
			if got := parsed.Get("error.message").String(); got != tc.wantMessage {
				t.Fatalf("error message = %s, want %s; payload=%s", got, tc.wantMessage, err.Error())
			}

			retryable, ok := err.(interface{ RetryAfter() *time.Duration })
			if tc.wantRetryAfter {
				if !ok || retryable.RetryAfter() == nil || *retryable.RetryAfter() != 0 {
					t.Fatalf("retryAfter = %#v, want 0", err)
				}
			} else if ok && retryable.RetryAfter() != nil {
				t.Fatalf("retryAfter = %v, want nil", *retryable.RetryAfter())
			}
		})
	}
}

func TestCodexWebsocketStatuslessErrorEventUsesTopLevelErrorType(t *testing.T) {
	err, ok := codexWebsocketStatuslessErrorEvent([]byte(`{"type":"error","error_type":"invalid_request_error","message":"bad request"}`))
	if !ok {
		t.Fatal("expected statusless websocket error")
	}
	statusErr, ok := err.(interface{ StatusCode() int })
	if !ok || statusErr.StatusCode() != http.StatusBadRequest {
		t.Fatalf("status = %#v, want %d", err, http.StatusBadRequest)
	}
	if got := gjson.Get(err.Error(), "error.type").String(); got != "invalid_request_error" {
		t.Fatalf("error type = %q, want invalid_request_error", got)
	}
}

func TestCodexWebsocketStatuslessErrorEventClassifiesRateLimits(t *testing.T) {
	tests := []struct {
		name           string
		payload        []byte
		wantRetryAfter *time.Duration
	}{
		{
			name:    "nested rate limit",
			payload: []byte(`{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Rate limit reached."}}`),
		},
		{
			name:    "top-level rate limit code",
			payload: []byte(`{"type":"error","code":"rate_limit_exceeded","message":"Rate limit reached."}`),
		},
		{
			name:           "usage limit retry after",
			payload:        []byte(`{"type":"error","error":{"type":"usage_limit_reached","message":"Usage limit reached.","resets_in_seconds":7}}`),
			wantRetryAfter: durationPointer(7 * time.Second),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err, ok := codexWebsocketStatuslessErrorEvent(tc.payload)
			if !ok {
				t.Fatal("expected statusless websocket error")
			}
			statusErr, ok := err.(interface{ StatusCode() int })
			if !ok || statusErr.StatusCode() != http.StatusTooManyRequests {
				t.Fatalf("status = %#v, want %d", err, http.StatusTooManyRequests)
			}
			retryable, ok := err.(interface{ RetryAfter() *time.Duration })
			if tc.wantRetryAfter == nil {
				if ok && retryable.RetryAfter() != nil {
					t.Fatalf("retryAfter = %v, want nil", *retryable.RetryAfter())
				}
				return
			}
			if !ok || retryable.RetryAfter() == nil || *retryable.RetryAfter() != *tc.wantRetryAfter {
				t.Fatalf("retryAfter = %#v, want %v", err, *tc.wantRetryAfter)
			}
		})
	}
}

func TestCodexWebsocketStatuslessErrorEventClassifiesStringErrorCodes(t *testing.T) {
	tests := []struct {
		name       string
		payload    []byte
		wantStatus int
	}{
		{name: "rate limit", payload: []byte(`{"type":"error","error":"rate_limit_exceeded"}`), wantStatus: http.StatusTooManyRequests},
		{name: "usage limit", payload: []byte(`{"type":"error","error":"usage_limit_reached"}`), wantStatus: http.StatusTooManyRequests},
		{name: "previous response", payload: []byte(`{"type":"error","error":"previous_response_not_found"}`), wantStatus: http.StatusBadRequest},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err, ok := codexWebsocketStatuslessErrorEvent(test.payload)
			if !ok {
				t.Fatal("expected statusless websocket error")
			}
			statusErr, ok := err.(interface{ StatusCode() int })
			if !ok || statusErr.StatusCode() != test.wantStatus {
				t.Fatalf("status = %#v, want %d", err, test.wantStatus)
			}
			if got := gjson.Get(err.Error(), "error").String(); got == "" {
				t.Fatalf("string error code was not preserved: %s", err.Error())
			}
		})
	}
}

func durationPointer(value time.Duration) *time.Duration {
	return &value
}
