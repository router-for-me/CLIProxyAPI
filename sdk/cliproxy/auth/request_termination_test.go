package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestRequestTerminatedErrorSkipsCreditsFallback(t *testing.T) {
	errTerminated := &cliproxyexecutor.RequestTerminatedError{HTTPStatus: http.StatusTooManyRequests}
	if !isRequestTerminatedError(errTerminated) {
		t.Fatal("isRequestTerminatedError() = false")
	}
	if shouldAttemptAntigravityCreditsFallback(&Manager{}, errTerminated, []string{"antigravity"}) {
		t.Fatal("terminated request must not use Antigravity credits fallback")
	}
}

type afterAuthTestExecutor struct{}

func (*afterAuthTestExecutor) Identifier() string { return "afterauth" }

func (*afterAuthTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (*afterAuthTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ch := make(chan cliproxyexecutor.StreamChunk)
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (*afterAuthTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) { return auth, nil }

func (*afterAuthTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (*afterAuthTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestRequestTerminatedErrorZeroValueIsUntrustedAndApplyAfterAuthSetsTrusted(t *testing.T) {
	// Zero-value Trusted must be false (safe default for untrusted upstreams).
	zero := &cliproxyexecutor.RequestTerminatedError{HTTPStatus: http.StatusBadGateway}
	if zero.Trusted {
		t.Fatal("zero-value RequestTerminatedError.Trusted must be false")
	}

	// A termination produced by applyRequestAfterAuthInterceptor must be trusted.
	executor := &afterAuthTestExecutor{}
	appliedTerminate := false
	interceptor := func(context.Context, cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
		appliedTerminate = true
		return cliproxyexecutor.RequestAfterAuthInterceptResponse{
			Terminate:       true,
			StatusCode:      http.StatusTeapot,
			ResponseHeaders: http.Header{"X-AfterAuth": []string{"yes"}},
			ResponseBody:    []byte(`{"ok":true}`),
		}
	}
	_, _, errIntercept := applyRequestAfterAuthInterceptor(context.Background(), executor, "test", cliproxyexecutor.Request{}, cliproxyexecutor.Options{
		RequestAfterAuthInterceptor: interceptor,
	}, "model")
	var terminated *cliproxyexecutor.RequestTerminatedError
	if !errors.As(errIntercept, &terminated) || terminated == nil {
		t.Fatalf("applyRequestAfterAuthInterceptor error = %v, want RequestTerminatedError", errIntercept)
	}
	if !terminated.Trusted {
		t.Fatal("after-auth interceptor termination must set Trusted=true")
	}
	if !appliedTerminate {
		t.Fatal("interceptor was not applied")
	}
}
