package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestManagerExecute_SessionAffinityRebindsToSuccessfulFallbackAuth(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	manager := NewManager(nil, selector, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"auth-a": &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
		},
	}
	manager.RegisterExecutor(executor)

	for _, auth := range []*Auth{
		{ID: "auth-a", Provider: "claude"},
		{ID: "auth-b", Provider: "claude"},
	} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth %s: %v", auth.ID, errRegister)
		}
	}

	const model = "claude-3"
	for _, authID := range []string{"auth-a", "auth-b"} {
		registry.GetGlobalRegistry().RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: model}})
	}
	t.Cleanup(func() {
		for _, authID := range []string{"auth-a", "auth-b"} {
			registry.GetGlobalRegistry().UnregisterClient(authID)
		}
	})

	payload := []byte(`{"metadata":{"user_id":"user_xxx_account__session_rebind-success-uuid"}}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}

	resp, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, opts)
	if errExecute != nil {
		t.Fatalf("first Execute error: %v", errExecute)
	}
	if got := string(resp.Payload); got != "auth-b" {
		t.Fatalf("first Execute payload = %q, want auth-b", got)
	}

	resp, errExecute = manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, opts)
	if errExecute != nil {
		t.Fatalf("second Execute error: %v", errExecute)
	}
	if got := string(resp.Payload); got != "auth-b" {
		t.Fatalf("second Execute payload = %q, want auth-b", got)
	}

	calls := executor.ExecuteCalls()
	want := []string{"auth-a", "auth-b", "auth-b"}
	if len(calls) != len(want) {
		t.Fatalf("execute calls = %#v, want %#v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("execute calls[%d] = %q, want %q (all calls: %#v)", i, calls[i], want[i], calls)
		}
	}
}
