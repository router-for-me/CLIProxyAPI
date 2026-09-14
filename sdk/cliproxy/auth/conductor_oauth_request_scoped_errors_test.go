package auth

import (
	"context"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestOAuthRequestScopedErrors_AppliesToOAuthAuth(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	cfg := &internalconfig.Config{
		OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
			"vertex": {
				{
					Status: 400,
					Match: []string{
						"maximum_context_length",
						"context_length_exceeded",
					},
					MatchRegexr: []string{
						"maximum_context_length$",
						"^context_length_exceeded",
					},
					Action: "stop",
				},
			},
		},
	}

	m := NewManager(nil, nil, nil)
	m.SetConfig(cfg)

	auth1 := &Auth{
		ID:         "auth-vertex-oauth",
		Provider:   "vertex",
		Status:     StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth", "priority": "10"},
	}
	auth2 := &Auth{
		ID:         "auth-vertex-oauth-2",
		Provider:   "vertex",
		Status:     StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth", "priority": "5"},
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "vertex", []*registry.ModelInfo{{ID: "claude-3-5-sonnet"}})
	reg.RegisterClient(auth2.ID, "vertex", []*registry.ModelInfo{{ID: "claude-3-5-sonnet"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	if _, err := m.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := m.Register(context.Background(), auth2); err != nil {
		t.Fatalf("register auth2: %v", err)
	}

	execCount := 0
	exec := &mockCustomErrorExecutor{
		identifier: "vertex",
		executeFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			execCount++
			return cliproxyexecutor.Response{}, customStatusError{
				code: http.StatusBadRequest,
				msg:  `{"error": "maximum_context_length"}`,
			}
		},
	}
	m.RegisterExecutor(exec)

	req := cliproxyexecutor.Request{Model: "claude-3-5-sonnet"}
	opts := cliproxyexecutor.Options{}

	_, errExec := m.Execute(context.Background(), []string{"vertex"}, req, opts)
	if errExec == nil {
		t.Fatal("expected error, got nil")
	}

	// Action: stop should terminate immediately and not try auth2
	if execCount != 1 {
		t.Fatalf("expected execCount = 1 (stopped), got %d", execCount)
	}

	// Action: stop without cooldown should leave auth1 active
	auth1State, ok := m.GetByID("auth-vertex-oauth")
	if !ok || auth1State.Status != StatusActive || auth1State.Unavailable {
		t.Fatalf("expected auth1 to remain active, got status=%v unavailable=%v", auth1State.Status, auth1State.Unavailable)
	}
}

func TestOAuthRequestScopedErrors_DoesNotApplyToAPIKey(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	cfg := &internalconfig.Config{
		OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
			"vertex": {
				{
					Status: 500,
					Match:  []string{"internal_server_error"},
					Action: "stop",
				},
			},
		},
	}

	m := NewManager(nil, nil, nil)
	m.SetConfig(cfg)

	// API key auth must not use oauth-request-scoped-errors
	auth1 := &Auth{
		ID:         "auth-vertex-apikey",
		Provider:   "vertex",
		Status:     StatusActive,
		Attributes: map[string]string{"auth_kind": "apikey", "api_key": "test-key", "priority": "10"},
	}
	auth2 := &Auth{
		ID:         "auth-vertex-apikey-2",
		Provider:   "vertex",
		Status:     StatusActive,
		Attributes: map[string]string{"auth_kind": "apikey", "api_key": "test-key-2", "priority": "5"},
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "vertex", []*registry.ModelInfo{{ID: "claude-3-5-sonnet"}})
	reg.RegisterClient(auth2.ID, "vertex", []*registry.ModelInfo{{ID: "claude-3-5-sonnet"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	if _, err := m.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := m.Register(context.Background(), auth2); err != nil {
		t.Fatalf("register auth2: %v", err)
	}

	execCount := 0
	exec := &mockCustomErrorExecutor{
		identifier: "vertex",
		executeFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			execCount++
			if execCount == 1 {
				return cliproxyexecutor.Response{}, customStatusError{
					code: http.StatusInternalServerError,
					msg:  `{"error": "internal_server_error"}`,
				}
			}
			return cliproxyexecutor.Response{Payload: []byte(`{"success": true}`)}, nil
		},
	}
	m.RegisterExecutor(exec)

	req := cliproxyexecutor.Request{Model: "claude-3-5-sonnet"}
	opts := cliproxyexecutor.Options{}

	resp, errExec := m.Execute(context.Background(), []string{"vertex"}, req, opts)
	if errExec != nil {
		t.Fatalf("unexpected Execute error: %v", errExec)
	}
	if string(resp.Payload) != `{"success": true}` {
		t.Fatalf("unexpected payload: %s", string(resp.Payload))
	}

	// Should not have stopped at auth1; fell back to auth2 because OAuth rule was skipped for API key
	if execCount != 2 {
		t.Fatalf("expected execCount = 2 (rotated because OAuth rule skipped for API key), got %d", execCount)
	}
}
