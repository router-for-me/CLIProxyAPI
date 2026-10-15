package executor

import (
	"errors"
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func isQuotaExhaustedEvidence(err error) bool {
	quotaErr, ok := errors.AsType[cliproxyexecutor.QuotaExhaustedError](err)
	return ok && quotaErr.IsQuotaExhausted()
}

func TestStatusErrorsReportOnlyStructuredQuotaExhaustion(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "codex usage_limit_reached",
			err:  newCodexStatusErr(http.StatusTooManyRequests, []byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_in_seconds":3600}}`)),
			want: true,
		},
		{
			name: "codex generic rate limit",
			err:  newCodexStatusErr(http.StatusTooManyRequests, []byte(`{"error":{"type":"rate_limit_exceeded","message":"Rate limit reached"}}`)),
			want: false,
		},
		{
			name: "codex websocket usage_limit_reached",
			err: func() error {
				err, _ := parseCodexWebsocketError([]byte(`{"type":"error","status":429,"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`))
				return err
			}(),
			want: true,
		},
		{
			name: "antigravity QUOTA_EXHAUSTED",
			err:  newAntigravityStatusErr(http.StatusTooManyRequests, []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Quota exhausted","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"}]}}`)),
			want: true,
		},
		{
			name: "antigravity long RATE_LIMIT_EXCEEDED",
			err:  newAntigravityStatusErr(http.StatusTooManyRequests, []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Rate limited","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"7200s"}]}}`)),
			want: false,
		},
		{
			name: "antigravity quota keyword only",
			err:  newAntigravityStatusErr(http.StatusTooManyRequests, []byte(`{"error":{"code":429,"message":"You have exhausted your capacity on this model. Your quota will reset after 2h."}}`)),
			want: false,
		},
		{
			name: "openai insufficient_quota",
			err:  newOpenAICompatStatusError(http.StatusTooManyRequests, nil, []byte(`{"error":{"message":"You exceeded your current quota","type":"insufficient_quota","code":"insufficient_quota"}}`)),
			want: true,
		},
		{
			name: "openai rate_limit_exceeded",
			err:  newOpenAICompatStatusError(http.StatusTooManyRequests, nil, []byte(`{"error":{"message":"Rate limit reached","type":"requests","code":"rate_limit_exceeded"}}`)),
			want: false,
		},
		{
			name: "openai insufficient_quota on a non-429",
			err:  newOpenAICompatStatusError(http.StatusForbidden, nil, []byte(`{"error":{"type":"insufficient_quota","code":"insufficient_quota"}}`)),
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.err == nil {
				t.Fatal("constructor returned nil error")
			}
			if got := isQuotaExhaustedEvidence(test.err); got != test.want {
				t.Fatalf("IsQuotaExhausted() = %v, want %v; err=%v", got, test.want, test.err)
			}
		})
	}
}
