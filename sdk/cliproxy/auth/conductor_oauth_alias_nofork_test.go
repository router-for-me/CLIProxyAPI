package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

type noForkAliasTestExecutor struct {
	id string

	mu             sync.Mutex
	executeModels  []string
	executeAliases []string
	lastAuthID     string
}

func (e *noForkAliasTestExecutor) Identifier() string { return e.id }

func (e *noForkAliasTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.executeModels = append(e.executeModels, req.Model)
	e.executeAliases = append(e.executeAliases, coreusage.RequestedModelAliasFromContext(ctx))
	if auth != nil {
		e.lastAuthID = auth.ID
	}
	e.mu.Unlock()
	return cliproxyexecutor.Response{Payload: []byte(req.Model)}, nil
}

func (e *noForkAliasTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "ExecuteStream not implemented"}
}

func (e *noForkAliasTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *noForkAliasTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *noForkAliasTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *noForkAliasTestExecutor) ExecuteModels() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeModels))
	copy(out, e.executeModels)
	return out
}

func (e *noForkAliasTestExecutor) ExecuteAliases() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeAliases))
	copy(out, e.executeAliases)
	return out
}

func (e *noForkAliasTestExecutor) LastAuthID() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastAuthID
}

// Test 1: no-fork alias retains cooldown across reconcile
func TestManager_NoForkAlias_RetainsCooldownAfterReconcile(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gemini-3.7-flash-high"
		targetModel = "gemini-3.7-flash-high"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	retryAfter := time.Now().Add(30 * time.Minute)
	auth := &Auth{
		ID:       "nofork-auth-1",
		Provider: provider,
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			targetModel: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: retryAfter,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "rate_limit_exceeded",
					NextRecoverAt: retryAfter,
				},
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// Perform reconciliation
	manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if currentAuth == nil {
		t.Fatal("auth not found after reconcile")
	}
	state, exists := currentAuth.ModelStates[targetModel]
	if !exists || state == nil {
		t.Fatalf("targetModel %q state was deleted or nil after reconcile", targetModel)
	}
	if !state.Unavailable {
		t.Fatalf("targetModel state Unavailable = false, want true")
	}
	if !state.NextRetryAfter.Equal(retryAfter) {
		t.Fatalf("targetModel state NextRetryAfter = %v, want %v", state.NextRetryAfter, retryAfter)
	}
	if !state.Quota.Exceeded {
		t.Fatalf("targetModel state Quota.Exceeded = false, want true")
	}

	// Verify registry projection
	if !reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q was not suspended in registry for client %s", routeModel, auth.ID)
	}
}

// Test 2: requesting alias skips cooling credential and executes on available credential
func TestManager_NoForkAlias_BypassesCoolingAuthOnSubsequentRequest(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gemini-3.7-flash-high"
		targetModel = "gemini-3.7-flash-high"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	retryAfter := time.Now().Add(30 * time.Minute)
	auth1 := &Auth{
		ID:       "nofork-cooling-auth",
		Provider: provider,
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			targetModel: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: retryAfter,
			},
		},
	}
	auth2 := &Auth{
		ID:       "nofork-available-auth",
		Provider: provider,
		Status:   StatusActive,
	}

	if _, errRegister := manager.Register(context.Background(), auth1); errRegister != nil {
		t.Fatalf("register auth1: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), auth2); errRegister != nil {
		t.Fatalf("register auth2: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	reg.RegisterClient(auth2.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	manager.ReconcileRegistryModelStates(context.Background(), auth1.ID)
	manager.ReconcileRegistryModelStates(context.Background(), auth2.ID)
	manager.RefreshSchedulerEntry(auth1.ID)
	manager.RefreshSchedulerEntry(auth2.ID)

	resp, errExecute := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success", errExecute)
	}
	if string(resp.Payload) != targetModel {
		t.Fatalf("execute payload = %q, want %q", string(resp.Payload), targetModel)
	}

	if executor.LastAuthID() != auth2.ID {
		t.Fatalf("execute selected auth = %q, want %q (auth1 should be skipped due to cooldown)", executor.LastAuthID(), auth2.ID)
	}
	gotModels := executor.ExecuteModels()
	if len(gotModels) != 1 || gotModels[0] != targetModel {
		t.Fatalf("executor executed model = %v, want [%s]", gotModels, targetModel)
	}
}

// Test 3: cooldown store persistence consistency
func TestManager_NoForkAlias_CooldownPersistenceConsistency(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gemini-3.7-flash-high"
		targetModel = "gemini-3.7-flash-high"
	)

	store := &recordingCooldownStateStore{}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)

	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth := &Auth{
		ID:       "nofork-persist-auth",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// Mark rate limit failure on targetModel
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: provider,
		Model:    targetModel,
		Success:  false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429 rate limit",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	records := store.savedRecords()
	if len(records) == 0 {
		t.Fatal("expected cooldown records saved to store, got 0")
	}
	foundTarget := false
	for _, rec := range records {
		if rec.Model == targetModel && rec.AuthID == auth.ID {
			foundTarget = true
			break
		}
	}
	if !foundTarget {
		t.Fatalf("expected cooldown record for model %q in store, records = %#v", targetModel, records)
	}

	// Trigger model re-registration and reconcile
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

	// Verify store still has the cooldown record
	recordsAfter := store.savedRecords()
	foundTargetAfter := false
	for _, rec := range recordsAfter {
		if rec.Model == targetModel && rec.AuthID == auth.ID {
			foundTargetAfter = true
			break
		}
	}
	if !foundTargetAfter {
		t.Fatalf("cooldown record for model %q was lost from store after reconcile, records = %#v", targetModel, recordsAfter)
	}
}

// Test 4: multiple aliases pointing to the same upstream target model
func TestManager_NoForkAlias_MultipleAliasesToSameTarget(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel1 = "[ant1]gemini-3.7-flash-high"
		routeModel2 = "[ant2]gemini-3.7-flash-high"
		targetModel = "gemini-3.7-flash-high"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {
			{Name: targetModel, Alias: routeModel1, Fork: false},
			{Name: targetModel, Alias: routeModel2, Fork: false},
		},
	})

	auth := &Auth{
		ID:       "nofork-multi-alias-auth",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{
		{ID: routeModel1},
		{ID: routeModel2},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// Trigger rate limit cooldown on targetModel
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: provider,
		Model:    targetModel,
		Success:  false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429 rate limit",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	// Verify that ModelStates only contains a single authoritative entry for targetModel
	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if len(currentAuth.ModelStates) != 1 {
		t.Fatalf("expected 1 model state, got %d: %#v", len(currentAuth.ModelStates), currentAuth.ModelStates)
	}
	if _, ok := currentAuth.ModelStates[targetModel]; !ok {
		t.Fatalf("expected state for targetModel %q", targetModel)
	}

	// Verify both routeModel1 and routeModel2 are suspended in registry
	if !reg.IsModelSuspendedForClient(auth.ID, routeModel1) {
		t.Fatalf("routeModel1 %q was not suspended in registry", routeModel1)
	}
	if !reg.IsModelSuspendedForClient(auth.ID, routeModel2) {
		t.Fatalf("routeModel2 %q was not suspended in registry", routeModel2)
	}

	// Re-register and reconcile
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{
		{ID: routeModel1},
		{ID: routeModel2},
	})
	manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

	// Cooldown and suspension must be retained on both
	if !reg.IsModelSuspendedForClient(auth.ID, routeModel1) {
		t.Fatalf("routeModel1 %q lost suspension after reconcile", routeModel1)
	}
	if !reg.IsModelSuspendedForClient(auth.ID, routeModel2) {
		t.Fatalf("routeModel2 %q lost suspension after reconcile", routeModel2)
	}
}

// Test 5: cleanup after all aliases pointing to target model are removed
func TestManager_NoForkAlias_CleanupAfterAliasRemoved(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gemini-3.7-flash-high"
		targetModel = "gemini-3.7-flash-high"
		otherModel  = "other-model"
	)

	store := &recordingCooldownStateStore{}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)

	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	retryAfter := time.Now().Add(30 * time.Minute)
	auth := &Auth{
		ID:       "nofork-cleanup-auth",
		Provider: provider,
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			targetModel: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: retryAfter,
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// Reconcile with alias present -> state retained
	manager.ReconcileRegistryModelStates(context.Background(), auth.ID)
	manager.mu.RLock()
	if _, exists := manager.auths[auth.ID].ModelStates[targetModel]; !exists {
		manager.mu.RUnlock()
		t.Fatal("targetModel should exist before alias removal")
	}
	manager.mu.RUnlock()

	// Now remove alias from config and register only otherModel
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: nil,
	})
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: otherModel}})

	// Reconcile -> targetModel is no longer reachable via any registered model or alias
	manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if currentAuth.ModelStates != nil {
		if _, exists := currentAuth.ModelStates[targetModel]; exists {
			t.Fatalf("targetModel %q should have been deleted after alias was removed", targetModel)
		}
	}

	// Check store has no targetModel record
	records := store.savedRecords()
	for _, rec := range records {
		if rec.Model == targetModel && rec.AuthID == auth.ID {
			t.Fatalf("cooldown record for deleted targetModel %q still exists in store", targetModel)
		}
	}
}

// Test 6: Fork: true regression test
func TestManager_ForkTrue_RetainsStateAndSuspendsBoth(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "claude-opus-4-6"
		targetModel = "claude-opus-4-6-thinking"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  true,
		}},
	})

	retryAfter := time.Now().Add(30 * time.Minute)
	auth := &Auth{
		ID:       "fork-true-auth",
		Provider: provider,
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			targetModel: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: retryAfter,
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{
		{ID: routeModel},
		{ID: targetModel},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// Reconcile
	manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	state, exists := currentAuth.ModelStates[targetModel]
	if !exists || state == nil {
		t.Fatalf("targetModel %q state was deleted after reconcile", targetModel)
	}
	if !state.Unavailable {
		t.Fatal("targetModel state Unavailable = false, want true")
	}

	// Verify both targetModel and routeModel are suspended in registry
	if !reg.IsModelSuspendedForClient(auth.ID, targetModel) {
		t.Fatalf("targetModel %q was not suspended in registry", targetModel)
	}
	if !reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q was not suspended in registry", routeModel)
	}
}

// Test 7: expired cooldown is reset cleanly on reconcile
func TestManager_NoForkAlias_ExpiredCooldownResetClean(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gemini-3.7-flash-high"
		targetModel = "gemini-3.7-flash-high"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	// Cooldown expired 10 minutes ago
	expiredTime := time.Now().Add(-10 * time.Minute)
	auth := &Auth{
		ID:       "nofork-expired-auth",
		Provider: provider,
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			targetModel: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: expiredTime,
				StatusMessage:  "old error",
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// Reconcile should reset the expired state
	manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	state, exists := currentAuth.ModelStates[targetModel]
	if exists && state != nil {
		if !modelStateIsClean(state) {
			t.Fatalf("expired targetModel state was not cleaned: %#v", state)
		}
	}
}

// Test 8: MarkResult and ResetQuota projection on no-fork alias
func TestManager_NoForkAlias_MarkResultAndResetQuotaProjection(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gemini-3.7-flash-high"
		targetModel = "gemini-3.7-flash-high"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth := &Auth{
		ID:       "nofork-mark-reset-auth",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// Trigger rate limit failure
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: provider,
		Model:    targetModel,
		Success:  false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429 rate limit",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	// Verify routeModel in registry is marked with quota exceeded and suspension
	if !reg.IsModelQuotaExceededForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q was not marked quota exceeded in registry", routeModel)
	}
	if !reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q was not suspended in registry", routeModel)
	}

	// Reset quota
	if _, _, errReset := manager.ResetQuota(context.Background(), auth.ID); errReset != nil {
		t.Fatalf("ResetQuota failed: %v", errReset)
	}

	// Verify routeModel in registry is cleared
	if reg.IsModelQuotaExceededForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q is still marked quota exceeded after ResetQuota", routeModel)
	}
	if reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q is still suspended after ResetQuota", routeModel)
	}
}

// Test 9: per-auth alias and per-auth overriding global alias with no-fork reconcile
func TestManager_NoForkAlias_PerAuthAlias_RetainsCooldownAfterReconcile(t *testing.T) {
	const (
		provider      = "antigravity"
		routeModel    = "[ant]gemini-3.7-flash-high"
		globalTarget  = "gemini-3.7-flash-global"
		perAuthTarget = "gemini-3.7-flash-perauth"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	// Set a global alias pointing routeModel -> globalTarget
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  globalTarget,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	retryAfter := time.Now().Add(30 * time.Minute)
	auth1 := &Auth{
		ID:       "nofork-perauth-auth-1",
		Provider: provider,
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			perAuthTarget: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: retryAfter,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "rate_limit_exceeded",
					NextRecoverAt: retryAfter,
				},
			},
			globalTarget: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: retryAfter,
			},
		},
	}
	// Configure per-auth alias overriding global alias: routeModel -> perAuthTarget
	SetOAuthModelAliasesAttribute(auth1, []internalconfig.OAuthModelAlias{{
		Name:  perAuthTarget,
		Alias: routeModel,
		Fork:  false,
	}})

	if _, errRegister := manager.Register(context.Background(), auth1); errRegister != nil {
		t.Fatalf("register auth1: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
	})

	// Reconcile
	manager.ReconcileRegistryModelStates(context.Background(), auth1.ID)

	manager.mu.RLock()
	currentAuth := manager.auths[auth1.ID]
	manager.mu.RUnlock()

	if currentAuth == nil {
		t.Fatal("auth1 not found after reconcile")
	}

	// perAuthTarget cooldown must be retained
	state, exists := currentAuth.ModelStates[perAuthTarget]
	if !exists || state == nil {
		t.Fatalf("perAuthTarget %q state was deleted or nil after reconcile", perAuthTarget)
	}
	if !state.Unavailable || !state.Quota.Exceeded {
		t.Fatalf("perAuthTarget state active cooldown was lost: %#v", state)
	}

	// globalTarget state must be pruned because per-auth alias overrode global alias
	if _, existsGlobal := currentAuth.ModelStates[globalTarget]; existsGlobal {
		t.Fatalf("overridden globalTarget %q should have been pruned from ModelStates", globalTarget)
	}

	// Registry must project suspension onto routeModel
	if !reg.IsModelSuspendedForClient(auth1.ID, routeModel) {
		t.Fatalf("routeModel %q was not suspended in registry for client %s", routeModel, auth1.ID)
	}
	if !reg.IsModelQuotaExceededForClient(auth1.ID, routeModel) {
		t.Fatalf("routeModel %q was not marked quota exceeded in registry for client %s", routeModel, auth1.ID)
	}
}

