package auth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

const requestScopedNotFoundMessage = "Item with id 'rs_0b5f3eb6f51f175c0169ca74e4a85881998539920821603a74' not found. Items are not persisted when `store` is set to false. Try again with `store` set to true, or remove this item from your input."

func TestManager_ShouldRetryAfterError_RespectsAuthRequestRetryOverride(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(3, 30*time.Second, 0)

	model := "test-model"
	next := time.Now().Add(5 * time.Second)

	auth := &Auth{
		ID:       "auth-1",
		Provider: "claude",
		Metadata: map[string]any{
			"request_retry": float64(0),
		},
		ModelStates: map[string]*ModelState{
			model: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: next,
			},
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	_, _, maxWait := m.retrySettings()
	wait, shouldRetry := m.shouldRetryAfterError(&Error{HTTPStatus: 500, Message: "boom"}, 0, []string{"claude"}, model, maxWait)
	if shouldRetry {
		t.Fatalf("expected shouldRetry=false for request_retry=0, got true (wait=%v)", wait)
	}

	auth.Metadata["request_retry"] = float64(1)
	if _, errUpdate := m.Update(context.Background(), auth); errUpdate != nil {
		t.Fatalf("update auth: %v", errUpdate)
	}

	wait, shouldRetry = m.shouldRetryAfterError(&Error{HTTPStatus: 500, Message: "boom"}, 0, []string{"claude"}, model, maxWait)
	if !shouldRetry {
		t.Fatalf("expected shouldRetry=true for request_retry=1, got false")
	}
	if wait <= 0 {
		t.Fatalf("expected wait > 0, got %v", wait)
	}

	_, shouldRetry = m.shouldRetryAfterError(&Error{HTTPStatus: 500, Message: "boom"}, 1, []string{"claude"}, model, maxWait)
	if shouldRetry {
		t.Fatalf("expected shouldRetry=false on attempt=1 for request_retry=1, got true")
	}
}

func TestManager_ShouldRetryAfterError_UsesOAuthModelAliasForCooldown(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(3, 30*time.Second, 0)
	m.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"qwen": {
			{Name: "qwen3.6-plus", Alias: "coder-model"},
		},
	})

	routeModel := "coder-model"
	upstreamModel := "qwen3.6-plus"
	next := time.Now().Add(5 * time.Second)

	auth := &Auth{
		ID:       "auth-1",
		Provider: "qwen",
		ModelStates: map[string]*ModelState{
			upstreamModel: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: next,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: next,
				},
			},
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	_, _, maxWait := m.retrySettings()
	wait, shouldRetry := m.shouldRetryAfterError(&Error{HTTPStatus: 429, Message: "quota"}, 0, []string{"qwen"}, routeModel, maxWait)
	if !shouldRetry {
		t.Fatalf("expected shouldRetry=true, got false (wait=%v)", wait)
	}
	if wait <= 0 {
		t.Fatalf("expected wait > 0, got %v", wait)
	}
}

type credentialRetryLimitExecutor struct {
	id string

	mu    sync.Mutex
	calls int
}

func (e *credentialRetryLimitExecutor) Identifier() string {
	return e.id
}

func (e *credentialRetryLimitExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.recordCall()
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: 500, Message: "boom"}
}

func (e *credentialRetryLimitExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.recordCall()
	return nil, &Error{HTTPStatus: 500, Message: "boom"}
}

func (e *credentialRetryLimitExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *credentialRetryLimitExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.recordCall()
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: 500, Message: "boom"}
}

func (e *credentialRetryLimitExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *credentialRetryLimitExecutor) recordCall() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
}

func (e *credentialRetryLimitExecutor) Calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

type authFallbackExecutor struct {
	id string

	mu                sync.Mutex
	executeCalls      []string
	streamCalls       []string
	httpRequests      []*http.Request
	executeErrors     map[string]error
	streamFirstErrors map[string]error
	httpRequestErr    error
	httpRequestSignal chan struct{}
	httpRequestBlock  chan struct{}
	httpRequestDone   chan struct{}
}

func (e *authFallbackExecutor) Identifier() string {
	return e.id
}

