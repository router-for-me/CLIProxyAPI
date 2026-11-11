package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type issue6503QuotaError struct {
	retryAfter time.Duration
}

func (issue6503QuotaError) Error() string                { return "usage_limit_reached" }
func (issue6503QuotaError) StatusCode() int              { return http.StatusTooManyRequests }
func (issue6503QuotaError) IsCredentialScoped() bool     { return true }
func (e issue6503QuotaError) RetryAfter() *time.Duration { return &e.retryAfter }

// issue6503StreamExecutor cancels the request context before returning, which mirrors
// the Codex websocket executor tearing down the downstream connection on an error frame.
type issue6503StreamExecutor struct {
	cancel context.CancelFunc
	err    error
}

func (*issue6503StreamExecutor) Identifier() string { return "issue6503" }

func (*issue6503StreamExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (e *issue6503StreamExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.cancel()
	return nil, e.err
}

func (*issue6503StreamExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*issue6503StreamExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (*issue6503StreamExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func runIssue6503CanceledStream(t *testing.T, executorErr error) (*Manager, string, string, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	model := "issue6503-model-" + uuid.NewString()
	auth := &Auth{ID: "issue6503-auth-" + uuid.NewString(), Provider: "issue6503", Status: StatusActive}
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(&issue6503StreamExecutor{cancel: cancel, err: executorErr})
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	_, errStream := manager.ExecuteStream(ctx, []string{"issue6503"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	return manager, auth.ID, model, errStream
}

func TestManagerExecuteStreamRecordsQuotaCooldownWhenContextCanceled(t *testing.T) {
	manager, authID, _, errStream := runIssue6503CanceledStream(t, issue6503QuotaError{retryAfter: 10 * time.Minute})
	if !errors.Is(errStream, context.Canceled) {
		t.Fatalf("ExecuteStream() error = %v, want context.Canceled", errStream)
	}
	auth, ok := manager.GetByID(authID)
	if !ok || auth == nil {
		t.Fatalf("GetByID(%q) did not return auth", authID)
	}
	if !auth.Unavailable || !auth.NextRetryAfter.After(time.Now().Add(5*time.Minute)) {
		t.Fatalf("auth was not cooled: unavailable=%t next=%v", auth.Unavailable, auth.NextRetryAfter)
	}
}

func TestManagerExecuteStreamPlainCancellationDoesNotCoolCredential(t *testing.T) {
	manager, authID, model, errStream := runIssue6503CanceledStream(t, context.Canceled)
	if !errors.Is(errStream, context.Canceled) {
		t.Fatalf("ExecuteStream() error = %v, want context.Canceled", errStream)
	}
	auth, ok := manager.GetByID(authID)
	if !ok || auth == nil {
		t.Fatalf("GetByID(%q) did not return auth", authID)
	}
	if auth.Unavailable || !auth.NextRetryAfter.IsZero() {
		t.Fatalf("auth was cooled: unavailable=%t next=%v", auth.Unavailable, auth.NextRetryAfter)
	}
	if state := auth.ModelStates[model]; state != nil && (state.Unavailable || !state.NextRetryAfter.IsZero() || state.Quota.Exceeded) {
		t.Fatalf("model was cooled: %#v", state)
	}
}

// issue6503BootstrapExecutor returns a stream whose first chunk is an upstream quota error
// while the request context is already canceled, which is what the unbuffered Codex websocket
// path produces after the downstream connection was closed for the same upstream error.
type issue6503BootstrapExecutor struct {
	issue6503StreamExecutor
}

func (e *issue6503BootstrapExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.cancel()
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Err: e.err}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func TestManagerExecuteStreamRecordsQuotaCooldownFromCanceledBootstrapChunk(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	model := "issue6503-bootstrap-model-" + uuid.NewString()
	auth := &Auth{ID: "issue6503-bootstrap-auth-" + uuid.NewString(), Provider: "issue6503", Status: StatusActive}
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(&issue6503BootstrapExecutor{issue6503StreamExecutor{cancel: cancel, err: issue6503QuotaError{retryAfter: 10 * time.Minute}}})
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	_, errStream := manager.ExecuteStream(ctx, []string{"issue6503"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if !errors.Is(errStream, context.Canceled) {
		t.Fatalf("ExecuteStream() error = %v, want context.Canceled", errStream)
	}
	current, ok := manager.GetByID(auth.ID)
	if !ok || current == nil {
		t.Fatalf("GetByID(%q) did not return auth", auth.ID)
	}
	if !current.Unavailable || !current.NextRetryAfter.After(time.Now().Add(5*time.Minute)) {
		t.Fatalf("auth was not cooled: unavailable=%t next=%v", current.Unavailable, current.NextRetryAfter)
	}
}

// issue6503RefreshExecutor fails the first stream with an unauthorized bootstrap chunk and, after
// the credential is refreshed, cancels the request context while returning a quota error.
type issue6503RefreshExecutor struct {
	issue6503StreamExecutor
	calls int
}

func (e *issue6503RefreshExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.calls++
	if e.calls > 1 {
		e.cancel()
		return nil, e.err
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: http.StatusUnauthorized, Message: "token invalidated"}}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func TestManagerExecuteStreamRecordsQuotaCooldownFromCanceledRefreshRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	model := "issue6503-refresh-model-" + uuid.NewString()
	auth := &Auth{
		ID:       "issue6503-refresh-auth-" + uuid.NewString(),
		Provider: "issue6503",
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "stale", "refresh_token": "refresh"},
	}
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(&issue6503RefreshExecutor{issue6503StreamExecutor: issue6503StreamExecutor{cancel: cancel, err: issue6503QuotaError{retryAfter: 10 * time.Minute}}})
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	_, errStream := manager.ExecuteStream(ctx, []string{"issue6503"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if !errors.Is(errStream, context.Canceled) {
		t.Fatalf("ExecuteStream() error = %v, want context.Canceled", errStream)
	}
	current, ok := manager.GetByID(auth.ID)
	if !ok || current == nil {
		t.Fatalf("GetByID(%q) did not return auth", auth.ID)
	}
	if !current.Unavailable || !current.NextRetryAfter.After(time.Now().Add(5*time.Minute)) {
		t.Fatalf("auth was not cooled: unavailable=%t next=%v", current.Unavailable, current.NextRetryAfter)
	}
}
