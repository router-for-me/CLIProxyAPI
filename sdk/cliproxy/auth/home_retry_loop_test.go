package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type repeatedHomeAuthDispatcher struct {
	calls atomic.Int32
}

func (d *repeatedHomeAuthDispatcher) HeartbeatOK() bool {
	return true
}

func (d *repeatedHomeAuthDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	d.calls.Add(1)
	raw, _ := json.Marshal(homeAuthDispatchResponse{
		Auth: Auth{
			ID:       "home-auth-1",
			Provider: "home-loop-test",
			Status:   StatusActive,
			Metadata: map[string]any{"email": "loop@example.com"},
		},
	})
	return raw, nil
}

func (*repeatedHomeAuthDispatcher) AbortAmbiguousDispatch() {}

type unauthorizedHomeExecutor struct {
	calls atomic.Int32
}

func (e *unauthorizedHomeExecutor) Identifier() string { return "home-loop-test" }

func (e *unauthorizedHomeExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls.Add(1)
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusUnauthorized, Message: "missing access token"}
}

func (e *unauthorizedHomeExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.calls.Add(1)
	return nil, &Error{HTTPStatus: http.StatusUnauthorized, Message: "missing access token"}
}

func (e *unauthorizedHomeExecutor) Refresh(context.Context, *Auth) (*Auth, error) {
	return nil, &Error{HTTPStatus: http.StatusUnauthorized, Message: "missing access token"}
}

func (e *unauthorizedHomeExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls.Add(1)
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusUnauthorized, Message: "missing access token"}
}

func (e *unauthorizedHomeExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusUnauthorized, Message: "missing access token"}
}

func TestManagerExecuteHomeStopsWhenDispatchRepeatsTriedAuth(t *testing.T) {
	dispatcher := &repeatedHomeAuthDispatcher{}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher {
		return dispatcher
	}
	t.Cleanup(func() {
		currentHomeDispatcher = oldCurrentHomeDispatcher
	})

	executor := &unauthorizedHomeExecutor{}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetHomeExecutionRegistry(executionregistry.New())
	manager.RegisterExecutor(executor)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := manager.Execute(ctx, []string{"home-loop-test"}, cliproxyexecutor.Request{Model: "gemini-3.5-flash-low"}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("Execute error = nil, want missing access token")
	}
	if statusCodeFromError(err) != http.StatusUnauthorized {
		t.Fatalf("Execute error status = %d, want 401 (%v)", statusCodeFromError(err), err)
	}
	if got := executor.calls.Load(); got != 1 {
		t.Fatalf("executor calls = %d, want 1", got)
	}
	if got := dispatcher.calls.Load(); got != 2 {
		t.Fatalf("home dispatch calls = %d, want 2", got)
	}
}

type imageHomeDispatcher struct {
	counts []int
}

func (d *imageHomeDispatcher) HeartbeatOK() bool { return true }

func (d *imageHomeDispatcher) RPopAuth(_ context.Context, model, _ string, _ http.Header, count int) ([]byte, error) {
	d.counts = append(d.counts, count)
	authID := map[int]string{1: "home-chat", 2: "home-image-failing", 3: "home-image-ready"}[count]
	raw, _ := json.Marshal(homeAuthDispatchResponse{
		Model: model,
		Auth:  Auth{ID: authID, Provider: "home-image-test", Status: StatusActive},
	})
	return raw, nil
}

func (*imageHomeDispatcher) AbortAmbiguousDispatch() {}

type imageHomeExecutor struct {
	authIDs []string
}

func (e *imageHomeExecutor) Identifier() string { return "home-image-test" }

func (e *imageHomeExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.authIDs = append(e.authIDs, auth.ID)
	if auth.ID == "home-image-failing" {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusBadGateway, Message: "image upstream failed"}
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *imageHomeExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *imageHomeExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) { return auth, nil }

func (e *imageHomeExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *imageHomeExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestManagerExecuteHomeSkipsKnownChatOnlyImageAuth(t *testing.T) {
	dispatcher := &imageHomeDispatcher{}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher { return dispatcher }
	t.Cleanup(func() { currentHomeDispatcher = oldCurrentHomeDispatcher })

	const model = "shared-image"
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient("home-chat", "home-image-test", []*registry.ModelInfo{{ID: model}})
	registryRef.RegisterClient("home-image-failing", "home-image-test", []*registry.ModelInfo{{ID: model, SupportsImageAPI: true}})
	registryRef.RegisterClient("home-image-ready", "home-image-test", []*registry.ModelInfo{{ID: model, SupportsImageAPI: true}})
	t.Cleanup(func() {
		registryRef.UnregisterClient("home-chat")
		registryRef.UnregisterClient("home-image-failing")
		registryRef.UnregisterClient("home-image-ready")
	})

	executor := &imageHomeExecutor{}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetHomeExecutionRegistry(executionregistry.New())
	manager.RegisterExecutor(executor)
	resp, err := manager.Execute(context.Background(), []string{"home-image-test"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.ImageExecutionMetadataKey: true},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := string(resp.Payload); got != "home-image-ready" {
		t.Fatalf("Execute() payload = %q, want home-image-ready", got)
	}
	if got, want := dispatcher.counts, []int{1, 2, 3}; !slices.Equal(got, want) {
		t.Fatalf("home dispatch counts = %v, want %v", got, want)
	}
	if got, want := executor.authIDs, []string{"home-image-failing", "home-image-ready"}; !slices.Equal(got, want) {
		t.Fatalf("executor auth IDs = %v, want %v", got, want)
	}
}

func TestHomeImageEligibilityPrefersRequestedModelRegistration(t *testing.T) {
	const authID = "home-shared-route"
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient(authID, "home-image-test", []*registry.ModelInfo{
		{ID: "public-model"},
		{ID: "upstream-model", SupportsImageAPI: true},
	})
	t.Cleanup(func() { registryRef.UnregisterClient(authID) })

	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.ImageExecutionMetadataKey: true}}
	if !homeAuthKnownIneligibleForImage(&Auth{ID: authID}, "public-model", "upstream-model", opts) {
		t.Fatal("chat-only requested model registration must not inherit upstream image capability")
	}
	if homeAuthKnownIneligibleForImage(&Auth{ID: authID}, "missing-public-model", "upstream-model", opts) {
		t.Fatal("upstream image registration should be used when the requested model is not registered")
	}
}
