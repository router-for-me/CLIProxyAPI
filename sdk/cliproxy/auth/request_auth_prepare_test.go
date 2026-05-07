package auth

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
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
}

func (e *requestPrepareExecutor) Identifier() string { return "antigravity" }

func (e *requestPrepareExecutor) ShouldPrepareRequestAuth(auth *Auth) bool {
	return auth == nil || auth.Metadata == nil || testStringValue(auth.Metadata["project_id"]) == ""
}

func (e *requestPrepareExecutor) PrepareRequestAuth(_ context.Context, auth *Auth) (*Auth, error) {
	e.prepareCalls.Add(1)
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["project_id"] = "prepared-project"
	return updated, nil
}

func (e *requestPrepareExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.executeCalls.Add(1)
	if got := testStringValue(auth.Metadata["project_id"]); got != "prepared-project" {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusBadRequest, Message: "missing prepared project"}
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *requestPrepareExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "stream not implemented"}
}

func (e *requestPrepareExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *requestPrepareExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "count not implemented"}
}

func (e *requestPrepareExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "http not implemented"}
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
