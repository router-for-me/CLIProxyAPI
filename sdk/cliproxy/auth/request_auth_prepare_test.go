package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type requestPrepareStore struct {
	saveCount atomic.Int32
	mu        sync.Mutex
	last      *Auth
}

func (s *requestPrepareStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *requestPrepareStore) Save(_ context.Context, auth *Auth) (string, error) {
	s.saveCount.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = auth.Clone()
	return "", nil
}

func (s *requestPrepareStore) Delete(context.Context, string) error { return nil }

func (s *requestPrepareStore) lastAuth() *Auth {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last.Clone()
}

type requestPrepareExecutor struct {
	prepareCalls atomic.Int32
	executeCalls atomic.Int32
	prepareErr   error
	executeErr   error
	mu           sync.Mutex
	observed     []*Auth
}

func (e *requestPrepareExecutor) Identifier() string { return "antigravity" }

func (e *requestPrepareExecutor) ShouldPrepareRequestAuth(auth *Auth) bool {
	return auth == nil || auth.Metadata == nil || testStringValue(auth.Metadata["project_id"]) == ""
}

func (e *requestPrepareExecutor) PrepareRequestAuth(_ context.Context, auth *Auth) (*Auth, error) {
	e.prepareCalls.Add(1)
	if e.prepareErr != nil {
		return nil, e.prepareErr
	}
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["project_id"] = "prepared-project"
	return updated, nil
}

func (e *requestPrepareExecutor) recordPreparedAuth(auth *Auth) error {
	e.executeCalls.Add(1)
	if got := testStringValue(auth.Metadata["project_id"]); got != "prepared-project" {
		return &Error{HTTPStatus: http.StatusBadRequest, Message: "missing prepared project"}
	}
	e.mu.Lock()
	e.observed = append(e.observed, auth.Clone())
	e.mu.Unlock()
	return nil
}

func (e *requestPrepareExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if errPrepared := e.recordPreparedAuth(auth); errPrepared != nil {
		return cliproxyexecutor.Response{}, errPrepared
	}
	if e.executeErr != nil {
		return cliproxyexecutor.Response{}, e.executeErr
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *requestPrepareExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if errPrepared := e.recordPreparedAuth(auth); errPrepared != nil {
		return nil, errPrepared
	}
	if e.executeErr != nil {
		return nil, e.executeErr
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed"}`)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *requestPrepareExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *requestPrepareExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if errPrepared := e.recordPreparedAuth(auth); errPrepared != nil {
		return cliproxyexecutor.Response{}, errPrepared
	}
	if e.executeErr != nil {
		return cliproxyexecutor.Response{}, e.executeErr
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *requestPrepareExecutor) lastObservedAuth() *Auth {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.observed) == 0 {
		return nil
	}
	return e.observed[len(e.observed)-1].Clone()
}

func (e *requestPrepareExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "http not implemented"}
}

type homeRequestPrepareDispatcher struct {
	calls atomic.Int32
}

func (*homeRequestPrepareDispatcher) HeartbeatOK() bool { return true }

func (d *homeRequestPrepareDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	if d.calls.Add(1) > 1 {
		return json.Marshal(homeErrorEnvelope{Error: &homeErrorDetail{Code: homeRequestRetryExceededErrorCode, Message: "no more Home auths"}})
	}
	return json.Marshal(homeAuthDispatchResponse{Auth: Auth{
		ID:       "same-id",
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "home-token", "source": "home"},
	}})
}

func (*homeRequestPrepareDispatcher) AbortAmbiguousDispatch() {}

