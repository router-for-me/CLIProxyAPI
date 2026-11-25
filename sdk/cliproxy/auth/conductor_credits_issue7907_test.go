package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type issue7907CancelRefreshExecutor struct {
	issue7658RefreshTrackingExecutor
	cancel context.CancelFunc
}

func (e *issue7907CancelRefreshExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	e.mu.Lock()
	e.refreshCalls = append(e.refreshCalls, auth.ID)
	e.mu.Unlock()
	e.cancel()
	return nil, ctx.Err()
}

func TestManagerExecute_AntigravityCreditsRefreshesAndRetriesUnauthorized(t *testing.T) {
	const (
		authID = "ag-issue7907-refresh"
		model  = "claude-sonnet-4-6"
	)

	exec := &issue7658RefreshTrackingExecutor{
		issue7658CreditsExecutor: issue7658CreditsExecutor{},
	}
	exec.executeFn = func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		if !AntigravityCreditsRequested(ctx) {
			return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
		}
		if authAccessToken(auth) == "initial-token" {
			return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"}
		}
		return cliproxyexecutor.Response{Payload: []byte("credits-refreshed-ok:" + req.Model)}, nil
	}

	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
	})
	manager.SetRetryConfig(0, 0, 1)
	manager.RegisterExecutor(exec)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
		SetAntigravityCreditsHint(authID, AntigravityCreditsHint{})
	})

	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID:       authID,
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "initial-token",
			"refresh_token": "refresh-token",
			"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	resp, errExecute := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute error = %v, want success after credits refresh retry", errExecute)
	}
	if got := string(resp.Payload); got != "credits-refreshed-ok:"+model {
		t.Fatalf("payload = %q, want refreshed credits response", got)
	}
	if got := exec.RefreshCalls(); len(got) != 1 || got[0] != authID {
		t.Fatalf("refresh calls = %v, want [%s]", got, authID)
	}
	if got := exec.CreditsCalls(); len(got) != 2 || got[0] != authID || got[1] != authID {
		t.Fatalf("credits calls = %v, want [%s %s]", got, authID, authID)
	}
}

func TestManagerExecute_AntigravityCreditsPropagatesCancellationDuringRefresh(t *testing.T) {
	const (
		authID = "ag-issue7907-cancel-refresh"
		model  = "claude-sonnet-4-6"
	)

	ctx, cancel := context.WithCancel(context.Background())
	exec := &issue7907CancelRefreshExecutor{
		issue7658RefreshTrackingExecutor: issue7658RefreshTrackingExecutor{},
		cancel:                           cancel,
	}
	exec.executeFn = func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		if !AntigravityCreditsRequested(ctx) {
			return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
		}
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"}
	}

	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
	})
	manager.SetRetryConfig(0, 0, 1)
	manager.RegisterExecutor(exec)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
		SetAntigravityCreditsHint(authID, AntigravityCreditsHint{})
	})

	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID:       authID,
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "initial-token",
			"refresh_token": "refresh-token",
			"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	_, errExecute := manager.Execute(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if !errors.Is(errExecute, context.Canceled) {
		t.Fatalf("Execute error = %v, want context canceled", errExecute)
	}
	if got := exec.RefreshCalls(); len(got) != 1 || got[0] != authID {
		t.Fatalf("refresh calls = %v, want [%s]", got, authID)
	}
}
