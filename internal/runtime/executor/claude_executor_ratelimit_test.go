package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// The classifier must surface Anthropic's retry-at hints (unified rate-limit
// headers plus Retry-After) on the status error it returns, so the conductor's
// cooldown path can honor the upstream hint instead of the default ladder.
func TestClassifyClaudeUpstreamError_RetryAfterHeader(t *testing.T) {
	headers := http.Header{}
	headers.Set("Retry-After", "30")
	err := classifyClaudeUpstreamError(http.StatusTooManyRequests, headers, []byte("{}"))

	want := 30 * time.Second
	var status interface{ RetryAfter() *time.Duration }
	if !errors.As(err, &status) {
		t.Fatalf("classifier returned %T, want an error with RetryAfter()", err)
	}
	if got := status.RetryAfter(); got == nil || *got != want {
		t.Fatalf("RetryAfter() = %v, want %s", got, want)
	}
}

// Overage-only rejections are account caps, not throttles: Task 1's parser
// returns no retry hint for them and the end-to-end wiring must not invent one.
func TestClassifyClaudeUpstreamError_OverageOnlyRejectionHasNoHint(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-status", "rejected")
	headers.Set("anthropic-ratelimit-unified-overage-status", "rejected")
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	err := classifyClaudeUpstreamError(http.StatusTooManyRequests, headers, []byte("{}"))

	var status interface{ RetryAfter() *time.Duration }
	if !errors.As(err, &status) {
		t.Fatalf("classifier returned %T, want an error with RetryAfter()", err)
	}
	if got := status.RetryAfter(); got != nil {
		t.Fatalf("RetryAfter() = %s, want nil for an overage-only rejection", got)
	}
}

// Without any rate-limit headers the default cooldown ladder stays in charge.
func TestClassifyClaudeUpstreamError_NoHeadersNoHint(t *testing.T) {
	err := classifyClaudeUpstreamError(http.StatusTooManyRequests, http.Header{}, []byte("{}"))

	var status interface{ RetryAfter() *time.Duration }
	if !errors.As(err, &status) {
		t.Fatalf("classifier returned %T, want an error with RetryAfter()", err)
	}
	if got := status.RetryAfter(); got != nil {
		t.Fatalf("RetryAfter() = %s, want nil when no rate-limit headers are present", got)
	}
}

// Retry-After is parsed for any non-2xx status; the conductor only consumes the
// hint where it is meaningful, so a 500 with a header is harmless to surface.
func TestClassifyClaudeUpstreamError_RetryAfterOnNon429(t *testing.T) {
	headers := http.Header{}
	headers.Set("Retry-After", "5")
	err := classifyClaudeUpstreamError(http.StatusInternalServerError, headers, []byte("{}"))

	var status interface{ RetryAfter() *time.Duration }
	if !errors.As(err, &status) {
		t.Fatalf("classifier returned %T, want an error with RetryAfter()", err)
	}
	if got := status.RetryAfter(); got == nil || *got != 5*time.Second {
		t.Fatalf("RetryAfter() = %v, want 5s", got)
	}
}

// An overage-only rejection is a billing cap on the account, not a throttle:
// the classifier must wrap the status error so downstream consumers can send
// the credential into a long overage cooldown instead of the throttle ladder,
// while StatusCode/RetryAfter stay reachable through Unwrap.
func TestClassifyClaudeUpstreamError_OverageOnlyRejectionMarksOverage(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-status", "allowed")
	headers.Set("anthropic-ratelimit-unified-overage-status", "rejected")
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	err := classifyClaudeUpstreamError(http.StatusTooManyRequests, headers, []byte("{}"))

	overage, ok := err.(interface{ OverageRejected() bool })
	if !ok || !overage.OverageRejected() {
		t.Fatalf("overage-only rejection = %T, want an error marked OverageRejected", err)
	}
	var status *statusErr
	if !errors.As(err, &status) {
		t.Fatalf("overage error %T does not unwrap to statusErr", err)
	}
	if status.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("StatusCode() = %d, want 429", status.StatusCode())
	}
}

// A plain throttle with a usable Retry-After is not an overage cap; the hint
// must survive untouched and no overage marker may be attached.
func TestClassifyClaudeUpstreamError_PlainThrottleNotOverage(t *testing.T) {
	headers := http.Header{}
	headers.Set("Retry-After", "30")
	err := classifyClaudeUpstreamError(http.StatusTooManyRequests, headers, []byte("{}"))

	if overage, ok := err.(interface{ OverageRejected() bool }); ok && overage.OverageRejected() {
		t.Fatalf("plain 429 with Retry-After was misclassified as overage")
	}
	var status interface {
		StatusCode() int
		RetryAfter() *time.Duration
	}
	if !errors.As(err, &status) {
		t.Fatalf("classifier returned %T, want an error unwrapping to statusErr", err)
	}
	if got := status.RetryAfter(); got == nil || *got != 30*time.Second {
		t.Fatalf("RetryAfter() = %v, want 30s", got)
	}
}

