package cliproxy_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type embeddedRuntimeTestExecutor struct {
	provider string
}

func (e *embeddedRuntimeTestExecutor) Identifier() string { return e.provider }

func (e *embeddedRuntimeTestExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *embeddedRuntimeTestExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	chunks := make(chan cliproxyexecutor.StreamChunk)
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *embeddedRuntimeTestExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *embeddedRuntimeTestExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *embeddedRuntimeTestExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestEmbeddedRuntimeRegistersNativeExecutorsWithoutServer(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(t.Context(), &coreauth.Auth{ID: "codex-1", Provider: "codex"}); errRegister != nil {
		t.Fatalf("register codex auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(t.Context(), &coreauth.Auth{ID: "claude-1", Provider: "claude"}); errRegister != nil {
		t.Fatalf("register claude auth: %v", errRegister)
	}

	runtime, errRuntime := cliproxy.NewEmbeddedRuntime(&config.Config{}, manager)
	if errRuntime != nil {
		t.Fatalf("new embedded runtime: %v", errRuntime)
	}
	if errStart := runtime.Start(t.Context()); errStart != nil {
		t.Fatalf("start embedded runtime: %v", errStart)
	}
	t.Cleanup(func() {
		if errClose := runtime.Close(context.Background()); errClose != nil {
			t.Errorf("close embedded runtime: %v", errClose)
		}
	})

	codexExecutor, okCodex := manager.Executor("codex")
	if !okCodex {
		t.Fatal("codex executor was not registered")
	}
	if _, okNative := codexExecutor.(*runtimeexecutor.CodexAutoExecutor); !okNative {
		t.Fatalf("codex executor type = %T, want *executor.CodexAutoExecutor", codexExecutor)
	}

	claudeExecutor, okClaude := manager.Executor("claude")
	if !okClaude {
		t.Fatal("claude executor was not registered")
	}
	if _, okNative := claudeExecutor.(*runtimeexecutor.ClaudeExecutor); !okNative {
		t.Fatalf("claude executor type = %T, want *executor.ClaudeExecutor", claudeExecutor)
	}

	if runtime.ServerStarted() {
		t.Fatal("embedded runtime started an HTTP server")
	}
	if runtime.WatcherStarted() {
		t.Fatal("embedded runtime started a file watcher")
	}
}

func TestEmbeddedRuntimeSyncRemoveAndReconcileModels(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	runtime, errRuntime := cliproxy.NewEmbeddedRuntime(&config.Config{}, manager)
	if errRuntime != nil {
		t.Fatalf("new embedded runtime: %v", errRuntime)
	}
	if errStart := runtime.Start(t.Context()); errStart != nil {
		t.Fatalf("start embedded runtime: %v", errStart)
	}
	t.Cleanup(func() {
		if errClose := runtime.Close(context.Background()); errClose != nil {
			t.Errorf("close embedded runtime: %v", errClose)
		}
	})

	auth := &coreauth.Auth{ID: "codex-sync-1", Provider: "codex"}
	if errSync := runtime.SyncAuth(t.Context(), auth); errSync != nil {
		t.Fatalf("sync auth: %v", errSync)
	}
	if _, okAuth := manager.GetByID(auth.ID); !okAuth {
		t.Fatal("synced auth is missing from manager")
	}

	models := registry.GetCodexProModels()
	if len(models) < 2 {
		t.Fatalf("codex model fixture count = %d, want at least 2", len(models))
	}
	keptModel := models[0].ID
	removedModel := models[1].ID
	if !cliproxy.GlobalModelRegistry().ClientSupportsModel(auth.ID, keptModel) {
		t.Fatalf("synced auth does not support native model %q", keptModel)
	}

	if errReconcile := runtime.ReconcileModels(t.Context(), []string{keptModel}); errReconcile != nil {
		t.Fatalf("reconcile models: %v", errReconcile)
	}
	if !cliproxy.GlobalModelRegistry().ClientSupportsModel(auth.ID, keptModel) {
		t.Fatalf("reconciled auth does not support active model %q", keptModel)
	}
	if cliproxy.GlobalModelRegistry().ClientSupportsModel(auth.ID, removedModel) {
		t.Fatalf("reconciled auth still supports inactive model %q", removedModel)
	}

	if errRemove := runtime.RemoveAuth(t.Context(), auth.ID); errRemove != nil {
		t.Fatalf("remove auth: %v", errRemove)
	}
	if _, okAuth := manager.GetByID(auth.ID); okAuth {
		t.Fatal("removed auth is still present in manager")
	}
	if cliproxy.GlobalModelRegistry().ClientSupportsModel(auth.ID, keptModel) {
		t.Fatalf("removed auth still supports model %q", keptModel)
	}
}

func TestEmbeddedRuntimeCloseUnregistersModelsWithoutRemovingAuth(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{ID: "claude-close-1", Provider: "claude"}
	if _, errRegister := manager.Register(t.Context(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	runtime, errRuntime := cliproxy.NewEmbeddedRuntime(&config.Config{}, manager)
	if errRuntime != nil {
		t.Fatalf("new embedded runtime: %v", errRuntime)
	}
	if errStart := runtime.Start(t.Context()); errStart != nil {
		t.Fatalf("start embedded runtime: %v", errStart)
	}

	models := registry.GetClaudeModels()
	if len(models) == 0 {
		t.Fatal("claude model fixture is empty")
	}
	model := models[0].ID
	if !cliproxy.GlobalModelRegistry().ClientSupportsModel(auth.ID, model) {
		t.Fatalf("started runtime does not register model %q", model)
	}

	if errClose := runtime.Close(t.Context()); errClose != nil {
		t.Fatalf("close embedded runtime: %v", errClose)
	}
	if _, okAuth := manager.GetByID(auth.ID); !okAuth {
		t.Fatal("closing runtime removed host-owned auth")
	}
	if cliproxy.GlobalModelRegistry().ClientSupportsModel(auth.ID, model) {
		t.Fatalf("closed runtime still registers model %q", model)
	}
	if _, okExecutor := manager.Executor("claude"); okExecutor {
		t.Fatal("closed runtime still exposes its claude executor")
	}
}

func TestEmbeddedRuntimePreservesHostExecutor(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	hostExecutor := &embeddedRuntimeTestExecutor{provider: "codex"}
	manager.RegisterExecutor(hostExecutor)
	if _, errRegister := manager.Register(t.Context(), &coreauth.Auth{ID: "codex-host-1", Provider: "codex"}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	runtime, errRuntime := cliproxy.NewEmbeddedRuntime(&config.Config{}, manager)
	if errRuntime != nil {
		t.Fatalf("new embedded runtime: %v", errRuntime)
	}
	if errStart := runtime.Start(t.Context()); errStart != nil {
		t.Fatalf("start embedded runtime: %v", errStart)
	}

	resolved, okResolved := manager.Executor("codex")
	if !okResolved || resolved != hostExecutor {
		t.Fatalf("executor after start = %T, want host executor", resolved)
	}
	if errClose := runtime.Close(t.Context()); errClose != nil {
		t.Fatalf("close embedded runtime: %v", errClose)
	}
	resolved, okResolved = manager.Executor("codex")
	if !okResolved || resolved != hostExecutor {
		t.Fatalf("executor after close = %T, want host executor", resolved)
	}
}

func TestEmbeddedRuntimeDoesNotProjectModelsWithoutExecutor(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{ID: "aistudio-headless-1", Provider: "aistudio"}
	if _, errRegister := manager.Register(t.Context(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	runtime, errRuntime := cliproxy.NewEmbeddedRuntime(&config.Config{}, manager)
	if errRuntime != nil {
		t.Fatalf("new embedded runtime: %v", errRuntime)
	}
	if errStart := runtime.Start(t.Context()); errStart != nil {
		t.Fatalf("start embedded runtime: %v", errStart)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })

	if _, okExecutor := manager.Executor("aistudio"); okExecutor {
		t.Fatal("headless runtime unexpectedly installed an AI Studio executor")
	}
	for _, model := range registry.GetAIStudioModels() {
		if model != nil && cliproxy.GlobalModelRegistry().ClientSupportsModel(auth.ID, model.ID) {
			t.Fatalf("auth without executor supports model %q", model.ID)
		}
	}
}

func TestEmbeddedRuntimeReconcileModelsIsCaseInsensitive(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{ID: "codex-case-1", Provider: "codex"}
	if _, errRegister := manager.Register(t.Context(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	runtime, errRuntime := cliproxy.NewEmbeddedRuntime(&config.Config{}, manager)
	if errRuntime != nil {
		t.Fatalf("new embedded runtime: %v", errRuntime)
	}
	if errStart := runtime.Start(t.Context()); errStart != nil {
		t.Fatalf("start embedded runtime: %v", errStart)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })

	models := registry.GetCodexProModels()
	if len(models) == 0 {
		t.Fatal("codex model fixture is empty")
	}
	keptModel := models[0].ID
	if errReconcile := runtime.ReconcileModels(t.Context(), []string{strings.ToUpper(keptModel)}); errReconcile != nil {
		t.Fatalf("reconcile models: %v", errReconcile)
	}
	if !cliproxy.GlobalModelRegistry().ClientSupportsModel(auth.ID, keptModel) {
		t.Fatalf("case-insensitive reconciliation removed model %q", keptModel)
	}
}

func TestEmbeddedRuntimeCanceledStartRollsBackOwnedExecutors(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(t.Context(), &coreauth.Auth{ID: "codex-cancel-1", Provider: "codex"}); errRegister != nil {
		t.Fatalf("register codex auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(t.Context(), &coreauth.Auth{ID: "claude-cancel-1", Provider: "claude"}); errRegister != nil {
		t.Fatalf("register claude auth: %v", errRegister)
	}
	runtime, errRuntime := cliproxy.NewEmbeddedRuntime(&config.Config{}, manager)
	if errRuntime != nil {
		t.Fatalf("new embedded runtime: %v", errRuntime)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if errStart := runtime.Start(ctx); errStart == nil {
		t.Fatal("canceled start returned nil error")
	}
	if _, okExecutor := manager.Executor("codex"); okExecutor {
		t.Fatal("canceled start leaked codex executor")
	}
	if _, okExecutor := manager.Executor("claude"); okExecutor {
		t.Fatal("canceled start leaked claude executor")
	}
}
