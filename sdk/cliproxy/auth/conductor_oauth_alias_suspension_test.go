package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type aliasRoutingExecutor struct {
	id string

	mu            sync.Mutex
	executeModels []string
}

func (e *aliasRoutingExecutor) Identifier() string { return e.id }

func (e *aliasRoutingExecutor) Execute(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.executeModels = append(e.executeModels, req.Model)
	e.mu.Unlock()
	return cliproxyexecutor.Response{Payload: []byte(req.Model)}, nil
}

func (e *aliasRoutingExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "ExecuteStream not implemented"}
}

func (e *aliasRoutingExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *aliasRoutingExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *aliasRoutingExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *aliasRoutingExecutor) ExecuteModels() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeModels))
	copy(out, e.executeModels)
	return out
}

func TestManagerExecute_OAuthAliasBypassesBlockedRouteModel(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "claude-opus-4-6"
		targetModel = "claude-opus-4-6-thinking"
	)

	manager := NewManager(nil, nil, nil)
	executor := &aliasRoutingExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  true,
		}},
	})

	auth := &Auth{
		ID:       "oauth-alias-auth",
		Provider: provider,
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			routeModel: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: time.Now().Add(1 * time.Hour),
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}, {ID: targetModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	manager.RefreshSchedulerEntry(auth.ID)

	resp, errExecute := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success", errExecute)
	}
	if string(resp.Payload) != targetModel {
		t.Fatalf("execute payload = %q, want %q", string(resp.Payload), targetModel)
	}

	gotModels := executor.ExecuteModels()
	if len(gotModels) != 1 {
		t.Fatalf("execute models len = %d, want 1", len(gotModels))
	}
	if gotModels[0] != targetModel {
		t.Fatalf("execute model = %q, want %q", gotModels[0], targetModel)
	}
}
