package executor

import (
	"net/http"
	"testing"
)

func TestNewAntigravityStatusErrMarksSignalLess429Transient(t *testing.T) {
	cases := []struct {
		name          string
		body          string
		wantTransient bool
	}{
		{
			name:          "signal-less generic 429 is transient",
			body:          `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`,
			wantTransient: true,
		},
		{
			name:          "empty 429 body is transient",
			body:          ``,
			wantTransient: true,
		},
		{
			name:          "structured QUOTA_EXHAUSTED stays credential quota",
			body:          `{"error":{"code":429,"message":"You have exhausted your capacity on this model.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"}]}}`,
			wantTransient: false,
		},
		{
			name:          "RATE_LIMIT_EXCEEDED with retryDelay keeps short cooldown path",
			body:          `{"error":{"code":429,"message":"Rate limited","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"30s"}]}}`,
			wantTransient: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := newAntigravityStatusErr(http.StatusTooManyRequests, []byte(tc.body))
			if got := err.IsTransientRateLimit(); got != tc.wantTransient {
				t.Fatalf("IsTransientRateLimit() = %v, want %v (body=%s)", got, tc.wantTransient, tc.body)
			}
		})
	}
}
