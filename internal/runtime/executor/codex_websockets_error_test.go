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
