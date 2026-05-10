package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestPickNextViaHomeReusesPinnedWebsocketAuthWithoutHomeDispatch(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.RegisterExecutor(schedulerTestExecutor{})

	auth := &Auth{
		ID:       "home-auth-1",
		Provider: "test",
		Status:   StatusActive,
		Attributes: map[string]string{
			"websockets":                  "true",
			homeUpstreamModelAttributeKey: "upstream-model",
		},
		Metadata: map[string]any{"email": "home@example.com"},
	}
	auth.EnsureIndex()
	manager.rememberHomeRuntimeAuth(auth)
	cachedAuth, ok := manager.GetByID("home-auth-1")
	if !ok || cachedAuth == nil || !authWebsocketsEnabled(cachedAuth) {
		t.Fatalf("GetByID() did not expose remembered websocket home auth: auth=%#v ok=%v", cachedAuth, ok)
	}

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "session-1",
			cliproxyexecutor.PinnedAuthMetadataKey:       "home-auth-1",
		},
		Headers: http.Header{"Authorization": {"Bearer client-key"}},
	}

	got, executor, provider, errPick := manager.pickNextViaHome(ctx, "gpt-5.4", opts)
	if errPick != nil {
		t.Fatalf("pickNextViaHome() error = %v", errPick)
	}
	if got == nil || got.ID != "home-auth-1" {
		t.Fatalf("pickNextViaHome() auth = %#v, want home-auth-1", got)
	}
	if executor == nil {
		t.Fatal("pickNextViaHome() executor is nil")
	}
	if provider != "test" {
		t.Fatalf("pickNextViaHome() provider = %q, want test", provider)
	}
}

func TestPickNextViaHomeDoesNotReusePinnedNonWebsocketAuth(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.RegisterExecutor(schedulerTestExecutor{})

	manager.mu.Lock()
	manager.homeRuntimeAuths["home-auth-1"] = &Auth{
		ID:       "home-auth-1",
		Provider: "test",
		Status:   StatusActive,
	}
	manager.mu.Unlock()

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "session-1",
			cliproxyexecutor.PinnedAuthMetadataKey:       "home-auth-1",
		},
		Headers: http.Header{"Authorization": {"Bearer client-key"}},
	}

	got, executor, provider, errPick := manager.pickNextViaHome(ctx, "gpt-5.4", opts)
	if errPick == nil {
		t.Fatal("pickNextViaHome() error is nil, want home unavailable error")
	}
	var authErr *Error
	if !errors.As(errPick, &authErr) || authErr.Code != "home_unavailable" {
		t.Fatalf("pickNextViaHome() error = %v, want home_unavailable", errPick)
	}
	if got != nil || executor != nil || provider != "" {
		t.Fatalf("pickNextViaHome() reused non-websocket auth: auth=%#v executor=%#v provider=%q", got, executor, provider)
	}
}

func TestHomeRuntimeAuthsClearWhenHomeDisabled(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.rememberHomeRuntimeAuth(&Auth{
		ID:       "home-auth-1",
		Provider: "test",
		Attributes: map[string]string{
			"websockets": "true",
		},
	})

	if _, ok := manager.GetByID("home-auth-1"); !ok {
		t.Fatal("expected remembered home auth before disabling home")
	}

	manager.SetConfig(&internalconfig.Config{})
	if _, ok := manager.GetByID("home-auth-1"); ok {
		t.Fatal("remembered home auth was not cleared when home was disabled")
	}
}
