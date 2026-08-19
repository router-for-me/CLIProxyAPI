package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type mockCustomErrorExecutor struct {
	identifier string
	executeFn  func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	countFn    func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
}

func (e *mockCustomErrorExecutor) Identifier() string {
	if e.identifier != "" {
		return e.identifier
	}
	return "mock"
}

func (e *mockCustomErrorExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.executeFn != nil {
		return e.executeFn(ctx, auth, req, opts)
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *mockCustomErrorExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *mockCustomErrorExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *mockCustomErrorExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.countFn != nil {
		return e.countFn(ctx, auth, req, opts)
	}
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (e *mockCustomErrorExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

type customStatusError struct {
	code       int
	msg        string
	retryAfter *time.Duration
}

func (e customStatusError) StatusCode() int {
	return e.code
}

func (e customStatusError) Error() string {
	return e.msg
}

func (e customStatusError) RetryAfter() *time.Duration {
	return e.retryAfter
}

func TestRequestScopedErrors_ActionStop(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)

	auth1 := &Auth{
		ID:         "auth-claude-1",
		Provider:   "claude",
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "10"},
		Metadata: map[string]any{
			"request_scoped_errors": []internalconfig.RequestScopedErrorRule{
				{
					Status: 400,
					Match: []string{
						"maximum_context_length",
						"context_length_exceeded",
					},
					MatchRegexr: []string{
						"maximum_context_length$",
						"^context_length_exceeded",
					},
					Action: "stop",
				},
			},
		},
	}
	auth2 := &Auth{
		ID:       "auth-claude-2",
		Provider: "claude",
		Status:   StatusActive,
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "claude-3"}})
	reg.RegisterClient(auth2.ID, "claude", []*registry.ModelInfo{{ID: "claude-3"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	if _, err := m.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := m.Register(context.Background(), auth2); err != nil {
		t.Fatalf("register auth2: %v", err)
	}

	execCount := 0
	exec := &mockCustomErrorExecutor{
		identifier: "claude",
		executeFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			execCount++
			return cliproxyexecutor.Response{}, customStatusError{
				code: 400,
				msg:  `{"error": {"message": "maximum_context_length exceeded"}}`,
			}
		},
	}
	m.RegisterExecutor(exec)

	resp, errExec := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-3"}, cliproxyexecutor.Options{})
	if errExec == nil {
		t.Fatalf("expected error, got resp: %+v", resp)
	}
	// Action: stop should return immediately on the first credential and not rotate to auth2.
	if execCount != 1 {
		t.Fatalf("execCount = %d, want 1 (should stop immediately)", execCount)
	}

	// Verify auth1 is NOT in cooldown
	a1, ok1 := m.GetByID("auth-claude-1")
	if !ok1 || a1.Unavailable || !a1.NextRetryAfter.IsZero() {
		t.Fatalf("expected auth1 not to be in cooldown, got unavailable=%v, nextRetry=%v", a1.Unavailable, a1.NextRetryAfter)
	}
}

func TestRequestScopedErrors_ActionStopAndCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)

	auth1 := &Auth{
		ID:         "auth-claude-stop-cool",
		Provider:   "claude",
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "10"},
		Metadata: map[string]any{
			"request_scoped_errors": []internalconfig.RequestScopedErrorRule{
				{
					Status: 400,
					Match: []string{
						"context_window_exceeded",
					},
					Action: "stop-and-cooldown",
				},
			},
		},
	}
	auth2 := &Auth{
		ID:       "auth-claude-second",
		Provider: "claude",
		Status:   StatusActive,
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "claude-3"}})
	reg.RegisterClient(auth2.ID, "claude", []*registry.ModelInfo{{ID: "claude-3"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	if _, err := m.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := m.Register(context.Background(), auth2); err != nil {
		t.Fatalf("register auth2: %v", err)
	}

	execCount := 0
	exec := &mockCustomErrorExecutor{
		identifier: "claude",
		executeFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			execCount++
			return cliproxyexecutor.Response{}, customStatusError{
				code: 400,
				msg:  `{"error": {"message": "context_window_exceeded"}}`,
			}
		},
	}
	m.RegisterExecutor(exec)

	_, errExec := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-3"}, cliproxyexecutor.Options{})
	if errExec == nil {
		t.Fatal("expected error, got nil")
	}
	// Action: stop-and-cooldown should return immediately on the first credential and not rotate to auth2.
	if execCount != 1 {
		t.Fatalf("execCount = %d, want 1 (should stop immediately)", execCount)
	}

	// Verify auth1 IS in cooldown
	a1, ok1 := m.GetByID("auth-claude-stop-cool")
	if !ok1 || !a1.Unavailable || a1.NextRetryAfter.IsZero() {
		t.Fatalf("expected auth1 to be in cooldown, got unavailable=%v, nextRetry=%v", a1.Unavailable, a1.NextRetryAfter)
	}
}

func TestRequestScopedErrors_ActionContinue(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)

	auth1 := &Auth{
		ID:         "auth-claude-continue-1",
		Provider:   "claude",
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "10"},
		Metadata: map[string]any{
			"request_scoped_errors": []internalconfig.RequestScopedErrorRule{
				{
					Status: 400,
					Match: []string{
						"try_another_key",
					},
					Action: "continue",
				},
			},
		},
	}
	auth2 := &Auth{
		ID:       "auth-claude-continue-2",
		Provider: "claude",
		Status:   StatusActive,
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "claude-3"}})
	reg.RegisterClient(auth2.ID, "claude", []*registry.ModelInfo{{ID: "claude-3"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	if _, err := m.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := m.Register(context.Background(), auth2); err != nil {
		t.Fatalf("register auth2: %v", err)
	}

	execCount := 0
	exec := &mockCustomErrorExecutor{
		identifier: "claude",
		executeFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			execCount++
			if auth.ID == "auth-claude-continue-1" {
				return cliproxyexecutor.Response{}, customStatusError{
					code: 400,
					msg:  `{"error": {"message": "try_another_key"}}`,
				}
			}
			return cliproxyexecutor.Response{Payload: []byte(`{"result":"success"}`)}, nil
		},
	}
	m.RegisterExecutor(exec)

	resp, errExec := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-3"}, cliproxyexecutor.Options{})
	if errExec != nil {
		t.Fatalf("unexpected error: %v", errExec)
	}
	if string(resp.Payload) != `{"result":"success"}` {
		t.Fatalf("unexpected response: %s", string(resp.Payload))
	}
	// Action: continue should continue to auth2 and succeed.
	if execCount != 2 {
		t.Fatalf("execCount = %d, want 2", execCount)
	}

	// Verify auth1 is NOT in cooldown
	a1, ok1 := m.GetByID("auth-claude-continue-1")
	if !ok1 || a1.Unavailable || !a1.NextRetryAfter.IsZero() {
		t.Fatalf("expected auth1 not to be in cooldown, got unavailable=%v, nextRetry=%v", a1.Unavailable, a1.NextRetryAfter)
	}
}

func TestRequestScopedErrors_ActionContinueAndCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)

	auth1 := &Auth{
		ID:         "auth-claude-continue-cool-1",
		Provider:   "claude",
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "10"},
		Metadata: map[string]any{
			"request_scoped_errors": []internalconfig.RequestScopedErrorRule{
				{
					Status: 400,
					Match: []string{
						"balance_insufficient",
					},
					Action: "continue-and-cooldown",
				},
			},
		},
	}
	auth2 := &Auth{
		ID:       "auth-claude-continue-cool-2",
		Provider: "claude",
		Status:   StatusActive,
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "claude-3"}})
	reg.RegisterClient(auth2.ID, "claude", []*registry.ModelInfo{{ID: "claude-3"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	if _, err := m.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := m.Register(context.Background(), auth2); err != nil {
		t.Fatalf("register auth2: %v", err)
	}

	execCount := 0
	exec := &mockCustomErrorExecutor{
		identifier: "claude",
		executeFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			execCount++
			if auth.ID == "auth-claude-continue-cool-1" {
				return cliproxyexecutor.Response{}, customStatusError{
					code: 400,
					msg:  `{"error": {"message": "balance_insufficient"}}`,
				}
			}
			return cliproxyexecutor.Response{Payload: []byte(`{"result":"success"}`)}, nil
		},
	}
	m.RegisterExecutor(exec)

	resp, errExec := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-3"}, cliproxyexecutor.Options{})
	if errExec != nil {
		t.Fatalf("unexpected error: %v", errExec)
	}
	if string(resp.Payload) != `{"result":"success"}` {
		t.Fatalf("unexpected response: %s", string(resp.Payload))
	}
	// Action: continue-and-cooldown should continue to auth2 and succeed.
	if execCount != 2 {
		t.Fatalf("execCount = %d, want 2", execCount)
	}

	// Verify auth1 IS in cooldown
	a1, ok1 := m.GetByID("auth-claude-continue-cool-1")
	if !ok1 || !a1.Unavailable || a1.NextRetryAfter.IsZero() {
		t.Fatalf("expected auth1 to be in cooldown, got unavailable=%v, nextRetry=%v", a1.Unavailable, a1.NextRetryAfter)
	}
}

func TestRequestScopedErrors_MatchRegexr(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)

	auth1 := &Auth{
		ID:         "auth-regex-1",
		Provider:   "claude",
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "10"},
		Metadata: map[string]any{
			"request_scoped_errors": []internalconfig.RequestScopedErrorRule{
				{
					Status: 400,
					MatchRegexr: []string{
						`context_length_exceeded:\s*\d+`,
					},
					Action: "stop",
				},
			},
		},
	}
	auth2 := &Auth{
		ID:       "auth-regex-2",
		Provider: "claude",
		Status:   StatusActive,
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "claude-3"}})
	reg.RegisterClient(auth2.ID, "claude", []*registry.ModelInfo{{ID: "claude-3"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	if _, err := m.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := m.Register(context.Background(), auth2); err != nil {
		t.Fatalf("register auth2: %v", err)
	}

	execCount := 0
	exec := &mockCustomErrorExecutor{
		identifier: "claude",
		executeFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			execCount++
			return cliproxyexecutor.Response{}, customStatusError{
				code: 400,
				msg:  `{"error": {"message": "context_length_exceeded: 128000"}}`,
			}
		},
	}
	m.RegisterExecutor(exec)

	_, errExec := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-3"}, cliproxyexecutor.Options{})
	if errExec == nil {
		t.Fatal("expected error, got nil")
	}
	if execCount != 1 {
		t.Fatalf("execCount = %d, want 1", execCount)
	}
	a1, _ := m.GetByID("auth-regex-1")
	if a1.Unavailable {
		t.Fatal("expected auth1 not to be in cooldown")
	}
}

type customStreamMockExecutor struct {
	mockCustomErrorExecutor
	identifier string
	streamFn   func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}

func (e *customStreamMockExecutor) Identifier() string {
	if e.identifier != "" {
		return e.identifier
	}
	return "claude"
}

func (e *customStreamMockExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.streamFn != nil {
		return e.streamFn(ctx, auth, req, opts)
	}
	return nil, errors.New("not implemented")
}

func TestRequestScopedErrors_Stream_ActionStop(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)

	auth1 := &Auth{
		ID:         "auth-stream-1",
		Provider:   "claude",
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "10"},
		Metadata: map[string]any{
			"request_scoped_errors": []internalconfig.RequestScopedErrorRule{
				{
					Status: 400,
					Match:  []string{"stream_context_overflow"},
					Action: "stop",
				},
			},
		},
	}
	auth2 := &Auth{
		ID:       "auth-stream-2",
		Provider: "claude",
		Status:   StatusActive,
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "claude-3"}})
	reg.RegisterClient(auth2.ID, "claude", []*registry.ModelInfo{{ID: "claude-3"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	if _, err := m.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := m.Register(context.Background(), auth2); err != nil {
		t.Fatalf("register auth2: %v", err)
	}

	execCount := 0
	streamExecutor := &customStreamMockExecutor{
		identifier: "claude",
		streamFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			execCount++
			return nil, customStatusError{code: 400, msg: "stream_context_overflow"}
		},
	}
	m.RegisterExecutor(streamExecutor)

	_, errStream := m.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-3"}, cliproxyexecutor.Options{})
	if errStream == nil {
		t.Fatal("expected error, got nil")
	}
	if execCount != 1 {
		t.Fatalf("execCount = %d, want 1 (should stop immediately)", execCount)
	}

	a1, _ := m.GetByID("auth-stream-1")
	if a1.Unavailable || !a1.NextRetryAfter.IsZero() {
		t.Fatal("expected auth1 not to be in cooldown")
	}
}

func TestRequestScopedErrors_StreamBootstrap_StopAndCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)

	auth1 := &Auth{
		ID:         "auth-stream-boot-1",
		Provider:   "claude",
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "10"},
		Metadata: map[string]any{
			"request_scoped_errors": []internalconfig.RequestScopedErrorRule{
				{
					Status: 400,
					Match:  []string{"bootstrap_chunk_error"},
					Action: "stop-and-cooldown",
				},
			},
		},
	}
	auth2 := &Auth{
		ID:       "auth-stream-boot-2",
		Provider: "claude",
		Status:   StatusActive,
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "claude-3"}})
	reg.RegisterClient(auth2.ID, "claude", []*registry.ModelInfo{{ID: "claude-3"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	if _, err := m.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := m.Register(context.Background(), auth2); err != nil {
		t.Fatalf("register auth2: %v", err)
	}

	execCount := 0
	streamExecutor := &customStreamMockExecutor{
		identifier: "claude",
		streamFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			execCount++
			ch := make(chan cliproxyexecutor.StreamChunk, 1)
			ch <- cliproxyexecutor.StreamChunk{Err: customStatusError{code: 400, msg: "bootstrap_chunk_error"}}
			close(ch)
			return &cliproxyexecutor.StreamResult{
				Headers: http.Header{"Content-Type": []string{"text/event-stream"}},
				Chunks:  ch,
			}, nil
		},
	}
	m.RegisterExecutor(streamExecutor)

	_, errStream := m.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-3"}, cliproxyexecutor.Options{})
	if errStream == nil {
		t.Fatal("expected error, got nil")
	}
	if execCount != 1 {
		t.Fatalf("execCount = %d, want 1", execCount)
	}

	a1, _ := m.GetByID("auth-stream-boot-1")
	if !a1.Unavailable || a1.NextRetryAfter.IsZero() {
		t.Fatal("expected auth1 to be in cooldown from bootstrap chunk error")
	}
}

func TestRequestScopedErrors_Stop_StopsOuterRetryOn429(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	retryDelay := 100 * time.Millisecond
	m.SetRetryConfig(3, 5*time.Second, 5)

	auth1 := &Auth{
		ID:         "auth-retry-stop-1",
		Provider:   "claude",
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "10"},
		Metadata: map[string]any{
			"request_scoped_errors": []internalconfig.RequestScopedErrorRule{
				{
					Status: 429,
					Match:  []string{"rate_limit_stop"},
					Action: "stop",
				},
			},
		},
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "claude-3"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
	})

	if _, err := m.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}

	execCount := 0
	exec := &mockCustomErrorExecutor{
		identifier: "claude",
		executeFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			execCount++
			return cliproxyexecutor.Response{}, customStatusError{
				code:       429,
				msg:        `{"error": {"message": "rate_limit_stop"}}`,
				retryAfter: &retryDelay,
			}
		},
	}
	m.RegisterExecutor(exec)

	_, errExec := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-3"}, cliproxyexecutor.Options{})
	if errExec == nil {
		t.Fatal("expected error, got nil")
	}
	// Even though 429 with retry-after would normally retry 3 times, action: stop must stop immediately.
	if execCount != 1 {
		t.Fatalf("execCount = %d, want 1 (should stop outer retries immediately)", execCount)
	}
}

