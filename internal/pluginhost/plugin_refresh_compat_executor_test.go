package pluginhost

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type stubCompatExecutor struct {
	id           string
	executeCalls int
	refreshCalls int
}

func (e *stubCompatExecutor) Identifier() string { return e.id }

func (e *stubCompatExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.executeCalls++
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *stubCompatExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return &cliproxyexecutor.StreamResult{}, nil
}

func (e *stubCompatExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	e.refreshCalls++
	return auth, nil
}

func (e *stubCompatExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *stubCompatExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *stubCompatExecutor) PrepareRequest(*http.Request, *coreauth.Auth) error {
	return nil
}

func TestPluginRefreshCompatExecutorDelegatesExecuteAndRefresh(t *testing.T) {
	refreshCalls := 0
	host := newHostWithRecords(capabilityRecord{
		id: "auth-plugin",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				AuthProvider: fakeAuthProvider{
					identifier: "plugin-provider",
					refreshAuth: func(ctx context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
						refreshCalls++
						if req.AuthID != "auth-1" || req.AuthProvider != "plugin-provider" {
							t.Fatalf("RefreshAuth request = %#v", req)
						}
						return pluginapi.AuthRefreshResponse{
							Auth: pluginapi.AuthData{
								ID:       "auth-1",
								Provider: "plugin-provider",
								Metadata: map[string]any{
									"access_token":  "new-token",
									"refresh_token": "refresh-1",
								},
								Attributes: map[string]string{
									"base_url": "https://compat.example.com/v1",
								},
							},
						}, nil
					},
				},
			},
		},
	})

	inner := &stubCompatExecutor{id: "plugin-provider"}
	wrapped := NewPluginRefreshCompatExecutor(inner, host, &config.Config{})
	if wrapped == nil {
		t.Fatal("NewPluginRefreshCompatExecutor() = nil")
	}
	if !IsPluginRefreshCompatExecutor(wrapped) {
		t.Fatal("IsPluginRefreshCompatExecutor() = false, want true")
	}
	if got, ok := UnwrapPluginRefreshCompatExecutor(wrapped); !ok || got != inner {
		t.Fatalf("UnwrapPluginRefreshCompatExecutor() = (%T, %v), want inner", got, ok)
	}
	if wrapped.Identifier() != "plugin-provider" {
		t.Fatalf("Identifier() = %q, want plugin-provider", wrapped.Identifier())
	}

	auth := &coreauth.Auth{
		ID:       "auth-1",
		Provider: "plugin-provider",
		Metadata: map[string]any{
			"access_token":  "old-token",
			"refresh_token": "refresh-1",
		},
		Attributes: map[string]string{
			"base_url": "https://compat.example.com/v1",
		},
	}

	if _, errExecute := wrapped.Execute(context.Background(), auth, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if inner.executeCalls != 1 {
		t.Fatalf("inner Execute calls = %d, want 1", inner.executeCalls)
	}

	refreshed, errRefresh := wrapped.Refresh(context.Background(), auth)
	if errRefresh != nil {
		t.Fatalf("Refresh() error = %v", errRefresh)
	}
	if refreshCalls != 1 {
		t.Fatalf("plugin RefreshAuth calls = %d, want 1", refreshCalls)
	}
	if inner.refreshCalls != 0 {
		t.Fatalf("inner Refresh calls = %d, want 0", inner.refreshCalls)
	}
	if refreshed == nil || refreshed.Metadata["access_token"] != "new-token" {
		t.Fatalf("Refresh() auth = %#v, want updated access_token", refreshed)
	}
	if refreshed.Attributes["base_url"] != "https://compat.example.com/v1" {
		t.Fatalf("Refresh() base_url = %q, want preserved", refreshed.Attributes["base_url"])
	}
}

func TestPluginRefreshCompatExecutorErrorsWhenRefreshUnavailable(t *testing.T) {
	inner := &stubCompatExecutor{id: "plugin-provider"}
	wrapped := NewPluginRefreshCompatExecutor(inner, New(), &config.Config{})
	auth := &coreauth.Auth{
		ID:       "auth-1",
		Provider: "plugin-provider",
		Metadata: map[string]any{
			"access_token":  "old-token",
			"refresh_token": "refresh-1",
		},
	}

	_, errRefresh := wrapped.Refresh(context.Background(), auth)
	if errRefresh == nil {
		t.Fatal("Refresh() error = nil, want unavailable plugin refresh error")
	}
	if !strings.Contains(errRefresh.Error(), "plugin auth provider refresh is unavailable") {
		t.Fatalf("Refresh() error = %v, want unavailable message", errRefresh)
	}
	if inner.refreshCalls != 0 {
		t.Fatalf("inner Refresh calls = %d, want 0", inner.refreshCalls)
	}
}

func TestPluginRefreshCompatExecutorNoOpForAPIKeyAuth(t *testing.T) {
	inner := &stubCompatExecutor{id: "plugin-provider"}
	wrapped := NewPluginRefreshCompatExecutor(inner, New(), &config.Config{})
	auth := &coreauth.Auth{
		ID:       "auth-1",
		Provider: "plugin-provider",
		Attributes: map[string]string{
			"api_key":  "sk-test",
			"base_url": "https://compat.example.com/v1",
		},
	}

	refreshed, errRefresh := wrapped.Refresh(context.Background(), auth)
	if errRefresh != nil {
		t.Fatalf("Refresh() error = %v", errRefresh)
	}
	if refreshed == nil || refreshed.Attributes["api_key"] != "sk-test" {
		t.Fatalf("Refresh() auth = %#v, want unchanged api key auth", refreshed)
	}
}
