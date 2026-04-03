package auth

import (
	"fmt"
	"testing"
)

// testStatusErr is a minimal error with an HTTP status code for testing
// isRequestInvalidError without depending on internal/runtime/executor.
type testStatusErr struct {
	code int
	msg  string
}

func (e testStatusErr) Error() string    { return e.msg }
func (e testStatusErr) StatusCode() int  { return e.code }

func TestIsRequestInvalidError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "400 with invalid_request_error",
			err:  testStatusErr{code: 400, msg: `{"error":{"type":"invalid_request_error","message":"max tokens exceeded"}}`},
			want: true,
		},
		{
			name: "400 Gemini INVALID_ARGUMENT token limit",
			err:  testStatusErr{code: 400, msg: `{"error":{"code":400,"message":"The input token count exceeds the maximum number of tokens allowed 1048576.","status":"INVALID_ARGUMENT"}}`},
			want: true,
		},
		{
			name: "400 Gemini INVALID_ARGUMENT generic",
			err:  testStatusErr{code: 400, msg: `{"error":{"code":400,"message":"Request payload size exceeds the limit.","status":"INVALID_ARGUMENT"}}`},
			want: true,
		},
		{
			name: "400 model support error is retryable",
			err:  testStatusErr{code: 400, msg: "model_not_supported: gemini-ultra is not available"},
			want: false,
		},
		{
			name: "400 requested model is unsupported",
			err:  testStatusErr{code: 400, msg: "requested model is unsupported for this account"},
			want: false,
		},
		{
			name: "413 payload too large",
			err:  testStatusErr{code: 413, msg: "request entity too large"},
			want: true,
		},
		{
			name: "422 unprocessable entity",
			err:  testStatusErr{code: 422, msg: "unprocessable entity"},
			want: true,
		},
		{
			name: "429 rate limit is retryable",
			err:  testStatusErr{code: 429, msg: "rate limit exceeded"},
			want: false,
		},
		{
			name: "503 service unavailable is retryable",
			err:  testStatusErr{code: 503, msg: "service unavailable"},
			want: false,
		},
		{
			name: "plain error without status code",
			err:  fmt.Errorf("connection refused"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRequestInvalidError(tt.err)
			if got != tt.want {
				t.Errorf("isRequestInvalidError() = %v, want %v", got, tt.want)
			}
		})
	}
}