func TestRequestScopedErrors_Cooldown_OverridesDisableCooling(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)

	auth1 := &Auth{
		ID:         "auth-disable-cooling-override",
		Provider:   "claude",
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "10"},
		Metadata: map[string]any{
			"disable_cooling": true,
			"request_scoped_errors": []internalconfig.RequestScopedErrorRule{
				{
					Status: 400,
					Match:  []string{"cooldown_anyway"},
					Action: "stop-and-cooldown",
				},
			},
		},
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "claude-3"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
	})

	if _, err := m.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}

	exec := &mockCustomErrorExecutor{
		identifier: "claude",
		executeFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{}, customStatusError{
				code: 400,
				msg:  `{"error": {"message": "cooldown_anyway"}}`,
			}
		},
	}
	m.RegisterExecutor(exec)

	_, errExec := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-3"}, cliproxyexecutor.Options{})
	if errExec == nil {
		t.Fatal("expected error, got nil")
	}

	a1, _ := m.GetByID("auth-disable-cooling-override")
	if !a1.Unavailable || a1.NextRetryAfter.IsZero() {
		t.Fatal("expected auth1 to be in cooldown despite disable_cooling=true")
	}
}

func TestRequestScopedErrors_CountTokens_StopAndCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)

	auth1 := &Auth{
		ID:         "auth-count-1",
		Provider:   "claude",
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "10"},
		Metadata: map[string]any{
			"request_scoped_errors": []internalconfig.RequestScopedErrorRule{
				{
					Status: 404,
					Match:  []string{"count_endpoint_cooldown"},
					Action: "stop-and-cooldown",
				},
			},
		},
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "claude-3"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
	})

	if _, err := m.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}

	exec := &mockCustomErrorExecutor{
		identifier: "claude",
		countFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{}, customStatusError{
				code: 404,
				msg:  "count_endpoint_cooldown",
			}
		},
	}
	m.RegisterExecutor(exec)

	_, errCount := m.ExecuteCount(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-3"}, cliproxyexecutor.Options{})
	if errCount == nil {
		t.Fatal("expected error, got nil")
	}

	a1, _ := m.GetByID("auth-count-1")
	if !a1.Unavailable || a1.NextRetryAfter.IsZero() {
		t.Fatal("expected auth1 to be in cooldown from CountTokens")
	}
}

