package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

type aliasRoutingExecutor struct {
	id string

	mu             sync.Mutex
	executeModels  []string
	executeAliases []string
}

func (e *aliasRoutingExecutor) Identifier() string { return e.id }

func (e *aliasRoutingExecutor) Execute(ctx context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.executeModels = append(e.executeModels, req.Model)
	e.executeAliases = append(e.executeAliases, coreusage.RequestedModelAliasFromContext(ctx))
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

func (e *aliasRoutingExecutor) ExecuteAliases() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeAliases))
	copy(out, e.executeAliases)
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

	gotAliases := executor.ExecuteAliases()
	if len(gotAliases) != 1 {
		t.Fatalf("execute aliases len = %d, want 1", len(gotAliases))
	}
	if gotAliases[0] != routeModel {
		t.Fatalf("execute alias = %q, want %q", gotAliases[0], routeModel)
	}
}

func TestManagerPickNextLegacySessionAffinityKeepsOAuthAliasBindingWhenRouteModelBlocked(t *testing.T) {
	const (
		provider    = "antigravity-affinity"
		routeModel  = "claude-opus-4-6-affinity"
		targetModel = "claude-opus-4-6-affinity-thinking"
	)

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	manager := NewManager(nil, selector, nil)
	manager.RegisterExecutor(&aliasRoutingExecutor{id: provider})
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  true,
		}},
	})

	reg := registry.GetGlobalRegistry()
	for _, authID := range []string{"alias-high", "alias-low"} {
		reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: routeModel}, {ID: targetModel}})
	}
	t.Cleanup(func() {
		reg.UnregisterClient("alias-high")
		reg.UnregisterClient("alias-low")
	})

	now := time.Now()
	high := &Auth{
		ID:       "alias-high",
		Provider: provider,
		Status:   StatusActive,
		Attributes: map[string]string{
			"priority": "10",
		},
		Metadata: map[string]any{"email": "high@example.com"},
		ModelStates: map[string]*ModelState{
			targetModel: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: now.Add(time.Hour),
			},
		},
	}
	low := &Auth{
		ID:             "alias-low",
		Provider:       provider,
		Status:         StatusActive,
		Unavailable:    true,
		NextRetryAfter: now.Add(time.Hour),
		Metadata:       map[string]any{"email": "low@example.com"},
		ModelStates: map[string]*ModelState{
			routeModel: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: now.Add(time.Hour),
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), high); errRegister != nil {
		t.Fatalf("register high: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), low); errRegister != nil {
		t.Fatalf("register low: %v", errRegister)
	}

	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"metadata":{"user_id":"user_xxx_account__session_alias-affinity"}}`),
	}
	got, _, errPick := manager.pickNext(context.Background(), provider, routeModel, opts, nil)
	if errPick != nil {
		t.Fatalf("first pickNext() error = %v", errPick)
	}
	if got == nil || got.ID != "alias-low" {
		t.Fatalf("first pickNext() = %#v, want alias-low", got)
	}

	high.ModelStates = map[string]*ModelState{
		routeModel: {
			Unavailable:    false,
			Status:         StatusActive,
			NextRetryAfter: time.Time{},
		},
	}
	if _, errUpdate := manager.Update(context.Background(), high); errUpdate != nil {
		t.Fatalf("update high: %v", errUpdate)
	}

	got, _, errPick = manager.pickNext(context.Background(), provider, routeModel, opts, nil)
	if errPick != nil {
		t.Fatalf("second pickNext() error = %v", errPick)
	}
	if got == nil || got.ID != "alias-low" {
		t.Fatalf("second pickNext() = %#v, want sticky alias-low", got)
	}
}
