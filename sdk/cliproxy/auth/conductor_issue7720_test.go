package auth

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// issue7720RetryAfterError carries a RetryAfter hint for cooldown verification.
type issue7720RetryAfterError struct {
	status     int
	message    string
	retryAfter time.Duration
}

func (e *issue7720RetryAfterError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func (e *issue7720RetryAfterError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.status
}

func (e *issue7720RetryAfterError) RetryAfter() *time.Duration {
	if e == nil {
		return nil
	}
	d := e.retryAfter
	return &d
}

// --- Issue 1: 401-refresh bootstrap retry missing syncMetadataSessionToContext ---
// The outer bootstrapErr != nil block already guards ctx.Err() on the retry, but the
// retry context is missing syncMetadataSessionToContext after newUpstreamAttemptContext.
// This test verifies the cooldown is still preserved when the retry bootstrap is canceled.

// issue7720RefreshBootstrapStreamExecutor fails the first stream with a 401 bootstrap
// chunk, then cancels ctx and returns a stream whose first chunk is a quota error on
// the refresh retry.
type issue7720RefreshBootstrapStreamExecutor struct {
	cancel context.CancelFunc
	err    error
	calls  atomic.Int32
}

func (*issue7720RefreshBootstrapStreamExecutor) Identifier() string { return "issue7720" }

func (*issue7720RefreshBootstrapStreamExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (e *issue7720RefreshBootstrapStreamExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	call := e.calls.Add(1)
	if call > 1 {
		// Refresh retry: cancel context then return a stream with quota error chunk
		e.cancel()
		chunks := make(chan cliproxyexecutor.StreamChunk, 1)
		chunks <- cliproxyexecutor.StreamChunk{Err: e.err}
		close(chunks)
		return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
	}
	// First call: return 401 in bootstrap chunk to trigger refresh
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: http.StatusUnauthorized, Message: "token invalidated"}}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*issue7720RefreshBootstrapStreamExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*issue7720RefreshBootstrapStreamExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (*issue7720RefreshBootstrapStreamExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestIssue7720_RefreshBootstrapRetryPreservesCooldownOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	model := "issue7720-refresh-model-" + uuid.NewString()
	auth := &Auth{
		ID:       "issue7720-refresh-auth-" + uuid.NewString(),
		Provider: "issue7720",
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "stale", "refresh_token": "refresh"},
	}
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	quotaErr := &issue7720RetryAfterError{status: http.StatusTooManyRequests, message: "quota exceeded", retryAfter: 10 * time.Minute}
	manager.RegisterExecutor(&issue7720RefreshBootstrapStreamExecutor{cancel: cancel, err: quotaErr})
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	_, errStream := manager.ExecuteStream(ctx, []string{"issue7720"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if !errors.Is(errStream, context.Canceled) {
		t.Fatalf("ExecuteStream() error = %v, want context.Canceled", errStream)
	}
	current, ok := manager.GetByID(auth.ID)
	if !ok || current == nil {
		t.Fatalf("GetByID(%q) did not return auth", auth.ID)
	}
	// The quota error with RetryAfter must be preserved via recordCanceledUpstreamCooldown
	if !current.Unavailable || !current.NextRetryAfter.After(time.Now().Add(5*time.Minute)) {
		t.Fatalf("auth was not cooled: unavailable=%t next=%v", current.Unavailable, current.NextRetryAfter)
	}
}

// --- Issue 2: wrapStreamResult emit records plain cancellation and drops WithoutCancel ---

// issue7720MidStreamCancelExecutor returns a stream that emits a context.Canceled error
// after the first payload chunk when ctx is canceled.
type issue7720MidStreamCancelExecutor struct {
	cancel context.CancelFunc
}

func (*issue7720MidStreamCancelExecutor) Identifier() string { return "issue7720" }

func (*issue7720MidStreamCancelExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (e *issue7720MidStreamCancelExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	chunks := make(chan cliproxyexecutor.StreamChunk, 3)
	// Send a valid payload first so readStreamBootstrap succeeds and wrapStreamResult is entered
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.created"}`)}
	// Cancel context after bootstrap succeeds, then emit a plain cancellation error mid-stream
	go func() {
		e.cancel()
		chunks <- cliproxyexecutor.StreamChunk{Err: context.Canceled}
		close(chunks)
	}()
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*issue7720MidStreamCancelExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*issue7720MidStreamCancelExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (*issue7720MidStreamCancelExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestIssue7720_MidStreamPlainCancelDoesNotRecordFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	model := "issue7720-midstream-model-" + uuid.NewString()
	auth := &Auth{ID: "issue7720-midstream-auth-" + uuid.NewString(), Provider: "issue7720", Status: StatusActive}
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(&issue7720MidStreamCancelExecutor{cancel: cancel})
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	// Drain the stream to trigger mid-stream processing
	streamResult, errStream := manager.ExecuteStream(ctx, []string{"issue7720"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	for range streamResult.Chunks {
		// drain
	}
	current, ok := manager.GetByID(auth.ID)
	if !ok || current == nil {
		t.Fatalf("GetByID(%q) did not return auth", auth.ID)
	}
	// Plain cancellation should NOT record a failure against the credential
	if current.Failed != 0 {
		t.Fatalf("auth recorded failure on plain cancellation: failed=%d", current.Failed)
	}
	if current.Unavailable {
		t.Fatalf("auth was marked unavailable on plain cancellation")
	}
}

// --- Issue 3: wrapStreamResult records Success on canceled non-Claude streams ---

// issue7720CanceledSuccessExecutor returns a stream that closes cleanly after
// ctx is canceled, which triggers the Success:true recording path.
type issue7720CanceledSuccessExecutor struct {
	cancel context.CancelFunc
}

func (*issue7720CanceledSuccessExecutor) Identifier() string { return "issue7720" }

func (*issue7720CanceledSuccessExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (e *issue7720CanceledSuccessExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	chunks := make(chan cliproxyexecutor.StreamChunk, 2)
	// Send a valid payload first so readStreamBootstrap succeeds and wrapStreamResult is entered
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.created"}`)}
	// Cancel context after bootstrap succeeds, then close channel cleanly mid-stream
	go func() {
		e.cancel()
		close(chunks)
	}()
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*issue7720CanceledSuccessExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*issue7720CanceledSuccessExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (*issue7720CanceledSuccessExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestIssue7720_CanceledStreamDoesNotRecordSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	model := "issue7720-canceled-model-" + uuid.NewString()
	auth := &Auth{ID: "issue7720-canceled-auth-" + uuid.NewString(), Provider: "issue7720", Status: StatusActive}
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(&issue7720CanceledSuccessExecutor{cancel: cancel})
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	// Drain the stream
	streamResult, errStream := manager.ExecuteStream(ctx, []string{"issue7720"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	for range streamResult.Chunks {
		// drain
	}
	current, ok := manager.GetByID(auth.ID)
	if !ok || current == nil {
		t.Fatalf("GetByID(%q) did not return auth", auth.ID)
	}
	// Canceled stream should NOT record Success:true
	if current.Success != 0 {
		t.Fatalf("auth recorded success on canceled stream: success=%d", current.Success)
	}
}

// --- Issue 4: Non-streaming paths never call recordCanceledUpstreamCooldown ---

// issue7720NonStreamExecutor cancels ctx and returns a quota error for
// non-streaming Execute and CountTokens paths.
type issue7720NonStreamExecutor struct {
	cancel context.CancelFunc
	err    error
}

func (*issue7720NonStreamExecutor) Identifier() string { return "issue7720" }

func (e *issue7720NonStreamExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.cancel()
	return cliproxyexecutor.Response{}, e.err
}

func (*issue7720NonStreamExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (*issue7720NonStreamExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *issue7720NonStreamExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.cancel()
	return cliproxyexecutor.Response{}, e.err
}

func (*issue7720NonStreamExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestIssue7720_ExecutePreservesCooldownOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	model := "issue7720-exec-model-" + uuid.NewString()
	auth := &Auth{ID: "issue7720-exec-auth-" + uuid.NewString(), Provider: "issue7720", Status: StatusActive}
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	quotaErr := &issue7720RetryAfterError{status: http.StatusTooManyRequests, message: "quota exceeded", retryAfter: 10 * time.Minute}
	manager.RegisterExecutor(&issue7720NonStreamExecutor{cancel: cancel, err: quotaErr})
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	_, errExecute := manager.Execute(ctx, []string{"issue7720"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if !errors.Is(errExecute, context.Canceled) {
		t.Fatalf("Execute() error = %v, want context.Canceled", errExecute)
	}
	current, ok := manager.GetByID(auth.ID)
	if !ok || current == nil {
		t.Fatalf("GetByID(%q) did not return auth", auth.ID)
	}
	// The RetryAfter cooldown must be preserved via recordCanceledUpstreamCooldown
	if !current.Unavailable || !current.NextRetryAfter.After(time.Now().Add(5*time.Minute)) {
		t.Fatalf("auth was not cooled: unavailable=%t next=%v", current.Unavailable, current.NextRetryAfter)
	}
}

func TestIssue7720_CountTokensPreservesCooldownOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	model := "issue7720-count-model-" + uuid.NewString()
	auth := &Auth{ID: "issue7720-count-auth-" + uuid.NewString(), Provider: "issue7720", Status: StatusActive}
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	quotaErr := &issue7720RetryAfterError{status: http.StatusTooManyRequests, message: "quota exceeded", retryAfter: 10 * time.Minute}
	manager.RegisterExecutor(&issue7720NonStreamExecutor{cancel: cancel, err: quotaErr})
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	_, errCount := manager.ExecuteCount(ctx, []string{"issue7720"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if !errors.Is(errCount, context.Canceled) {
		t.Fatalf("ExecuteCount() error = %v, want context.Canceled", errCount)
	}
	current, ok := manager.GetByID(auth.ID)
	if !ok || current == nil {
		t.Fatalf("GetByID(%q) did not return auth", auth.ID)
	}
	// The RetryAfter cooldown must be preserved via recordCanceledUpstreamCooldown
	if !current.Unavailable || !current.NextRetryAfter.After(time.Now().Add(5*time.Minute)) {
		t.Fatalf("auth was not cooled: unavailable=%t next=%v", current.Unavailable, current.NextRetryAfter)
	}
}
