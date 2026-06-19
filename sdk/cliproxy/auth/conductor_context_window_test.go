package auth

import (
	"fmt"
	"testing"
)

// ctxStatusErr is a minimal error that also implements StatusCode() for testing.
type ctxStatusErr struct {
	code int
	msg  string
}

func (e *ctxStatusErr) Error() string   { return e.msg }
func (e *ctxStatusErr) StatusCode() int { return e.code }

func TestIsContextWindowExceededError(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{
			name: "code context_too_large",
			msg:  `{"error":{"message":"Your input exceeds the context window of this model.","type":"invalid_request_error","code":"context_too_large"}}`,
			want: true,
		},
		{
			name: "code context_length_exceeded",
			msg:  `{"error":{"code":"context_length_exceeded","message":"prompt is too long"}}`,
			want: true,
		},
		{
			name: "message contains context window",
			msg:  "Your input exceeds the context window of this model",
			want: true,
		},
		{
			name: "message contains context length",
			msg:  "maximum context length is 8192 tokens",
			want: true,
		},
		{
			name: "message contains too many tokens",
			msg:  "This model's maximum context length is 4096 tokens. Too many tokens in your request.",
			want: true,
		},
		{
			name: "unrelated error",
			msg:  "payload too large",
			want: false,
		},
		{
			name: "nil error",
			msg:  "", // tested via nil path below
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "nil error" {
				if got := isContextWindowExceededError(nil); got != false {
					t.Errorf("isContextWindowExceededError(nil) = %v, want false", got)
				}
				return
			}
			err := fmt.Errorf("%s", tc.msg)
			if got := isContextWindowExceededError(err); got != tc.want {
				t.Errorf("isContextWindowExceededError(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}

func TestIsRequestInvalidError_ContextWindow(t *testing.T) {
	contextTooLargeJSON := `{"error":{"message":"Your input exceeds the context window of this model.","type":"invalid_request_error","code":"context_too_large"}}`

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			// Regression: 413 with context_too_large must fast-fail, not cycle credentials.
			name: "413 context_too_large fast-fails",
			err:  &ctxStatusErr{code: 413, msg: contextTooLargeJSON},
			want: true,
		},
		{
			// Pre-existing: 400 with context_too_large still fast-fails.
			name: "400 context_too_large fast-fails",
			err:  &ctxStatusErr{code: 400, msg: contextTooLargeJSON},
			want: true,
		},
		{
			// No over-classification: 413 without context wording must NOT fast-fail.
			name: "413 plain payload too large does not fast-fail",
			err:  &ctxStatusErr{code: 413, msg: "payload too large"},
			want: false,
		},
		{
			name: "nil does not fast-fail",
			err:  nil,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRequestInvalidError(tc.err); got != tc.want {
				t.Errorf("isRequestInvalidError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