// Test 10: real persistence restore from store and verify execution bypasses cooling credential
func TestManager_NoForkAlias_RealPersistenceRestore_BypassesCoolingAuth(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gemini-3.7-flash-high"
		targetModel = "gemini-3.7-flash-high"
	)

	initialStore := &recordingCooldownStateStore{}
	manager1 := NewManager(nil, nil, nil)
	manager1.SetCooldownStateStore(initialStore)

	executor1 := &noForkAliasTestExecutor{id: provider}
	manager1.RegisterExecutor(executor1)
	manager1.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth1 := &Auth{ID: "restore-auth-1", Provider: provider, Status: StatusActive}
	auth2 := &Auth{ID: "restore-auth-2", Provider: provider, Status: StatusActive}

	if _, errRegister := manager1.Register(context.Background(), auth1); errRegister != nil {
		t.Fatalf("register auth1: %v", errRegister)
	}
	if _, errRegister := manager1.Register(context.Background(), auth2); errRegister != nil {
		t.Fatalf("register auth2: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	reg.RegisterClient(auth2.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	// Put auth1 into cooldown on targetModel via MarkResult
	retryAfter := 30 * time.Minute
	manager1.MarkResult(context.Background(), Result{
		AuthID:   auth1.ID,
		Provider: provider,
		Model:    targetModel,
		Success:  false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429 rate limit",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	savedRecords := initialStore.savedRecords()
	if len(savedRecords) == 0 {
		t.Fatal("expected saved cooldown records in store, got 0")
	}

	// Create new Manager instance simulating fresh startup
	restoreStore := &recordingCooldownStateStore{load: savedRecords}
	manager2 := NewManager(nil, nil, nil)
	manager2.SetCooldownStateStore(restoreStore)

	executor2 := &noForkAliasTestExecutor{id: provider}
	manager2.RegisterExecutor(executor2)
	manager2.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	freshAuth1 := &Auth{ID: "restore-auth-1", Provider: provider, Status: StatusActive}
	freshAuth2 := &Auth{ID: "restore-auth-2", Provider: provider, Status: StatusActive}
	if _, errRegister := manager2.Register(context.Background(), freshAuth1); errRegister != nil {
		t.Fatalf("register freshAuth1: %v", errRegister)
	}
	if _, errRegister := manager2.Register(context.Background(), freshAuth2); errRegister != nil {
		t.Fatalf("register freshAuth2: %v", errRegister)
	}

	// Restore cooldown states from store
	if errRestore := manager2.RestoreCooldownStates(context.Background()); errRestore != nil {
		t.Fatalf("RestoreCooldownStates failed: %v", errRestore)
	}

	// Reconcile and refresh scheduler
	manager2.ReconcileRegistryModelStates(context.Background(), freshAuth1.ID)
	manager2.ReconcileRegistryModelStates(context.Background(), freshAuth2.ID)
	manager2.RefreshSchedulerAll()

	// Verify routeModel on freshAuth1 is suspended in registry
	if !reg.IsModelSuspendedForClient(freshAuth1.ID, routeModel) {
		t.Fatalf("routeModel %q should be suspended for restored auth1", routeModel)
	}

	// Execute request on alias -> should skip freshAuth1 and select freshAuth2
	resp, errExecute := manager2.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error: %v", errExecute)
	}
	if string(resp.Payload) != targetModel {
		t.Fatalf("payload = %q, want %q", string(resp.Payload), targetModel)
	}
	if executor2.LastAuthID() != freshAuth2.ID {
		t.Fatalf("selected auth = %q, want %q (cooling auth1 should have been skipped)", executor2.LastAuthID(), freshAuth2.ID)
	}
	executedModels := executor2.ExecuteModels()
	if len(executedModels) != 1 || executedModels[0] != targetModel {
		t.Fatalf("executed model = %v, want [%s]", executedModels, targetModel)
	}
}

// Test 11: store persistence records cleaned when alias is removed
func TestManager_NoForkAlias_StorePersistenceCleanedWhenAliasRemoved(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gemini-3.7-flash-high"
		targetModel = "gemini-3.7-flash-high"
		otherModel  = "other-clean-model"
	)

	store := &recordingCooldownStateStore{}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)

	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth := &Auth{
		ID:       "nofork-store-clean-auth",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// Cooldown on targetModel
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: provider,
		Model:    targetModel,
		Success:  false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429 rate limit",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	// Ensure store has record
	recordsBefore := store.savedRecords()
	found := false
	for _, rec := range recordsBefore {
		if rec.AuthID == auth.ID && rec.Model == targetModel {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected store record for targetModel %q before alias removal", targetModel)
	}

	// Remove alias from configuration and update registry
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: nil,
	})
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: otherModel}})

	// Reconcile -> removes targetModel from auth.ModelStates and persists updated snapshot to store
	manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

	recordsAfter := store.savedRecords()
	for _, rec := range recordsAfter {
		if rec.AuthID == auth.ID && rec.Model == targetModel {
			t.Fatalf("store record for %q should have been removed, records = %#v", targetModel, recordsAfter)
		}
	}
}

// Test 12: multiple aliases (B1, B2 -> A) both skip cooling auth end-to-end
func TestManager_NoForkAlias_MultipleAliases_BothSkipCoolingAuthEndToEnd(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel1 = "[ant1]gemini-3.7-flash-high"
		routeModel2 = "[ant2]gemini-3.7-flash-high"
		targetModel = "gemini-3.7-flash-high"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {
			{Name: targetModel, Alias: routeModel1, Fork: false},
			{Name: targetModel, Alias: routeModel2, Fork: false},
		},
	})

	retryAfter := time.Now().Add(30 * time.Minute)
	auth1 := &Auth{
		ID:       "multi-cooling-auth",
		Provider: provider,
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			targetModel: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: retryAfter,
			},
		},
	}
	auth2 := &Auth{
		ID:       "multi-available-auth",
		Provider: provider,
		Status:   StatusActive,
	}

	if _, errRegister := manager.Register(context.Background(), auth1); errRegister != nil {
		t.Fatalf("register auth1: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), auth2); errRegister != nil {
		t.Fatalf("register auth2: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, provider, []*registry.ModelInfo{{ID: routeModel1}, {ID: routeModel2}})
	reg.RegisterClient(auth2.ID, provider, []*registry.ModelInfo{{ID: routeModel1}, {ID: routeModel2}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	manager.ReconcileRegistryModelStates(context.Background(), auth1.ID)
	manager.ReconcileRegistryModelStates(context.Background(), auth2.ID)
	manager.RefreshSchedulerAll()

	// Execute via routeModel1 -> skips auth1, selects auth2
	resp1, errExecute1 := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeModel1}, cliproxyexecutor.Options{})
	if errExecute1 != nil {
		t.Fatalf("execute routeModel1: %v", errExecute1)
	}
	if string(resp1.Payload) != targetModel {
		t.Fatalf("payload1 = %q, want %q", string(resp1.Payload), targetModel)
	}
	if executor.LastAuthID() != auth2.ID {
		t.Fatalf("selected auth = %q, want %q for routeModel1", executor.LastAuthID(), auth2.ID)
	}

	// Execute via routeModel2 -> skips auth1, selects auth2
	resp2, errExecute2 := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeModel2}, cliproxyexecutor.Options{})
	if errExecute2 != nil {
		t.Fatalf("execute routeModel2: %v", errExecute2)
	}
	if string(resp2.Payload) != targetModel {
		t.Fatalf("payload2 = %q, want %q", string(resp2.Payload), targetModel)
	}
	if executor.LastAuthID() != auth2.ID {
		t.Fatalf("selected auth = %q, want %q for routeModel2", executor.LastAuthID(), auth2.ID)
	}
}

// Test 13: alias repointing scenarios (B -> A changed to B -> C, and route A mapped to C)
func TestManager_NoForkAlias_AliasRepointing_PrunesOldTargetAndReconciles(t *testing.T) {
	const (
		provider = "antigravity"
		modelA   = "gemini-model-a"
		modelB   = "[alias]gemini-model-b"
		modelC   = "gemini-model-c"
	)

	// Scenario 1: B -> A changed to B -> C
	t.Run("Repointing B from A to C", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		executor := &noForkAliasTestExecutor{id: provider}
		manager.RegisterExecutor(executor)
		manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
			provider: {{
				Name:  modelA,
				Alias: modelB,
				Fork:  false,
			}},
		})

		retryAfter := time.Now().Add(30 * time.Minute)
		auth := &Auth{
			ID:       "repoint-auth-1",
			Provider: provider,
			Status:   StatusActive,
			ModelStates: map[string]*ModelState{
				modelA: {
					Unavailable:    true,
					Status:         StatusError,
					NextRetryAfter: retryAfter,
				},
			},
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth: %v", errRegister)
		}

		reg := registry.GetGlobalRegistry()
		reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: modelB}})
		t.Cleanup(func() {
			reg.UnregisterClient(auth.ID)
		})

		// First reconcile: modelA cooldown is retained, modelB suspended
		manager.ReconcileRegistryModelStates(context.Background(), auth.ID)
		if !reg.IsModelSuspendedForClient(auth.ID, modelB) {
			t.Fatalf("modelB should be suspended for client %s", auth.ID)
		}

		// Change alias: B -> C
		manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
			provider: {{
				Name:  modelC,
				Alias: modelB,
				Fork:  false,
			}},
		})

		// Reconcile: old modelA state pruned, modelB resumed because modelC is clean
		manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

		manager.mu.RLock()
		currentAuth := manager.auths[auth.ID]
		manager.mu.RUnlock()

		if currentAuth.ModelStates != nil {
			if _, existsA := currentAuth.ModelStates[modelA]; existsA {
				t.Fatalf("old modelA state should have been pruned after repointing to modelC")
			}
		}
		if reg.IsModelSuspendedForClient(auth.ID, modelB) {
			t.Fatalf("modelB should no longer be suspended after repointing to clean modelC")
		}

		// Now trigger cooldown on modelC via MarkResult
		retryAfterC := 30 * time.Minute
		manager.MarkResult(context.Background(), Result{
			AuthID:   auth.ID,
			Provider: provider,
			Model:    modelC,
			Success:  false,
			Error: &Error{
				Code:       "rate_limit_exceeded",
				Message:    "429 rate limit",
				HTTPStatus: http.StatusTooManyRequests,
			},
			RetryAfter: &retryAfterC,
		})

		// modelB in registry must now be suspended for modelC cooldown
		if !reg.IsModelSuspendedForClient(auth.ID, modelB) {
			t.Fatalf("modelB should now be suspended after modelC cooldown")
		}
	})

	// Scenario 2: Route named A originally, now mapped to C (A -> C)
	t.Run("Route named A mapped to C", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		executor := &noForkAliasTestExecutor{id: provider}
		manager.RegisterExecutor(executor)
		// Alias maps route modelA -> modelC
		manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
			provider: {{
				Name:  modelC,
				Alias: modelA,
				Fork:  false,
			}},
		})

		retryAfter := time.Now().Add(30 * time.Minute)
		auth := &Auth{
			ID:       "repoint-auth-2",
			Provider: provider,
			Status:   StatusActive,
			ModelStates: map[string]*ModelState{
				// Legacy state for modelA
				modelA: {
					Unavailable:    true,
					Status:         StatusError,
					NextRetryAfter: retryAfter,
				},
			},
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth: %v", errRegister)
		}

		reg := registry.GetGlobalRegistry()
		reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: modelA}})
		t.Cleanup(func() {
			reg.UnregisterClient(auth.ID)
		})

		// Reconcile: route modelA now resolves to modelC. Legacy modelA state migrated to modelC.
		manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

		manager.mu.RLock()
		currentAuth := manager.auths[auth.ID]
		manager.mu.RUnlock()

		if currentAuth.ModelStates != nil {
			if _, existsA := currentAuth.ModelStates[modelA]; existsA {
				t.Fatalf("legacy modelA state should have been pruned after migrating to modelC")
			}
			stateC, existsC := currentAuth.ModelStates[modelC]
			if !existsC || stateC == nil || !stateC.Unavailable {
				t.Fatalf("modelC should have inherited active cooldown from legacy modelA")
			}
		}
		if !reg.IsModelSuspendedForClient(auth.ID, modelA) {
			t.Fatalf("route modelA should be suspended because target modelC has migrated cooldown")
		}
	})
}

// Test 14: concurrent Reconcile and MarkResult stability
func TestManager_NoForkAlias_ConcurrentReconcileAndMarkResult(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gemini-3.7-flash-high"
		targetModel = "gemini-3.7-flash-high"
	)

	store := &recordingCooldownStateStore{}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)

	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth := &Auth{
		ID:       "nofork-concurrent-auth",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	const iterations = 100
	var wg sync.WaitGroup
	wg.Add(3)

	// Goroutine 1: Rapid Reconcile
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			manager.ReconcileRegistryModelStates(context.Background(), auth.ID)
			time.Sleep(time.Millisecond)
		}
	}()

	// Goroutine 2: Alternate MarkResult failure and ResetQuota
	go func() {
		defer wg.Done()
		retryAfter := 10 * time.Minute
		for i := 0; i < iterations; i++ {
			if i%2 == 0 {
				manager.MarkResult(context.Background(), Result{
					AuthID:   auth.ID,
					Provider: provider,
					Model:    targetModel,
					Success:  false,
					Error: &Error{
						Code:       "rate_limit_exceeded",
						Message:    "429 rate limit",
						HTTPStatus: http.StatusTooManyRequests,
					},
					RetryAfter: &retryAfter,
				})
			} else {
				_, _, _ = manager.ResetQuota(context.Background(), auth.ID)
			}
			time.Sleep(time.Millisecond)
		}
	}()

	// Goroutine 3: Concurrent Execute calls
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_, _ = manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{})
			time.Sleep(time.Millisecond)
		}
	}()

	wg.Wait()
}

