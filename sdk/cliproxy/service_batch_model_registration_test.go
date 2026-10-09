package cliproxy

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type mockBatchTestExecutor struct {
	provider string
}

func (e mockBatchTestExecutor) Identifier() string { return e.provider }
func (e mockBatchTestExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e mockBatchTestExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (e mockBatchTestExecutor) Refresh(context.Context, *coreauth.Auth) (*coreauth.Auth, error) {
	return nil, nil
}
func (e mockBatchTestExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e mockBatchTestExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestRegisterModelsForAuthBatch_StaleSnapshot_Issue6453(t *testing.T) {
	ctx := context.Background()
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(mockBatchTestExecutor{provider: "codex"})
	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
	}

	authID := "auth-codex-batch-stale-6453"
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"plan_type": "pro",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("failed to register auth: %v", errRegister)
	}

	allowedID := "auth-codex-batch-allowed-6453"
	allowedAuth := &coreauth.Auth{
		ID:       allowedID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"plan_type": "pro",
		},
	}
	if _, errRegister := manager.Register(ctx, allowedAuth); errRegister != nil {
		t.Fatalf("failed to register allowed auth: %v", errRegister)
	}

	reg := GlobalModelRegistry()
	reg.UnregisterClient(authID)
	reg.UnregisterClient(allowedID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
		reg.UnregisterClient(allowedID)
	})

	// Capture a stale snapshot of auths before excluded_models is configured on authID
	staleSnapshot := manager.List()

	// Update the auth in coreManager to exclude gpt-6-astra and gpt-6.1-sol
	updatedAuth := auth.Clone()
	updatedAuth.Attributes = map[string]string{
		"auth_kind":       "oauth",
		"plan_type":       "pro",
		"excluded_models": "gpt-6-astra,gpt-6.1-sol",
	}
	if _, errUpdate := manager.Update(ctx, updatedAuth); errUpdate != nil {
		t.Fatalf("failed to update auth: %v", errUpdate)
	}

	// Run batch registration with the stale snapshot
	service.registerModelsForAuthBatch(ctx, staleSnapshot)

	// Verify that the excluded models are not supported for this client
	if reg.ClientSupportsModel(authID, "gpt-6-astra") {
		t.Errorf("expected gpt-6-astra to be excluded for %s, but registry supports it", authID)
	}
	if reg.ClientSupportsModel(authID, "gpt-6.1-sol") {
		t.Errorf("expected gpt-6.1-sol to be excluded for %s, but registry supports it", authID)
	}

	clientModels := reg.GetModelsForClient(authID)
	for _, m := range clientModels {
		if m != nil && (strings.EqualFold(m.ID, "gpt-6-astra") || strings.EqualFold(m.ID, "gpt-6.1-sol")) {
			t.Errorf("model %q should have been excluded from registered models for %s", m.ID, authID)
		}
	}

	// Verify scheduler selection picks the allowed account and never the masked account
	selected, errSelect := manager.SelectAuth(ctx, "codex", "gpt-6-astra", cliproxyexecutor.Options{})
	if errSelect != nil {
		t.Fatalf("failed to select auth for gpt-6-astra: %v", errSelect)
	}
	if selected == nil || selected.ID != allowedID {
		t.Errorf("SelectAuth selected %v, want %s (masked auth %s was selected!)", selected, allowedID, authID)
	}
}

