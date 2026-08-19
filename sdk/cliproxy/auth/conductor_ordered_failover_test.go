package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestPermanentOrderedStopError covers AC#4 permanent-stop taxonomy: the chain
// must NOT advance on these statuses.
func TestPermanentOrderedStopError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"400 invalid_request_error", &Error{HTTPStatus: 400, Message: "invalid_request_error: bad shape"}, true},
		{"400 generic", &Error{HTTPStatus: 400, Message: "some other 400"}, false},
		{"401 unauthorized", &Error{HTTPStatus: 401, Message: "unauthorized"}, true},
		{"403 forbidden", &Error{HTTPStatus: 403, Message: "forbidden"}, true},
		{"404 not found", &Error{HTTPStatus: 404, Message: "model not found"}, true},
		{"413 too large", &Error{HTTPStatus: 413, Message: "payload too large"}, true},
		{"410 gone", &Error{HTTPStatus: 410, Message: "gone"}, true},
		{"422 unprocessable", &Error{HTTPStatus: 422, Message: "unprocessable"}, true},
		{"request_scoped", &Error{Code: "request_scoped", Message: "scoped"}, true},
		{"empty_stream", &Error{Code: "empty_stream", Message: "empty"}, false},
		{"429 too many requests", &Error{HTTPStatus: 429, Message: "too many"}, false},
		{"500 server error", &Error{HTTPStatus: 500, Message: "server"}, false},
		{"503 service unavailable", &Error{HTTPStatus: 503, Message: "unavailable"}, false},
		{"504 gateway timeout", &Error{HTTPStatus: 504, Message: "gateway timeout"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := permanentOrderedStopError(tc.err)
			if got != tc.want {
				t.Fatalf("permanentOrderedStopError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestRetryablePreFirstByteError covers AC#4 retryable pre-first-byte taxonomy:
// the chain advances to the next candidate on these statuses.
func TestRetryablePreFirstByteError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"429", &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limit"}, true},
		{"408 timeout", &Error{HTTPStatus: http.StatusRequestTimeout, Message: "timeout"}, true},
		{"500", &Error{HTTPStatus: http.StatusInternalServerError, Message: "server"}, true},
		{"502", &Error{HTTPStatus: http.StatusBadGateway, Message: "bad gateway"}, true},
		{"503", &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "unavailable"}, true},
		{"504", &Error{HTTPStatus: http.StatusGatewayTimeout, Message: "gateway timeout"}, true},
		{"509 bandwidth", &Error{HTTPStatus: 509, Message: "bandwidth"}, true},
		{"529 cloudflare", &Error{HTTPStatus: 529, Message: "overloaded"}, true},
		{"empty_stream", &Error{Code: "empty_stream", Message: "empty"}, true},
		{"retryable flag", &Error{Code: "custom", Message: "x", Retryable: true}, true},
		{"401 not retryable", &Error{HTTPStatus: 401, Message: "unauthorized"}, false},
		{"403 not retryable", &Error{HTTPStatus: 403, Message: "forbidden"}, false},
		{"400 invalid_request not retryable", &Error{HTTPStatus: 400, Message: "invalid_request_error"}, false},
		{"422 not retryable", &Error{HTTPStatus: 422, Message: "unprocessable"}, false},
		{"generic 400 not retryable", &Error{HTTPStatus: 400, Message: "some 400"}, false},
		{"canceled", errors.New("context canceled"), false},
		{"stream bootstrap wraps retryable", newStreamBootstrapError(&Error{HTTPStatus: 429, Message: "429"}, nil), true},
		{"stream bootstrap wraps permanent", newStreamBootstrapError(&Error{HTTPStatus: 401, Message: "401"}, nil), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := retryablePreFirstByteError(tc.err)
			if got != tc.want {
				t.Fatalf("retryablePreFirstByteError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestOrderedFailoverErrorFallbackTrace proves AC#4 secret-safe observability:
// the wrapper exposes from/to/reason/status/chain_index without secrets.
func TestOrderedFailoverErrorFallbackTrace(t *testing.T) {
	chain := []OrderedCandidate{
		{Channel: "claude", UpstreamModel: "claude-opus-4", ConfigAlias: "sonnet"},
		{Channel: "claude", UpstreamModel: "gpt-5", ConfigAlias: "sonnet"},
		{Channel: "claude", UpstreamModel: "kimi-k3", ConfigAlias: "sonnet"},
	}
	innerErr := &Error{HTTPStatus: 429, Message: "rate limited"}
	wrapped := annotateOrderedError(innerErr, chain, 1, "exhausted")
	if wrapped == nil {
		t.Fatal("annotateOrderedError returned nil")
	}
	var fe *orderedFailoverError
	if !errors.As(wrapped, &fe) || fe == nil {
		t.Fatalf("wrapped error is not *orderedFailoverError: %T", wrapped)
	}
	attempted, from, to, reason, status, idx, length := fe.FallbackTrace()
	if !attempted {
		t.Error("FallbackTrace() attempted = false, want true")
	}
	if from != "claude-opus-4" {
		t.Errorf("FallbackTrace() from = %q, want claude-opus-4", from)
	}
	if to != "gpt-5" {
		t.Errorf("FallbackTrace() to = %q, want gpt-5", to)
	}
	if reason != "exhausted" {
		t.Errorf("FallbackTrace() reason = %q, want exhausted", reason)
	}
	if status != 429 {
		t.Errorf("FallbackTrace() status = %d, want 429", status)
	}
	if idx != 1 {
		t.Errorf("FallbackTrace() idx = %d, want 1", idx)
	}
	if length != 3 {
		t.Errorf("FallbackTrace() length = %d, want 3", length)
	}
	h := fe.Headers()
	if h.Get("X-Fallback-Attempted") != "true" {
		t.Errorf("X-Fallback-Attempted = %q, want true", h.Get("X-Fallback-Attempted"))
	}
	if h.Get("X-Fallback-From-Model") != "claude-opus-4" {
		t.Errorf("X-Fallback-From-Model = %q", h.Get("X-Fallback-From-Model"))
	}
	if h.Get("X-Fallback-To-Model") != "gpt-5" {
		t.Errorf("X-Fallback-To-Model = %q", h.Get("X-Fallback-To-Model"))
	}
	if h.Get("X-Fallback-Reason") != "exhausted" {
		t.Errorf("X-Fallback-Reason = %q", h.Get("X-Fallback-Reason"))
	}
	if h.Get("X-Fallback-Status") != "429" {
		t.Errorf("X-Fallback-Status = %q, want 429", h.Get("X-Fallback-Status"))
	}
	if h.Get("X-Fallback-Chain-Index") != "1" {
		t.Errorf("X-Fallback-Chain-Index = %q", h.Get("X-Fallback-Chain-Index"))
	}
	if h.Get("X-Fallback-Chain-Length") != "3" {
		t.Errorf("X-Fallback-Chain-Length = %q", h.Get("X-Fallback-Chain-Length"))
	}
	if !errors.Is(wrapped, innerErr) {
		t.Error("errors.Is(wrapped, innerErr) = false; unwrap chain is broken")
	}
}

// TestOrderedFailoverErrorSanitizesHeaderValue proves that model names with
// embedded CR/LF cannot inject headers into the response (AC#4 secret-safe).
func TestOrderedFailoverErrorSanitizesHeaderValue(t *testing.T) {
	chain := []OrderedCandidate{
		{Channel: "claude", UpstreamModel: "claude\r\n-Evil: yes", ConfigAlias: "x"},
	}
	wrapped := annotateOrderedError(&Error{HTTPStatus: 503, Message: "x"}, chain, 0, "permanent")
	var fe *orderedFailoverError
	if !errors.As(wrapped, &fe) || fe == nil {
		t.Fatalf("wrapped is not *orderedFailoverError: %T", wrapped)
	}
	h := fe.Headers()
	for key, values := range h {
		for _, v := range values {
			if strings.ContainsAny(v, "\r\n") {
				t.Errorf("header %q contains CR/LF: %q", key, v)
			}
		}
	}
}

// TestRouteGuardExcludedModel_ProvesAC5RouteRejection proves AC#5: a request
// for a model that matches oauth-excluded-models for the resolved channel is
// rejected with a permanent model_excluded error before any upstream call.
func TestRouteGuardExcludedModel_ProvesAC5RouteRejection(t *testing.T) {
	m := &Manager{}
	m.runtimeConfig.Store(&internalconfig.Config{
		OAuthExcludedModels: map[string][]string{
			"claude": {"claude-opus-disabled*"},
		},
	})

	err := m.routeGuardExcludedModel([]string{"claude"}, cliproxyexecutor.Request{Model: "claude-opus-disabled-v1"}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("expected model_excluded error, got nil")
	}
	authErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T: %v", err, err)
	}
	if authErr.Code != "model_excluded" {
		t.Errorf("Code = %q, want model_excluded", authErr.Code)
	}
	if !strings.Contains(authErr.Message, "claude-opus-disabled-v1") {
		t.Errorf("Message should contain the requested model: %q", authErr.Message)
	}
	if permanentOrderedStopError(err) == false {
		t.Errorf("model_excluded must be a permanent-stop error for the ordered chain")
	}
}

// TestRouteGuardExcludedModel_AllowsNonExcluded proves that non-excluded
// models are NOT rejected by the guard.
func TestRouteGuardExcludedModel_AllowsNonExcluded(t *testing.T) {
	m := &Manager{}
	m.runtimeConfig.Store(&internalconfig.Config{
		OAuthExcludedModels: map[string][]string{
			"claude": {"claude-opus-disabled*"},
		},
	})

	err := m.routeGuardExcludedModel([]string{"claude"}, cliproxyexecutor.Request{Model: "claude-sonnet-4"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("expected nil error for non-excluded model, got %v", err)
	}
}

// TestRouteGuardExcludedModel_WildcardMatch proves wildcard exclusion patterns
// work correctly (mirrors the registry's applyExcludedModels semantics).
func TestRouteGuardExcludedModel_WildcardMatch(t *testing.T) {
	m := &Manager{}
	m.runtimeConfig.Store(&internalconfig.Config{
		OAuthExcludedModels: map[string][]string{
			"codex": {"gpt-4*"},
		},
	})

	cases := []struct {
		model string
		want  bool
	}{
		{"gpt-4", true},
		{"gpt-4-turbo", true},
		{"gpt-4o-mini", true},
		{"gpt-5", false},
		{"claude-opus-4", false},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			err := m.routeGuardExcludedModel([]string{"codex"}, cliproxyexecutor.Request{Model: tc.model}, cliproxyexecutor.Options{})
			if tc.want && err == nil {
				t.Fatalf("expected exclusion for %s, got nil", tc.model)
			}
			if !tc.want && err != nil {
				t.Fatalf("expected no exclusion for %s, got %v", tc.model, err)
			}
		})
	}
}

// TestMatchExcludedPattern proves the underlying wildcard matcher used by the
// route guard matches semantics with sdk/cliproxy/service.go applyExcludedModels.
func TestMatchExcludedPattern(t *testing.T) {
	cases := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"gpt-4*", "gpt-4", true},
		{"gpt-4*", "gpt-4-turbo", true},
		{"gpt-4*", "gpt-5", false},
		{"*-turbo", "gpt-4-turbo", true},
		{"*-turbo", "gpt-4", false},
		{"gpt-*mini", "gpt-4o-mini", true},
		{"gpt-*mini", "gpt-4o-max", false},
		{"exact", "exact", true},
		{"exact", "other", false},
		{"", "anything", false},
	}
	for _, tc := range cases {
		t.Run(tc.pattern+"->"+tc.value, func(t *testing.T) {
			got := matchExcludedPattern(tc.pattern, tc.value)
			if got != tc.want {
				t.Fatalf("matchExcludedPattern(%q, %q) = %v, want %v", tc.pattern, tc.value, got, tc.want)
			}
		})
	}
}

// stubExecuteFn is a minimal ProviderExecutor stub used for failover tests
// where we want to verify which model the conductor called. It does NOT
// exercise real HTTP; the retry-taxonomy tests above prove the decision logic.
type stubExecuteFn struct {
	respByModel map[string]cliproxyexecutor.Response
	errByModel  map[string]error
	calls       []string
}

func (s *stubExecuteFn) Identifier() string { return "stub" }
func (s *stubExecuteFn) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	s.calls = append(s.calls, req.Model)
	if err, ok := s.errByModel[req.Model]; ok {
		return cliproxyexecutor.Response{}, err
	}
	if resp, ok := s.respByModel[req.Model]; ok {
		return resp, nil
	}
	return cliproxyexecutor.Response{}, nil
}
func (s *stubExecuteFn) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{Code: "not_implemented", Message: "stream not implemented in stub"}
}
func (s *stubExecuteFn) Refresh(ctx context.Context, auth *Auth) (*Auth, error) { return auth, nil }
func (s *stubExecuteFn) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (s *stubExecuteFn) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

// Compile-time assertion that stubExecuteFn satisfies ProviderExecutor.
var _ ProviderExecutor = (*stubExecuteFn)(nil)