func TestRequestScopedErrors_ResolvedFromManagerConfig(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	cfg := &internalconfig.Config{
		ClaudeKey: []internalconfig.ClaudeKey{
			{
				APIKey: "sk-ant-test",
				RequestScopedErrors: []internalconfig.RequestScopedErrorRule{
					{
						Status: 400,
						Match:  []string{"from_config_rule"},
						Action: "stop",
					},
				},
			},
		},
		OpenAICompatibility: []internalconfig.OpenAICompatibility{
			{
				Name:    "my-compat",
				BaseURL: "https://compat.api",
				RequestScopedErrors: []internalconfig.RequestScopedErrorRule{
					{
						Status: 400,
						Match:  []string{"from_compat_config_rule"},
						Action: "stop",
					},
				},
			},
		},
	}
	m := NewManager(nil, nil, nil)
	m.SetConfig(cfg)

	auth1 := &Auth{
		ID:         "auth-config-resolve-1",
		Provider:   "claude",
		Status:     StatusActive,
		Attributes: map[string]string{AttributeConfigIndex: "0", "priority": "10"},
	}
	authCompat := &Auth{
		ID:       "auth-config-resolve-compat",
		Provider: "openai-compatible-my-compat",
		Status:   StatusActive,
		Attributes: map[string]string{
			AttributeConfigIndex: "0",
			"compat_name":        "my-compat",
			"priority":           "10",
		},
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "claude-3"}})
	reg.RegisterClient(authCompat.ID, "openai-compatible-my-compat", []*registry.ModelInfo{{ID: "compat-model"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(authCompat.ID)
	})

	if _, err := m.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := m.Register(context.Background(), authCompat); err != nil {
		t.Fatalf("register authCompat: %v", err)
	}

	execClaude := &mockCustomErrorExecutor{
		identifier: "claude",
		executeFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{}, customStatusError{code: 400, msg: "from_config_rule occurred"}
		},
	}
	execCompat := &mockCustomErrorExecutor{
		identifier: "openai-compatible-my-compat",
		executeFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{}, customStatusError{code: 400, msg: "from_compat_config_rule occurred"}
		},
	}
	m.RegisterExecutor(execClaude)
	m.RegisterExecutor(execCompat)

	_, errExec1 := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-3"}, cliproxyexecutor.Options{})
	if errExec1 == nil {
		t.Fatal("expected error, got nil")
	}
	a1, _ := m.GetByID("auth-config-resolve-1")
	if a1.Unavailable || !a1.NextRetryAfter.IsZero() {
		t.Fatal("expected auth1 not to be in cooldown when resolved from manager config")
	}

	_, errExec2 := m.Execute(context.Background(), []string{"openai-compatible-my-compat"}, cliproxyexecutor.Request{Model: "compat-model"}, cliproxyexecutor.Options{})
	if errExec2 == nil {
		t.Fatal("expected error, got nil")
	}
	aCompat, _ := m.GetByID("auth-config-resolve-compat")
	if aCompat.Unavailable || !aCompat.NextRetryAfter.IsZero() {
		t.Fatal("expected aCompat not to be in cooldown when resolved from manager config")
	}
}