// Test 15: MarkResult with route model B generates authoritative ModelStates[A] and never ModelStates[B]
func TestManager_NoForkAlias_MarkResultRouteModelWritesAuthoritativeState(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gemini-3.7-flash-high"
		targetModel = "gemini-3.7-flash-high"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth := &Auth{
		ID:       "auth-mark-route-b",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// Pass routeModel B to MarkResult via RouteModel with 429 failure
	retryAfter := 20 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   provider,
		RouteModel: routeModel,
		Success:    false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429 rate limit",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if currentAuth == nil {
		t.Fatal("auth not found")
	}

	// ModelStates[routeModel] must NEVER be created
	if _, existsB := currentAuth.ModelStates[routeModel]; existsB {
		t.Fatalf("ModelStates[%q] was created, but only authoritative targetModel should exist", routeModel)
	}

	// Authoritative ModelStates[targetModel] must exist and be cooling
	stateA, existsA := currentAuth.ModelStates[targetModel]
	if !existsA || stateA == nil {
		t.Fatalf("authoritative ModelStates[%q] missing", targetModel)
	}
	if !stateA.Unavailable || !stateA.Quota.Exceeded {
		t.Fatalf("authoritative ModelStates[%q] not in cooling: %#v", targetModel, stateA)
	}

	// Registry must project suspension onto routeModel
	if !reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q should be suspended in registry", routeModel)
	}

	// Now mark success on routeModel via RouteModel
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   provider,
		RouteModel: routeModel,
		Success:    true,
	})

	manager.mu.RLock()
	currentAuth = manager.auths[auth.ID]
	manager.mu.RUnlock()

	// Still no ModelStates[routeModel]
	if _, existsB := currentAuth.ModelStates[routeModel]; existsB {
		t.Fatalf("ModelStates[%q] was created after success", routeModel)
	}
	// ModelStates[targetModel] is clean
	stateA, existsA = currentAuth.ModelStates[targetModel]
	if !existsA || stateA == nil || !modelStateIsClean(stateA) {
		t.Fatalf("authoritative ModelStates[%q] should be clean after success: %#v", targetModel, stateA)
	}
	// Registry resumed routeModel
	if reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q should be resumed in registry after success", routeModel)
	}
}

// Test 16: Legacy ModelStates[B] is automatically pruned on Reconcile when configured as B -> A
func TestManager_NoForkAlias_LegacyModelStatePrunedOnReconcile(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gemini-3.7-flash-high"
		targetModel = "gemini-3.7-flash-high"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	retryAfter := time.Now().Add(30 * time.Minute)
	auth := &Auth{
		ID:       "auth-legacy-prune",
		Provider: provider,
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			routeModel: {
				Unavailable:    true,
				Status:         StatusError,
				StatusMessage:  "legacy route state",
				NextRetryAfter: retryAfter,
			},
			targetModel: {
				Unavailable:    true,
				Status:         StatusError,
				StatusMessage:  "authoritative target state",
				NextRetryAfter: retryAfter,
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

	// Reconcile
	manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	// ModelStates[routeModel] must be pruned
	if _, existsLegacy := currentAuth.ModelStates[routeModel]; existsLegacy {
		t.Fatalf("legacy ModelStates[%q] was not pruned after Reconcile", routeModel)
	}

	// ModelStates[targetModel] must be retained
	stateA, existsA := currentAuth.ModelStates[targetModel]
	if !existsA || stateA == nil {
		t.Fatalf("authoritative ModelStates[%q] missing after Reconcile", targetModel)
	}
	if !stateA.Unavailable {
		t.Fatalf("authoritative ModelStates[%q] should be unavailable", targetModel)
	}

	// Both routeModel and targetModel in registry are suspended
	if !reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q should be suspended in registry", routeModel)
	}
	if !reg.IsModelSuspendedForClient(auth.ID, targetModel) {
		t.Fatalf("targetModel %q should be suspended in registry", targetModel)
	}
}

// Test 17: Real disk file persistence restore using NewFileCooldownStateStoreWithAuthDir
func TestManager_NoForkAlias_RealDiskFileStoreRestore_BypassesCoolingAuth(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gemini-3.7-flash-high"
		targetModel = "gemini-3.7-flash-high"
	)

	tempDir := t.TempDir()
	authDir := filepath.Join(tempDir, "auths")
	storeDir := filepath.Join(tempDir, "cooldowns")
	if errMkdir := os.MkdirAll(authDir, 0755); errMkdir != nil {
		t.Fatalf("mkdir authDir: %v", errMkdir)
	}
	if errMkdir := os.MkdirAll(storeDir, 0755); errMkdir != nil {
		t.Fatalf("mkdir storeDir: %v", errMkdir)
	}

	auth1File := filepath.Join(authDir, "auth1.json")
	auth2File := filepath.Join(authDir, "auth2.json")
	if errWrite := os.WriteFile(auth1File, []byte(`{"id":"disk-auth-1","provider":"antigravity"}`), 0644); errWrite != nil {
		t.Fatalf("write auth1File: %v", errWrite)
	}
	if errWrite := os.WriteFile(auth2File, []byte(`{"id":"disk-auth-2","provider":"antigravity"}`), 0644); errWrite != nil {
		t.Fatalf("write auth2File: %v", errWrite)
	}

	diskStore1 := NewFileCooldownStateStoreWithAuthDir(storeDir, authDir)
	manager1 := NewManager(nil, nil, nil)
	manager1.SetCooldownStateStore(diskStore1)

	executor1 := &noForkAliasTestExecutor{id: provider}
	manager1.RegisterExecutor(executor1)
	manager1.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth1 := &Auth{ID: "disk-auth-1", Provider: provider, Status: StatusActive, FileName: auth1File}
	auth2 := &Auth{ID: "disk-auth-2", Provider: provider, Status: StatusActive, FileName: auth2File}

	if _, errRegister := manager1.Register(context.Background(), auth1); errRegister != nil {
		t.Fatalf("register auth1: %v", errRegister)
	}
	if _, errRegister := manager1.Register(context.Background(), auth2); errRegister != nil {
		t.Fatalf("register auth2: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	reg.RegisterClient(auth2.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	// Put auth1 into cooldown on targetModel (via RouteModel) via MarkResult
	retryAfter := 30 * time.Minute
	manager1.MarkResult(context.Background(), Result{
		AuthID:     auth1.ID,
		Provider:   provider,
		Model:      targetModel,
		RouteModel: routeModel,
		Success:    false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429 rate limit",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	// Verify disk cooldown file exists
	loadedRecords, errLoad := diskStore1.Load(context.Background())
	if errLoad != nil {
		t.Fatalf("diskStore1.Load failed: %v", errLoad)
	}
	if len(loadedRecords) == 0 {
		t.Fatal("expected persisted cooldown records on disk, got 0")
	}

	// Create fresh Manager instance with real disk store
	diskStore2 := NewFileCooldownStateStoreWithAuthDir(storeDir, authDir)
	manager2 := NewManager(nil, nil, nil)
	manager2.SetCooldownStateStore(diskStore2)

	executor2 := &noForkAliasTestExecutor{id: provider}
	manager2.RegisterExecutor(executor2)
	manager2.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	freshAuth1 := &Auth{ID: "disk-auth-1", Provider: provider, Status: StatusActive, FileName: auth1File}
	freshAuth2 := &Auth{ID: "disk-auth-2", Provider: provider, Status: StatusActive, FileName: auth2File}
	if _, errRegister := manager2.Register(context.Background(), freshAuth1); errRegister != nil {
		t.Fatalf("register freshAuth1: %v", errRegister)
	}
	if _, errRegister := manager2.Register(context.Background(), freshAuth2); errRegister != nil {
		t.Fatalf("register freshAuth2: %v", errRegister)
	}

	// Restore cooldown states from disk
	if errRestore := manager2.RestoreCooldownStates(context.Background()); errRestore != nil {
		t.Fatalf("RestoreCooldownStates failed: %v", errRestore)
	}

	manager2.ReconcileRegistryModelStates(context.Background(), freshAuth1.ID)
	manager2.ReconcileRegistryModelStates(context.Background(), freshAuth2.ID)
	manager2.RefreshSchedulerAll()

	// Verify routeModel on freshAuth1 is suspended in registry
	if !reg.IsModelSuspendedForClient(freshAuth1.ID, routeModel) {
		t.Fatalf("routeModel %q should be suspended for restored disk-auth-1", routeModel)
	}

	// Execute alias request -> should bypass cooling disk-auth-1 and select disk-auth-2
	resp, errExecute := manager2.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error: %v", errExecute)
	}
	if string(resp.Payload) != targetModel {
		t.Fatalf("payload = %q, want %q", string(resp.Payload), targetModel)
	}
	if executor2.LastAuthID() != freshAuth2.ID {
		t.Fatalf("selected auth = %q, want %q", executor2.LastAuthID(), freshAuth2.ID)
	}
}

// Test 18: Concurrent Reconcile, MarkResult, and ResetQuota with full consistency assertion
func TestManager_NoForkAlias_ConcurrentReconcileMarkResult_FullConsistency(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gemini-3.7-flash-high"
		targetModel = "gemini-3.7-flash-high"
	)

	store := &recordingCooldownStateStore{}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)

	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth := &Auth{
		ID:       "concurrent-consistency-auth",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	const iterations = 100
	var wg sync.WaitGroup
	wg.Add(4)

	// Goroutine 1: Continuous Reconcile
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			manager.ReconcileRegistryModelStates(context.Background(), auth.ID)
			time.Sleep(50 * time.Microsecond)
		}
	}()

	// Goroutine 2: MarkResult on routeModel
	go func() {
		defer wg.Done()
		retryAfter := 10 * time.Minute
		for i := 0; i < iterations; i++ {
			if i%3 == 0 {
				manager.MarkResult(context.Background(), Result{
					AuthID:     auth.ID,
					Provider:   provider,
					Model:      targetModel,
					RouteModel: routeModel,
					Success:    false,
					Error: &Error{
						Code:       "rate_limit_exceeded",
						Message:    "429 rate limit",
						HTTPStatus: http.StatusTooManyRequests,
					},
					RetryAfter: &retryAfter,
				})
			} else if i%3 == 1 {
				manager.MarkResult(context.Background(), Result{
					AuthID:     auth.ID,
					Provider:   provider,
					Model:      targetModel,
					RouteModel: routeModel,
					Success:    true,
				})
			} else {
				_, _, _ = manager.ResetQuota(context.Background(), auth.ID)
			}
			time.Sleep(50 * time.Microsecond)
		}
	}()

	// Goroutine 3: MarkResult on targetModel
	go func() {
		defer wg.Done()
		retryAfter := 10 * time.Minute
		for i := 0; i < iterations; i++ {
			if i%2 == 0 {
				manager.MarkResult(context.Background(), Result{
					AuthID:   auth.ID,
					Provider: provider,
					Model:    targetModel,
					Success:  false,
					Error: &Error{
						Code:       "rate_limit_exceeded",
						Message:    "429 rate limit",
						HTTPStatus: http.StatusTooManyRequests,
					},
					RetryAfter: &retryAfter,
				})
			} else {
				_, _, _ = manager.ResetQuota(context.Background(), auth.ID)
			}
			time.Sleep(50 * time.Microsecond)
		}
	}()

	// Goroutine 4: Execute
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_, _ = manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{})
			time.Sleep(50 * time.Microsecond)
		}
	}()

	wg.Wait()

	// Natural final consistency assertion without artificial post-reconcile
	manager.mu.RLock()
	finalAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if finalAuth == nil {
		t.Fatal("finalAuth is nil")
	}

	// ModelStates must never contain routeModel
	if _, existsB := finalAuth.ModelStates[routeModel]; existsB {
		t.Fatalf("ModelStates still contains routeModel %q after concurrency", routeModel)
	}

	// Registry suspension must match targetModel availability
	stateA := finalAuth.ModelStates[targetModel]
	isTargetCooling := stateA != nil && (stateA.Unavailable || (!stateA.NextRetryAfter.IsZero() && stateA.NextRetryAfter.After(time.Now())))
	isRegistrySuspended := reg.IsModelSuspendedForClient(auth.ID, routeModel)
	if isTargetCooling != isRegistrySuspended {
		t.Fatalf("registry suspended mismatch: targetCooling=%v, registrySuspended=%v", isTargetCooling, isRegistrySuspended)
	}

	// Cooldown store must be consistent with memory ModelStates
	records := store.savedRecords()
	var storeRecordForTarget *CooldownStateRecord
	for i := range records {
		if records[i].AuthID == auth.ID && records[i].Model == targetModel {
			storeRecordForTarget = &records[i]
		}
		if records[i].AuthID == auth.ID && records[i].Model == routeModel {
			t.Fatalf("store contains record for routeModel %q", routeModel)
		}
	}
	if isTargetCooling && storeRecordForTarget == nil {
		t.Fatalf("targetModel is cooling in memory but missing in store records: %#v", records)
	}
	if !isTargetCooling && storeRecordForTarget != nil {
		t.Fatalf("targetModel is clean in memory but present in store records: %#v", storeRecordForTarget)
	}
}

