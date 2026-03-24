package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type refreshFailureTestExecutor struct {
	provider string
	err      error
}

func (e refreshFailureTestExecutor) Identifier() string { return e.provider }

func (e refreshFailureTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e refreshFailureTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e refreshFailureTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, e.err
}

func (e refreshFailureTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e refreshFailureTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

type terminalRefreshTestError struct {
	status int
	msg    string
}

func (e terminalRefreshTestError) Error() string   { return e.msg }
func (e terminalRefreshTestError) StatusCode() int { return e.status }
func (e terminalRefreshTestError) Terminal() bool  { return true }

type transientRefreshTestError struct {
	status int
	msg    string
}

func (e transientRefreshTestError) Error() string   { return e.msg }
func (e transientRefreshTestError) StatusCode() int { return e.status }
func (e transientRefreshTestError) Terminal() bool  { return false }

func TestManagerRefreshAuth_PersistsTerminalRefresh401ForMaintenance(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(refreshFailureTestExecutor{
		provider: "codex",
		err: terminalRefreshTestError{
			status: http.StatusUnauthorized,
			msg:    "token refresh failed with status 401: unauthorized",
		},
	})

	auth := &Auth{
		ID:       "refresh-401",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"email":         "user@example.com",
			"refresh_token": "refresh-token",
		},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	started := time.Now()
	manager.refreshAuth(ctx, auth.ID)

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth %q to remain registered", auth.ID)
	}
	if updated.LastError == nil {
		t.Fatal("expected refresh failure to persist LastError")
	}
	if updated.LastError.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("expected LastError.HTTPStatus = 401, got %d", updated.LastError.HTTPStatus)
	}
	if !strings.Contains(updated.LastError.Message, "status 401") {
		t.Fatalf("expected LastError.Message to preserve refresh failure details, got %q", updated.LastError.Message)
	}
	if updated.Status != StatusActive {
		t.Fatalf("expected auth status to remain active, got %q", updated.Status)
	}
	if updated.Unavailable {
		t.Fatal("expected auth to remain schedulable until maintenance handles deletion")
	}
	if updated.NextRefreshAfter.IsZero() || !updated.NextRefreshAfter.After(started) {
		t.Fatalf("expected NextRefreshAfter to be scheduled after refresh failure, got %v", updated.NextRefreshAfter)
	}
}

func TestManagerRefreshAuth_DoesNotMarkTransientRefreshStatusForMaintenance(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(refreshFailureTestExecutor{
		provider: "codex",
		err: transientRefreshTestError{
			status: http.StatusTooManyRequests,
			msg:    "token refresh failed with status 429: rate limited",
		},
	})

	auth := &Auth{
		ID:       "refresh-429",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"email":         "user@example.com",
			"refresh_token": "refresh-token",
		},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	manager.refreshAuth(ctx, auth.ID)

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth %q to remain registered", auth.ID)
	}
	if updated.LastError == nil {
		t.Fatal("expected refresh failure to persist LastError")
	}
	if updated.LastError.HTTPStatus != 0 {
		t.Fatalf("expected transient refresh error to avoid maintenance status code, got %d", updated.LastError.HTTPStatus)
	}
	if updated.Status != StatusActive {
		t.Fatalf("expected auth status to remain active, got %q", updated.Status)
	}
	if updated.Unavailable {
		t.Fatal("expected transient refresh failure to avoid blocking scheduler")
	}
}