// A rejected shared window is the real throttle even when the overage header
// exists; Retry-After wins and the rejection stays a throttle.
func TestClassifyClaudeUpstreamError_SharedWindowThrottleNotOverage(t *testing.T) {
	headers := http.Header{}
	headers.Set("Retry-After", "30")
	headers.Set("anthropic-ratelimit-unified-status", "rejected")
	headers.Set("anthropic-ratelimit-unified-overage-status", "rejected")
	err := classifyClaudeUpstreamError(http.StatusTooManyRequests, headers, []byte("{}"))

	if overage, ok := err.(interface{ OverageRejected() bool }); ok && overage.OverageRejected() {
		t.Fatalf("shared-window rejected 429 was misclassified as overage")
	}
	var status interface {
		StatusCode() int
		RetryAfter() *time.Duration
	}
	if !errors.As(err, &status) {
		t.Fatalf("classifier returned %T, want an error unwrapping to statusErr", err)
	}
	if got := status.RetryAfter(); got == nil || *got != 30*time.Second {
		t.Fatalf("RetryAfter() = %v, want 30s preserved on the shared throttle", got)
	}
}

// Without headers, the body message is the only signal of a spend cap; a 403
// billing error and a 429 usage-limit notice must both be marked as overage.
func TestClassifyClaudeUpstreamError_BodyKeywordFallback(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		body       []byte
	}{
		{
			name:       "403 spend cap",
			statusCode: http.StatusForbidden,
			body:       []byte(`{"type":"error","error":{"type":"billing_error","message":"Your org has reached its monthly spend cap."}}`),
		},
		{
			name:       "429 usage limit",
			statusCode: http.StatusTooManyRequests,
			body:       []byte(`{"type":"error","error":{"type":"billing_error","message":"Your usage limit has been reached."}}`),
		},
	}
	for _, tc := range cases {
		err := classifyClaudeUpstreamError(tc.statusCode, http.Header{}, tc.body)
		overage, ok := err.(interface{ OverageRejected() bool })
		if !ok || !overage.OverageRejected() {
			t.Fatalf("%s: error = %T, want an error marked OverageRejected", tc.name, err)
		}
		var status *statusErr
		if !errors.As(err, &status) {
			t.Fatalf("%s: error %T does not unwrap to statusErr", tc.name, err)
		}
		if status.StatusCode() != tc.statusCode {
			t.Fatalf("%s: StatusCode() = %d, want %d", tc.name, status.StatusCode(), tc.statusCode)
		}
	}
}

// A usable Retry-After means the upstream is throttling, not capping: the body
// keyword alone must not override an explicit wait hint.
func TestClassifyClaudeUpstreamError_RetryAfterBeatsBodyKeyword(t *testing.T) {
	headers := http.Header{}
	headers.Set("Retry-After", "30")
	headers.Set("anthropic-ratelimit-unified-status", "rejected")
	body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Your usage limit has been reached."}}`)
	err := classifyClaudeUpstreamError(http.StatusTooManyRequests, headers, body)

	if overage, ok := err.(interface{ OverageRejected() bool }); ok && overage.OverageRejected() {
		t.Fatalf("throttle with Retry-After and a usage-limit body was misclassified as overage")
	}
	var status interface {
		StatusCode() int
		RetryAfter() *time.Duration
	}
	if !errors.As(err, &status) {
		t.Fatalf("classifier returned %T, want an error unwrapping to statusErr", err)
	}
	if got := status.RetryAfter(); got == nil || *got != 30*time.Second {
		t.Fatalf("RetryAfter() = %v, want 30s", got)
	}
}

// The body-keyword fallback applies to overage statuses only (403/429): a 400
// mentioning a usage limit is an ordinary request failure and must stay
// unmarked, pinning the status gate in overageIndicated.
func TestClassifyClaudeUpstreamError_StatusGateExcludesNonOverageStatus(t *testing.T) {
	body := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"Your usage limit has been reached."}}`)
	err := classifyClaudeUpstreamError(http.StatusBadRequest, http.Header{}, body)

	if overage, ok := err.(interface{ OverageRejected() bool }); ok && overage.OverageRejected() {
		t.Fatalf("400 with a usage-limit body was misclassified as overage")
	}
}

// The credit-balance keyword is part of the body fallback: a 429 carrying it
// without any Retry-After header marks an overage cap.
func TestClassifyClaudeUpstreamError_CreditBalanceKeywordMarksOverage(t *testing.T) {
	body := []byte(`{"type":"error","error":{"type":"billing_error","message":"Insufficient credit balance for this request."}}`)
	err := classifyClaudeUpstreamError(http.StatusTooManyRequests, http.Header{}, body)

	overage, ok := err.(interface{ OverageRejected() bool })
	if !ok || !overage.OverageRejected() {
		t.Fatalf("429 credit-balance refusal = %T, want an error marked OverageRejected", err)
	}
}

// The fast-mode credits entitlement path returns before the overage check and
// must remain unmarked: it is request-scoped, never a credential cooldown.
func TestClassifyClaudeUpstreamError_FastModeEntitlementNeverOverage(t *testing.T) {
	body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Usage credits are required for fast mode."}}`)
	err := classifyClaudeUpstreamError(http.StatusTooManyRequests, http.Header{}, body)

	if _, ok := err.(claudeEntitlementError); !ok {
		t.Fatalf("fast-mode credits refusal = %T, want claudeEntitlementError", err)
	}
	if overage, ok := err.(interface{ OverageRejected() bool }); ok && overage.OverageRejected() {
		t.Fatalf("fast-mode entitlement refusal was misclassified as overage")
	}
}