// Test 19: Reconcile with cancelled context still cleans cooldown store properly
func TestManager_NoForkAlias_CancelledContextReconcile_CooldownStoreCleaned(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gemini-3.7-flash-high"
		targetModel = "gemini-3.7-flash-high"
		otherModel  = "other-clean-model"
	)

	store := &recordingCooldownStateStore{}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)

	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth := &Auth{
		ID:       "nofork-ctx-cancel-auth",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// Put into cooldown on targetModel (via RouteModel)
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   provider,
		Model:      targetModel,
		RouteModel: routeModel,
		Success:    false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429 rate limit",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	recordsBefore := store.savedRecords()
	foundTarget := false
	for _, rec := range recordsBefore {
		if rec.AuthID == auth.ID && rec.Model == targetModel {
			foundTarget = true
			break
		}
	}
	if !foundTarget {
		t.Fatalf("expected store record for targetModel %q initially, got records: %#v", targetModel, recordsBefore)
	}

	// Remove alias and change supported model in registry
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: nil,
	})
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: otherModel}})

	// Reconcile with cancelled context
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	manager.ReconcileRegistryModelStates(cancelledCtx, auth.ID)

	// Memory ModelStates must have targetModel deleted
	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if currentAuth.ModelStates != nil {
		if _, existsA := currentAuth.ModelStates[targetModel]; existsA {
			t.Fatalf("targetModel was not pruned from memory ModelStates")
		}
	}

	// Cooldown store must be cleanly updated and no longer have targetModel
	recordsAfter := store.savedRecords()
	for _, rec := range recordsAfter {
		if rec.AuthID == auth.ID && rec.Model == targetModel {
			t.Fatalf("targetModel %q still exists in cooldown store after cancelled-context reconcile", targetModel)
		}
	}
}

// Test 20: MarkResult receiving already targetModel does NOT perform secondary alias resolution
func TestManager_NoForkAlias_MarkResultTargetModelNoSecondaryAliasResolution(t *testing.T) {
	const (
		provider = "antigravity"
		modelB   = "route-model-b"
		modelA   = "target-model-a"
		modelC   = "chained-model-c"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	// Configure chained aliases: modelB -> modelA, and modelA -> modelC
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {
			{Name: modelA, Alias: modelB, Fork: false},
			{Name: modelC, Alias: modelA, Fork: false},
		},
	})

	auth := &Auth{
		ID:       "auth-no-secondary-resolve",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: modelB}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// Execution layer passes authoritative target Model: modelA and RouteModel: modelB
	retryAfter := 20 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   provider,
		Model:      modelA,
		RouteModel: modelB,
		Success:    false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429 rate limit",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if currentAuth == nil {
		t.Fatal("auth not found")
	}

	// ModelStates must record state directly on modelA
	stateA, existsA := currentAuth.ModelStates[modelA]
	if !existsA || stateA == nil {
		t.Fatalf("expected ModelStates[%q] to exist", modelA)
	}
	if !stateA.Unavailable || !stateA.Quota.Exceeded {
		t.Fatalf("ModelStates[%q] expected cooling, got %#v", modelA, stateA)
	}

	// Secondary alias resolution must NOT have occurred on modelA -> modelC
	if _, existsC := currentAuth.ModelStates[modelC]; existsC {
		t.Fatalf("ModelStates[%q] was created due to secondary alias resolution", modelC)
	}
	if _, existsB := currentAuth.ModelStates[modelB]; existsB {
		t.Fatalf("ModelStates[%q] should not exist", modelB)
	}

	// Registry must project suspension onto route modelB
	if !reg.IsModelSuspendedForClient(auth.ID, modelB) {
		t.Fatalf("route model %q should be suspended in registry", modelB)
	}
}

// Test 21: MarkResult on 500/503/transient errors immediately suspends route alias in registry
func TestManager_NoForkAlias_MarkResultTransientErrorsSyncSuspendRegistry(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gemini-3.7-flash-transient"
		targetModel = "gemini-3.7-flash-transient"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth := &Auth{
		ID:       "auth-transient-suspend",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// Initial check: routeModel is active and count is 1
	if reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q should not be suspended initially", routeModel)
	}
	if count := reg.GetModelCount(routeModel); count != 1 {
		t.Fatalf("expected count 1 for routeModel %q, got %d", routeModel, count)
	}

	// 1. MarkResult with 500 Internal Server Error
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   provider,
		Model:      targetModel,
		RouteModel: routeModel,
		Success:    false,
		Error: &Error{
			Code:       "internal_error",
			Message:    "500 Internal Server Error",
			HTTPStatus: http.StatusInternalServerError,
		},
	})

	// Immediately verify routeModel is suspended in registry (before any Reconcile)
	if !reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q should be synchronously suspended in registry after 500 error", routeModel)
	}
	if count := reg.GetModelCount(routeModel); count != 0 {
		t.Fatalf("expected count 0 for suspended routeModel, got %d", count)
	}

	// 2. MarkResult with 503 Service Unavailable
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   provider,
		Model:      targetModel,
		RouteModel: routeModel,
		Success:    false,
		Error: &Error{
			Code:       "service_unavailable",
			Message:    "503 Service Unavailable",
			HTTPStatus: http.StatusServiceUnavailable,
		},
	})

	// Still suspended in registry
	if !reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q should remain suspended after 503 error", routeModel)
	}

	// 3. MarkResult with Success -> immediately resumes routeModel in registry
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   provider,
		Model:      targetModel,
		RouteModel: routeModel,
		Success:    true,
	})

	// Verify routeModel is resumed
	if reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q should be resumed in registry after success", routeModel)
	}
	if count := reg.GetModelCount(routeModel); count != 1 {
		t.Fatalf("expected count 1 for resumed routeModel, got %d", count)
	}
}

// Test 22: Stale disabled snapshot cannot remove updated active entry in scheduler
func TestManager_Scheduler_StaleDisabledSnapshotCannotRemoveActiveEntry(t *testing.T) {
	const (
		provider = "antigravity"
		model    = "gemini-3.7-flash-gen-test"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "auth-gen-race-test",
		Provider: provider,
		Status:   StatusActive,
	}
	registeredAuth, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// Initial generation should be >= 1
	if registeredAuth.Generation == 0 {
		t.Fatalf("expected initial generation >= 1, got %d", registeredAuth.Generation)
	}

	// Trigger MarkResult on success to advance generation
	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: provider,
		Model:    model,
		Success:  true,
	})

	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID].Clone()
	manager.mu.RUnlock()

	activeGen := currentAuth.Generation
	if activeGen <= registeredAuth.Generation {
		t.Fatalf("expected active generation %d > %d", activeGen, registeredAuth.Generation)
	}

	// Scheduler must be able to pick the active auth
	picked, errPick := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("expected scheduler to pick active auth, got error: %v", errPick)
	}
	if picked.ID != auth.ID {
		t.Fatalf("picked auth ID = %q, want %q", picked.ID, auth.ID)
	}

	// Construct a stale snapshot with Disabled = true and older Generation
	staleDisabled := currentAuth.Clone()
	staleDisabled.Disabled = true
	staleDisabled.Generation = registeredAuth.Generation // older generation
	staleDisabled.UpdatedAt = currentAuth.UpdatedAt.Add(-10 * time.Minute)

	// Send stale disabled snapshot to scheduler
	manager.scheduler.upsertAuth(staleDisabled)

	// Scheduler MUST NOT have removed the newer active entry
	pickedAfterStale, errPickAfterStale := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPickAfterStale != nil {
		t.Fatalf("stale disabled snapshot deleted active scheduler entry: %v", errPickAfterStale)
	}
	if pickedAfterStale.ID != auth.ID {
		t.Fatalf("picked auth after stale = %q, want %q", pickedAfterStale.ID, auth.ID)
	}

	// Now send a fresh disabled snapshot with newer Generation
	freshDisabled := currentAuth.Clone()
	freshDisabled.Disabled = true
	freshDisabled.Generation = activeGen + 1
	freshDisabled.UpdatedAt = time.Now().Add(1 * time.Minute)

	manager.scheduler.upsertAuth(freshDisabled)

	// Scheduler MUST now remove the entry
	_, errPickAfterFresh := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPickAfterFresh == nil {
		t.Fatal("expected auth_not_found after fresh disabled snapshot, but auth was still picked")
	}
}

type strictContextCheckingStore struct {
	mu      sync.Mutex
	records []CooldownStateRecord
}

func (s *strictContextCheckingStore) Save(ctx context.Context, records []CooldownStateRecord) error {
	if ctx == nil {
		return errors.New("nil context")
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return fmt.Errorf("strict store context error: %w", errCtx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = make([]CooldownStateRecord, len(records))
	copy(s.records, records)
	return nil
}

func (s *strictContextCheckingStore) Load(ctx context.Context) ([]CooldownStateRecord, error) {
	if ctx == nil {
		return nil, errors.New("nil context")
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return nil, fmt.Errorf("strict store context error: %w", errCtx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CooldownStateRecord, len(s.records))
	copy(out, s.records)
	return out, nil
}

func (s *strictContextCheckingStore) savedRecords() []CooldownStateRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CooldownStateRecord, len(s.records))
	copy(out, s.records)
	return out
}

// Test 23: Strict context-checking store persists and cleans invalid records on Reconcile even with cancelled outer context
func TestManager_NoForkAlias_StrictContextCheckingStore_ReconcilePersistsUnderCancelledContext(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gemini-3.7-flash-strict"
		targetModel = "gemini-3.7-flash-strict"
		otherModel  = "other-model-strict"
	)

	store := &strictContextCheckingStore{}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)

	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth := &Auth{
		ID:       "strict-ctx-auth",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// Put into cooldown on targetModel
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   provider,
		Model:      targetModel,
		RouteModel: routeModel,
		Success:    false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429 rate limit",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	recordsBefore := store.savedRecords()
	foundTarget := false
	for _, rec := range recordsBefore {
		if rec.AuthID == auth.ID && rec.Model == targetModel {
			foundTarget = true
			break
		}
	}
	if !foundTarget {
		t.Fatalf("expected store record for targetModel %q initially, got records: %#v", targetModel, recordsBefore)
	}

	// Remove alias and change supported model in registry
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: nil,
	})
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: otherModel}})

	// Reconcile with cancelled context
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	// Reconcile must reliably persist using context.Background() without failing due to cancelledCtx
	manager.ReconcileRegistryModelStates(cancelledCtx, auth.ID)

	// Memory ModelStates must have targetModel deleted
	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if currentAuth.ModelStates != nil {
		if _, existsA := currentAuth.ModelStates[targetModel]; existsA {
			t.Fatalf("targetModel was not pruned from memory ModelStates")
		}
	}

	// Cooldown store must be cleanly updated and no longer have targetModel
	recordsAfter := store.savedRecords()
	for _, rec := range recordsAfter {
		if rec.AuthID == auth.ID && rec.Model == targetModel {
			t.Fatalf("targetModel %q still exists in strict cooldown store after cancelled-context reconcile", targetModel)
		}
	}
}

// Test 24: Scheduler tombstone protection - delayed stale active snapshot cannot reactivate disabled auth
func TestManager_Scheduler_Tombstone_StaleActiveSnapshotCannotReactivateDisabledEntry(t *testing.T) {
	const (
		provider = "antigravity"
		model    = "gemini-3.7-tombstone-test"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "auth-tombstone-test",
		Provider: provider,
		Status:   StatusActive,
	}
	registeredAuth, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	manager.RefreshSchedulerEntry(auth.ID)

	manager.mu.RLock()
	currentActiveSnapshot := manager.auths[auth.ID].Clone()
	manager.mu.RUnlock()

	// Verify auth can be picked initially
	picked, errPick := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPick != nil || picked.ID != auth.ID {
		t.Fatalf("initial pickSingle failed: picked=%v, err=%v", picked, errPick)
	}

	// Capture active snapshot at current Generation
	staleActiveSnapshot := currentActiveSnapshot

	// Apply higher generation disabled snapshot (e.g. Generation 10)
	highGenDisabled := registeredAuth.Clone()
	highGenDisabled.Generation = 10
	highGenDisabled.Disabled = true
	highGenDisabled.Status = StatusDisabled
	highGenDisabled.UpdatedAt = time.Now().Add(5 * time.Minute)

	manager.scheduler.upsertAuth(highGenDisabled)

	// Scheduler must reject picking because auth is disabled
	_, errPickAfterDisable := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPickAfterDisable == nil {
		t.Fatal("expected scheduler pickSingle to fail after highGenDisabled, but succeeded")
	}

	// Now send the delayed low generation active snapshot (Generation 1)
	manager.scheduler.upsertAuth(staleActiveSnapshot)

	// Scheduler must still reject picking because the tombstone at Generation 10 firmly blocks Generation 1
	_, errPickAfterStaleActive := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPickAfterStaleActive == nil {
		t.Fatal("expected scheduler pickSingle to fail after staleActiveSnapshot was rejected by tombstone, but succeeded")
	}
}

// Test 25: Registry projection generation protection - stale cooling projection cannot overwrite fresh clean projection
func TestModelRegistry_Projection_GenerationProtection(t *testing.T) {
	const (
		clientID = "client-projection-gen-test"
		modelID  = "gemini-3.7-proj-test"
		provider = "gemini"
	)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, provider, []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})

	epoch := reg.ClientRegistrationEpoch(clientID)

	// Apply clean projection at Generation 10
	appliedHigh := reg.ApplyClientModelProjections(clientID, epoch, 10, []registry.ClientModelProjection{
		{
			ModelID:       modelID,
			Suspended:     false,
			QuotaExceeded: false,
		},
	})
	if !appliedHigh {
		t.Fatalf("expected ApplyClientModelProjections to succeed at gen 10")
	}
	if reg.IsModelSuspendedForClient(clientID, modelID) {
		t.Fatalf("model %q should not be suspended at gen 10 clean projection", modelID)
	}

	// Attempt to apply stale cooling projection at Generation 5 (older than 10)
	appliedStale := reg.ApplyClientModelProjections(clientID, epoch, 5, []registry.ClientModelProjection{
		{
			ModelID:       modelID,
			Suspended:     true,
			SuspendReason: "stale_rate_limit",
			QuotaExceeded: true,
		},
	})
	if appliedStale {
		t.Fatalf("expected ApplyClientModelProjections to return false for stale generation 5 < 10")
	}

	// Model MUST remain clean and not suspended in registry
	if reg.IsModelSuspendedForClient(clientID, modelID) {
		t.Fatalf("stale generation 5 cooling projection erroneously overwrote generation 10 clean state")
	}

	// Apply newer cooling projection at Generation 11
	appliedNewer := reg.ApplyClientModelProjections(clientID, epoch, 11, []registry.ClientModelProjection{
		{
			ModelID:       modelID,
			Suspended:     true,
			SuspendReason: "fresh_rate_limit",
			QuotaExceeded: true,
		},
	})
	if !appliedNewer {
		t.Fatalf("expected ApplyClientModelProjections to succeed at gen 11")
	}
	if !reg.IsModelSuspendedForClient(clientID, modelID) {
		t.Fatalf("model %q should now be suspended at gen 11 cooling projection", modelID)
	}
}