func TestHomePrepareUsesEphemeralDispatchAuthAcrossExecutionPaths(t *testing.T) {
	for _, path := range []struct {
		name string
		run  func(*Manager, context.Context) error
	}{
		{
			name: "Execute",
			run: func(manager *Manager, ctx context.Context) error {
				_, errExecute := manager.Execute(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: "test-model"}, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "Count",
			run: func(manager *Manager, ctx context.Context) error {
				_, errCount := manager.ExecuteCount(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: "test-model"}, cliproxyexecutor.Options{})
				return errCount
			},
		},
		{
			name: "Stream",
			run: func(manager *Manager, ctx context.Context) error {
				result, errStream := manager.ExecuteStream(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: "test-model"}, cliproxyexecutor.Options{Stream: true})
				if errStream != nil {
					return errStream
				}
				for range result.Chunks {
				}
				return nil
			},
		},
	} {
		t.Run(path.name, func(t *testing.T) {
			store := &requestPrepareStore{}
			executor := &requestPrepareExecutor{}
			manager := NewManager(store, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.PublishHomeDispatch(&homeRequestPrepareDispatcher{}, executionregistry.New(), 1)
			manager.RegisterExecutor(executor)
			localAuth := &Auth{ID: "same-id", Provider: "antigravity", Status: StatusActive, Metadata: map[string]any{"access_token": "local-token", "source": "local"}}
			if _, errRegister := manager.Register(WithSkipPersist(context.Background()), localAuth); errRegister != nil {
				t.Fatalf("register local auth: %v", errRegister)
			}
			if errRun := path.run(manager, context.Background()); errRun != nil {
				t.Fatalf("%s error: %v", path.name, errRun)
			}
			observed := executor.lastObservedAuth()
			if observed == nil {
				t.Fatal("executor did not receive prepared auth")
			}
			if got := testStringValue(observed.Metadata["access_token"]); got != "home-token" {
				t.Fatalf("executor access token = %q, want Home token", got)
			}
			if got := testStringValue(observed.Metadata["source"]); got != "home" {
				t.Fatalf("executor source = %q, want Home metadata", got)
			}
			current, ok := manager.GetByID("same-id")
			if !ok {
				t.Fatal("local auth disappeared")
			}
			if got := testStringValue(current.Metadata["access_token"]); got != "local-token" {
				t.Fatalf("local access token = %q, want unchanged local token", got)
			}
			if got := testStringValue(current.Metadata["source"]); got != "local" {
				t.Fatalf("local source = %q, want unchanged local metadata", got)
			}
		})
	}
}

func TestHomeExecutionResultsDoNotMutateSameIDLocalAuth(t *testing.T) {
	paths := []struct {
		name string
		run  func(*Manager, context.Context) error
	}{
		{
			name: "Execute",
			run: func(manager *Manager, ctx context.Context) error {
				_, errExecute := manager.Execute(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: "test-model"}, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "Count",
			run: func(manager *Manager, ctx context.Context) error {
				_, errCount := manager.ExecuteCount(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: "test-model"}, cliproxyexecutor.Options{})
				return errCount
			},
		},
		{
			name: "Stream",
			run: func(manager *Manager, ctx context.Context) error {
				result, errStream := manager.ExecuteStream(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: "test-model"}, cliproxyexecutor.Options{Stream: true})
				if errStream != nil {
					return errStream
				}
				for range result.Chunks {
				}
				return nil
			},
		},
	}
	outcomes := []struct {
		name       string
		prepareErr error
		executeErr error
	}{
		{name: "success"},
		{name: "execution failure", executeErr: errors.New("upstream failed")},
		{name: "prepare failure", prepareErr: errors.New("prepare failed")},
	}

	for _, path := range paths {
		for _, outcome := range outcomes {
			t.Run(path.name+"/"+outcome.name, func(t *testing.T) {
				store := &requestPrepareStore{}
				hook := &resultCaptureHook{}
				executor := &requestPrepareExecutor{prepareErr: outcome.prepareErr, executeErr: outcome.executeErr}
				manager := NewManager(store, nil, hook)
				manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
				manager.PublishHomeDispatch(&homeRequestPrepareDispatcher{}, executionregistry.New(), 1)
				manager.RegisterExecutor(executor)
				localAuth := &Auth{
					ID:        "same-id",
					Provider:  "antigravity",
					Status:    StatusActive,
					Success:   7,
					Failed:    4,
					UpdatedAt: time.Unix(123, 0),
					Metadata:  map[string]any{"access_token": "local-token", "source": "local"},
					ModelStates: map[string]*ModelState{
						"test-model": {Status: StatusError, Unavailable: true, StatusMessage: "local failure", UpdatedAt: time.Unix(122, 0)},
					},
				}
				if _, errRegister := manager.Register(WithSkipPersist(context.Background()), localAuth); errRegister != nil {
					t.Fatalf("register local auth: %v", errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(localAuth.ID, localAuth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(localAuth.ID) })

				beforeLocal, ok := manager.GetByID(localAuth.ID)
				if !ok {
					t.Fatal("local auth is missing before Home execution")
				}
				beforeScheduler := homeExecutionSchedulerAuthSnapshot(t, manager, localAuth.ID)
				beforeModels := registry.GetGlobalRegistry().GetModelsForClient(localAuth.ID)
				failed := outcome.prepareErr != nil || outcome.executeErr != nil
				if errRun := path.run(manager, context.Background()); failed != (errRun != nil) {
					t.Fatalf("%s error = %v, want failure=%t", path.name, errRun, failed)
				}
				if outcome.prepareErr == nil {
					observed := executor.lastObservedAuth()
					if observed == nil {
						t.Fatal("executor did not receive prepared auth")
					}
					if got := testStringValue(observed.Metadata["access_token"]); got != "home-token" {
						t.Fatalf("executor access token = %q, want Home token", got)
					}
				}
				assertHomeExecutionResultStateUnchanged(t, manager, store, hook, beforeLocal, beforeScheduler, beforeModels)
			})
		}
	}
}