// The count-tokens path builds its status errors directly; when the upstream
// *http.Response is in scope there, the retry-at hint must survive just as it
// does on the execute and stream paths. Direct call: the CountTokens gate only
// selects the upstream path for https://api.anthropic.com, which a test
// transport stubs instead.
func TestClaudeCountTokensUpstreamErrorCarriesRetryAfterHint(t *testing.T) {
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": []string{"12"}, "Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)),
			Request:    req,
		}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-ant-api03-count-tokens-retry-after"}}
	payload := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	_, err := NewClaudeExecutor(&config.Config{}).countTokensUpstream(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err == nil {
		t.Fatal("countTokensUpstream() error = nil, want the upstream 429")
	}
	if got, want := err.Error(), "rate_limit_error"; !strings.Contains(got, want) {
		t.Fatalf("error body missing upstream message sentinel %q: %s", want, got)
	}
	var status interface{ RetryAfter() *time.Duration }
	if !errors.As(err, &status) {
		t.Fatalf("countTokensUpstream() error = %T, want an error with RetryAfter()", err)
	}
	if got := status.RetryAfter(); got == nil || *got != 12*time.Second {
		t.Fatalf("RetryAfter() = %v, want 12s from the upstream header", got)
	}
}

// The count-tokens decode-failure site wraps its statusErr before the body is
// ever read; a 429 whose body cannot be decoded must still carry the upstream
// Retry-After hint. Corrupt gzip bytes force the decode failure.
func TestClaudeCountTokensDecodeFailureErrorCarriesRetryAfterHint(t *testing.T) {
	corruptGzip := []byte{0x1f, 0x8b, 0x00, 0x00}
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": []string{"14"}, "Content-Encoding": []string{"gzip"}, "Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(corruptGzip)),
			Request:    req,
		}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-ant-api03-count-tokens-decode-failure"}}
	payload := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	_, err := NewClaudeExecutor(&config.Config{}).countTokensUpstream(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err == nil {
		t.Fatal("countTokensUpstream() error = nil, want the decode failure 429")
	}
	if got, want := err.Error(), "failed to decode error response body"; !strings.Contains(got, want) {
		t.Fatalf("error = %s, want the decode-failure sentinel %q", got, want)
	}
	var status interface{ RetryAfter() *time.Duration }
	if !errors.As(err, &status) {
		t.Fatalf("countTokensUpstream() error = %T, want an error with RetryAfter()", err)
	}
	if got := status.RetryAfter(); got == nil || *got != 14*time.Second {
		t.Fatalf("RetryAfter() = %v, want 14s from the upstream header", got)
	}
}

// The execute and stream decode-failure sites wrap a raw statusErr when the
// error body itself cannot be decoded; the upstream *http.Response is in scope
// there, so a Retry-After hint on such a 429 must survive on the returned
// error. Corrupt gzip bytes force the decode failure.
func TestClaudeExecuteDecodeFailureErrorCarriesRetryAfterHint(t *testing.T) {
	corruptGzip := []byte{0x1f, 0x8b, 0x00, 0x00}
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": []string{"20"}, "Content-Encoding": []string{"gzip"}, "Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(corruptGzip)),
			Request:    req,
		}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	auth := &cliproxyauth.Auth{
		ID:         "decode-failure-retry-after",
		Attributes: map[string]string{"api_key": "sk-ant-oat-decode-failure-retry-after"},
		Metadata:   map[string]any{"account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
	}
	payload := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	req := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}

	executor := NewClaudeExecutor(&config.Config{})
	_, errExecute := executor.Execute(ctx, auth, req, opts)
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want the decode failure 429")
	}
	var status interface{ RetryAfter() *time.Duration }
	if !errors.As(errExecute, &status) {
		t.Fatalf("Execute() error = %T, want an error with RetryAfter()", errExecute)
	}
	if got := status.RetryAfter(); got == nil || *got != 20*time.Second {
		t.Fatalf("Execute() RetryAfter() = %v, want 20s from the upstream header", got)
	}

	_, errStream := executor.ExecuteStream(ctx, auth, req, opts)
	if errStream == nil {
		t.Fatal("ExecuteStream() error = nil, want the decode failure 429")
	}
	if !errors.As(errStream, &status) {
		t.Fatalf("ExecuteStream() error = %T, want an error with RetryAfter()", errStream)
	}
	if got := status.RetryAfter(); got == nil || *got != 20*time.Second {
		t.Fatalf("ExecuteStream() RetryAfter() = %v, want 20s from the upstream header", got)
	}
}