// Test 26: Historical state only containing route ModelStates[B] migrates to authoritative target ModelStates[A]
func TestManager_NoForkAlias_HistoricalRouteStateOnly_MigratedToTargetCooldown(t *testing.T) {
	const (
		provider     = "antigravity"
		routeModelB  = "[ant]gemini-3.7-flash-mig"
		targetModelA = "gemini-3.7-flash-mig"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModelA,
			Alias: routeModelB,
			Fork:  false,
		}},
	})

	retryAfter := time.Now().Add(30 * time.Minute)
	auth := &Auth{
		ID:       "auth-historical-mig-test",
		Provider: provider,
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			// Only historical routeModelB state exists initially
			routeModelB: {
				Unavailable:    true,
				Status:         StatusError,
				StatusMessage:  "historical cooling state on route B",
				NextRetryAfter: retryAfter,
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModelB}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// Reconcile must migrate historical ModelStates[B] to authoritative ModelStates[A]
	manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	// Route B state must be pruned
	if _, existsB := currentAuth.ModelStates[routeModelB]; existsB {
		t.Fatalf("historical ModelStates[%q] was not pruned after reconcile", routeModelB)
	}

	// Authoritative target A state must exist with active cooldown
	stateA, existsA := currentAuth.ModelStates[targetModelA]
	if !existsA || stateA == nil {
		t.Fatalf("authoritative ModelStates[%q] missing after reconcile migration", targetModelA)
	}
	if !stateA.Unavailable || stateA.NextRetryAfter.IsZero() {
		t.Fatalf("authoritative ModelStates[%q] lost cooldown during migration: %#v", targetModelA, stateA)
	}

	// Registry must have routeModelB suspended
	if !reg.IsModelSuspendedForClient(auth.ID, routeModelB) {
		t.Fatalf("routeModelB %q should be suspended in registry after reconcile migration", routeModelB)
	}
}

// Test 27: ResetQuota reliably clears CooldownStateStore even when outer context is cancelled
func TestManager_NoForkAlias_ResetQuota_CancelledContextCleansCooldownStore(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gemini-3.7-flash-rq-test"
		targetModel = "gemini-3.7-flash-rq-test"
	)

	store := &strictContextCheckingStore{}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)

	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth := &Auth{
		ID:       "auth-resetquota-ctx-test",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	// Put into cooldown
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   provider,
		Model:      targetModel,
		RouteModel: routeModel,
		Success:    false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429 rate limit",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	// Verify store has the cooldown record
	recordsBefore := store.savedRecords()
	found := false
	for _, rec := range recordsBefore {
		if rec.AuthID == auth.ID && rec.Model == targetModel {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected store to contain cooldown record before ResetQuota, got: %#v", recordsBefore)
	}

	// Call ResetQuota with a cancelled context
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, _ = manager.ResetQuota(cancelledCtx, auth.ID)

	// Cooldown store MUST have no records remaining for this auth/model
	recordsAfter := store.savedRecords()
	for _, rec := range recordsAfter {
		if rec.AuthID == auth.ID && rec.Model == targetModel {
			t.Fatalf("cooldown record for %q was not cleared from store after cancelled-context ResetQuota", targetModel)
		}
	}

	// Memory and registry must be clean
	if reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q should no longer be suspended in registry after ResetQuota", routeModel)
	}
}

// Test 28: Concurrent operations natural termination final consistency (no manual post-reconciliation)
func TestManager_NoForkAlias_ConcurrentOperations_NaturalFinalConsistency(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "[ant]gemini-3.7-flash-nat-test"
		targetModel = "gemini-3.7-flash-nat-test"
	)

	store := &strictContextCheckingStore{}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)

	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  false,
		}},
	})

	auth := &Auth{
		ID:       "auth-nat-consistency",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	const iterations = 80
	var wg sync.WaitGroup
	wg.Add(4)

	// Worker 1: Reconcile
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			manager.ReconcileRegistryModelStates(context.Background(), auth.ID)
			time.Sleep(100 * time.Microsecond)
		}
	}()

	// Worker 2: Failures and Successes
	go func() {
		defer wg.Done()
		retryAfter := 10 * time.Minute
		for i := 0; i < iterations; i++ {
			if i%2 == 0 {
				manager.MarkResult(context.Background(), Result{
					AuthID:     auth.ID,
					Provider:   provider,
					Model:      targetModel,
					RouteModel: routeModel,
					Success:    false,
					Error: &Error{
						Code:       "rate_limit_exceeded",
						Message:    "429",
						HTTPStatus: http.StatusTooManyRequests,
					},
					RetryAfter: &retryAfter,
				})
			} else {
				manager.MarkResult(context.Background(), Result{
					AuthID:     auth.ID,
					Provider:   provider,
					Model:      targetModel,
					RouteModel: routeModel,
					Success:    true,
				})
			}
			time.Sleep(100 * time.Microsecond)
		}
	}()

	// Worker 3: ResetQuota
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			if i%3 == 0 {
				_, _, _ = manager.ResetQuota(context.Background(), auth.ID)
			}
			time.Sleep(100 * time.Microsecond)
		}
	}()

	// Worker 4: Execute
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_, _ = manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{})
			time.Sleep(100 * time.Microsecond)
		}
	}()

	wg.Wait()

	// Assert NATURAL final consistency without calling ReconcileRegistryModelStates or RefreshSchedulerEntry!
	manager.mu.RLock()
	finalAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	if finalAuth == nil {
		t.Fatal("finalAuth is nil")
	}

	// ModelStates must never contain routeModel
	if _, existsB := finalAuth.ModelStates[routeModel]; existsB {
		t.Fatalf("ModelStates contains routeModel %q after concurrent natural termination", routeModel)
	}

	// Registry suspension must strictly match memory targetModel availability
	stateA := finalAuth.ModelStates[targetModel]
	isTargetCooling := stateA != nil && (stateA.Unavailable || (!stateA.NextRetryAfter.IsZero() && stateA.NextRetryAfter.After(time.Now())))
	isRegistrySuspended := reg.IsModelSuspendedForClient(auth.ID, routeModel)
	if isTargetCooling != isRegistrySuspended {
		t.Fatalf("natural consistency mismatch: isTargetCooling=%v, isRegistrySuspended=%v", isTargetCooling, isRegistrySuspended)
	}

	// Cooldown store records must match in-memory state
	records := store.savedRecords()
	foundStoreTarget := false
	for _, rec := range records {
		if rec.AuthID == auth.ID && rec.Model == targetModel {
			foundStoreTarget = true
		}
		if rec.AuthID == auth.ID && rec.Model == routeModel {
			t.Fatalf("store contains prohibited routeModel record %q", routeModel)
		}
	}
	if isTargetCooling && !foundStoreTarget {
		t.Fatalf("targetModel is cooling in memory but missing in cooldown store")
	}
	if !isTargetCooling && foundStoreTarget {
		t.Fatalf("targetModel is clean in memory but present in cooldown store")
	}
}

// Test 29: Remove records tombstone so stale active snapshots cannot resurrect removed auth (including via rebuild)
func TestManager_Scheduler_Tombstone_RemoveRejectsDelayedActiveSnapshotAndRebuild(t *testing.T) {
	const (
		provider = "antigravity"
		model    = "gemini-3.7-flash-remove-tombstone"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "auth-remove-tombstone-test",
		Provider: provider,
		Status:   StatusActive,
	}
	registeredAuth, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	manager.RefreshSchedulerEntry(auth.ID)

	manager.mu.RLock()
	activeSnapshot := manager.auths[auth.ID].Clone()
	manager.mu.RUnlock()

	// Initial pick succeeds
	picked, errPickInitial := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPickInitial != nil || picked.ID != auth.ID {
		t.Fatalf("initial pick failed: picked=%v, err=%v", picked, errPickInitial)
	}

	// Remove the auth from manager
	manager.Remove(context.Background(), auth.ID)

	// Scheduler must immediately reject picking
	_, errPickAfterRemove := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPickAfterRemove == nil {
		t.Fatal("expected auth_not_found after Remove, but pick succeeded")
	}

	// Simulate delayed in-flight active snapshot arriving at scheduler
	manager.scheduler.upsertAuth(activeSnapshot)

	// Scheduler must still reject picking because the tombstone rejects stale active snapshot
	_, errPickAfterDelayedActive := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPickAfterDelayedActive == nil {
		t.Fatal("stale active snapshot resurrected removed auth in scheduler")
	}

	// Also test rebuild: rebuild must preserve authGenerations and filter out stale snapshot
	manager.scheduler.rebuild([]*Auth{activeSnapshot, registeredAuth})
	_, errPickAfterRebuild := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPickAfterRebuild == nil {
		t.Fatal("stale active snapshot resurrected removed auth during scheduler rebuild")
	}
}

// Test 30: UnregisterClient tombstone and multi-client shared model ghost projection protection
func TestModelRegistry_UnregisterClient_TombstoneAndGhostProjectionProtection(t *testing.T) {
	const (
		clientOwner  = "client-owner-multi-test"
		clientOther  = "client-other-multi-test"
		modelShared  = "model-shared-multi-test"
		modelOwner   = "model-owner-only-multi-test"
		providerName = "gemini"
	)

	reg := registry.GetGlobalRegistry()

	// Part 1: Unregister client keeps generation tombstone & rejects future projections
	clientUnreg := "client-unreg-gen-test"
	modelUnreg := "model-unreg-test"
	reg.RegisterClient(clientUnreg, providerName, []*registry.ModelInfo{{ID: modelUnreg}})
	t.Cleanup(func() {
		reg.UnregisterClient(clientUnreg)
	})

	unregEpoch1 := reg.ClientRegistrationEpoch(clientUnreg)

	appliedHigh := reg.ApplyClientModelProjections(clientUnreg, unregEpoch1, 10, []registry.ClientModelProjection{
		{
			ModelID:       modelUnreg,
			Suspended:     false,
			QuotaExceeded: false,
		},
	})
	if !appliedHigh {
		t.Fatalf("expected ApplyClientModelProjections to succeed at gen 10")
	}

	// Unregister clientUnreg
	reg.UnregisterClient(clientUnreg)
	unregEpoch2 := reg.ClientRegistrationEpoch(clientUnreg)

	// Older generation projection must be rejected
	appliedStale := reg.ApplyClientModelProjections(clientUnreg, unregEpoch1, 5, []registry.ClientModelProjection{
		{
			ModelID:       modelUnreg,
			Suspended:     true,
			SuspendReason: "stale_unreg",
			QuotaExceeded: true,
		},
	})
	if appliedStale {
		t.Fatalf("stale generation 5 projection accepted after unregistering client")
	}

	// Newer generation projection for unregistered client must also be rejected
	appliedNewerUnreg := reg.ApplyClientModelProjections(clientUnreg, unregEpoch2, 15, []registry.ClientModelProjection{
		{
			ModelID:       modelUnreg,
			Suspended:     true,
			SuspendReason: "newer_unreg",
			QuotaExceeded: true,
		},
	})
	if appliedNewerUnreg {
		t.Fatalf("projection accepted for unregistered client without models")
	}

	// Part 2: Multi-client ghost projection protection
	reg.RegisterClient(clientOwner, providerName, []*registry.ModelInfo{
		{ID: modelShared},
		{ID: modelOwner},
	})
	reg.RegisterClient(clientOther, providerName, []*registry.ModelInfo{
		{ID: modelShared},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(clientOwner)
		reg.UnregisterClient(clientOther)
	})

	otherEpoch := reg.ClientRegistrationEpoch(clientOther)

	// clientOther submits projection affecting modelOwner (which clientOther does NOT own)
	reg.ApplyClientModelProjections(clientOther, otherEpoch, 1, []registry.ClientModelProjection{
		{
			ModelID:       modelOwner,
			Suspended:     true,
			SuspendReason: "ghost_suspension_attempt",
			QuotaExceeded: true,
		},
	})

	// Verify modelOwner is NOT suspended for clientOwner
	if reg.IsModelSuspendedForClient(clientOwner, modelOwner) {
		t.Fatalf("ghost projection from clientOther suspended clientOwner on model %q", modelOwner)
	}
	if reg.IsModelSuspendedForClient(clientOther, modelOwner) {
		t.Fatalf("modelOwner unexpectedly reported suspended for non-owning clientOther")
	}
}

