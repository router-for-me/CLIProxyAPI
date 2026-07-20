package cliproxy

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type serviceCodexPlanRefreshExecutor struct{}

func (serviceCodexPlanRefreshExecutor) Identifier() string { return "codex" }

func (serviceCodexPlanRefreshExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (serviceCodexPlanRefreshExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (serviceCodexPlanRefreshExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes["plan_type"] = "plus"
	return auth, nil
}

func (serviceCodexPlanRefreshExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (serviceCodexPlanRefreshExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestServiceCodexPlanRefreshReregistersPlusModels(t *testing.T) {
	ctx := context.Background()
	manager := coreauth.NewManager(nil, nil, nil)
	service, errBuild := NewBuilder().
		WithConfig(&config.Config{}).
		WithConfigPath(filepath.Join(t.TempDir(), "config.yaml")).
		WithCoreAuthManager(manager).
		Build()
	if errBuild != nil {
		t.Fatalf("Build() error = %v", errBuild)
	}
	service.watcher = &WatcherWrapper{
		dispatchPersistedAuth: func(watcher.AuthUpdate) bool { return true },
	}
	manager.RegisterExecutor(serviceCodexPlanRefreshExecutor{})

	auth := &coreauth.Auth{
		ID:         "codex-free-to-plus",
		Provider:   "codex",
		Attributes: map[string]string{"plan_type": "free"},
		Metadata: map[string]any{
			"access_token":  "stale-access-token",
			"refresh_token": "refresh-token",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	service.registerModelsForAuth(ctx, auth)
	manager.RefreshSchedulerEntry(auth.ID)
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	if hasModel(registry.GetGlobalRegistry().GetModelsForClient(auth.ID), "gpt-5.6-sol") {
		t.Fatal("free Codex auth unexpectedly exposes gpt-5.6-sol before refresh")
	}

	refreshed, errRefresh := manager.RefreshAuth(ctx, auth.ID)
	if errRefresh != nil {
		t.Fatalf("RefreshAuth() error = %v", errRefresh)
	}
	if refreshed == nil || refreshed.Attributes["plan_type"] != "plus" {
		t.Fatalf("refreshed auth = %#v, want plus plan", refreshed)
	}
	if !hasModel(registry.GetGlobalRegistry().GetModelsForClient(auth.ID), "gpt-5.6-sol") {
		t.Fatal("plus Codex auth does not expose gpt-5.6-sol after refresh")
	}
}

func hasModel(models []*registry.ModelInfo, id string) bool {
	for _, model := range models {
		if model != nil && model.ID == id {
			return true
		}
	}
	return false
}
