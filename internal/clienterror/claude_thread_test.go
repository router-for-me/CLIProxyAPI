package clienterror

import (
	"errors"
	"net/http"
	"testing"
)

func TestIsRequestFault_ClaudeThreadNotFound(t *testing.T) {
	const structured = `{"type":"error","error":{"type":"not_found_error","details":{"error_code":"thread_not_found"}}}`
	const message = "No thread state was found for the requested `previous_message_id`. Replay the full conversation."
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"structured", http.StatusNotFound, structured, true},
		{"message only", http.StatusNotFound, message, true},
		{"generic not found", http.StatusNotFound, `{"error":{"type":"not_found_error","message":"Not found"}}`, false},
		{"model not found", http.StatusNotFound, `{"error":{"code":"model_not_found"}}`, false},
		{"rate limit remains authoritative", http.StatusTooManyRequests, structured, false},
		{"payment remains authoritative", http.StatusPaymentRequired, structured, false},
		{"server error remains retryable", http.StatusInternalServerError, structured, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRequestFault(tc.status, errors.New(tc.body)); got != tc.want {
				t.Fatalf("IsRequestFault() = %v, want %v", got, tc.want)
			}
		})
	}
}