// Test 31: Chained alias configuration (B -> A and A -> C) preserves authoritative ModelStates[A] on Reconcile
func TestManager_NoForkAlias_ChainedAlias_MarkResultTargetA_ReconcilePreservesStateA(t *testing.T) {
	const (
		provider = "antigravity"
		routeB   = "route-model-b-chain"
		targetA  = "canonical-model-a-chain"
		targetC  = "target-model-c-chain"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	// Configure chained aliases: routeB -> targetA, and targetA -> targetC
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {
			{
				Name:  targetA,
				Alias: routeB,
				Fork:  false,
			},
			{
				Name:  targetC,
				Alias: targetA,
				Fork:  false,
			},
		},
	})

	auth1 := &Auth{
		ID:       "auth1-chain-test",
		Provider: provider,
		Status:   StatusActive,
	}
	auth2 := &Auth{
		ID:       "auth2-chain-test",
		Provider: provider,
		Status:   StatusActive,
	}

	if _, errReg1 := manager.Register(context.Background(), auth1); errReg1 != nil {
		t.Fatalf("register auth1: %v", errReg1)
	}
	if _, errReg2 := manager.Register(context.Background(), auth2); errReg2 != nil {
		t.Fatalf("register auth2: %v", errReg2)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, provider, []*registry.ModelInfo{{ID: routeB}})
	reg.RegisterClient(auth2.ID, provider, []*registry.ModelInfo{{ID: routeB}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	manager.RefreshSchedulerEntry(auth1.ID)
	manager.RefreshSchedulerEntry(auth2.ID)

	// Record error on authoritative target A for Auth1
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth1.ID,
		Provider:   provider,
		Model:      targetA,
		RouteModel: routeB,
		Success:    false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	// Execute Reconcile on Auth1
	manager.ReconcileRegistryModelStates(context.Background(), auth1.ID)

	manager.mu.RLock()
	currentAuth1 := manager.auths[auth1.ID]
	manager.mu.RUnlock()

	// Authoritative ModelStates[A] must be intact with active cooldown
	stateA, existsA := currentAuth1.ModelStates[targetA]
	if !existsA || stateA == nil {
		t.Fatalf("authoritative ModelStates[%q] missing after Reconcile", targetA)
	}
	if !stateA.Unavailable || stateA.NextRetryAfter.IsZero() {
		t.Fatalf("authoritative ModelStates[%q] lost cooldown: %#v", targetA, stateA)
	}

	// ModelStates[C] must NOT exist (no incorrect secondary alias migration)
	if _, existsC := currentAuth1.ModelStates[targetC]; existsC {
		t.Fatalf("ModelStates erroneously contains targetC %q due to secondary alias resolution", targetC)
	}

	// ModelStates[routeB] must NOT exist
	if _, existsB := currentAuth1.ModelStates[routeB]; existsB {
		t.Fatalf("ModelStates contains routeB %q", routeB)
	}

	// Registry must report routeB suspended for Auth1 and not suspended for Auth2
	if !reg.IsModelSuspendedForClient(auth1.ID, routeB) {
		t.Fatalf("routeB %q should be suspended in registry for Auth1", routeB)
	}
	if reg.IsModelSuspendedForClient(auth2.ID, routeB) {
		t.Fatalf("routeB %q should not be suspended in registry for Auth2", routeB)
	}

	// Manager Execute for routeB must skip Auth1 (cooling on targetA) and execute on Auth2
	resp, errExec := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeB}, cliproxyexecutor.Options{})
	if errExec != nil {
		t.Fatalf("manager.Execute failed: %v", errExec)
	}
	if string(resp.Payload) != targetA {
		t.Fatalf("execute payload = %q, want targetA %q", string(resp.Payload), targetA)
	}
	if executor.LastAuthID() != auth2.ID {
		t.Fatalf("execute picked auth = %q, want Auth2 %q (Auth1 was not skipped)", executor.LastAuthID(), auth2.ID)
	}
}

// Test 32: Unregister and re-register same clientID resets tombstone epoch in ModelRegistry
func TestModelRegistry_UnregisterAndReRegister_ResetsTombstone(t *testing.T) {
	const (
		clientID     = "client-rereg-gen-test"
		modelName    = "model-rereg-test"
		providerName = "gemini"
	)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, providerName, []*registry.ModelInfo{{ID: modelName}})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})

	epoch1 := reg.ClientRegistrationEpoch(clientID)

	// Generation 10 projection succeeds
	if !reg.ApplyClientModelProjections(clientID, epoch1, 10, []registry.ClientModelProjection{
		{ModelID: modelName, Suspended: true, SuspendReason: "cooldown_test"},
	}) {
		t.Fatalf("expected ApplyClientModelProjections at gen 10 to succeed")
	}
	if !reg.IsModelSuspendedForClient(clientID, modelName) {
		t.Fatalf("expected model to be suspended")
	}

	// Unregister client
	reg.UnregisterClient(clientID)
	epoch2 := reg.ClientRegistrationEpoch(clientID)

	// Projection on unregistered client must be rejected
	if reg.ApplyClientModelProjections(clientID, epoch2, 15, []registry.ClientModelProjection{
		{ModelID: modelName, Suspended: false},
	}) {
		t.Fatalf("expected ApplyClientModelProjections on unregistered client to return false")
	}

	// Re-register same clientID
	reg.RegisterClient(clientID, providerName, []*registry.ModelInfo{{ID: modelName}})
	epoch3 := reg.ClientRegistrationEpoch(clientID)

	// Fresh projection starting at generation 1 must succeed because tombstone epoch was reset
	if !reg.ApplyClientModelProjections(clientID, epoch3, 1, []registry.ClientModelProjection{
		{ModelID: modelName, Suspended: false},
	}) {
		t.Fatalf("expected ApplyClientModelProjections at gen 1 to succeed after re-registration")
	}
	if reg.IsModelSuspendedForClient(clientID, modelName) {
		t.Fatalf("expected model to NOT be suspended after fresh resumed projection")
	}
}

// Test 33: Remove and re-register same authID clears scheduler tombstone
func TestManager_RemoveAndReRegister_ClearsSchedulerTombstone(t *testing.T) {
	const (
		provider = "gemini"
		model    = "gemini-3.7-flash-rereg-test"
		authID   = "auth-rereg-tombstone-test"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:         authID,
		Provider:   provider,
		Status:     StatusActive,
		Generation: 5,
	}

	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("register auth: %v", errReg)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	manager.RefreshSchedulerEntry(auth.ID)

	// Initial pick succeeds
	picked, errPickInitial := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPickInitial != nil || picked.ID != auth.ID {
		t.Fatalf("initial pick failed: picked=%v, err=%v", picked, errPickInitial)
	}

	// Remove auth
	manager.Remove(context.Background(), auth.ID)

	// Scheduler must immediately reject picking
	if _, errPickAfterRemove := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil); errPickAfterRemove == nil {
		t.Fatal("expected pickSingle to fail after Remove, but succeeded")
	}

	// Re-register same auth ID with generation 1
	freshAuth := &Auth{
		ID:         authID,
		Provider:   provider,
		Status:     StatusActive,
		Generation: 1,
	}
	if _, errReReg := manager.Register(context.Background(), freshAuth); errReReg != nil {
		t.Fatalf("re-register auth: %v", errReReg)
	}
	manager.RefreshSchedulerEntry(freshAuth.ID)

	// Scheduler pick must succeed for the re-registered auth
	pickedAfterReReg, errPickAfterReReg := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPickAfterReReg != nil || pickedAfterReReg.ID != freshAuth.ID {
		t.Fatalf("pick after re-register failed: picked=%v, err=%v", pickedAfterReReg, errPickAfterReReg)
	}
}

// Test 34: Chained alias when both B and A are registered routes: targetA is authoritative and never migrated/deleted
func TestManager_NoForkAlias_ChainedAlias_BothRoutesRegistered_SafeTwoPhaseMigration(t *testing.T) {
	const (
		provider = "antigravity"
		routeB   = "route-model-b-chain-both"
		targetA  = "canonical-model-a-chain-both"
		targetC  = "target-model-c-chain-both"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	// Aliases: routeB -> targetA, targetA -> targetC
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {
			{
				Name:  targetA,
				Alias: routeB,
				Fork:  false,
			},
			{
				Name:  targetC,
				Alias: targetA,
				Fork:  false,
			},
		},
	})

	auth := &Auth{
		ID:       "auth-chain-both-test",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("register auth: %v", errReg)
	}

	reg := registry.GetGlobalRegistry()
	// Both routeB and targetA are registered as supported models for this client
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{
		{ID: routeB},
		{ID: targetA},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	manager.RefreshSchedulerEntry(auth.ID)

	// Set cooldown on targetA
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   provider,
		Model:      targetA,
		RouteModel: routeB,
		Success:    false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	// Reconcile
	manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

	manager.mu.RLock()
	currentAuth := manager.auths[auth.ID]
	manager.mu.RUnlock()

	// targetA is authoritative for routeB (authoritativeTargets[targetA] == true)
	// It must NOT be deleted or mutated into targetC
	stateA, existsA := currentAuth.ModelStates[targetA]
	if !existsA || stateA == nil {
		t.Fatalf("authoritative ModelStates[%q] missing after two-phase reconcile", targetA)
	}
	if !stateA.Unavailable || stateA.NextRetryAfter.IsZero() {
		t.Fatalf("authoritative ModelStates[%q] lost cooldown: %#v", targetA, stateA)
	}

	if _, existsC := currentAuth.ModelStates[targetC]; existsC {
		t.Fatalf("ModelStates incorrectly contains targetC %q", targetC)
	}
	if _, existsB := currentAuth.ModelStates[routeB]; existsB {
		t.Fatalf("ModelStates contains routeB %q", routeB)
	}
}

type mockFailingAuthStoreForResetQuota struct {
	saveErr error
}

func (s *mockFailingAuthStoreForResetQuota) List(ctx context.Context) ([]*Auth, error) {
	return nil, nil
}

func (s *mockFailingAuthStoreForResetQuota) Save(ctx context.Context, auth *Auth) (string, error) {
	if s.saveErr != nil {
		return "", s.saveErr
	}
	if auth != nil {
		return auth.ID, nil
	}
	return "", nil
}

func (s *mockFailingAuthStoreForResetQuota) Delete(ctx context.Context, id string) error {
	return nil
}

// Test 35: ResetQuota reliably clears CooldownStore via defer even when Auth Store.Save returns error
func TestManager_NoForkAlias_ResetQuota_ErrorPersistCleansCooldownStore(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "route-model-err-persist"
		targetModel = "target-model-err-persist"
	)

	failingStore := &mockFailingAuthStoreForResetQuota{
		saveErr: errors.New("simulated disk write failure"),
	}
	cooldownStore := &strictContextCheckingStore{}
	manager := NewManager(failingStore, nil, nil)
	manager.SetCooldownStateStore(cooldownStore)

	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {
			{
				Name:  targetModel,
				Alias: routeModel,
				Fork:  false,
			},
		},
	})

	auth := &Auth{
		ID:       "auth-err-persist-test",
		Provider: provider,
		Status:   StatusActive,
		Metadata: map[string]any{"type": "oauth"},
	}
	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("register auth: %v", errReg)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	manager.RefreshSchedulerEntry(auth.ID)

	// Trigger cooldown
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   provider,
		Model:      targetModel,
		RouteModel: routeModel,
		Success:    false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	// Verify cooldownStore contains records
	recordsBefore, errLoadBefore := cooldownStore.Load(context.Background())
	if errLoadBefore != nil || len(recordsBefore) == 0 {
		t.Fatalf("expected cooldown records in store before ResetQuota, got: %#v, err: %v", recordsBefore, errLoadBefore)
	}

	// ResetQuota will encounter failingStore error on persist
	_, _, errReset := manager.ResetQuota(context.Background(), auth.ID)
	if errReset == nil {
		t.Fatal("expected ResetQuota to return simulated error from auth store, got nil")
	}

	// Verify cooldown store is nevertheless cleanly cleared via defer
	recordsAfter, errLoadAfter := cooldownStore.Load(context.Background())
	if errLoadAfter != nil {
		t.Fatalf("failed to load cooldown store: %v", errLoadAfter)
	}
	if len(recordsAfter) != 0 {
		t.Fatalf("expected cooldown store to be cleared after ResetQuota, got: %#v", recordsAfter)
	}

	// Verify registry projection is also resumed
	if reg.IsModelSuspendedForClient(auth.ID, routeModel) {
		t.Fatalf("routeModel %q should no longer be suspended in registry after ResetQuota", routeModel)
	}
}

