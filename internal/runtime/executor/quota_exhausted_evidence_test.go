package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const googleQuotaExhaustedBody = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Quota exhausted","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"7200s"}]}}`

const googleQuotaExhaustedNoDelayBody = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Quota exhausted","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"}]}}`

// quotaEvidenceOf returns the evidence an error reports: exhausted, window
// minutes and the reset reported apart from the retry hint (0 when none).
func quotaEvidenceOf(err error) (bool, int64, time.Duration) {
	quotaErr, ok := errors.AsType[cliproxyexecutor.QuotaExhaustedError](err)
	if !ok || !quotaErr.IsQuotaExhausted() {
		return false, 0, 0
	}
	var reset time.Duration
	if after := quotaErr.QuotaResetAfter(); after != nil {
		reset = *after
	}
	return true, quotaErr.QuotaLimitWindowMinutes(), reset
}

func TestStatusErrorsReportOnlyStructuredQuotaExhaustion(t *testing.T) {
	codexUsageLimit := []byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_in_seconds":3600}}`)
	codexHeaders := func(secondaryUsed string) http.Header {
		return http.Header{
			"X-Codex-Primary-Used-Percent": {"100"}, "X-Codex-Primary-Window-Minutes": {"300"}, "X-Codex-Primary-Reset-After-Seconds": {"3600"},
			"X-Codex-Secondary-Used-Percent": {secondaryUsed}, "X-Codex-Secondary-Window-Minutes": {"10080"}, "X-Codex-Secondary-Reset-After-Seconds": {"259200"},
		}
	}
	unix := func(d time.Duration) string { return strconv.FormatInt(time.Now().Add(d).Unix(), 10) }
	claudeRejected := func(reset5h, reset7d time.Duration, extra ...string) http.Header {
		headers := http.Header{"Anthropic-Ratelimit-Unified-Status": {"rejected"}}
		if reset5h > 0 {
			headers["Anthropic-Ratelimit-Unified-5h-Status"], headers["Anthropic-Ratelimit-Unified-5h-Reset"] = []string{"rejected"}, []string{unix(reset5h)}
		}
		if reset7d > 0 {
			headers["Anthropic-Ratelimit-Unified-7d-Status"], headers["Anthropic-Ratelimit-Unified-7d-Reset"] = []string{"rejected"}, []string{unix(reset7d)}
		}
		for i := 0; i+1 < len(extra); i += 2 {
			headers.Set(extra[i], extra[i+1])
		}
		return headers
	}
	claudeBody := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"limited"}}`)
	googleRateLimit := []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Rate limited","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"7200s"}]}}`)
	tests := []struct {
		name       string
		err        error
		want       bool
		wantWindow int64
		wantReset  time.Duration
	}{
		{name: "codex usage_limit_reached without window headers", err: newCodexStatusErr(http.StatusTooManyRequests, codexUsageLimit), want: true},
		{name: "codex 5-hour window", err: newCodexStatusErrWithHeaders(http.StatusTooManyRequests, codexHeaders("40"), codexUsageLimit), want: true, wantWindow: 300, wantReset: time.Hour},
		{name: "codex reset only in the window headers", err: newCodexStatusErrWithHeaders(http.StatusTooManyRequests, codexHeaders("40"), []byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`)), want: true, wantWindow: 300, wantReset: time.Hour},
		{name: "codex weekly window resets last", err: newCodexStatusErrWithHeaders(http.StatusTooManyRequests, codexHeaders("100"), codexUsageLimit), want: true, wantWindow: 10080, wantReset: 72 * time.Hour},
		{name: "codex generic rate limit", err: newCodexStatusErr(http.StatusTooManyRequests, []byte(`{"error":{"type":"rate_limit_exceeded","message":"Rate limit reached"}}`))},
		{name: "codex websocket usage_limit_reached", err: func() error {
			err, _ := parseCodexWebsocketError([]byte(`{"type":"error","status":429,"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"},"headers":{"x-codex-primary-used-percent":"100","x-codex-primary-window-minutes":"300"}}`))
			return err
		}(), want: true, wantWindow: 300},
		{name: "codex HTTP stream usage_limit_reached reads the response headers", err: func() error {
			err, _, _ := codexTerminalFailureErrWithHeaders([]byte(`{"type":"error","error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_in_seconds":3600}}`), codexHeaders("40"), false)
			return err
		}(), want: true, wantWindow: 300, wantReset: time.Hour},
		{name: "codex HTTP stream usage_limit_reached with the reset only in Retry-After", err: func() error {
			err, _, _ := codexTerminalFailureErrWithHeaders([]byte(`{"type":"error","error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`), http.Header{"Retry-After": {"7200"}}, false)
			return err
		}(), want: true, wantReset: 2 * time.Hour},
		{name: "openai-compat stream insufficient_quota frame with the reset in Retry-After", err: func() error {
			err, _ := openAICompatStreamDataError([]byte(`{"error":{"type":"insufficient_quota","status":429}}`), "", http.Header{"Retry-After": {"7200"}})
			return err
		}(), want: true, wantReset: 2 * time.Hour},
		{name: "openai-compat stream QUOTA_EXHAUSTED frame", err: func() error {
			frame := ojson.NewDocString(googleQuotaExhaustedBody)
			if errSet := frame.Set("status", http.StatusTooManyRequests); errSet != nil {
				return errSet
			}
			err, _ := openAICompatStreamDataError(frame.Bytes(), "", nil)
			return err
		}(), want: true, wantReset: 2 * time.Hour},
		{name: "openai insufficient_quota", err: newOpenAICompatStatusError(http.StatusTooManyRequests, nil, []byte(`{"error":{"type":"insufficient_quota","code":"insufficient_quota"}}`)), want: true},
		{name: "openai insufficient_quota with an immediate Retry-After", err: newOpenAICompatStatusError(http.StatusTooManyRequests, http.Header{"Retry-After": {"0"}}, []byte(`{"error":{"type":"insufficient_quota"}}`)), want: true},
		{name: "openai rate_limit_exceeded", err: newOpenAICompatStatusError(http.StatusTooManyRequests, nil, []byte(`{"error":{"type":"requests","code":"rate_limit_exceeded"}}`))},
		{name: "openai insufficient_quota on a non-429", err: newOpenAICompatStatusError(http.StatusForbidden, nil, []byte(`{"error":{"type":"insufficient_quota"}}`))},
		{name: "vertex openapi QUOTA_EXHAUSTED", err: newOpenAICompatStatusError(http.StatusTooManyRequests, nil, []byte(googleQuotaExhaustedBody)), want: true, wantReset: 2 * time.Hour},
		{name: "google QUOTA_EXHAUSTED", err: newGoogleStatusErr(http.StatusTooManyRequests, nil, []byte(googleQuotaExhaustedBody)), want: true, wantReset: 2 * time.Hour},
		{name: "google long RATE_LIMIT_EXCEEDED", err: newGoogleStatusErr(http.StatusTooManyRequests, nil, googleRateLimit)},
		{name: "antigravity QUOTA_EXHAUSTED", err: newAntigravityStatusErr(http.StatusTooManyRequests, nil, []byte(googleQuotaExhaustedBody)), want: true, wantReset: 2 * time.Hour},
		{name: "antigravity QUOTA_EXHAUSTED reset only in Retry-After", err: newAntigravityStatusErr(http.StatusTooManyRequests, http.Header{"Retry-After": {"7200"}}, []byte(googleQuotaExhaustedNoDelayBody)), want: true, wantReset: 2 * time.Hour},
		{name: "antigravity quota keyword only", err: newAntigravityStatusErr(http.StatusTooManyRequests, nil, []byte(`{"error":{"code":429,"message":"You have exhausted your capacity on this model. Your quota will reset after 2h."}}`))},
		{name: "claude unified 5h rejection", err: classifyClaudeUpstreamError(http.StatusTooManyRequests, claudeRejected(2*time.Hour, 0), claudeBody), want: true, wantWindow: 300},
		{name: "claude fast unified 5h rejection", err: newClaudeFastDirectResponseError(&http.Response{StatusCode: http.StatusTooManyRequests, Header: claudeRejected(2*time.Hour, 0)}, claudeBody), want: true, wantWindow: 300},
		{name: "claude fast ordinary 429", err: newClaudeFastDirectResponseError(&http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"30"}}}, claudeBody)},
		{name: "claude 7d rejection with model-level cooling", err: classifyClaudeUpstreamErrorWithCooling(http.StatusTooManyRequests, claudeRejected(0, 72*time.Hour), claudeBody, true), want: true, wantWindow: 10080},
		{name: "claude 5h resets after weekly", err: classifyClaudeUpstreamError(http.StatusTooManyRequests, claudeRejected(4*time.Hour, time.Hour), claudeBody), want: true, wantWindow: 300},
		{name: "claude weekly resets after 5h", err: classifyClaudeUpstreamError(http.StatusTooManyRequests, claudeRejected(time.Hour, 72*time.Hour), claudeBody), want: true, wantWindow: 10080},
		{name: "claude Retry-After past the rejected window", err: classifyClaudeUpstreamError(http.StatusTooManyRequests, claudeRejected(time.Hour, 0, "Retry-After", "7200"), claudeBody), want: true},
		{name: "claude ordinary 429", err: classifyClaudeUpstreamError(http.StatusTooManyRequests, http.Header{"Retry-After": {"30"}}, []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed the requests per minute rate limit. Please try again later."}}`))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, window, reset := quotaEvidenceOf(test.err)
			if got != test.want || window != test.wantWindow || reset != test.wantReset {
				t.Fatalf("evidence = (%t, %d, %v), want (%t, %d, %v); err=%v", got, window, reset, test.want, test.wantWindow, test.wantReset, test.err)
			}
		})
	}
}