func (e *authFallbackExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.executeCalls = append(e.executeCalls, auth.ID)
	err := e.executeErrors[auth.ID]
	e.mu.Unlock()
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *authFallbackExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.streamCalls = append(e.streamCalls, auth.ID)
	err := e.streamFirstErrors[auth.ID]
	e.mu.Unlock()

	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	if err != nil {
		ch <- cliproxyexecutor.StreamChunk{Err: err}
		close(ch)
		return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Auth": {auth.ID}}, Chunks: ch}, nil
	}
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Auth": {auth.ID}}, Chunks: ch}, nil
}

func (e *authFallbackExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *authFallbackExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: 500, Message: "not implemented"}
}

func (e *authFallbackExecutor) HttpRequest(_ context.Context, _ *Auth, req *http.Request) (*http.Response, error) {
	e.mu.Lock()
	if req != nil {
		e.httpRequests = append(e.httpRequests, req.Clone(req.Context()))
	}
	err := e.httpRequestErr
	signal := e.httpRequestSignal
	block := e.httpRequestBlock
	done := e.httpRequestDone
	e.mu.Unlock()
	if signal != nil {
		select {
		case signal <- struct{}{}:
		default:
		}
	}
	if block != nil && req != nil {
		select {
		case <-block:
		case <-req.Context().Done():
			if done != nil {
				select {
				case done <- struct{}{}:
				default:
				}
			}
			return nil, req.Context().Err()
		}
	}
	if done != nil {
		select {
		case done <- struct{}{}:
		default:
		}
	}
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"id":"warmup"}`)),
		Header:     make(http.Header),
	}, nil
}

func (e *authFallbackExecutor) ExecuteCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeCalls))
	copy(out, e.executeCalls)
	return out
}

func (e *authFallbackExecutor) StreamCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.streamCalls))
	copy(out, e.streamCalls)
	return out
}

func (e *authFallbackExecutor) HTTPRequestCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.httpRequests)
}

func (e *authFallbackExecutor) HTTPRequests() []*http.Request {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]*http.Request, len(e.httpRequests))
	copy(out, e.httpRequests)
	return out
}

type retryAfterStatusError struct {
	status     int
	message    string
	retryAfter time.Duration
}

func (e *retryAfterStatusError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func (e *retryAfterStatusError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.status
}

func (e *retryAfterStatusError) RetryAfter() *time.Duration {
	if e == nil {
		return nil
	}
	d := e.retryAfter
	return &d
}

func newCredentialRetryLimitTestManager(t *testing.T, maxRetryCredentials int) (*Manager, *credentialRetryLimitExecutor) {
	t.Helper()

	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(0, 0, maxRetryCredentials)

	executor := &credentialRetryLimitExecutor{id: "claude"}
	m.RegisterExecutor(executor)

	baseID := uuid.NewString()
	auth1 := &Auth{ID: baseID + "-auth-1", Provider: "claude"}
	auth2 := &Auth{ID: baseID + "-auth-2", Provider: "claude"}

	// Auth selection requires that the global model registry knows each credential supports the model.
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "test-model"}})
	reg.RegisterClient(auth2.ID, "claude", []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	if _, errRegister := m.Register(context.Background(), auth1); errRegister != nil {
		t.Fatalf("register auth1: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), auth2); errRegister != nil {
		t.Fatalf("register auth2: %v", errRegister)
	}

	return m, executor
}

func TestManager_MaxRetryCredentials_LimitsCrossCredentialRetries(t *testing.T) {
	request := cliproxyexecutor.Request{Model: "test-model"}
	testCases := []struct {
		name   string
		invoke func(*Manager) error
	}{
		{
			name: "execute",
			invoke: func(m *Manager) error {
				_, errExecute := m.Execute(context.Background(), []string{"claude"}, request, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "execute_count",
			invoke: func(m *Manager) error {
				_, errExecute := m.ExecuteCount(context.Background(), []string{"claude"}, request, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "execute_stream",
			invoke: func(m *Manager) error {
				_, errExecute := m.ExecuteStream(context.Background(), []string{"claude"}, request, cliproxyexecutor.Options{})
				return errExecute
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			limitedManager, limitedExecutor := newCredentialRetryLimitTestManager(t, 1)
			if errInvoke := tc.invoke(limitedManager); errInvoke == nil {
				t.Fatalf("expected error for limited retry execution")
			}
			if calls := limitedExecutor.Calls(); calls != 1 {
				t.Fatalf("expected 1 call with max-retry-credentials=1, got %d", calls)
			}

			unlimitedManager, unlimitedExecutor := newCredentialRetryLimitTestManager(t, 0)
			if errInvoke := tc.invoke(unlimitedManager); errInvoke == nil {
				t.Fatalf("expected error for unlimited retry execution")
			}
			if calls := unlimitedExecutor.Calls(); calls != 2 {
				t.Fatalf("expected 2 calls with max-retry-credentials=0, got %d", calls)
			}
		})
	}
}

func TestManager_ModelSupportBadRequest_FallsBackAndSuspendsAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"aa-bad-auth": &Error{
				HTTPStatus: http.StatusBadRequest,
				Message:    "invalid_request_error: The requested model is not supported.",
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "claude-opus-4-6"
	badAuth := &Auth{ID: "aa-bad-auth", Provider: "claude"}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: "claude"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(badAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(badAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), badAuth); errRegister != nil {
		t.Fatalf("register bad auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	request := cliproxyexecutor.Request{Model: model}
	for i := 0; i < 2; i++ {
		resp, errExecute := m.Execute(context.Background(), []string{"claude"}, request, cliproxyexecutor.Options{})
		if errExecute != nil {
			t.Fatalf("execute %d error = %v, want success", i, errExecute)
		}
		if string(resp.Payload) != goodAuth.ID {
			t.Fatalf("execute %d payload = %q, want %q", i, string(resp.Payload), goodAuth.ID)
		}
	}

	got := executor.ExecuteCalls()
	want := []string{badAuth.ID, goodAuth.ID, goodAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d auth = %q, want %q", i, got[i], want[i])
		}
	}

	updatedBad, ok := m.GetByID(badAuth.ID)
	if !ok || updatedBad == nil {
		t.Fatalf("expected bad auth to remain registered")
	}
	state := updatedBad.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state for %q", model)
	}
	if !state.Unavailable {
		t.Fatalf("expected bad auth model state to be unavailable")
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatalf("expected bad auth model state cooldown to be set")
	}
}

func TestManagerExecuteStream_ModelSupportBadRequestFallsBackAndSuspendsAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		streamFirstErrors: map[string]error{
			"aa-bad-auth": &Error{
				HTTPStatus: http.StatusBadRequest,
				Message:    "invalid_request_error: The requested model is not supported.",
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "claude-opus-4-6"
	badAuth := &Auth{ID: "aa-bad-auth", Provider: "claude"}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: "claude"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(badAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(badAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), badAuth); errRegister != nil {
		t.Fatalf("register bad auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	request := cliproxyexecutor.Request{Model: model}
	for i := 0; i < 2; i++ {
		streamResult, errExecute := m.ExecuteStream(context.Background(), []string{"claude"}, request, cliproxyexecutor.Options{})
		if errExecute != nil {
			t.Fatalf("execute stream %d error = %v, want success", i, errExecute)
		}
		var payload []byte
		for chunk := range streamResult.Chunks {
			if chunk.Err != nil {
				t.Fatalf("execute stream %d chunk error = %v, want success", i, chunk.Err)
			}
			payload = append(payload, chunk.Payload...)
		}
		if string(payload) != goodAuth.ID {
			t.Fatalf("execute stream %d payload = %q, want %q", i, string(payload), goodAuth.ID)
		}
	}

	got := executor.StreamCalls()
	want := []string{badAuth.ID, goodAuth.ID, goodAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("stream calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stream call %d auth = %q, want %q", i, got[i], want[i])
		}
	}

	updatedBad, ok := m.GetByID(badAuth.ID)
	if !ok || updatedBad == nil {
		t.Fatalf("expected bad auth to remain registered")
	}
	state := updatedBad.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state for %q", model)
	}
	if !state.Unavailable {
		t.Fatalf("expected bad auth model state to be unavailable")
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatalf("expected bad auth model state cooldown to be set")
	}
}

func TestManager_MarkResult_RespectsAuthDisableCoolingOverride(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:       "auth-1",
		Provider: "claude",
		Metadata: map[string]any{
			"disable_cooling": true,
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "test-model"
	m.MarkResult(context.Background(), Result{
		AuthID:   "auth-1",
		Provider: "claude",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: 500, Message: "boom"},
	})

	updated, ok := m.GetByID("auth-1")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to be zero when disable_cooling=true, got %v", state.NextRetryAfter)
	}
}

func TestManager_MarkResult_RespectsAuthDisableCoolingOverride_On403(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:       "auth-403",
		Provider: "claude",
		Metadata: map[string]any{
			"disable_cooling": true,
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "test-model-403"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "claude",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusForbidden, Message: "forbidden"},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to be zero when disable_cooling=true, got %v", state.NextRetryAfter)
	}

	if count := reg.GetModelCount(model); count <= 0 {
		t.Fatalf("expected model count > 0 when disable_cooling=true, got %d", count)
	}
}

func TestManager_Execute_DisableCooling_DoesNotBlackoutAfter403(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"auth-403-exec": &Error{
				HTTPStatus: http.StatusForbidden,
				Message:    "forbidden",
			},
		},
	}
	m.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "auth-403-exec",
		Provider: "claude",
		Metadata: map[string]any{
			"disable_cooling": true,
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "test-model-403-exec"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	req := cliproxyexecutor.Request{Model: model}
	_, errExecute1 := m.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
	if errExecute1 == nil {
		t.Fatal("expected first execute error")
	}
	if statusCodeFromError(errExecute1) != http.StatusForbidden {
		t.Fatalf("first execute status = %d, want %d", statusCodeFromError(errExecute1), http.StatusForbidden)
	}

	_, errExecute2 := m.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
	if errExecute2 == nil {
		t.Fatal("expected second execute error")
	}
	if statusCodeFromError(errExecute2) != http.StatusForbidden {
		t.Fatalf("second execute status = %d, want %d", statusCodeFromError(errExecute2), http.StatusForbidden)
	}
}

func TestManager_Execute_DisableCooling_DoesNotBlackoutAfter429RetryAfter(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"auth-429-exec": &retryAfterStatusError{
				status:     http.StatusTooManyRequests,
				message:    "quota exhausted",
				retryAfter: 2 * time.Minute,
			},
		},
	}
	m.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "auth-429-exec",
		Provider: "claude",
		Metadata: map[string]any{
			"disable_cooling": true,
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "test-model-429-exec"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	req := cliproxyexecutor.Request{Model: model}
	_, errExecute1 := m.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
	if errExecute1 == nil {
		t.Fatal("expected first execute error")
	}
	if statusCodeFromError(errExecute1) != http.StatusTooManyRequests {
		t.Fatalf("first execute status = %d, want %d", statusCodeFromError(errExecute1), http.StatusTooManyRequests)
	}

	_, errExecute2 := m.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
	if errExecute2 == nil {
		t.Fatal("expected second execute error")
	}
	if statusCodeFromError(errExecute2) != http.StatusTooManyRequests {
		t.Fatalf("second execute status = %d, want %d", statusCodeFromError(errExecute2), http.StatusTooManyRequests)
	}

	calls := executor.ExecuteCalls()
	if len(calls) != 2 {
		t.Fatalf("execute calls = %d, want 2", len(calls))
	}

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to be zero when disable_cooling=true, got %v", state.NextRetryAfter)
	}
}

func TestManager_Execute_DisableCooling_RetriesAfter429RetryAfter(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(3, 100*time.Millisecond, 0)

	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"auth-429-retryafter-exec": &retryAfterStatusError{
				status:     http.StatusTooManyRequests,
				message:    "quota exhausted",
				retryAfter: 5 * time.Millisecond,
			},
		},
	}
	m.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "auth-429-retryafter-exec",
		Provider: "claude",
		Metadata: map[string]any{
			"disable_cooling": true,
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "test-model-429-retryafter-exec"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	req := cliproxyexecutor.Request{Model: model}
	_, errExecute := m.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected execute error")
	}
	if statusCodeFromError(errExecute) != http.StatusTooManyRequests {
		t.Fatalf("execute status = %d, want %d", statusCodeFromError(errExecute), http.StatusTooManyRequests)
	}

	calls := executor.ExecuteCalls()
	if len(calls) != 4 {
		t.Fatalf("execute calls = %d, want 4 (initial + 3 retries)", len(calls))
	}
}

func TestManager_MarkResult_RequestScopedNotFoundDoesNotCooldownAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:       "auth-1",
		Provider: "openai",
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "gpt-4.1"
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error: &Error{
			HTTPStatus: http.StatusNotFound,
			Message:    requestScopedNotFoundMessage,
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if updated.Unavailable {
		t.Fatalf("expected request-scoped 404 to keep auth available")
	}
	if !updated.NextRetryAfter.IsZero() {
		t.Fatalf("expected request-scoped 404 to keep auth cooldown unset, got %v", updated.NextRetryAfter)
	}
	if state := updated.ModelStates[model]; state != nil {
		t.Fatalf("expected request-scoped 404 to avoid model cooldown state, got %#v", state)
	}
}

func TestManager_RequestScopedNotFoundStopsRetryWithoutSuspendingAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "openai",
		executeErrors: map[string]error{
			"aa-bad-auth": &Error{
				HTTPStatus: http.StatusNotFound,
				Message:    requestScopedNotFoundMessage,
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "gpt-4.1"
	badAuth := &Auth{ID: "aa-bad-auth", Provider: "openai"}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: "openai"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(badAuth.ID, "openai", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "openai", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(badAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), badAuth); errRegister != nil {
		t.Fatalf("register bad auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	_, errExecute := m.Execute(context.Background(), []string{"openai"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected request-scoped not-found error")
	}
	errResult, ok := errExecute.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T", errExecute)
	}
	if errResult.HTTPStatus != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", errResult.HTTPStatus, http.StatusNotFound)
	}
	if errResult.Message != requestScopedNotFoundMessage {
		t.Fatalf("message = %q, want %q", errResult.Message, requestScopedNotFoundMessage)
	}

	got := executor.ExecuteCalls()
	want := []string{badAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d auth = %q, want %q", i, got[i], want[i])
		}
	}

	updatedBad, ok := m.GetByID(badAuth.ID)
	if !ok || updatedBad == nil {
		t.Fatalf("expected bad auth to remain registered")
	}
	if updatedBad.Unavailable {
		t.Fatalf("expected request-scoped 404 to keep bad auth available")
	}
	if !updatedBad.NextRetryAfter.IsZero() {
		t.Fatalf("expected request-scoped 404 to keep bad auth cooldown unset, got %v", updatedBad.NextRetryAfter)
	}
	if state := updatedBad.ModelStates[model]; state != nil {
		t.Fatalf("expected request-scoped 404 to avoid bad auth model cooldown state, got %#v", state)
	}
}

func TestManager_FillFirstWarmsOtherOpenAIProviderAsync(t *testing.T) {
	m := NewManager(nil, &FillFirstSelector{}, nil)
	signal := make(chan struct{}, 4)
	primaryExecutor := &authFallbackExecutor{id: "claude"}
	warmExecutor := &authFallbackExecutor{id: "openai", httpRequestSignal: signal}
	m.RegisterExecutor(primaryExecutor)
	m.RegisterExecutor(warmExecutor)

	model := "gpt-4.1"
	selectedAuth := &Auth{ID: "aa-selected", Provider: "claude"}
	warmedAuth := &Auth{ID: "bb-warmed", Provider: "openai", Attributes: map[string]string{"base_url": "https://warmed.example"}}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(selectedAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(warmedAuth.ID, "openai", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(selectedAuth.ID)
		reg.UnregisterClient(warmedAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), selectedAuth); errRegister != nil {
		t.Fatalf("register selected auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), warmedAuth); errRegister != nil {
		t.Fatalf("register warmed auth: %v", errRegister)
	}

	resp, errExecute := m.Execute(context.Background(), []string{"claude", "openai"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute: %v", errExecute)
	}
	if string(resp.Payload) != selectedAuth.ID {
		t.Fatalf("payload = %q, want %q", string(resp.Payload), selectedAuth.ID)
	}

	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("expected async warmup request")
	}

	if warmExecutor.HTTPRequestCount() != 1 {
		t.Fatalf("http request count = %d, want 1", warmExecutor.HTTPRequestCount())
	}
	requests := warmExecutor.HTTPRequests()
	if len(requests) != 1 {
		t.Fatalf("http requests len = %d, want 1", len(requests))
	}
	if got := requests[0].URL.String(); got != "https://warmed.example/chat/completions" {
		t.Fatalf("warmup url = %q, want %q", got, "https://warmed.example/chat/completions")
	}
}

func TestManager_RoundRobinDoesNotWarmOtherProviders(t *testing.T) {
	m := NewManager(nil, &RoundRobinSelector{}, nil)
	signal := make(chan struct{}, 4)
	primaryExecutor := &authFallbackExecutor{id: "claude"}
	warmExecutor := &authFallbackExecutor{id: "openai", httpRequestSignal: signal}
	m.RegisterExecutor(primaryExecutor)
	m.RegisterExecutor(warmExecutor)

	model := "gpt-4.1"
	selectedAuth := &Auth{ID: "aa-selected", Provider: "claude"}
	warmedAuth := &Auth{ID: "bb-warmed", Provider: "openai", Attributes: map[string]string{"base_url": "https://warmed.example"}}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(selectedAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(warmedAuth.ID, "openai", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(selectedAuth.ID)
		reg.UnregisterClient(warmedAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), selectedAuth); errRegister != nil {
		t.Fatalf("register selected auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), warmedAuth); errRegister != nil {
		t.Fatalf("register warmed auth: %v", errRegister)
	}

	resp, errExecute := m.Execute(context.Background(), []string{"claude", "openai"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute: %v", errExecute)
	}
	if string(resp.Payload) != selectedAuth.ID {
		t.Fatalf("payload = %q, want %q", string(resp.Payload), selectedAuth.ID)
	}
	select {
	case <-signal:
		t.Fatal("unexpected warmup request under round-robin")
	case <-time.After(200 * time.Millisecond):
	}
	if warmExecutor.HTTPRequestCount() != 0 {
		t.Fatalf("http request count = %d, want 0", warmExecutor.HTTPRequestCount())
	}
}

func TestManager_WarmupFailureDoesNotAffectPrimaryRequest(t *testing.T) {
	m := NewManager(nil, &FillFirstSelector{}, nil)
	primaryExecutor := &authFallbackExecutor{id: "claude"}
	warmExecutor := &authFallbackExecutor{id: "openai", httpRequestErr: context.DeadlineExceeded}
	m.RegisterExecutor(primaryExecutor)
	m.RegisterExecutor(warmExecutor)

	model := "gpt-4.1"
	selectedAuth := &Auth{ID: "aa-selected", Provider: "claude"}
	warmedAuth := &Auth{ID: "bb-warmed", Provider: "openai", Attributes: map[string]string{"base_url": "https://warmed.example"}}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(selectedAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(warmedAuth.ID, "openai", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(selectedAuth.ID)
		reg.UnregisterClient(warmedAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), selectedAuth); errRegister != nil {
		t.Fatalf("register selected auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), warmedAuth); errRegister != nil {
		t.Fatalf("register warmed auth: %v", errRegister)
	}

	resp, errExecute := m.Execute(context.Background(), []string{"claude", "openai"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute: %v", errExecute)
	}
	if string(resp.Payload) != selectedAuth.ID {
		t.Fatalf("payload = %q, want %q", string(resp.Payload), selectedAuth.ID)
	}
}

func TestManager_WarmupDeduplicatesWithinTTL(t *testing.T) {
	m := NewManager(nil, &FillFirstSelector{}, nil)
	signal := make(chan struct{}, 4)
	primaryExecutor := &authFallbackExecutor{id: "claude"}
	warmExecutor := &authFallbackExecutor{id: "openai", httpRequestSignal: signal}
	m.RegisterExecutor(primaryExecutor)
	m.RegisterExecutor(warmExecutor)

	model := "gpt-4.1"
	selectedAuth := &Auth{ID: "aa-selected", Provider: "claude"}
	warmedAuth := &Auth{ID: "bb-warmed", Provider: "openai", Attributes: map[string]string{"base_url": "https://warmed.example"}}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(selectedAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(warmedAuth.ID, "openai", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(selectedAuth.ID)
		reg.UnregisterClient(warmedAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), selectedAuth); errRegister != nil {
		t.Fatalf("register selected auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), warmedAuth); errRegister != nil {
		t.Fatalf("register warmed auth: %v", errRegister)
	}

	if _, errExecute := m.Execute(context.Background(), []string{"claude", "openai"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errExecute != nil {
		t.Fatalf("first execute: %v", errExecute)
	}
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("expected first async warmup request")
	}

	if _, errExecute := m.Execute(context.Background(), []string{"claude", "openai"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errExecute != nil {
		t.Fatalf("second execute: %v", errExecute)
	}
	select {
	case <-signal:
		t.Fatal("unexpected second warmup request within ttl")
	case <-time.After(200 * time.Millisecond):
	}

	if warmExecutor.HTTPRequestCount() != 1 {
		t.Fatalf("http request count = %d, want 1", warmExecutor.HTTPRequestCount())
	}
}

func TestManager_WarmupSingleFlightsPerProvider(t *testing.T) {
	m := NewManager(nil, &FillFirstSelector{}, nil)
	signal := make(chan struct{}, 4)
	block := make(chan struct{})
	done := make(chan struct{}, 4)
	primaryExecutor := &authFallbackExecutor{id: "claude"}
	warmExecutor := &authFallbackExecutor{id: "openai", httpRequestSignal: signal, httpRequestBlock: block, httpRequestDone: done}
	m.RegisterExecutor(primaryExecutor)
	m.RegisterExecutor(warmExecutor)

	model := "gpt-4.1"
	selectedAuth := &Auth{ID: "aa-selected", Provider: "claude"}
	warmedAuth := &Auth{ID: "bb-warmed", Provider: "openai", Attributes: map[string]string{"base_url": "https://warmed.example"}}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(selectedAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(warmedAuth.ID, "openai", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(selectedAuth.ID)
		reg.UnregisterClient(warmedAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), selectedAuth); errRegister != nil {
		t.Fatalf("register selected auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), warmedAuth); errRegister != nil {
		t.Fatalf("register warmed auth: %v", errRegister)
	}

	start := make(chan struct{})
	errCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			_, err := m.Execute(context.Background(), []string{"claude", "openai"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			errCh <- err
		}()
	}
	close(start)

	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("expected async warmup request")
	}
	select {
	case <-signal:
		t.Fatal("unexpected second concurrent warmup request")
	case <-time.After(200 * time.Millisecond):
	}

	close(block)
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("execute: %v", err)
		}
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected blocked warmup request to finish")
	}

	if warmExecutor.HTTPRequestCount() != 1 {
		t.Fatalf("http request count = %d, want 1", warmExecutor.HTTPRequestCount())
	}
}

func TestManager_WarmupUsesIndependentTimeout(t *testing.T) {
	m := NewManager(nil, &FillFirstSelector{}, nil)
	signal := make(chan struct{}, 4)
	block := make(chan struct{})
	done := make(chan struct{}, 4)
	primaryExecutor := &authFallbackExecutor{id: "claude"}
	warmExecutor := &authFallbackExecutor{id: "openai", httpRequestSignal: signal, httpRequestBlock: block, httpRequestDone: done}
	m.RegisterExecutor(primaryExecutor)
	m.RegisterExecutor(warmExecutor)

	model := "gpt-4.1"
	selectedAuth := &Auth{ID: "aa-selected", Provider: "claude"}
	warmedAuth := &Auth{ID: "bb-warmed", Provider: "openai", Attributes: map[string]string{"base_url": "https://warmed.example"}}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(selectedAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(warmedAuth.ID, "openai", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(selectedAuth.ID)
		reg.UnregisterClient(warmedAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), selectedAuth); errRegister != nil {
		t.Fatalf("register selected auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), warmedAuth); errRegister != nil {
		t.Fatalf("register warmed auth: %v", errRegister)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if _, errExecute := m.Execute(ctx, []string{"claude", "openai"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errExecute != nil {
		t.Fatalf("execute: %v", errExecute)
	}
	cancel()

	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("expected async warmup request")
	}
	requests := warmExecutor.HTTPRequests()
	if len(requests) != 1 {
		t.Fatalf("http requests len = %d, want 1", len(requests))
	}
	deadline, ok := requests[0].Context().Deadline()
	if !ok {
		t.Fatal("expected warmup request context to have a deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > mixedProviderWarmupTimeout {
		t.Fatalf("warmup deadline remaining = %v, want within (0, %v]", remaining, mixedProviderWarmupTimeout)
	}

	select {
	case <-done:
	case <-time.After(mixedProviderWarmupTimeout + 2*time.Second):
		t.Fatal("expected warmup request to finish after timeout")
	}
	deadlineWait := time.Now().Add(500 * time.Millisecond)
	for len(m.mixedWarmInFlight) != 0 && time.Now().Before(deadlineWait) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(m.mixedWarmInFlight) != 0 {
		t.Fatalf("expected no in-flight warmups after timeout, got %d", len(m.mixedWarmInFlight))
	}
}

func TestManager_ReserveMixedProviderWarmupCleansExpiredEntries(t *testing.T) {
	m := NewManager(nil, &FillFirstSelector{}, nil)
	now := time.Now()
	m.mixedWarmUntil["openai|expired"] = now.Add(-time.Second)
	m.mixedWarmUntil["openai|active"] = now.Add(time.Second)

	if !m.reserveMixedProviderWarmup("openai", "fresh", now) {
		t.Fatal("expected fresh warmup reservation to succeed")
	}
	if _, ok := m.mixedWarmUntil["openai|expired"]; ok {
		t.Fatal("expected expired warmup entry to be removed")
	}
	if _, ok := m.mixedWarmUntil["openai|active"]; !ok {
		t.Fatal("expected active warmup entry to remain")
	}
	if _, ok := m.mixedWarmUntil["openai|fresh"]; !ok {
		t.Fatal("expected fresh warmup entry to be added")
	}
}

func TestManager_ExecuteCountDoesNotWarmOtherProviders(t *testing.T) {
	m := NewManager(nil, &FillFirstSelector{}, nil)
	signal := make(chan struct{}, 4)
	primaryExecutor := &authFallbackExecutor{id: "claude"}
	warmExecutor := &authFallbackExecutor{id: "openai", httpRequestSignal: signal}
	m.RegisterExecutor(primaryExecutor)
	m.RegisterExecutor(warmExecutor)

	model := "gpt-4.1"
	selectedAuth := &Auth{ID: "aa-selected", Provider: "claude"}
	warmedAuth := &Auth{ID: "bb-warmed", Provider: "openai", Attributes: map[string]string{"base_url": "https://warmed.example"}}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(selectedAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(warmedAuth.ID, "openai", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(selectedAuth.ID)
		reg.UnregisterClient(warmedAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), selectedAuth); errRegister != nil {
		t.Fatalf("register selected auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), warmedAuth); errRegister != nil {
		t.Fatalf("register warmed auth: %v", errRegister)
	}

	if _, errExecute := m.ExecuteCount(context.Background(), []string{"claude", "openai"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errExecute == nil {
		t.Fatal("expected count tokens to use executor.CountTokens stub and return not implemented")
	}
	select {
	case <-signal:
		t.Fatal("unexpected warmup request during ExecuteCount")
	case <-time.After(200 * time.Millisecond):
	}
	if warmExecutor.HTTPRequestCount() != 0 {
		t.Fatalf("http request count = %d, want 0", warmExecutor.HTTPRequestCount())
	}
}

func TestManager_TryStartMixedProviderWarmupDoesNotUseProviderWideTTL(t *testing.T) {
	m := NewManager(nil, &FillFirstSelector{}, nil)
	now := time.Now()
	m.mixedWarmUntil["openai|auth-a"] = now.Add(time.Second)

	if !m.tryStartMixedProviderWarmup("openai", now) {
		t.Fatal("expected provider warmup gate to allow another auth while no warmup is in flight")
	}
	if _, ok := m.mixedWarmInFlight["openai"]; !ok {
		t.Fatal("expected in-flight gate to be set for provider")
	}
	m.finishMixedProviderWarmup("openai")
	if len(m.mixedWarmInFlight) != 0 {
		t.Fatalf("expected in-flight gate to clear, got %d entries", len(m.mixedWarmInFlight))
	}
}