// Test 36: Model A cooldown does NOT suspend independent Model C, and no aggregation fallback flip occurs
func TestManager_NoForkAlias_PerModelIsolation_NoAggregationFallbackFlip(t *testing.T) {
	const (
		provider = "gemini"
		modelA   = "model-a-isolated"
		modelC   = "model-c-isolated"
		authID   = "auth-isolation-test"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("register auth: %v", errReg)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{
		{ID: modelA},
		{ID: modelC},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	manager.RefreshSchedulerEntry(auth.ID)

	// Cooldown model A only
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   provider,
		Model:      modelA,
		RouteModel: modelA,
		Success:    false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	// Reconcile
	manager.ReconcileRegistryModelStates(context.Background(), auth.ID)

	// Model A must be suspended
	if !reg.IsModelSuspendedForClient(auth.ID, modelA) {
		t.Fatalf("model A should be suspended in registry")
	}

	// Model C has state == nil and auth is not disabled, so Model C must NOT be suspended!
	if reg.IsModelSuspendedForClient(auth.ID, modelC) {
		t.Fatalf("model C was incorrectly suspended due to aggregated availability fallback!")
	}
}

// Test 37: Monotonically increasing Registration Epoch ensures delayed high-generation snapshots from old epoch are rejected in both ModelRegistry and Scheduler
func TestManager_NoForkAlias_MonotonicRegistrationEpoch_RejectsOldEpochHighGeneration(t *testing.T) {
	const (
		provider = "gemini"
		modelID  = "gemini-epoch-test-model"
		authID   = "auth-epoch-gen-test"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()

	// --- 1. ModelRegistry Epoch Verification ---
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	epoch1 := reg.ClientRegistrationEpoch(authID)
	if epoch1 == 0 {
		t.Fatalf("expected epoch >= 1 after RegisterClient, got %d", epoch1)
	}

	// Apply projection at epoch1, generation 100
	if !reg.ApplyClientModelProjections(authID, epoch1, 100, []registry.ClientModelProjection{
		{ModelID: modelID, Suspended: true, SuspendReason: "cooldown_epoch_1"},
	}) {
		t.Fatalf("expected ApplyClientModelProjections to succeed at epoch1 gen 100")
	}
	if !reg.IsModelSuspendedForClient(authID, modelID) {
		t.Fatalf("expected model to be suspended at epoch1")
	}

	// Unregister client -> bumps epoch
	reg.UnregisterClient(authID)
	epoch2 := reg.ClientRegistrationEpoch(authID)
	if epoch2 <= epoch1 {
		t.Fatalf("expected epoch to increment on UnregisterClient: epoch2=%d, epoch1=%d", epoch2, epoch1)
	}

	// Re-register client -> bumps epoch again
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: modelID}})
	epoch3 := reg.ClientRegistrationEpoch(authID)
	if epoch3 <= epoch2 {
		t.Fatalf("expected epoch to increment on re-RegisterClient: epoch3=%d, epoch2=%d", epoch3, epoch2)
	}

	// Delayed snapshot from epoch1 with extremely high generation (gen 99999) arrives
	staleDelayedOldEpoch := reg.ApplyClientModelProjections(authID, epoch1, 99999, []registry.ClientModelProjection{
		{ModelID: modelID, Suspended: true, SuspendReason: "delayed_stale_suspension"},
	})
	if staleDelayedOldEpoch {
		t.Fatalf("expected ApplyClientModelProjections to REJECT stale old epoch %d < current epoch %d with high generation 99999", epoch1, epoch3)
	}

	// Fresh projection on epoch3 with low generation 1 MUST succeed
	if !reg.ApplyClientModelProjections(authID, epoch3, 1, []registry.ClientModelProjection{
		{ModelID: modelID, Suspended: false},
	}) {
		t.Fatalf("expected ApplyClientModelProjections on current epoch3 gen 1 to succeed")
	}
	if reg.IsModelSuspendedForClient(authID, modelID) {
		t.Fatalf("expected model to NOT be suspended after fresh epoch3 gen 1 projection")
	}

	// --- 2. AuthScheduler Epoch Verification ---
	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	registeredAuth, errReg := manager.Register(context.Background(), auth)
	if errReg != nil {
		t.Fatalf("register auth: %v", errReg)
	}
	manager.RefreshSchedulerEntry(authID)

	authEpoch1 := registeredAuth.RegistrationEpoch
	if authEpoch1 == 0 {
		t.Fatalf("expected RegistrationEpoch >= 1, got %d", authEpoch1)
	}

	// Verify initial scheduler pick succeeds
	picked1, errPick1 := manager.scheduler.pickSingle(context.Background(), provider, modelID, cliproxyexecutor.Options{}, nil)
	if errPick1 != nil || picked1.ID != authID {
		t.Fatalf("initial scheduler pick failed: picked=%v, err=%v", picked1, errPick1)
	}

	// Unregister auth from manager
	manager.Remove(context.Background(), authID)

	// Verify scheduler pick fails after removal
	if _, errPickAfterRemove := manager.scheduler.pickSingle(context.Background(), provider, modelID, cliproxyexecutor.Options{}, nil); errPickAfterRemove == nil {
		t.Fatalf("expected scheduler pickSingle to fail after remove")
	}

	// Re-register same auth ID
	freshAuth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	registeredFreshAuth, errReReg := manager.Register(context.Background(), freshAuth)
	if errReReg != nil {
		t.Fatalf("re-register auth: %v", errReReg)
	}
	manager.RefreshSchedulerEntry(authID)

	authEpoch2 := registeredFreshAuth.RegistrationEpoch
	if authEpoch2 <= authEpoch1 {
		t.Fatalf("expected RegistrationEpoch to monotonically increase: authEpoch2=%d, authEpoch1=%d", authEpoch2, authEpoch1)
	}

	// Simulate delayed snapshot from old epoch1 with high generation (gen 99999) and Disabled = true
	delayedOldEpochSnapshot := &Auth{
		ID:                authID,
		Provider:          provider,
		RegistrationEpoch: authEpoch1,
		Generation:        99999,
		Disabled:          true,
		Status:            StatusDisabled,
		UpdatedAt:         time.Now(),
	}
	manager.scheduler.upsertAuth(delayedOldEpochSnapshot)

	// Scheduler must still pick the fresh active auth because old epoch snapshot was ignored
	picked2, errPick2 := manager.scheduler.pickSingle(context.Background(), provider, modelID, cliproxyexecutor.Options{}, nil)
	if errPick2 != nil || picked2.ID != authID {
		t.Fatalf("expected scheduler pick to succeed after stale old epoch snapshot ignored: picked=%v, err=%v", picked2, errPick2)
	}
}

// Test 38: Reconcile detects concurrent RegisterClient epoch bump, reloads latest supported models atomically and preserves active cooldown
func TestManager_NoForkAlias_Reconcile_ConcurrentClientRegistrationEpochChange_PreservesCooldown(t *testing.T) {
	const (
		provider = "gemini"
		modelA   = "gemini-route-model-a"
		modelB   = "gemini-route-model-b"
		modelC   = "gemini-route-model-c"
		authID   = "auth-reconcile-epoch-change-test"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	// Initially client supports modelA and modelB
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{
		{ID: modelA},
		{ID: modelB},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("register auth: %v", errReg)
	}
	manager.RefreshSchedulerEntry(authID)

	// Cooldown modelB
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     authID,
		Provider:   provider,
		Model:      modelB,
		RouteModel: modelB,
		Success:    false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	// Concurrently re-register client with updated model set (modelB and modelC), bumping epoch
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{
		{ID: modelB},
		{ID: modelC},
	})

	// Reconcile must atomically detect the epoch change and retain modelB cooldown while dropping disappeared modelA
	manager.ReconcileRegistryModelStates(context.Background(), authID)

	// ModelB must still be suspended in registry
	if !reg.IsModelSuspendedForClient(authID, modelB) {
		t.Fatalf("expected modelB to remain suspended in registry after reconcile with epoch update")
	}

	// ModelC is newly registered and must NOT be suspended
	if reg.IsModelSuspendedForClient(authID, modelC) {
		t.Fatalf("expected new modelC to NOT be suspended in registry")
	}

	// Check auth state in manager
	manager.mu.RLock()
	reconciledAuth := manager.auths[authID]
	manager.mu.RUnlock()

	if reconciledAuth == nil {
		t.Fatalf("auth not found in manager")
	}
	if _, hasA := reconciledAuth.ModelStates[modelA]; hasA {
		t.Fatalf("expected modelA to be pruned from ModelStates since it was removed in updated registry registration")
	}
	if stateB, hasB := reconciledAuth.ModelStates[modelB]; !hasB || stateB == nil || !isModelStateActiveCooldown(stateB, time.Now()) {
		t.Fatalf("expected modelB to retain active cooldown in ModelStates, got: %#v", stateB)
	}
}

// Test 39: High concurrency stability test under simultaneous Register, Reconcile, MarkResult, Unregister, and Projections
func TestManager_NoForkAlias_ConcurrentHighLoadStability_EpochAndReconcile(t *testing.T) {
	const (
		provider   = "gemini"
		authID     = "auth-high-load-stability"
		numWorkers = 8
		numIters   = 50
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("register auth: %v", errReg)
	}
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{
		{ID: "model-stable-1"},
		{ID: "model-stable-2"},
	})

	var wg sync.WaitGroup

	// Worker 1: Concurrent ReconcileRegistryModelStates
	for i := 0; i < numWorkers/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numIters; j++ {
				manager.ReconcileRegistryModelStates(context.Background(), authID)
			}
		}()
	}

	// Worker 2: Concurrent MarkResult (cooldown / success)
	for i := 0; i < numWorkers/2; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			modelName := "model-stable-1"
			if workerID%2 == 1 {
				modelName = "model-stable-2"
			}
			retryAfter := 10 * time.Minute
			for j := 0; j < numIters; j++ {
				if j%2 == 0 {
					manager.MarkResult(context.Background(), Result{
						AuthID:     authID,
						Provider:   provider,
						Model:      modelName,
						RouteModel: modelName,
						Success:    false,
						Error: &Error{
							Code:       "rate_limit_exceeded",
							Message:    "429",
							HTTPStatus: http.StatusTooManyRequests,
						},
						RetryAfter: &retryAfter,
					})
				} else {
					manager.MarkResult(context.Background(), Result{
						AuthID:     authID,
						Provider:   provider,
						Model:      modelName,
						RouteModel: modelName,
						Success:    true,
					})
				}
			}
		}(i)
	}

	// Worker 3: Concurrent RegisterClient with alternating models
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < numIters; j++ {
			if j%2 == 0 {
				reg.RegisterClient(authID, provider, []*registry.ModelInfo{
					{ID: "model-stable-1"},
					{ID: "model-stable-2"},
				})
			} else {
				reg.RegisterClient(authID, provider, []*registry.ModelInfo{
					{ID: "model-stable-2"},
					{ID: "model-stable-3"},
				})
			}
		}
	}()

	// Worker 4: Concurrent scheduler pickSingle
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < numIters; j++ {
			_, _ = manager.scheduler.pickSingle(context.Background(), provider, "model-stable-2", cliproxyexecutor.Options{}, nil)
		}
	}()

	wg.Wait()

	// Final verification: manager and registry remain consistent and pick succeeds on clean model
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{
		{ID: "model-stable-final"},
	})
	_, _, _ = manager.ResetQuota(context.Background(), authID)
	manager.ReconcileRegistryModelStates(context.Background(), authID)

	if reg.IsModelSuspendedForClient(authID, "model-stable-final") {
		t.Fatalf("expected model-stable-final to not be suspended after reset and reconcile")
	}
}

// Test 40: Manager Update rejects stale registration epoch
func TestManager_Update_RejectsStaleRegistrationEpoch(t *testing.T) {
	const (
		provider = "gemini"
		authID   = "auth-update-stale-epoch-test"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	registered1, errReg1 := manager.Register(context.Background(), auth)
	if errReg1 != nil {
		t.Fatalf("register auth: %v", errReg1)
	}
	epoch1 := registered1.RegistrationEpoch
	if epoch1 == 0 {
		t.Fatalf("expected registration epoch >= 1, got %d", epoch1)
	}

	// Remove and re-register
	manager.Remove(context.Background(), authID)

	freshAuth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	registered2, errReg2 := manager.Register(context.Background(), freshAuth)
	if errReg2 != nil {
		t.Fatalf("re-register auth: %v", errReg2)
	}
	epoch2 := registered2.RegistrationEpoch
	if epoch2 <= epoch1 {
		t.Fatalf("expected epoch2 (%d) > epoch1 (%d)", epoch2, epoch1)
	}

	// Attempt Update with stale epoch1
	staleUpdateAuth := registered1.Clone()
	staleUpdateAuth.RegistrationEpoch = epoch1
	_, errStaleUpdate := manager.Update(context.Background(), staleUpdateAuth)
	if errStaleUpdate == nil {
		t.Fatalf("expected Update with stale epoch %d < current epoch %d to fail, but succeeded", epoch1, epoch2)
	}

	// Update with current epoch2 must succeed
	validUpdateAuth := registered2.Clone()
	validUpdateAuth.RegistrationEpoch = epoch2
	updated, errValidUpdate := manager.Update(context.Background(), validUpdateAuth)
	if errValidUpdate != nil {
		t.Fatalf("expected Update with current epoch to succeed: %v", errValidUpdate)
	}
	if updated.Generation <= registered2.Generation {
		t.Fatalf("expected Generation to increase on valid update: %d <= %d", updated.Generation, registered2.Generation)
	}
}

// Test 41: Manager Remove increments epoch and tombstone blocks high-gen old epoch snapshot
func TestManager_Remove_IncrementsAuthEpoch_AndRejectsDelayedOldEpochSnapshot(t *testing.T) {
	const (
		provider = "gemini"
		model    = "gemini-remove-tombstone-epoch-model"
		authID   = "auth-remove-tombstone-epoch-test"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	registered, errReg := manager.Register(context.Background(), auth)
	if errReg != nil {
		t.Fatalf("register auth: %v", errReg)
	}
	manager.RefreshSchedulerEntry(authID)

	epoch1 := registered.RegistrationEpoch

	// Pick must succeed initially
	picked, errPick1 := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPick1 != nil || picked.ID != authID {
		t.Fatalf("initial pick failed: picked=%v, err=%v", picked, errPick1)
	}

	// Remove auth
	manager.Remove(context.Background(), authID)

	manager.mu.RLock()
	currentEpoch := manager.authEpochs[authID]
	manager.mu.RUnlock()

	if currentEpoch <= epoch1 {
		t.Fatalf("expected manager.authEpochs to increment on Remove: %d <= %d", currentEpoch, epoch1)
	}

	// Delayed snapshot from epoch1 with generation 99999 arrives at scheduler
	delayedSnapshot := &Auth{
		ID:                authID,
		Provider:          provider,
		RegistrationEpoch: epoch1,
		Generation:        99999,
		Status:            StatusActive,
		UpdatedAt:         time.Now(),
	}
	manager.scheduler.upsertAuth(delayedSnapshot)

	// Scheduler pick must still fail because the removal tombstone rejects old epoch
	if _, errPickAfterStale := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil); errPickAfterStale == nil {
		t.Fatalf("expected scheduler pickSingle to fail after stale snapshot rejected by tombstone, but succeeded")
	}
}

// Test 42: ModelRegistry ApplyClientModelProjections strict epoch and batch validation
func TestModelRegistry_ApplyClientModelProjections_StrictEpochAndBatchValidation(t *testing.T) {
	const (
		clientID     = "client-proj-strict-test"
		model1       = "model-proj-strict-1"
		modelUnowned = "model-proj-strict-unowned"
		provider     = "gemini"
	)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, provider, []*registry.ModelInfo{{ID: model1}})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})

	epoch := reg.ClientRegistrationEpoch(clientID)

	// 1. Mismatched epoch (higher than current) must be rejected
	if reg.ApplyClientModelProjections(clientID, epoch+10, 1, []registry.ClientModelProjection{
		{ModelID: model1, Suspended: true},
	}) {
		t.Fatalf("expected ApplyClientModelProjections with mismatched epoch to return false")
	}

	// 2. Empty projections batch must return false and not advance generation
	if reg.ApplyClientModelProjections(clientID, epoch, 10, []registry.ClientModelProjection{}) {
		t.Fatalf("expected ApplyClientModelProjections with empty batch to return false")
	}

	// 3. Batch with only unowned models must return false and not advance generation
	if reg.ApplyClientModelProjections(clientID, epoch, 10, []registry.ClientModelProjection{
		{ModelID: modelUnowned, Suspended: true},
	}) {
		t.Fatalf("expected ApplyClientModelProjections with unowned model batch to return false")
	}

	// 4. Valid batch on exact epoch must succeed and advance generation to 5
	if !reg.ApplyClientModelProjections(clientID, epoch, 5, []registry.ClientModelProjection{
		{ModelID: model1, Suspended: true, SuspendReason: "strict_test"},
	}) {
		t.Fatalf("expected ApplyClientModelProjections with valid batch to return true")
	}
	if !reg.IsModelSuspendedForClient(clientID, model1) {
		t.Fatalf("expected model1 to be suspended")
	}

	// 5. Stale generation (3 < 5) on exact epoch must be rejected
	if reg.ApplyClientModelProjections(clientID, epoch, 3, []registry.ClientModelProjection{
		{ModelID: model1, Suspended: false},
	}) {
		t.Fatalf("expected ApplyClientModelProjections with stale generation to return false")
	}
	if !reg.IsModelSuspendedForClient(clientID, model1) {
		t.Fatalf("expected model1 to remain suspended after stale projection rejected")
	}
}

