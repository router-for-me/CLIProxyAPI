 package copilot
 
 import (
 	"errors"
 	"testing"
 )
 
 func TestHTTPStatusError_Error(t *testing.T) {
 	tests := []struct {
 		name     string
 		err      *HTTPStatusError
 		contains string
 	}{
 		{
 			name:     "without cause",
 			err:      NewHTTPStatusError(401, "unauthorized", nil),
 			contains: "status 401: unauthorized",
 		},
 		{
 			name:     "with cause",
 			err:      NewHTTPStatusError(500, "internal error", errors.New("database error")),
 			contains: "database error",
 		},
 	}
 
 	for _, tt := range tests {
 		t.Run(tt.name, func(t *testing.T) {
 			errStr := tt.err.Error()
 			if !contains(errStr, tt.contains) {
 				t.Errorf("HTTPStatusError.Error() = %q, want to contain %q", errStr, tt.contains)
 			}
 		})
 	}
 }
 
 func TestHTTPStatusError_Unwrap(t *testing.T) {
 	cause := errors.New("original error")
 	err := NewHTTPStatusError(500, "wrapped", cause)
 
 	unwrapped := err.Unwrap()
 	if unwrapped != cause {
 		t.Errorf("HTTPStatusError.Unwrap() = %v, want %v", unwrapped, cause)
 	}
 }
 
 func TestStatusCode(t *testing.T) {
 	tests := []struct {
 		name string
 		err  error
 		want int
 	}{
 		{
 			name: "HTTPStatusError",
 			err:  NewHTTPStatusError(404, "not found", nil),
 			want: 404,
 		},
 		{
 			name: "wrapped HTTPStatusError",
 			err:  errors.New("outer: " + NewHTTPStatusError(403, "forbidden", nil).Error()),
 			want: 0, // Can't unwrap from string concatenation
 		},
 		{
 			name: "regular error",
 			err:  errors.New("regular error"),
 			want: 0,
 		},
 		{
 			name: "nil error",
 			err:  nil,
 			want: 0,
 		},
 	}
 
 	for _, tt := range tests {
 		t.Run(tt.name, func(t *testing.T) {
 			got := StatusCode(tt.err)
 			if got != tt.want {
 				t.Errorf("StatusCode() = %d, want %d", got, tt.want)
 			}
 		})
 	}
 }
 
 func TestSentinelErrors(t *testing.T) {
 	// Verify sentinel errors are non-nil and have meaningful messages
 	sentinels := []error{
 		ErrDeviceCodeFailed,
 		ErrAccessTokenFailed,
 		ErrCopilotTokenFailed,
 		ErrTokenExpired,
 		ErrNoGitHubToken,
 		ErrNoCopilotToken,
 		ErrAuthorizationPending,
 		ErrSlowDown,
 		ErrAccessDenied,
 		ErrExpiredToken,
 		ErrNoCopilotSubscription,
 	}
 
 	for _, err := range sentinels {
 		if err == nil {
 			t.Error("sentinel error is nil")
 		}
 		if err.Error() == "" {
 			t.Error("sentinel error has empty message")
 		}
 	}
 }
 
 func contains(s, substr string) bool {
 	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
 		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
 }
 
 func findSubstring(s, substr string) bool {
 	for i := 0; i <= len(s)-len(substr); i++ {
 		if s[i:i+len(substr)] == substr {
 			return true
 		}
 	}
 	return false
 }
