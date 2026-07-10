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
	if count := reg.GetModelCount(model); count != 0 {
		t.Fatalf("registry model count after reconciliation = %d, want 0", count)
	}
}
