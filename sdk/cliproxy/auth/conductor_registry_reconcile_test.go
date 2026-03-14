package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestManager_ReconcileRegistryModelStates_ClearsStaleSupportedModelErrors(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)

	auth := &Auth{
		ID:       "reconcile-auth",
		Provider: "codex",
		ModelStates: map[string]*ModelState{
			"gpt-5.4": {
				Status:         StatusError,
				StatusMessage:  "not_found",
				Unavailable:    true,
				NextRetryAfter: time.Now().Add(12 * time.Hour),
				LastError:      &Error{HTTPStatus: http.StatusNotFound, Message: "not_found"},
			},
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	registerSchedulerModels(t, "codex", "gpt-5.4", auth.ID)
	manager.RefreshSchedulerEntry(auth.ID)

	got, errPick := manager.scheduler.pickSingle(ctx, "codex", "gpt-5.4", cliproxyexecutor.Options{}, nil)
	var authErr *Error
	if !errors.As(errPick, &authErr) || authErr == nil {
		t.Fatalf("pickSingle() before reconcile error = %v, want auth_unavailable", errPick)
	}
	if authErr.Code != "auth_unavailable" {
		t.Fatalf("pickSingle() before reconcile code = %q, want %q", authErr.Code, "auth_unavailable")
	}
	if got != nil {
		t.Fatalf("pickSingle() before reconcile auth = %v, want nil", got)
	}

	manager.ReconcileRegistryModelStates(ctx, auth.ID)

	got, errPick = manager.scheduler.pickSingle(ctx, "codex", "gpt-5.4", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() after reconcile error = %v", errPick)
	}
	if got == nil || got.ID != auth.ID {
		t.Fatalf("pickSingle() after reconcile auth = %v, want %q", got, auth.ID)
	}

	reconciled, ok := manager.GetByID(auth.ID)
	if !ok || reconciled == nil {
		t.Fatalf("expected auth to still exist")
	}
	state := reconciled.ModelStates["gpt-5.4"]
	if state == nil {
		t.Fatalf("expected reconciled model state to exist")
	}
	if state.Unavailable {
		t.Fatalf("state.Unavailable = true, want false")
	}
	if state.Status != StatusActive {
		t.Fatalf("state.Status = %q, want %q", state.Status, StatusActive)
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("state.NextRetryAfter = %v, want zero", state.NextRetryAfter)
	}
	if state.LastError != nil {
		t.Fatalf("state.LastError = %v, want nil", state.LastError)
	}
}

func TestManager_ReconcileRegistryModelStates_PrunesUnsupportedModelStates(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)

	nextRetry := time.Now().Add(30 * time.Minute)
	auth := &Auth{
		ID:            "reconcile-unsupported-auth",
		Provider:      "codex",
		Status:        StatusError,
		Unavailable:   true,
		StatusMessage: "payment_required",
		LastError:     &Error{HTTPStatus: http.StatusPaymentRequired, Message: "payment_required"},
		ModelStates: map[string]*ModelState{
			"gpt-5.4": {
				Status:         StatusError,
				StatusMessage:  "payment_required",
				Unavailable:    true,
				NextRetryAfter: nextRetry,
			},
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	registerSchedulerModels(t, "codex", "gpt-5.5", auth.ID)
	manager.ReconcileRegistryModelStates(ctx, auth.ID)

	reconciled, ok := manager.GetByID(auth.ID)
	if !ok || reconciled == nil {
		t.Fatalf("expected auth to still exist")
	}
	if len(reconciled.ModelStates) != 0 {
		t.Fatalf("expected stale unsupported model state to be pruned, got %+v", reconciled.ModelStates)
	}
	if reconciled.Unavailable {
		t.Fatalf("auth.Unavailable = true, want false")
	}
	if reconciled.Status != StatusActive {
		t.Fatalf("auth.Status = %q, want %q", reconciled.Status, StatusActive)
	}
	if reconciled.StatusMessage != "" {
		t.Fatalf("auth.StatusMessage = %q, want empty", reconciled.StatusMessage)
	}
	if reconciled.LastError != nil {
		t.Fatalf("auth.LastError = %v, want nil", reconciled.LastError)
	}
	if !reconciled.NextRetryAfter.IsZero() {
		t.Fatalf("auth.NextRetryAfter = %v, want zero", reconciled.NextRetryAfter)
	}
}

func TestManager_ReconcileRegistryModelStates_ClearsRemovedModelStateWhenRegistryIsEmpty(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)

	auth := &Auth{
		ID:            "reconcile-empty-registry-auth",
		Provider:      "codex",
		Status:        StatusError,
		Unavailable:   true,
		StatusMessage: "not_found",
		LastError:     &Error{HTTPStatus: http.StatusNotFound, Message: "not_found"},
		ModelStates: map[string]*ModelState{
			"gpt-5.4": {
				Status:         StatusError,
				StatusMessage:  "not_found",
				Unavailable:    true,
				NextRetryAfter: time.Now().Add(12 * time.Hour),
				LastError:      &Error{HTTPStatus: http.StatusNotFound, Message: "not_found"},
			},
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.ReconcileRegistryModelStates(ctx, auth.ID)

	reconciled, ok := manager.GetByID(auth.ID)
	if !ok || reconciled == nil {
		t.Fatalf("expected auth to still exist")
	}
	if len(reconciled.ModelStates) != 0 {
		t.Fatalf("expected stale model state to be pruned when registry is empty, got %+v", reconciled.ModelStates)
	}
	if reconciled.Unavailable {
		t.Fatalf("auth.Unavailable = true, want false")
	}
	if reconciled.Status != StatusActive {
		t.Fatalf("auth.Status = %q, want %q", reconciled.Status, StatusActive)
	}
	if reconciled.StatusMessage != "" {
		t.Fatalf("auth.StatusMessage = %q, want empty", reconciled.StatusMessage)
	}
	if reconciled.LastError != nil {
		t.Fatalf("auth.LastError = %v, want nil", reconciled.LastError)
	}
	if !reconciled.NextRetryAfter.IsZero() {
		t.Fatalf("auth.NextRetryAfter = %v, want zero", reconciled.NextRetryAfter)
	}
}
