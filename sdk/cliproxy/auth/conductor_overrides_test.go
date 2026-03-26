package auth

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

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
	countCalls        []string
	streamCalls       []string
	executeErrors     map[string]error
	countErrors       map[string]error
	streamFirstErrors map[string]error
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
	e.countCalls = append(e.countCalls, auth.ID)
	err := e.countErrors[auth.ID]
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

func (e *authFallbackExecutor) CountCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.countCalls))
	copy(out, e.countCalls)
	return out
}

type deleteTrackingStore struct {
	mu         sync.Mutex
	deletedIDs []string
}

func (s *deleteTrackingStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *deleteTrackingStore) Save(context.Context, *Auth) (string, error) { return "", nil }

func (s *deleteTrackingStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletedIDs = append(s.deletedIDs, id)
	return nil
}

func (s *deleteTrackingStore) DeletedIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.deletedIDs))
	copy(out, s.deletedIDs)
	return out
}

func newUnauthorizedEvictionTestManager(t *testing.T) (*Manager, *authFallbackExecutor, *deleteTrackingStore, string, string, string) {
	t.Helper()

	const model = "test-model"
	const badAuthID = "aa-bad-auth"
	const goodAuthID = "bb-good-auth"

	store := &deleteTrackingStore{}
	selector := &SequentialFillSelector{
		current: map[string]string{
			"claude:" + model: badAuthID,
		},
	}
	manager := NewManager(store, selector, nil)
	manager.SetRetryConfig(0, 0, 1)

	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			badAuthID: &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
		},
		countErrors: map[string]error{
			badAuthID: &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
		},
		streamFirstErrors: map[string]error{
			badAuthID: &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
		},
	}
	manager.RegisterExecutor(executor)

	badAuth := &Auth{ID: badAuthID, Provider: "claude", Metadata: map[string]any{"type": "claude"}}
	goodAuth := &Auth{ID: goodAuthID, Provider: "claude", Metadata: map[string]any{"type": "claude"}}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(badAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(badAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := manager.Register(context.Background(), badAuth); errRegister != nil {
		t.Fatalf("register bad auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	return manager, executor, store, model, badAuthID, goodAuthID
}

func assertUnauthorizedAuthEvicted(t *testing.T, manager *Manager, store *deleteTrackingStore, badAuthID string) {
	t.Helper()
	if _, ok := manager.GetByID(badAuthID); ok {
		t.Fatalf("expected unauthorized auth %q to be evicted", badAuthID)
	}
	gotDeleted := store.DeletedIDs()
	if len(gotDeleted) != 1 || gotDeleted[0] != badAuthID {
		t.Fatalf("deleted auth IDs = %v, want [%s]", gotDeleted, badAuthID)
	}
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

func TestManager_Execute_UnauthorizedAuthEviction(t *testing.T) {
	manager, executor, store, model, badAuthID, goodAuthID := newUnauthorizedEvictionTestManager(t)

	var buf bytes.Buffer
	logger := log.StandardLogger()
	oldOut := logger.Out
	oldFormatter := logger.Formatter
	oldLevel := logger.Level
	log.SetOutput(&buf)
	log.SetFormatter(&log.TextFormatter{DisableTimestamp: true, DisableColors: true})
	log.SetLevel(log.InfoLevel)
	defer func() {
		log.SetOutput(oldOut)
		log.SetFormatter(oldFormatter)
		log.SetLevel(oldLevel)
	}()

	resp, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success", errExecute)
	}
	if string(resp.Payload) != goodAuthID {
		t.Fatalf("execute payload = %q, want %q", string(resp.Payload), goodAuthID)
	}
	if gotCalls := executor.ExecuteCalls(); len(gotCalls) != 2 || gotCalls[0] != badAuthID || gotCalls[1] != goodAuthID {
		t.Fatalf("execute calls = %v, want [%s %s]", gotCalls, badAuthID, goodAuthID)
	}
	assertUnauthorizedAuthEvicted(t, manager, store, badAuthID)
	logOutput := buf.String()
	if !strings.Contains(logOutput, "evicting unauthorized auth") {
		t.Fatalf("expected info log for unauthorized auth eviction, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, badAuthID) {
		t.Fatalf("expected log to contain auth id %q, got: %s", badAuthID, logOutput)
	}
	if !strings.Contains(logOutput, model) {
		t.Fatalf("expected log to contain model %q, got: %s", model, logOutput)
	}
}

func TestManager_ExecuteCount_UnauthorizedAuthEviction(t *testing.T) {
	manager, executor, store, model, badAuthID, goodAuthID := newUnauthorizedEvictionTestManager(t)

	resp, errExecute := manager.ExecuteCount(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute count error = %v, want success", errExecute)
	}
	if string(resp.Payload) != goodAuthID {
		t.Fatalf("execute count payload = %q, want %q", string(resp.Payload), goodAuthID)
	}
	if gotCalls := executor.CountCalls(); len(gotCalls) != 2 || gotCalls[0] != badAuthID || gotCalls[1] != goodAuthID {
		t.Fatalf("count calls = %v, want [%s %s]", gotCalls, badAuthID, goodAuthID)
	}
	assertUnauthorizedAuthEvicted(t, manager, store, badAuthID)
}

func TestManager_ExecuteStream_UnauthorizedAuthEviction(t *testing.T) {
	manager, executor, store, model, badAuthID, goodAuthID := newUnauthorizedEvictionTestManager(t)

	streamResult, errExecute := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute stream error = %v, want success", errExecute)
	}
	var payload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("execute stream chunk error = %v, want success", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if string(payload) != goodAuthID {
		t.Fatalf("execute stream payload = %q, want %q", string(payload), goodAuthID)
	}
	if gotCalls := executor.StreamCalls(); len(gotCalls) != 2 || gotCalls[0] != badAuthID || gotCalls[1] != goodAuthID {
		t.Fatalf("stream calls = %v, want [%s %s]", gotCalls, badAuthID, goodAuthID)
	}
	assertUnauthorizedAuthEvicted(t, manager, store, badAuthID)
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