func TestRequestScopedErrors_NonMatching_FallsBackToDefault(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)

	auth1 := &Auth{
		ID:         "auth-nomatch-1",
		Provider:   "claude",
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "10"},
		Metadata: map[string]any{
			"request_scoped_errors": []internalconfig.RequestScopedErrorRule{
				{
					Status: 500,
					Match:  []string{"some_500_error"},
					Action: "stop",
				},
			},
		},
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "claude-3"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
	})

	if _, err := m.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}

	exec := &mockCustomErrorExecutor{
		identifier: "claude",
		executeFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			// Status 400 with standard request-fault message (unmatched by rule)
			return cliproxyexecutor.Response{}, customStatusError{
				code: 400,
				msg:  `{"error": {"message": "Invalid request parameter", "type": "invalid_request_error"}}`,
			}
		},
	}
	m.RegisterExecutor(exec)

	_, errExec := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-3"}, cliproxyexecutor.Options{})
	if errExec == nil {
		t.Fatal("expected error, got nil")
	}

	// Unmatched 400 request fault should still use default request fault handling (no cooldown)
	a1, _ := m.GetByID("auth-nomatch-1")
	if a1.Unavailable || !a1.NextRetryAfter.IsZero() {
		t.Fatal("expected auth1 not to be in cooldown under default fallback")
	}
}