// Gemini and Vertex report Google quota exhaustion on every request path
// without changing the error text or retry hint the default mode uses.
func TestGeminiAndVertexReportGoogleQuotaExhaustion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, googleQuotaExhaustedBody)
	}))
	t.Cleanup(server.Close)
	for _, provider := range []string{"gemini", "vertex"} {
		for _, stream := range []bool{false, true} {
			exec := task6Executor(provider)
			auth := &cliproxyauth.Auth{ID: "google-quota-" + provider, Provider: exec.Identifier(), Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
			req := cliproxyexecutor.Request{Model: "gemini-2.5-pro", Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini, Stream: stream}
			var err error
			if stream {
				_, err = exec.ExecuteStream(context.Background(), auth, req, opts)
			} else {
				_, err = exec.Execute(context.Background(), auth, req, opts)
			}
			hint, _ := err.(interface{ RetryAfter() *time.Duration })
			if exhausted, _, reset := quotaEvidenceOf(err); !exhausted || reset != 2*time.Hour || err.Error() != googleQuotaExhaustedBody || (hint != nil && hint.RetryAfter() != nil) {
				t.Fatalf("%s stream=%t: err = %T %v, want QUOTA_EXHAUSTED evidence with a 2h reset and the error unchanged", provider, stream, err, err)
			}
		}
	}
}

// Antigravity token counting reports Google quota exhaustion like the other
// request paths.
func TestAntigravityCountTokensReportsQuotaExhaustion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, googleQuotaExhaustedBody)
	}))
	t.Cleanup(server.Close)
	auth := &cliproxyauth.Auth{ID: "antigravity-count-quota", Provider: "antigravity", Attributes: map[string]string{"base_url": server.URL}, Metadata: map[string]any{
		"access_token": "token", "project_id": "project-1", "expired": time.Now().Add(time.Hour).Format(time.RFC3339),
	}}
	_, err := task6Executor("antigravity").CountTokens(context.Background(), auth, cliproxyexecutor.Request{
		Model: "gemini-3.6-flash-high", Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini})
	if exhausted, _, _ := quotaEvidenceOf(err); !exhausted || err.Error() != googleQuotaExhaustedBody {
		t.Fatalf("err = %T %v, want QUOTA_EXHAUSTED evidence with the error unchanged", err, err)
	}
}
