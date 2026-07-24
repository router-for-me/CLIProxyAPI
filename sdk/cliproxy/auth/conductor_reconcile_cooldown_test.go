package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestManager_ReconcileRegistryModelStatesPreservesUnauthorizedCooldown(t *testing.T) {
	t.Parallel()

	const (
		authID = "reconcile-unauthorized-auth"
		model  = "reconcile-unauthorized-model"
	)
	ctx := context.Background()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	manager := NewManager(nil, &FillFirstSelector{}, nil)
	if _, err := manager.Register(ctx, &Auth{ID: authID, Provider: "codex"}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "codex",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
	})

	before, ok := manager.GetByID(authID)
	if !ok || before.ModelStates[model] == nil {
		t.Fatal("unauthorized model cooldown was not recorded")
	}
	wantRetryAfter := before.ModelStates[model].NextRetryAfter
	if !wantRetryAfter.After(time.Now()) {
		t.Fatalf("NextRetryAfter = %v, want future cooldown", wantRetryAfter)
	}
	resetAggregatedAuthStateForReconcileTest(t, manager, before)

	// RegisterClient replaces the registry-side suspension snapshot.
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	if count := reg.GetModelCount(model); count != 1 {
		t.Fatalf("registry model count after re-registration = %d, want 1", count)
	}

	manager.ReconcileRegistryModelStates(ctx, authID)
	after, ok := manager.GetByID(authID)
	if !ok || after.ModelStates[model] == nil {
		t.Fatal("unauthorized model cooldown was removed during reconciliation")
	}
	state := after.ModelStates[model]
	if !state.Unavailable || !state.NextRetryAfter.Equal(wantRetryAfter) {
		t.Fatalf("reconciled model state = %+v, want active cooldown until %v", state, wantRetryAfter)
	}
	if after.Status != StatusError || !after.Unavailable || !after.NextRetryAfter.Equal(wantRetryAfter) {
		t.Fatalf("reconciled auth state = status %q unavailable %v next %v", after.Status, after.Unavailable, after.NextRetryAfter)
	}
	if after.LastError == nil || after.LastError.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("reconciled auth error = %+v, want unauthorized", after.LastError)
	}
	if count := reg.GetModelCount(model); count != 0 {
		t.Fatalf("registry model count after reconciliation = %d, want 0", count)
	}
}

func TestManager_ReconcileRegistryModelStatesPreservesQuotaCooldown(t *testing.T) {
	t.Parallel()

	const (
		authID = "reconcile-quota-auth"
		model  = "reconcile-quota-model"
	)
	ctx := context.Background()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	manager := NewManager(nil, &FillFirstSelector{}, nil)
	if _, err := manager.Register(ctx, &Auth{ID: authID, Provider: "codex"}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	retryAfter := 20 * time.Minute
	manager.MarkResult(ctx, Result{
		AuthID:     authID,
		Provider:   "codex",
		Model:      model,
		Success:    false,
		Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
		RetryAfter: &retryAfter,
	})

	before, ok := manager.GetByID(authID)
	if !ok || before.ModelStates[model] == nil {
		t.Fatal("quota model cooldown was not recorded")
	}
	wantRetryAfter := before.ModelStates[model].NextRetryAfter
	resetAggregatedAuthStateForReconcileTest(t, manager, before)

	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	if count := reg.GetModelCount(model); count != 1 {
		t.Fatalf("registry model count after re-registration = %d, want 1", count)
	}

	manager.ReconcileRegistryModelStates(ctx, authID)
	after, ok := manager.GetByID(authID)
	if !ok || after.ModelStates[model] == nil {
		t.Fatal("quota model cooldown was removed during reconciliation")
	}
	state := after.ModelStates[model]
	if !state.Unavailable || !state.Quota.Exceeded || !state.NextRetryAfter.Equal(wantRetryAfter) {
		t.Fatalf("reconciled model state = %+v, want active quota cooldown until %v", state, wantRetryAfter)
	}
	if after.Status != StatusError || !after.Unavailable || !after.Quota.Exceeded || !after.NextRetryAfter.Equal(wantRetryAfter) {
		t.Fatalf("reconciled auth state = status %q unavailable %v quota %+v next %v", after.Status, after.Unavailable, after.Quota, after.NextRetryAfter)
	}
	if count := reg.GetModelCount(model); count != 0 {
		t.Fatalf("registry model count after reconciliation = %d, want 0", count)
	}
}

func resetAggregatedAuthStateForReconcileTest(t *testing.T, manager *Manager, auth *Auth) {
	t.Helper()
	incoming := auth.Clone()
	incoming.Status = StatusActive
	incoming.StatusMessage = ""
	incoming.Unavailable = false
	incoming.NextRetryAfter = time.Time{}
	incoming.Quota = QuotaState{}
	incoming.LastError = nil
	if _, err := manager.Update(context.Background(), incoming); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
}

func TestManager_ReconcileRegistryModelStatesDoesNotSuspendCloudflareChallenge(t *testing.T) {
	const (
		authID = "reconcile-cloudflare-auth"
		model  = "reconcile-cloudflare-model"
	)
	ctx := context.Background()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	manager := NewManager(nil, &FillFirstSelector{}, nil)
	if _, err := manager.Register(ctx, &Auth{ID: authID, Provider: "claude"}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "claude",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusForbidden, Message: "cf-mitigated: challenge"},
	})

	before, ok := manager.GetByID(authID)
	if !ok || before.ModelStates[model] == nil {
		t.Fatal("cloudflare challenge cooldown was not recorded")
	}
	wantRetryAfter := before.ModelStates[model].NextRetryAfter

	reg.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: model}})
	manager.ReconcileRegistryModelStates(ctx, authID)

	after, ok := manager.GetByID(authID)
	if !ok || after.ModelStates[model] == nil {
		t.Fatal("cloudflare challenge cooldown was removed during reconciliation")
	}
	state := after.ModelStates[model]
	if !state.Unavailable || !state.NextRetryAfter.Equal(wantRetryAfter) {
		t.Fatalf("reconciled model state = %+v, want transient cooldown until %v", state, wantRetryAfter)
	}
	if count := reg.GetModelCount(model); count != 1 {
		t.Fatalf("registry model count after reconciliation = %d, want 1", count)
	}
}

func TestManager_RestoreRegistryCooldownRechecksClearedState(t *testing.T) {
	const (
		authID = "reconcile-stale-cooldown-auth"
		model  = "reconcile-stale-cooldown-model"
	)
	ctx := context.Background()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	manager := NewManager(nil, &FillFirstSelector{}, nil)
	if _, err := manager.Register(ctx, &Auth{ID: authID, Provider: "codex"}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "codex",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
	})

	// Simulate a candidate captured before a concurrent successful result.
	candidate := registryCooldownCandidate{stateKey: model, model: model}
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	manager.MarkResult(ctx, Result{AuthID: authID, Provider: "codex", Model: model, Success: true})
	manager.restoreRegistryCooldown(authID, candidate)

	after, ok := manager.GetByID(authID)
	if !ok || !modelStateIsClean(after.ModelStates[model]) {
		t.Fatalf("model state after success = %+v, want clean", after.ModelStates[model])
	}
	if count := reg.GetModelCount(model); count != 1 {
		t.Fatalf("registry model count after stale restore = %d, want 1", count)
	}
}