// Test 43: Scheduler rejects delayed old-epoch Remove tombstone after new-epoch registration
func TestManager_Scheduler_DelayedOldEpochRemoveTombstone_RejectedAndNewAuthSchedulable(t *testing.T) {
	const (
		provider = "gemini"
		model    = "gemini-old-tombstone-rejection-model"
		authID   = "auth-old-tombstone-rejection-test"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	// 1. Initial registration at epoch 1
	auth1 := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	regAuth1, errReg1 := manager.Register(context.Background(), auth1)
	if errReg1 != nil {
		t.Fatalf("initial register: %v", errReg1)
	}
	manager.RefreshSchedulerEntry(authID)
	epoch1 := regAuth1.RegistrationEpoch

	// Pick must succeed
	picked1, errPick1 := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPick1 != nil || picked1.ID != authID {
		t.Fatalf("pick 1 failed: picked=%v, err=%v", picked1, errPick1)
	}

	// 2. Auth is removed -> Manager increments epoch and records tombstone
	manager.Remove(context.Background(), authID)
	manager.mu.RLock()
	removalEpoch := manager.authEpochs[authID]
	manager.mu.RUnlock()
	if removalEpoch <= epoch1 {
		t.Fatalf("expected removal epoch > epoch1: %d <= %d", removalEpoch, epoch1)
	}

	// Pick must fail
	if _, errPickRemoved := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil); errPickRemoved == nil {
		t.Fatalf("expected pickSingle to fail after removal, but succeeded")
	}

	// 3. Re-register same authID -> gets new monotonic epoch > removalEpoch
	auth2 := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	regAuth2, errReg2 := manager.Register(context.Background(), auth2)
	if errReg2 != nil {
		t.Fatalf("re-register: %v", errReg2)
	}
	manager.RefreshSchedulerEntry(authID)
	epoch2 := regAuth2.RegistrationEpoch
	if epoch2 <= removalEpoch {
		t.Fatalf("expected re-registered epoch > removalEpoch: %d <= %d", epoch2, removalEpoch)
	}

	// Pick must succeed again
	picked2, errPick2 := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPick2 != nil || picked2.ID != authID {
		t.Fatalf("pick 2 failed: picked=%v, err=%v", picked2, errPick2)
	}

	// 4. Delayed old tombstone from removalEpoch arrives at scheduler
	manager.scheduler.RecordRemovalTombstone(authID, removalEpoch)

	// Verify that the scheduler ignored the old tombstone:
	// - authGenerations must retain epoch2
	manager.scheduler.mu.Lock()
	meta := manager.scheduler.authGenerations[authID]
	manager.scheduler.mu.Unlock()
	if meta.epoch != epoch2 {
		t.Fatalf("expected scheduler authGenerations to retain epoch %d, got %d", epoch2, meta.epoch)
	}
	if meta.generation == 0 {
		t.Fatalf("expected scheduler generation > 0, got 0 (incorrectly zeroed by old tombstone)")
	}

	// - Pick must STILL succeed and return the new auth
	picked3, errPick3 := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPick3 != nil || picked3.ID != authID {
		t.Fatalf("pick 3 failed after delayed tombstone: picked=%v, err=%v", picked3, errPick3)
	}
}

// Test 44: Reconcile with exhausted retries due to persistent epoch mismatches safely aborts without committing stale snapshot
func TestManager_NoForkAlias_Reconcile_ExhaustedRetryOnEpochMismatch_PreservesStateAndNoStaleSnapshot(t *testing.T) {
	const (
		provider = "gemini"
		modelA   = "gemini-reconcile-exhaust-model-a"
		modelB   = "gemini-reconcile-exhaust-model-b"
		authID   = "auth-reconcile-exhaust-test"
	)

	manager := NewManager(nil, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{
		{ID: modelA},
		{ID: modelB},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("register auth: %v", errReg)
	}
	manager.RefreshSchedulerEntry(authID)

	// Cooldown modelB
	retryAfter := 30 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     authID,
		Provider:   provider,
		Model:      modelB,
		RouteModel: modelB,
		Success:    false,
		Error: &Error{
			Code:       "rate_limit_exceeded",
			Message:    "429",
			HTTPStatus: http.StatusTooManyRequests,
		},
		RetryAfter: &retryAfter,
	})

	manager.mu.RLock()
	authBefore := manager.auths[authID].Clone()
	manager.mu.RUnlock()
	initialGen := authBefore.Generation

	// Start continuous background client registration updates to cause epoch mismatch on every retry
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		for {
			select {
			case <-stopCh:
				return
			default:
				reg.RegisterClient(authID, provider, []*registry.ModelInfo{
					{ID: modelA},
					{ID: modelB},
				})
			}
		}
	}()

	// Trigger Reconcile while epochs are constantly shifting
	manager.ReconcileRegistryModelStates(context.Background(), authID)
	close(stopCh)
	<-doneCh

	// Verify that if epoch mismatch occurred and retries exhausted without commit:
	// 1. In-memory auth was NOT modified to an invalid or stale snapshot
	manager.mu.RLock()
	authAfter := manager.auths[authID]
	manager.mu.RUnlock()

	if authAfter == nil {
		t.Fatalf("auth was lost in manager")
	}
	// Cooldown for modelB must be preserved
	stateB, hasB := authAfter.ModelStates[modelB]
	if !hasB || stateB == nil || !isModelStateActiveCooldown(stateB, time.Now()) {
		t.Fatalf("expected modelB to retain active cooldown after retry exhaustion, got: %#v", stateB)
	}
	// Generation must not have mutated if reconcile aborted without committing
	// Or if it committed on a matched retry, it must still have valid state
	if authAfter.Generation < initialGen {
		t.Fatalf("unexpected generation decrease: %d < %d", authAfter.Generation, initialGen)
	}

	// Perform a clean, unperturbed Reconcile now to ensure full eventual consistency
	manager.ReconcileRegistryModelStates(context.Background(), authID)

	manager.mu.RLock()
	authFinal := manager.auths[authID]
	manager.mu.RUnlock()

	stateBFinal, hasBFinal := authFinal.ModelStates[modelB]
	if !hasBFinal || stateBFinal == nil || !isModelStateActiveCooldown(stateBFinal, time.Now()) {
		t.Fatalf("expected modelB to retain active cooldown after clean reconcile, got: %#v", stateBFinal)
	}
}

type memoryAuthTestStore struct {
	mu    sync.Mutex
	auths map[string]*Auth
}

func newMemoryAuthTestStore() *memoryAuthTestStore {
	return &memoryAuthTestStore{auths: make(map[string]*Auth)}
}

func (s *memoryAuthTestStore) List(ctx context.Context) ([]*Auth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := make([]*Auth, 0, len(s.auths))
	for _, a := range s.auths {
		res = append(res, a.Clone())
	}
	return res, nil
}

func (s *memoryAuthTestStore) Save(ctx context.Context, auth *Auth) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auths[auth.ID] = auth.Clone()
	return auth.ID, nil
}

func (s *memoryAuthTestStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.auths, id)
	return nil
}

// Test 45: Manager Load assigns monotonically increasing epochs on reload and records tombstones for removed items
func TestManager_Load_MonotonicEpochAssignment_AndRemovalTombstones(t *testing.T) {
	const (
		provider = "gemini"
		model    = "gemini-load-epoch-test-model"
		authID1  = "auth-load-epoch-1"
		authID2  = "auth-load-epoch-2"
	)

	store := newMemoryAuthTestStore()
	store.Save(context.Background(), &Auth{
		ID:                authID1,
		Provider:          provider,
		Status:            StatusActive,
		RegistrationEpoch: 5,
		Generation:        10,
	})
	store.Save(context.Background(), &Auth{
		ID:                authID2,
		Provider:          provider,
		Status:            StatusActive,
		RegistrationEpoch: 3,
		Generation:        20,
	})

	manager := NewManager(store, nil, nil)
	executor := &noForkAliasTestExecutor{id: provider}
	manager.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID1, provider, []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(authID2, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID1)
		reg.UnregisterClient(authID2)
	})

	// 1. Initial Load from store
	if errLoad := manager.Load(context.Background()); errLoad != nil {
		t.Fatalf("manager.Load failed: %v", errLoad)
	}

	manager.mu.RLock()
	loadedAuth1 := manager.auths[authID1]
	loadedAuth2 := manager.auths[authID2]
	manager.mu.RUnlock()

	if loadedAuth1 == nil || loadedAuth2 == nil {
		t.Fatalf("expected both auths loaded: auth1=%v, auth2=%v", loadedAuth1, loadedAuth2)
	}
	// Epochs must be strictly greater than disk epochs:
	// auth1: max(0, 5) + 1 = 6
	if loadedAuth1.RegistrationEpoch != 6 {
		t.Fatalf("expected auth1 RegistrationEpoch == 6, got %d", loadedAuth1.RegistrationEpoch)
	}
	if loadedAuth1.Generation != 1 {
		t.Fatalf("expected auth1 Generation == 1, got %d", loadedAuth1.Generation)
	}
	// auth2: max(0, 3) + 1 = 4
	if loadedAuth2.RegistrationEpoch != 4 {
		t.Fatalf("expected auth2 RegistrationEpoch == 4, got %d", loadedAuth2.RegistrationEpoch)
	}
	if loadedAuth2.Generation != 1 {
		t.Fatalf("expected auth2 Generation == 1, got %d", loadedAuth2.Generation)
	}

	// Both auths must be picked from scheduler
	manager.RefreshSchedulerEntry(authID1)
	manager.RefreshSchedulerEntry(authID2)

	pickedSet := make(map[string]bool)
	for i := 0; i < 4; i++ {
		p, errPick := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("scheduler pick failed: %v", errPick)
		}
		pickedSet[p.ID] = true
	}
	if !pickedSet[authID1] || !pickedSet[authID2] {
		t.Fatalf("expected both auth1 and auth2 to be picked, got set: %#v", pickedSet)
	}

	// 2. Remove authID2 from store and reload
	_ = store.Delete(context.Background(), authID2)

	if errLoad2 := manager.Load(context.Background()); errLoad2 != nil {
		t.Fatalf("manager.Load 2 failed: %v", errLoad2)
	}

	manager.mu.RLock()
	reloadedAuth1 := manager.auths[authID1]
	reloadedAuth2 := manager.auths[authID2]
	epochAuth2Tombstone := manager.authEpochs[authID2]
	manager.mu.RUnlock()

	// auth1 must get new epoch: max(6, 6) + 1 = 7
	if reloadedAuth1 == nil || reloadedAuth1.RegistrationEpoch != 7 {
		t.Fatalf("expected reloaded auth1 with epoch 7, got %#v", reloadedAuth1)
	}
	// auth2 must NOT exist in manager.auths
	if reloadedAuth2 != nil {
		t.Fatalf("expected auth2 to be absent from manager.auths, got %#v", reloadedAuth2)
	}
	// auth2 tombstone epoch must be incremented: 4 + 1 = 5
	if epochAuth2Tombstone != 5 {
		t.Fatalf("expected auth2 tombstone epoch == 5, got %d", epochAuth2Tombstone)
	}

	// Scheduler must have tombstone for auth2 with epoch 5
	manager.scheduler.mu.Lock()
	metaAuth2 := manager.scheduler.authGenerations[authID2]
	manager.scheduler.mu.Unlock()
	if metaAuth2.epoch != 5 || metaAuth2.generation != 0 {
		t.Fatalf("expected scheduler tombstone for auth2 with epoch 5, generation 0, got %#v", metaAuth2)
	}

	// Refresh scheduler for auth1 and test picks
	manager.RefreshSchedulerEntry(authID1)
	pickedAfter, errPickAfter := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	if errPickAfter != nil || pickedAfter.ID != authID1 {
		t.Fatalf("expected pickSingle to return auth1, got: picked=%v, err=%v", pickedAfter, errPickAfter)
	}

	// 3. Stale snapshot of authID2 with old epoch 4 (or 3) arriving at scheduler MUST be rejected by tombstone
	staleAuth2 := &Auth{
		ID:                authID2,
		Provider:          provider,
		Status:            StatusActive,
		RegistrationEpoch: 4,
		Generation:        999,
		UpdatedAt:         time.Now(),
	}
	manager.scheduler.upsertAuth(staleAuth2)

	// Pick must still ONLY return authID1, never authID2
	for i := 0; i < 3; i++ {
		p, errP := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
		if errP != nil || p.ID != authID1 {
			t.Fatalf("expected only auth1 schedulable, got: picked=%v, err=%v", p, errP)
		}
	}
}
