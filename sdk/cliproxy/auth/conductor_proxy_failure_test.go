package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

func TestManagerMarkResult_DisablesSOCKS5AuthOnProxyDialFailure(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "socks5-auth",
		Provider: "claude",
		ProxyURL: "socks5://127.0.0.1:1080",
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	proxyErr := &proxyutil.ProxyDialError{
		Scheme: "socks5",
		Host:   "127.0.0.1:1080",
		Err:    errors.New("connect: connection refused"),
	}
	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "claude-sonnet-4-6",
		Success:  false,
		Error:    &Error{Code: "proxy_dial_failed", Message: proxyErr.Error(), Retryable: true},
		Cause:    proxyErr,
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("auth not found")
	}
	if !updated.Disabled {
		t.Fatal("expected auth to be disabled after SOCKS5 proxy dial failure")
	}
	if updated.Status != StatusDisabled {
		t.Fatalf("status = %q, want %q", updated.Status, StatusDisabled)
	}
	if updated.StatusMessage != "disabled due to SOCKS5 proxy failure" {
		t.Fatalf("status message = %q", updated.StatusMessage)
	}
	state := updated.ModelStates["claude-sonnet-4-6"]
	if state == nil || state.Status != StatusDisabled {
		t.Fatalf("model state = %+v, want disabled", state)
	}
}

func TestManagerMarkResult_DoesNotDisableNonSOCKS5AuthOnProxyDialFailure(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "http-proxy-auth",
		Provider: "claude",
		ProxyURL: "http://127.0.0.1:8080",
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	proxyErr := &proxyutil.ProxyDialError{
		Scheme: "http",
		Host:   "127.0.0.1:8080",
		Err:    errors.New("connect: connection refused"),
	}
	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "claude-sonnet-4-6",
		Success:  false,
		Error:    &Error{Code: "proxy_dial_failed", Message: proxyErr.Error(), Retryable: true},
		Cause:    proxyErr,
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("auth not found")
	}
	if updated.Disabled || updated.Status == StatusDisabled {
		t.Fatalf("auth disabled=%v status=%q, want active/error but not disabled", updated.Disabled, updated.Status)
	}
}