func homeExecutionSchedulerAuthSnapshot(t *testing.T, manager *Manager, authID string) *Auth {
	t.Helper()
	manager.scheduler.mu.Lock()
	defer manager.scheduler.mu.Unlock()
	provider := manager.scheduler.authProviders[authID]
	entry := manager.scheduler.providers[provider]
	if entry == nil || entry.auths[authID] == nil || entry.auths[authID].auth == nil {
		t.Fatalf("scheduler auth %q is missing", authID)
	}
	return entry.auths[authID].auth.Clone()
}

func assertHomeExecutionResultStateUnchanged(t *testing.T, manager *Manager, store *requestPrepareStore, hook *resultCaptureHook, beforeLocal, beforeScheduler *Auth, beforeModels []*registry.ModelInfo) {
	t.Helper()
	current, ok := manager.GetByID(beforeLocal.ID)
	if !ok {
		t.Fatal("local auth disappeared")
	}
	if !reflect.DeepEqual(current, beforeLocal) {
		t.Fatalf("Home execution mutated local auth:\n got %#v\nwant %#v", current, beforeLocal)
	}
	if currentScheduler := homeExecutionSchedulerAuthSnapshot(t, manager, beforeLocal.ID); !reflect.DeepEqual(currentScheduler, beforeScheduler) {
		t.Fatalf("Home execution mutated scheduler auth:\n got %#v\nwant %#v", currentScheduler, beforeScheduler)
	}
	if afterModels := registry.GetGlobalRegistry().GetModelsForClient(beforeLocal.ID); !reflect.DeepEqual(afterModels, beforeModels) {
		t.Fatalf("Home execution mutated global model state:\n got %#v\nwant %#v", afterModels, beforeModels)
	}
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("Home execution save count = %d, want 0", got)
	}
	if results := hook.Results(); len(results) != 1 {
		t.Fatalf("Home execution hook results = %#v, want exactly one ephemeral result", results)
	}
}

func TestManagerExecute_PreparesAndPersistsMissingRequestAuthMetadata(t *testing.T) {
	const model = "gemini-3.1-pro"
	store := &requestPrepareStore{}
	executor := &requestPrepareExecutor{}
	manager := NewManager(store, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "auth-request-prepare",
		Provider: "antigravity",
		Metadata: map[string]any{"access_token": "token"},
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "antigravity", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	resp, errExecute := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute error: %v", errExecute)
	}
	if string(resp.Payload) != "ok" {
		t.Fatalf("payload = %q, want ok", string(resp.Payload))
	}
	if got := executor.prepareCalls.Load(); got != 1 {
		t.Fatalf("prepare calls = %d, want 1", got)
	}
	if got := store.saveCount.Load(); got < 1 {
		t.Fatalf("save count = %d, want at least 1", got)
	}
	if got := testStringValue(store.lastAuth().Metadata["project_id"]); got != "prepared-project" {
		t.Fatalf("persisted project_id = %q, want prepared-project", got)
	}
	current, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("expected auth in manager")
	}
	if got := testStringValue(current.Metadata["project_id"]); got != "prepared-project" {
		t.Fatalf("manager project_id = %q, want prepared-project", got)
	}

	if _, errExecute = manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errExecute != nil {
		t.Fatalf("second Execute error: %v", errExecute)
	}
	if got := executor.prepareCalls.Load(); got != 1 {
		t.Fatalf("prepare calls after second execute = %d, want 1", got)
	}
}

func testStringValue(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []byte:
		return strings.TrimSpace(string(typed))
	default:
		return ""
	}
}
