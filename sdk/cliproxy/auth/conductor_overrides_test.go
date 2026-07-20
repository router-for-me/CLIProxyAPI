package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
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
		"kimi": {
			{Name: "deepseek-v3.1", Alias: "pool-model"},
		},
	})

	routeModel := "pool-model"
	upstreamModel := "deepseek-v3.1"
	next := time.Now().Add(5 * time.Second)

	auth := &Auth{
		ID:       "auth-1",
		Provider: "kimi",
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
	wait, shouldRetry := m.shouldRetryAfterError(&Error{HTTPStatus: 429, Message: "quota"}, 0, []string{"kimi"}, routeModel, maxWait)
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
	executeErrors     map[string]error
	streamFirstErrors map[string]error
	countTokenErrors  map[string]error
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

func (e *authFallbackExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	err := e.countTokenErrors[auth.ID]
	e.mu.Unlock()
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *authFallbackExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
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

type resultCaptureHook struct {
	NoopHook

	mu      sync.Mutex
	results []Result
}

func (h *resultCaptureHook) OnResult(_ context.Context, result Result) {
	h.mu.Lock()
	h.results = append(h.results, result)
	h.mu.Unlock()
}

func (h *resultCaptureHook) Results() []Result {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Result, len(h.results))
	copy(out, h.results)
	return out
}

type retryAfterStatusError struct {
	status     int
	message    string
	retryAfter time.Duration
}

type requestScopedStatusError struct {
	status  int
	message string
}

func (e *requestScopedStatusError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func (e *requestScopedStatusError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.status
}

func (e *requestScopedStatusError) IsRequestScoped() bool {
	return e != nil
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

func TestManagerExecute_AntigravityInvalidGrantFallsBackAndSuspendsAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	invalidGrantErr := &Error{
		HTTPStatus: http.StatusBadRequest,
		Message:    `bad response status code 400, message: {"error":"invalid_grant","error_description":"Bad Request"}, body: {"type":"error","error":{"type":"invalid_request_error","message":"{\"error\":\"invalid_grant\"}"}}`,
	}
	executor := &authFallbackExecutor{
		id: "antigravity",
		executeErrors: map[string]error{
			"aa-bad-auth": invalidGrantErr,
		},
	}
	m.RegisterExecutor(executor)

	model := "gemini-3-pro-preview"
	badAuth := &Auth{ID: "aa-bad-auth", Provider: "antigravity"}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: "antigravity"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(badAuth.ID, "antigravity", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "antigravity", []*registry.ModelInfo{{ID: model}})
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
		resp, errExecute := m.Execute(context.Background(), []string{"antigravity"}, request, cliproxyexecutor.Options{})
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
	if state.StatusMessage != invalidGrantErr.Message {
		t.Fatalf("status message = %q, want %q", state.StatusMessage, invalidGrantErr.Message)
	}
}

func TestManagerExecuteStream_AntigravityInvalidGrantFallsBackAndSuspendsAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	invalidGrantErr := &Error{
		HTTPStatus: http.StatusBadRequest,
		Message:    `bad response status code 400, message: {"error":"invalid_grant","error_description":"Bad Request"}, body: {"type":"error","error":{"type":"invalid_request_error","message":"{\"error\":\"invalid_grant\"}"}}`,
	}
	executor := &authFallbackExecutor{
		id: "antigravity",
		streamFirstErrors: map[string]error{
			"aa-bad-auth": invalidGrantErr,
		},
	}
	m.RegisterExecutor(executor)

	model := "gemini-3-pro-preview"
	badAuth := &Auth{ID: "aa-bad-auth", Provider: "antigravity"}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: "antigravity"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(badAuth.ID, "antigravity", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "antigravity", []*registry.ModelInfo{{ID: model}})
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
		streamResult, errExecute := m.ExecuteStream(context.Background(), []string{"antigravity"}, request, cliproxyexecutor.Options{})
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

func TestManager_MarkResult_TransientErrorCooldownDefault(t *testing.T) {
	prevQuota := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(0)
	t.Cleanup(func() {
		quotaCooldownDisabled.Store(prevQuota)
		transientErrorCooldownSeconds.Store(prevTransient)
	})

	m := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:       "auth-transient-default",
		Provider: "claude",
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "test-model-transient-default"
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusBadGateway, Message: "bad gateway"},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatal("expected transient error cooldown to keep the legacy default")
	}
	diff := time.Until(state.NextRetryAfter)
	if diff < 55*time.Second || diff > 65*time.Second {
		t.Fatalf("expected transient error cooldown to be ~60 seconds, got %v", diff)
	}
}

