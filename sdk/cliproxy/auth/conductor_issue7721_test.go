package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// issue7721Executor returns a StreamResult carrying upstream diagnostic headers
// whose first chunk is a request-invalid (HTTP 400) bootstrap error, mirroring a
// streaming 400 failure where the upstream response headers were already captured.
type issue7721Executor struct {
	headers http.Header
}

func (*issue7721Executor) Identifier() string { return "issue7721" }

func (*issue7721Executor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (e *issue7721Executor) ExecuteStream(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Err: NewRequestScopedError("invalid request", http.StatusBadRequest)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Headers: e.headers, Chunks: chunks}, nil
}

func (*issue7721Executor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*issue7721Executor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (*issue7721Executor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

// TestStreamInvalidRequestBootstrapPreservesHeaders verifies that a streaming 400
// bootstrap error stays a direct error (nil stream result, original status kept)
// while still carrying the upstream response headers via Headers() so the error
// path can forward them when passthrough-headers is enabled.
func TestStreamInvalidRequestBootstrapPreservesHeaders(t *testing.T) {
	model := "issue7721-model-" + uuid.NewString()
	auth := &Auth{ID: "issue7721-auth-" + uuid.NewString(), Provider: "issue7721", Status: StatusActive}
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	wantHeaders := http.Header{"X-Trace-Id": []string{"trace-abc-123"}}
	manager.RegisterExecutor(&issue7721Executor{headers: wantHeaders})
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	res, errStream := manager.ExecuteStream(context.Background(), []string{"issue7721"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if res != nil {
		t.Fatalf("ExecuteStream() result = %+v, want nil (request-fault errors must stay direct errors)", res)
	}
	if errStream == nil {
		t.Fatal("ExecuteStream() error = nil, want non-nil")
	}
	if got := statusCodeFromError(errStream); got != http.StatusBadRequest {
		t.Fatalf("statusCodeFromError = %d, want 400; original error classification lost", got)
	}
	var headerCarrier interface{ Headers() http.Header }
	if !errors.As(errStream, &headerCarrier) || headerCarrier == nil {
		t.Fatalf("ExecuteStream() error %T (%v) does not expose Headers(); upstream headers were dropped", errStream, errStream)
	}
	if got := headerCarrier.Headers().Get("X-Trace-Id"); got != "trace-abc-123" {
		t.Fatalf("Headers().Get(X-Trace-Id) = %q, want %q; upstream headers were dropped", got, "trace-abc-123")
	}
	// The returned error must not be converted into a *streamBootstrapError so it
	// does not get downgraded to a streamErrorResult at the boundary.
	var bootstrap *streamBootstrapError
	if errors.As(errStream, &bootstrap) && bootstrap != nil {
		t.Fatalf("ExecuteStream() error must not be a *streamBootstrapError (would become error-stream); got %T", bootstrap)
	}
}

// TestNewStreamBootstrapErrorIdempotent verifies that wrapping an error that is
// already a *streamBootstrapError does not produce a nested streamBootstrapError,
// mirroring the idempotent markUpstreamExecutionAttempt behavior.
func TestNewStreamBootstrapErrorIdempotent(t *testing.T) {
	headers := http.Header{"X-Trace-Id": []string{"trace-1"}}
	base := &Error{HTTPStatus: http.StatusInternalServerError, Message: "upstream boom", Retryable: true}
	once := newStreamBootstrapError(base, headers)
	twice := newStreamBootstrapError(once, headers)

	var outer *streamBootstrapError
	if !errors.As(twice, &outer) || outer == nil {
		t.Fatalf("newStreamBootstrapError result %T is not a *streamBootstrapError", twice)
	}
	if inner, ok := outer.cause.(*streamBootstrapError); ok && inner != nil {
		t.Fatalf("streamBootstrapError is double-wrapped: outer %p inner %p", outer, inner)
	}
	if got := outer.headers.Get("X-Trace-Id"); got != "trace-1" {
		t.Fatalf("headers.Get(X-Trace-Id) = %q, want %q", got, "trace-1")
	}
}

func TestNewStreamBootstrapErrorPreservesAttemptID(t *testing.T) {
	const attemptID = uint64(42)
	provider := "issue7953"
	base := &Error{HTTPStatus: http.StatusInternalServerError, Message: "upstream boom", Retryable: true}

	tests := []struct {
		name  string
		cause error
	}{
		{name: "new bootstrap", cause: base},
		{name: "existing bootstrap", cause: &streamBootstrapError{cause: base, attemptID: attemptID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			marked := markUpstreamExecutionAttemptWithProviderAndID(tt.cause, provider, attemptID)
			wrapped := newStreamBootstrapError(marked, http.Header{"X-Trace-Id": []string{"trace-2"}})

			var outer *upstreamExecutionAttemptError
			if !errors.As(wrapped, &outer) || outer == nil {
				t.Fatalf("newStreamBootstrapError result %T is not marked as an upstream attempt", wrapped)
			}
			if outer.attemptID != attemptID {
				t.Fatalf("outer attemptID = %d, want %d", outer.attemptID, attemptID)
			}
			if outer.provider != provider {
				t.Fatalf("outer provider = %q, want %q", outer.provider, provider)
			}

			var bootstrap *streamBootstrapError
			if !errors.As(wrapped, &bootstrap) || bootstrap == nil {
				t.Fatalf("newStreamBootstrapError result %T is not a *streamBootstrapError", wrapped)
			}
			if bootstrap.attemptID != attemptID {
				t.Fatalf("bootstrap attemptID = %d, want %d", bootstrap.attemptID, attemptID)
			}
		})
	}
}
