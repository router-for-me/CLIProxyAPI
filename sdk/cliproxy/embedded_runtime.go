package cliproxy

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

// EmbeddedRuntime provides native provider execution and model registration
// without starting the CLIProxyAPI HTTP server or file watcher.
type EmbeddedRuntime struct {
	manager *coreauth.Manager
	service *Service

	mu                 sync.Mutex
	started            bool
	closed             bool
	modelsReconciled   bool
	activeModels       map[string]struct{}
	registeredAuthIDs  map[string]struct{}
	installedExecutors map[string]coreauth.ProviderExecutor
}

// NewEmbeddedRuntime creates a headless runtime backed by a host-owned auth manager.
func NewEmbeddedRuntime(cfg *config.Config, manager *coreauth.Manager) (*EmbeddedRuntime, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy: embedded runtime configuration is required")
	}
	if manager == nil {
		return nil, fmt.Errorf("cliproxy: embedded runtime auth manager is required")
	}

	manager.SetRoundTripperProvider(newDefaultRoundTripperProvider())
	manager.SetConfig(cfg)
	manager.SetOAuthModelAlias(cfg.OAuthModelAlias)

	return &EmbeddedRuntime{
		manager:            manager,
		service:            &Service{cfg: cfg, coreManager: manager},
		registeredAuthIDs:  make(map[string]struct{}),
		installedExecutors: make(map[string]coreauth.ProviderExecutor),
	}, nil
}

// Start registers native executors and model projections for existing auths.
func (r *EmbeddedRuntime) Start(ctx context.Context) error {
	if r == nil {
		return fmt.Errorf("cliproxy: embedded runtime is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fmt.Errorf("cliproxy: embedded runtime is closed")
	}
	if r.started {
		return nil
	}

	auths := r.manager.List()
	r.service.registerAvailableExecutors(ctx, executorRegistrationOptions{
		auths:             auths,
		forceReplaceAuths: true,
	})
	for _, auth := range auths {
		if errContext := ctx.Err(); errContext != nil {
			return errContext
		}
		r.trackExecutorForAuthLocked(auth)
		r.registerAuthModelsLocked(ctx, auth)
	}
	r.started = true
	return nil
}

// SyncAuth adds or updates one auth in the native runtime without persisting it.
func (r *EmbeddedRuntime) SyncAuth(ctx context.Context, auth *coreauth.Auth) error {
	if r == nil {
		return fmt.Errorf("cliproxy: embedded runtime is nil")
	}
	if auth == nil || strings.TrimSpace(auth.ID) == "" {
		return fmt.Errorf("cliproxy: embedded runtime auth ID is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if errState := r.requireStartedLocked(); errState != nil {
		return errState
	}

	runtimeCtx := coreauth.WithSkipPersist(ctx)
	prepared := r.service.prepareCoreAuthForModelRegistration(runtimeCtx, auth)
	if prepared == nil {
		return fmt.Errorf("cliproxy: failed to synchronize auth %q", auth.ID)
	}
	r.trackExecutorForAuthLocked(prepared)
	r.registerAuthModelsLocked(runtimeCtx, prepared)
	return nil
}

// RemoveAuth removes one auth and its model projection from the native runtime.
func (r *EmbeddedRuntime) RemoveAuth(ctx context.Context, authID string) error {
	if r == nil {
		return fmt.Errorf("cliproxy: embedded runtime is nil")
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return fmt.Errorf("cliproxy: embedded runtime auth ID is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if errState := r.requireStartedLocked(); errState != nil {
		return errState
	}

	r.service.applyCoreAuthRemoval(coreauth.WithSkipPersist(ctx), authID)
	delete(r.registeredAuthIDs, authID)
	return nil
}

// ReconcileModels limits each auth's native model projection to active model IDs.
func (r *EmbeddedRuntime) ReconcileModels(ctx context.Context, activeModels []string) error {
	if r == nil {
		return fmt.Errorf("cliproxy: embedded runtime is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if errState := r.requireStartedLocked(); errState != nil {
		return errState
	}

	r.activeModels = make(map[string]struct{}, len(activeModels))
	for _, modelID := range activeModels {
		modelID = strings.TrimSpace(modelID)
		if modelID != "" {
			r.activeModels[modelID] = struct{}{}
		}
	}
	r.modelsReconciled = true

	for _, auth := range r.manager.List() {
		if errContext := ctx.Err(); errContext != nil {
			return errContext
		}
		r.registerAuthModelsLocked(ctx, auth)
	}
	return nil
}

// Close removes model projections owned by the runtime. Host-owned auths remain registered.
func (r *EmbeddedRuntime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	for authID := range r.registeredAuthIDs {
		registry.GetGlobalRegistry().UnregisterClient(authID)
		r.manager.RefreshSchedulerEntry(authID)
	}
	for provider, installed := range r.installedExecutors {
		current, okExecutor := r.manager.Executor(provider)
		if !okExecutor || !sameProviderExecutor(current, installed) {
			continue
		}
		if closer, okCloser := current.(coreauth.ExecutionSessionCloser); okCloser {
			closer.CloseExecutionSession(coreauth.CloseAllExecutionSessionsID)
		}
		r.manager.UnregisterExecutor(provider)
	}
	r.registeredAuthIDs = make(map[string]struct{})
	r.installedExecutors = make(map[string]coreauth.ProviderExecutor)
	r.closed = true
	return nil
}

// ServerStarted reports whether an HTTP server was constructed by this runtime.
func (r *EmbeddedRuntime) ServerStarted() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.service != nil && r.service.server != nil
}

// WatcherStarted reports whether a file watcher was constructed by this runtime.
func (r *EmbeddedRuntime) WatcherStarted() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.service != nil && r.service.watcher != nil
}

func (r *EmbeddedRuntime) requireStartedLocked() error {
	if r.closed {
		return fmt.Errorf("cliproxy: embedded runtime is closed")
	}
	if !r.started {
		return fmt.Errorf("cliproxy: embedded runtime is not started")
	}
	return nil
}

func (r *EmbeddedRuntime) registerAuthModelsLocked(ctx context.Context, auth *coreauth.Auth) {
	if auth == nil || strings.TrimSpace(auth.ID) == "" {
		return
	}
	var activeModels map[string]struct{}
	if r.modelsReconciled {
		activeModels = r.activeModels
	}
	r.service.reconcileModelsForAuth(ctx, auth, activeModels)
	r.registeredAuthIDs[auth.ID] = struct{}{}
}

func (r *EmbeddedRuntime) trackExecutorForAuthLocked(auth *coreauth.Auth) {
	provider := embeddedRuntimeExecutorProvider(auth)
	if provider == "" {
		return
	}
	executor, okExecutor := r.manager.Executor(provider)
	if okExecutor && executor != nil {
		r.installedExecutors[provider] = executor
	}
}

func embeddedRuntimeExecutorProvider(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if providerKey, _, isCompat := openAICompatInfoFromAuth(auth); isCompat {
		if providerKey != "" {
			return providerKey
		}
		return "openai-compatibility"
	}
	return strings.ToLower(strings.TrimSpace(auth.Provider))
}

func sameProviderExecutor(first, second coreauth.ProviderExecutor) bool {
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	firstType := reflect.TypeOf(first)
	if firstType != reflect.TypeOf(second) || !firstType.Comparable() {
		return false
	}
	return first == second
}