func TestManager_MarkResult_TransientErrorCooldownDisabled(t *testing.T) {
	prevQuota := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(-1)
	t.Cleanup(func() {
		quotaCooldownDisabled.Store(prevQuota)
		transientErrorCooldownSeconds.Store(prevTransient)
	})

	m := NewManager(nil, nil, nil)

	modelAuth := &Auth{
		ID:       "auth-transient-model-disabled",
		Provider: "claude",
	}
	if _, errRegisterModel := m.Register(context.Background(), modelAuth); errRegisterModel != nil {
		t.Fatalf("register model auth: %v", errRegisterModel)
	}

	model := "test-model-transient-disabled"
	m.MarkResult(context.Background(), Result{
		AuthID:   modelAuth.ID,
		Provider: modelAuth.Provider,
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusBadGateway, Message: "bad gateway"},
	})

	updatedModelAuth, okModelAuth := m.GetByID(modelAuth.ID)
	if !okModelAuth || updatedModelAuth == nil {
		t.Fatalf("expected model auth to be present")
	}
	state := updatedModelAuth.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected transient model cooldown to be disabled, got %v", state.NextRetryAfter)
	}

	authLevelAuth := &Auth{
		ID:       "auth-transient-auth-disabled",
		Provider: "claude",
	}
	if _, errRegisterAuth := m.Register(context.Background(), authLevelAuth); errRegisterAuth != nil {
		t.Fatalf("register auth-level auth: %v", errRegisterAuth)
	}

	m.MarkResult(context.Background(), Result{
		AuthID:   authLevelAuth.ID,
		Provider: authLevelAuth.Provider,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "unavailable"},
	})

	updatedAuthLevel, okAuthLevel := m.GetByID(authLevelAuth.ID)
	if !okAuthLevel || updatedAuthLevel == nil {
		t.Fatalf("expected auth-level auth to be present")
	}
	if !updatedAuthLevel.NextRetryAfter.IsZero() {
		t.Fatalf("expected transient auth cooldown to be disabled, got %v", updatedAuthLevel.NextRetryAfter)
	}
}