func TestRegisterModelsForAuthBatch_ConcurrentUpdateDuringBatch_Issue6453(t *testing.T) {
	ctx := context.Background()
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(mockBatchTestExecutor{provider: "codex"})
	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
	}

	authID := "auth-codex-batch-concurrent-6453"
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"plan_type": "pro",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("failed to register auth: %v", errRegister)
	}

	allowedID := "auth-codex-batch-concurrent-allowed-6453"
	allowedAuth := &coreauth.Auth{
		ID:       allowedID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"plan_type": "pro",
		},
	}
	if _, errRegister := manager.Register(ctx, allowedAuth); errRegister != nil {
		t.Fatalf("failed to register allowed auth: %v", errRegister)
	}

	reg := GlobalModelRegistry()
	reg.UnregisterClient(authID)
	reg.UnregisterClient(allowedID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
		reg.UnregisterClient(allowedID)
		modelRegistrationTaskHook = nil
	})

	var updateDone atomic.Bool
	modelRegistrationTaskHook = func() {
		if updateDone.CompareAndSwap(false, true) {
			updatedAuth := auth.Clone()
			updatedAuth.Attributes = map[string]string{
				"auth_kind":       "oauth",
				"plan_type":       "pro",
				"excluded_models": "gpt-6-astra,gpt-6.1-sol",
			}
			if _, errUpdate := manager.Update(ctx, updatedAuth); errUpdate != nil {
				t.Errorf("failed to update auth in hook: %v", errUpdate)
			}
		}
	}

	// Run batch registration with the initial snapshot
	service.registerModelsForAuthBatch(ctx, manager.List())

	// Verify that the excluded models are not supported for this client after batch registration completes
	if reg.ClientSupportsModel(authID, "gpt-6-astra") {
		t.Errorf("expected gpt-6-astra to be excluded for %s, but registry supports it", authID)
	}
	if reg.ClientSupportsModel(authID, "gpt-6.1-sol") {
		t.Errorf("expected gpt-6.1-sol to be excluded for %s, but registry supports it", authID)
	}

	clientModels := reg.GetModelsForClient(authID)
	for _, m := range clientModels {
		if m != nil && (strings.EqualFold(m.ID, "gpt-6-astra") || strings.EqualFold(m.ID, "gpt-6.1-sol")) {
			t.Errorf("model %q should have been excluded from registered models for %s", m.ID, authID)
		}
	}

	// Verify scheduler selection picks the allowed account and never the masked account
	selected, errSelect := manager.SelectAuth(ctx, "codex", "gpt-6-astra", cliproxyexecutor.Options{})
	if errSelect != nil {
		t.Fatalf("failed to select auth for gpt-6-astra: %v", errSelect)
	}
	if selected == nil || selected.ID != allowedID {
		t.Errorf("SelectAuth selected %v, want %s (masked auth %s was selected!)", selected, allowedID, authID)
	}
}

func TestRegisterModelsForAuthBatch_PostBatchReconciliation_Issue6453(t *testing.T) {
	ctx := context.Background()
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(mockBatchTestExecutor{provider: "codex"})
	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
	}

	authID := "auth-codex-batch-reconcile-6453"
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"plan_type": "pro",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("failed to register auth: %v", errRegister)
	}

	allowedID := "auth-codex-batch-reconcile-allowed-6453"
	allowedAuth := &coreauth.Auth{
		ID:       allowedID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"plan_type": "pro",
		},
	}
	if _, errRegister := manager.Register(ctx, allowedAuth); errRegister != nil {
		t.Fatalf("failed to register allowed auth: %v", errRegister)
	}

	reg := GlobalModelRegistry()
	reg.UnregisterClient(authID)
	reg.UnregisterClient(allowedID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
		reg.UnregisterClient(allowedID)
		modelRegistrationTaskPostRunHook = nil
	})

	var updateTriggered atomic.Bool
	modelRegistrationTaskPostRunHook = func(id string) {
		if id == authID && updateTriggered.CompareAndSwap(false, true) {
			// After the initial task ran completeModelRegistrationForAuthWithCache with Generation 1,
			// update coreManager to Generation 2 with excluded_models before the batch loop finishes.
			// This tests that post-batch reconciliation catches the newer generation and re-registers.
			updatedAuth := auth.Clone()
			updatedAuth.Attributes = map[string]string{
				"auth_kind":       "oauth",
				"plan_type":       "pro",
				"excluded_models": "gpt-6-astra,gpt-6.1-sol",
			}
			if _, errUpdate := manager.Update(ctx, updatedAuth); errUpdate != nil {
				t.Errorf("failed to update auth in post-run hook: %v", errUpdate)
			}
		}
	}

	// Run batch registration with the initial snapshot.
	service.registerModelsForAuthBatch(ctx, manager.List())

	if !updateTriggered.Load() {
		t.Fatal("expected post-run hook to trigger during batch registration")
	}

	// Verify that the post-batch reconciliation corrected the model list so excluded models are not supported
	if reg.ClientSupportsModel(authID, "gpt-6-astra") {
		t.Errorf("expected gpt-6-astra to be excluded for %s after reconciliation, but registry supports it", authID)
	}
	if reg.ClientSupportsModel(authID, "gpt-6.1-sol") {
		t.Errorf("expected gpt-6.1-sol to be excluded for %s after reconciliation, but registry supports it", authID)
	}

	clientModels := reg.GetModelsForClient(authID)
	for _, m := range clientModels {
		if m != nil && (strings.EqualFold(m.ID, "gpt-6-astra") || strings.EqualFold(m.ID, "gpt-6.1-sol")) {
			t.Errorf("model %q should have been excluded from registered models for %s", m.ID, authID)
		}
	}

	// Verify scheduler selection picks the allowed account and never the masked account
	selected, errSelect := manager.SelectAuth(ctx, "codex", "gpt-6-astra", cliproxyexecutor.Options{})
	if errSelect != nil {
		t.Fatalf("failed to select auth for gpt-6-astra: %v", errSelect)
	}
	if selected == nil || selected.ID != allowedID {
		t.Errorf("SelectAuth selected %v, want %s (masked auth %s was selected!)", selected, allowedID, authID)
	}
}