func TestRequestScopedErrors_StreamSubsequentChunkError(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)

	auth1 := &Auth{
		ID:         "auth-stream-subsequent-1",
		Provider:   "claude",
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "10"},
		Metadata: map[string]any{
			"request_scoped_errors": []internalconfig.RequestScopedErrorRule{
				{
					Status: 400,
					Match:  []string{"mid_stream_context_length"},
					Action: "stop-and-cooldown",
				},
			},
		},
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "claude-3"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
	})

	if _, err := m.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}

	streamExecutor := &customStreamMockExecutor{
		identifier: "claude",
		streamFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			ch := make(chan cliproxyexecutor.StreamChunk, 2)
			ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"message_start"}\n\n`)}
			ch <- cliproxyexecutor.StreamChunk{Err: customStatusError{code: 400, msg: "mid_stream_context_length"}}
			close(ch)
			return &cliproxyexecutor.StreamResult{
				Headers: http.Header{"Content-Type": []string{"text/event-stream"}},
				Chunks:  ch,
			}, nil
		},
	}
	m.RegisterExecutor(streamExecutor)

	streamResult, errStream := m.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-3"}, cliproxyexecutor.Options{})
	if errStream != nil {
		t.Fatalf("unexpected stream start error: %v", errStream)
	}

	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			// Chunk error encountered
		}
	}

	// Verify auth1 was put into cooldown via action: stop-and-cooldown applied in wrapStreamResult
	a1, _ := m.GetByID("auth-stream-subsequent-1")
	if !a1.Unavailable || a1.NextRetryAfter.IsZero() {
		t.Fatal("expected auth1 to be in cooldown after mid-stream chunk error")
	}
}

func TestRequestScopedErrors_UnmatchedBootstrapError_PreservesDefault(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)

	auth1 := &Auth{
		ID:         "auth-unmatched-boot-1",
		Provider:   "claude",
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "10"},
		Metadata: map[string]any{
			"request_scoped_errors": []internalconfig.RequestScopedErrorRule{
				{
					Status: 500,
					Match:  []string{"rule_does_not_match"},
					Action: "stop",
				},
			},
		},
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "claude-3"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
	})

	if _, err := m.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}

	streamExecutor := &customStreamMockExecutor{
		identifier: "claude",
		streamFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			ch := make(chan cliproxyexecutor.StreamChunk, 1)
			ch <- cliproxyexecutor.StreamChunk{Err: customStatusError{code: 400, msg: `{"error":{"type":"invalid_request_error","message":"Unmatched bad request"}}`}}
			close(ch)
			return &cliproxyexecutor.StreamResult{
				Headers: http.Header{"Content-Type": []string{"text/event-stream"}},
				Chunks:  ch,
			}, nil
		},
	}
	m.RegisterExecutor(streamExecutor)

	_, errStream := m.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-3"}, cliproxyexecutor.Options{})
	if errStream == nil {
		t.Fatal("expected error, got nil")
	}

	// Default request invalid error skips cooldown
	a1, _ := m.GetByID("auth-unmatched-boot-1")
	if a1.Unavailable || !a1.NextRetryAfter.IsZero() {
		t.Fatal("expected auth1 not to be cooled down under default fallback for 400 bootstrap error")
	}
}

func TestRequestScopedErrors_TransientCooldownDisabled_ForceCooldownStillApplies(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	transientErrorCooldownSeconds.Store(-1)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	m := NewManager(nil, nil, nil)

	auth1 := &Auth{
		ID:         "auth-transient-disabled-1",
		Provider:   "claude",
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "10"},
		Metadata: map[string]any{
			"request_scoped_errors": []internalconfig.RequestScopedErrorRule{
				{
					Status: 500,
					Match:  []string{"cooldown_on_500"},
					Action: "stop-and-cooldown",
				},
			},
		},
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "claude-3"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
	})

	if _, err := m.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}

	exec := &mockCustomErrorExecutor{
		identifier: "claude",
		executeFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{}, customStatusError{code: 500, msg: "cooldown_on_500"}
		},
	}
	m.RegisterExecutor(exec)

	_, errExec := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-3"}, cliproxyexecutor.Options{})
	if errExec == nil {
		t.Fatal("expected error, got nil")
	}

	a1, _ := m.GetByID("auth-transient-disabled-1")
	if !a1.Unavailable || a1.NextRetryAfter.IsZero() {
		t.Fatal("expected auth1 to be in cooldown despite transientErrorCooldownSeconds=-1")
	}
}

func TestRequestScopedErrors_OpenAICompat_BareProviderKeyFallback(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	cfg := &internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{
			{
				Name:    "bare-compat",
				BaseURL: "https://compat.api",
				RequestScopedErrors: []internalconfig.RequestScopedErrorRule{
					{
						Status: 400,
						Match:  []string{"from_bare_compat_rule"},
						Action: "stop",
					},
				},
			},
		},
	}
	m := NewManager(nil, nil, nil)
	m.SetConfig(cfg)

	authCompat := &Auth{
		ID:       "auth-bare-compat",
		Provider: "openai-compatible-bare-compat",
		Status:   StatusActive,
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authCompat.ID, "openai-compatible-bare-compat", []*registry.ModelInfo{{ID: "bare-model"}})
	t.Cleanup(func() {
		reg.UnregisterClient(authCompat.ID)
	})

	if _, err := m.Register(context.Background(), authCompat); err != nil {
		t.Fatalf("register authCompat: %v", err)
	}

	execCompat := &mockCustomErrorExecutor{
		identifier: "openai-compatible-bare-compat",
		executeFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{}, customStatusError{code: 400, msg: "from_bare_compat_rule"}
		},
	}
	m.RegisterExecutor(execCompat)

	_, errExec := m.Execute(context.Background(), []string{"openai-compatible-bare-compat"}, cliproxyexecutor.Request{Model: "bare-model"}, cliproxyexecutor.Options{})
	if errExec == nil {
		t.Fatal("expected error, got nil")
	}
	aCompat, _ := m.GetByID("auth-bare-compat")
	if aCompat.Unavailable || !aCompat.NextRetryAfter.IsZero() {
		t.Fatal("expected aCompat not to be in cooldown from bare provider fallback")
	}
}

type wrappedResponseBodyError struct {
	status int
	msg    string
	body   []byte
}

func (e wrappedResponseBodyError) StatusCode() int {
	return e.status
}

func (e wrappedResponseBodyError) Error() string {
	return e.msg
}

func (e wrappedResponseBodyError) ResponseBody() []byte {
	return e.body
}

func TestRequestScopedErrors_ResponseBodyProvider_MatchesUnderlyingPayload(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)

	auth1 := &Auth{
		ID:         "auth-fast-wrapped-1",
		Provider:   "claude",
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "10"},
		Metadata: map[string]any{
			"request_scoped_errors": []internalconfig.RequestScopedErrorRule{
				{
					Status: 400,
					Match:  []string{"claude_fast_overload"},
					Action: "stop-and-cooldown",
				},
			},
		},
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "claude-3"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
	})

	if _, err := m.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}

	exec := &mockCustomErrorExecutor{
		identifier: "claude",
		executeFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			// Error() returns a generic wrapper text, while ResponseBody() provides the underlying json payload
			return cliproxyexecutor.Response{}, wrappedResponseBodyError{
				status: 400,
				msg:    "claude Fast upstream request failed with status 400",
				body:   []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"claude_fast_overload"}}`),
			}
		},
	}
	m.RegisterExecutor(exec)

	_, errExec := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-3"}, cliproxyexecutor.Options{})
	if errExec == nil {
		t.Fatal("expected error, got nil")
	}

	a1, _ := m.GetByID("auth-fast-wrapped-1")
	if !a1.Unavailable || a1.NextRetryAfter.IsZero() {
		t.Fatal("expected auth1 to be in cooldown when matching ResponseBody()")
	}
}