func TestManager_MarkResult_TransientErrorCooldownDoesNotDisableAuthErrors(t *testing.T) {
	prevQuota := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(-1)
	t.Cleanup(func() {
		quotaCooldownDisabled.Store(prevQuota)
		transientErrorCooldownSeconds.Store(prevTransient)
	})

	m := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:       "auth-transient-auth-error",
		Provider: "claude",
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "test-model-auth-error"
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
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
	if state.NextRetryAfter.IsZero() {
		t.Fatal("expected auth error cooldown to remain enabled")
	}
	diff := time.Until(state.NextRetryAfter)
	if diff < 29*time.Minute || diff > 31*time.Minute {
		t.Fatalf("expected auth error cooldown to be ~30 minutes, got %v", diff)
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

func TestManager_MarkResult_CloudflareChallenge_On403(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:       "auth-cf-403",
		Provider: "claude",
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "test-model-cf-403"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "claude",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusForbidden, Message: "cf-mitigated: challenge"},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to be non-zero for cloudflare challenge")
	}
	diff := time.Until(state.NextRetryAfter)
	if diff < 5*time.Second || diff > 25*time.Second {
		t.Fatalf("expected NextRetryAfter to be ~10 seconds, got %v", diff)
	}
	if state.StatusMessage != "cloudflare challenge" {
		t.Fatalf("expected StatusMessage to be 'cloudflare challenge', got %s", state.StatusMessage)
	}

	// Because Cloudflare Challenge is treated as transient (no suspension),
	// the model should NOT be suspended in the global registry, so count > 0.
	if count := reg.GetModelCount(model); count <= 0 {
		t.Fatalf("expected model count > 0 for cloudflare challenge transient cooldown, got %d", count)
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

func TestManager_RequestScopedErrorStopsCredentialFallbackWithoutSuspendingAuth(t *testing.T) {
	incompleteErr := &requestScopedStatusError{
		status:  http.StatusRequestTimeout,
		message: "stream error: stream disconnected before completion: stream closed before response.completed",
	}
	messageTooBigErr := &requestScopedStatusError{
		status:  http.StatusRequestEntityTooLarge,
		message: `{"error":{"message":"upstream websocket message too big","type":"invalid_request_error","code":"message_too_big"}}`,
	}
	invalidRequestErr := &Error{
		HTTPStatus: http.StatusBadRequest,
		Message:    `{"error":{"type":"invalid_request_error","code":"invalid_value","message":"Invalid input."}}`,
	}
	badRequestErr := &Error{
		HTTPStatus: http.StatusBadRequest,
		Message:    `{"error":{"type":"bad_request_error","code":"invalid_value","message":"Bad input."}}`,
	}
	tests := []struct {
		name       string
		provider   string
		stream     bool
		err        error
		wantStatus int
	}{
		{name: "non-streaming incomplete", err: incompleteErr, wantStatus: http.StatusRequestTimeout},
		{name: "streaming incomplete", stream: true, err: incompleteErr, wantStatus: http.StatusRequestTimeout},
		{name: "streaming codex websocket message too big", provider: "codex", stream: true, err: messageTooBigErr, wantStatus: http.StatusRequestEntityTooLarge},
		{name: "streaming xai websocket message too big", provider: "xai", stream: true, err: messageTooBigErr, wantStatus: http.StatusRequestEntityTooLarge},
		{name: "non-streaming invalid request", err: invalidRequestErr, wantStatus: http.StatusBadRequest},
		{name: "streaming invalid request", stream: true, err: invalidRequestErr, wantStatus: http.StatusBadRequest},
		{name: "non-streaming bad request", err: badRequestErr, wantStatus: http.StatusBadRequest},
		{name: "streaming bad request", stream: true, err: badRequestErr, wantStatus: http.StatusBadRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider := tc.provider
			if provider == "" {
				provider = "codex"
			}
			m := NewManager(nil, nil, nil)
			m.SetRetryConfig(2, 30*time.Second, 0)

			executor := &authFallbackExecutor{id: provider}
			if tc.stream {
				executor.streamFirstErrors = map[string]error{"aa-bad-auth": tc.err}
			} else {
				executor.executeErrors = map[string]error{"aa-bad-auth": tc.err}
			}
			m.RegisterExecutor(executor)

			model := "gpt-5.5"
			badAuth := &Auth{ID: "aa-bad-auth", Provider: provider}
			goodAuth := &Auth{ID: "bb-good-auth", Provider: provider}

			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(badAuth.ID, badAuth.Provider, []*registry.ModelInfo{{ID: model}})
			reg.RegisterClient(goodAuth.ID, goodAuth.Provider, []*registry.ModelInfo{{ID: model}})
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

			var errExecute error
			if tc.stream {
				result, errStream := m.ExecuteStream(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
				if result != nil {
					for range result.Chunks {
					}
				}
				errExecute = errStream
			} else {
				_, errExecute = m.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			}
			if errExecute == nil {
				t.Fatal("expected request-scoped stream error")
			}
			if got := statusCodeFromError(errExecute); got != tc.wantStatus {
				t.Fatalf("status = %d, want %d", got, tc.wantStatus)
			}

			var calls []string
			if tc.stream {
				calls = executor.StreamCalls()
			} else {
				calls = executor.ExecuteCalls()
			}
			if len(calls) != 1 || calls[0] != badAuth.ID {
				t.Fatalf("credential calls = %v, want [%s]", calls, badAuth.ID)
			}

			updatedBad, ok := m.GetByID(badAuth.ID)
			if !ok || updatedBad == nil {
				t.Fatal("expected bad auth to remain registered")
			}
			if updatedBad.Unavailable {
				t.Fatal("expected request-scoped error to keep auth available")
			}
			if !updatedBad.NextRetryAfter.IsZero() {
				t.Fatalf("expected auth cooldown to remain unset, got %v", updatedBad.NextRetryAfter)
			}
			if state := updatedBad.ModelStates[model]; state != nil {
				t.Fatalf("expected request-scoped error to avoid model cooldown state, got %#v", state)
			}
		})
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

func TestManager_ExecuteCount_GenericRouteNotFoundDoesNotSuspendModel(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	hook := &resultCaptureHook{}
	m := NewManager(nil, nil, hook)
	executor := &authFallbackExecutor{
		id: "claude",
		countTokenErrors: map[string]error{
			"count-route-not-found-auth": &Error{
				HTTPStatus: http.StatusNotFound,
				Message:    "404 page not found",
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "count-route-not-found-model"
	auth := &Auth{ID: "count-route-not-found-auth", Provider: "claude"}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	if _, errCount := m.ExecuteCount(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errCount == nil {
		t.Fatal("expected count_tokens route 404 error")
	}

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to remain registered")
	}
	if updated.Failed != 1 {
		t.Fatalf("failed request count = %d, want 1", updated.Failed)
	}
	results := hook.Results()
	if len(results) != 1 || results[0].Success || results[0].Error == nil || results[0].Error.HTTPStatus != http.StatusNotFound {
		t.Fatalf("recorded results = %#v, want one failed 404", results)
	}
	if updated.Unavailable {
		t.Fatal("expected route 404 to keep auth available")
	}
	if state := updated.ModelStates[model]; state != nil {
		t.Fatalf("expected route 404 to avoid model cooldown state, got %#v", state)
	}
	if count := reg.GetModelCount(model); count != 1 {
		t.Fatalf("available model count = %d, want 1", count)
	}

	resp, errExecute := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute after count_tokens route 404: %v", errExecute)
	}
	if string(resp.Payload) != auth.ID {
		t.Fatalf("execute payload = %q, want %q", string(resp.Payload), auth.ID)
	}
}

func TestManager_ExecuteCount_ExplicitModelNotFoundSuspendsModel(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	hook := &resultCaptureHook{}
	m := NewManager(nil, nil, hook)
	executor := &authFallbackExecutor{
		id: "claude",
		countTokenErrors: map[string]error{
			"count-model-not-found-auth": &Error{
				Code:       "model_not_found",
				HTTPStatus: http.StatusNotFound,
				Message:    `{"type":"error","error":{"type":"not_found_error","message":"model count-explicitly-missing-model was not found"}}`,
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "count-explicitly-missing-model"
	auth := &Auth{ID: "count-model-not-found-auth", Provider: "claude"}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	if _, errCount := m.ExecuteCount(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errCount == nil {
		t.Fatal("expected count_tokens model-not-found error")
	}

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to remain registered")
	}
	state := updated.ModelStates[model]
	if state == nil || !state.Unavailable {
		t.Fatalf("expected model-not-found cooldown state, got %#v", state)
	}
	if state.LastError == nil || state.LastError.Code != "model_not_found" {
		t.Fatalf("model state error = %#v, want preserved model_not_found code", state.LastError)
	}
	results := hook.Results()
	if len(results) != 1 || results[0].Error == nil || results[0].Error.Code != "model_not_found" {
		t.Fatalf("hook results = %#v, want preserved model_not_found code", results)
	}
	remaining := time.Until(state.NextRetryAfter)
	if remaining < 11*time.Hour || remaining > 12*time.Hour {
		t.Fatalf("model-not-found cooldown = %v, want about 12h", remaining)
	}
	if count := reg.GetModelCount(model); count != 0 {
		t.Fatalf("available model count = %d, want 0", count)
	}
}

func TestIsCountTokensEndpointNotFoundError(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		model string
		want  bool
	}{
		{
			name: "empty router 404",
			err:  &Error{HTTPStatus: http.StatusNotFound},
			want: true,
		},
		{
			name: "plain router 404",
			err:  &Error{HTTPStatus: http.StatusNotFound, Message: "404 page not found"},
			want: true,
		},
		{
			name: "wrapped router 404",
			err:  &Error{HTTPStatus: http.StatusNotFound, Message: "upstream request failed: 404 page not found"},
			want: true,
		},
		{
			name: "fastapi route 404",
			err:  &Error{HTTPStatus: http.StatusNotFound, Message: `{"detail":"Not Found"}`},
			want: true,
		},
		{
			name: "problem details route 404",
			err:  &Error{HTTPStatus: http.StatusNotFound, Message: `{"title":"Not Found","status":404}`},
			want: true,
		},
		{
			name: "nested generic route 404",
			err:  &Error{HTTPStatus: http.StatusNotFound, Message: `{"error":{"type":"not_found_error","message":"Not Found"}}`},
			want: true,
		},
		{
			name: "generic model api route 404",
			err:  &Error{HTTPStatus: http.StatusNotFound, Message: `{"type":"not_found_error","title":"Model API","detail":"Not Found"}`},
			want: true,
		},
		{
			name: "generic model metadata route 404",
			err:  &Error{HTTPStatus: http.StatusNotFound, Message: `{"error":{"type":"not_found_error","message":"model metadata route not found"}}`},
			want: true,
		},
		{
			name: "generic model provider 404",
			err:  &Error{HTTPStatus: http.StatusNotFound, Message: `{"error":{"type":"not_found_error","message":"model provider was not found"}}`},
			want: true,
		},
		{
			name: "generic route with misleading metadata",
			err:  &Error{HTTPStatus: http.StatusNotFound, Message: `{"message":"Not Found","request_id":"model_not_found"}`},
			want: true,
		},
		{
			name: "express count route 404",
			err:  &Error{HTTPStatus: http.StatusNotFound, Message: "Cannot POST /v1/messages/count_tokens"},
			want: true,
		},
		{
			name: "html route 404",
			err:  &Error{HTTPStatus: http.StatusNotFound, Message: "<html><title>404 Not Found</title></html>"},
			want: true,
		},
		{
			name: "structured model 404",
			err:  &Error{HTTPStatus: http.StatusNotFound, Message: `{"error":{"type":"not_found_error","message":"model claude-missing was not found"}}`},
			want: false,
		},
		{
			name: "anthropic exact model reference",
			err:  &Error{HTTPStatus: http.StatusNotFound, Message: `{"error":{"type":"not_found_error","message":"model: claude-missing"}}`},
			want: false,
		},
		{
			name:  "anthropic model reference with thinking suffix",
			err:   &Error{HTTPStatus: http.StatusNotFound, Message: `{"error":{"type":"not_found_error","message":"model: claude-missing"}}`},
			model: "claude-missing(high)",
			want:  false,
		},
		{
			name: "requested model does not exist",
			err:  &Error{HTTPStatus: http.StatusNotFound, Message: `{"error":{"type":"not_found_error","message":"The requested model does not exist"}}`},
			want: false,
		},
		{
			name:  "requested quoted model could not be found",
			err:   &Error{HTTPStatus: http.StatusNotFound, Message: `{"error":{"type":"not_found_error","message":"The requested model 'foo' could not be found"}}`},
			model: "foo",
			want:  false,
		},
		{
			name: "problem details model type uri",
			err:  &Error{HTTPStatus: http.StatusNotFound, Message: `{"type":"https://example.com/problems/model-not-found","title":"Not Found","status":404}`},
			want: false,
		},
		{
			name: "structured model error string",
			err:  &Error{HTTPStatus: http.StatusNotFound, Message: `{"error":"model claude-missing does not exist"}`},
			want: false,
		},
		{
			name: "model code with generic message",
			err:  &Error{HTTPStatus: http.StatusNotFound, Message: `{"message":"Not Found","code":"model_not_found","model":"claude-missing"}`},
			want: false,
		},
		{
			name: "typed model not found code",
			err:  &Error{Code: "model_not_found", HTTPStatus: http.StatusNotFound, Message: "Not Found"},
			want: false,
		},
		{
			name: "typed wrapper with structured model code",
			err:  &Error{Code: "not_found", HTTPStatus: http.StatusNotFound, Message: `{"error":{"code":"model_not_found","message":"Not Found"}}`},
			want: false,
		},
		{
			name: "wrapped structured model code",
			err: fmt.Errorf("upstream failed: %w", &requestScopedStatusError{
				status:  http.StatusNotFound,
				message: `{"error":{"code":"model_not_found","message":"Not Found"}}`,
			}),
			want: false,
		},
		{
			name: "joined structured model code",
			err: errors.Join(
				errors.New("upstream failed"),
				&requestScopedStatusError{
					status:  http.StatusNotFound,
					message: `{"error":{"code":"model_not_found","message":"Not Found"}}`,
				},
			),
			want: false,
		},
		{
			name: "outer generic inner model 404",
			err:  &Error{HTTPStatus: http.StatusNotFound, Message: `{"message":"Not Found","error":{"type":"not_found_error","message":"model claude-missing does not exist"}}`},
			want: false,
		},
		{
			name: "unstructured model text",
			err:  &Error{HTTPStatus: http.StatusNotFound, Message: "model claude-missing was not found"},
			want: true,
		},
		{
			name: "non 404",
			err:  &Error{HTTPStatus: http.StatusInternalServerError, Message: "404 page not found"},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			model := tc.model
			if model == "" {
				model = "claude-missing"
			}
			if got := isCountTokensEndpointNotFoundError(tc.err, model); got != tc.want {
				t.Fatalf("isCountTokensEndpointNotFoundError() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestManager_Execute_GenericRouteNotFoundStillSuspendsModel(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"messages-route-not-found-auth": &Error{
				HTTPStatus: http.StatusNotFound,
				Message:    "404 page not found",
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "messages-route-not-found-model"
	auth := &Auth{ID: "messages-route-not-found-auth", Provider: "claude"}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	if _, errExecute := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errExecute == nil {
		t.Fatal("expected messages route 404")
	}

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to remain registered")
	}
	state := updated.ModelStates[model]
	if state == nil || !state.Unavailable || state.NextRetryAfter.IsZero() {
		t.Fatalf("expected ordinary messages 404 to suspend model, got %#v", state)
	}
}

func TestManager_RecordResult_AvailabilityNeutralSkipsSchedulerUpdate(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "availability-neutral-auth", Provider: "claude"}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	m.scheduler.mu.Lock()
	provider := m.scheduler.providers[auth.Provider]
	if provider == nil || provider.auths[auth.ID] == nil {
		m.scheduler.mu.Unlock()
		t.Fatal("expected scheduler auth metadata")
	}
	before := provider.auths[auth.ID].auth
	m.scheduler.mu.Unlock()

	m.recordAvailabilityNeutralResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "availability-neutral-model",
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusNotFound, Message: "404 page not found"},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil || updated.Failed != 1 {
		t.Fatalf("updated auth = %#v, want one recorded failure", updated)
	}
	m.scheduler.mu.Lock()
	after := m.scheduler.providers[auth.Provider].auths[auth.ID].auth
	m.scheduler.mu.Unlock()
	if after != before {
		t.Fatal("availability-neutral result unexpectedly replaced scheduler auth snapshot")
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
