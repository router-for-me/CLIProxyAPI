package auth

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type countingRefreshExecutor struct {
	id           string
	refreshCalls atomic.Int32
}

func (e *countingRefreshExecutor) Identifier() string { return e.id }

func (e *countingRefreshExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *countingRefreshExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *countingRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	e.refreshCalls.Add(1)
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = "refreshed-token"
	return auth, nil
}

func (e *countingRefreshExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *countingRefreshExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestRefreshAuthForRequest_UsesExecutorKeyFromAuth(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &countingRefreshExecutor{id: "openai-compatible-custom"}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "compat-oauth",
		Provider: "plugin-provider",
		Attributes: map[string]string{
			"compat_name":  "custom",
			"provider_key": "custom",
			"base_url":     "https://compat.example.com/v1",
		},
		Metadata: map[string]any{
			"access_token":  "old-token",
			"refresh_token": "refresh-1",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	refreshed, errRefresh := manager.refreshAuthForRequest(ctx, auth.ID, "old-token")
	if errRefresh != nil {
		t.Fatalf("refreshAuthForRequest() error = %v", errRefresh)
	}
	if executor.refreshCalls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", executor.refreshCalls.Load())
	}
	if refreshed == nil || refreshed.Metadata["access_token"] != "refreshed-token" {
		t.Fatalf("refreshed auth = %#v, want updated access_token", refreshed)
	}
}
