package auth

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Cloudflare "error code: 1010" blocks the client's browser signature, not the credential:
// the model must stay available for other clients instead of cooling down for 30 minutes.
func TestManager_Execute_CloudflareSignatureBlockDoesNotCoolModel(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(&authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"auth-cf-1010": &Error{HTTPStatus: http.StatusForbidden, Message: "error code: 1010"},
		},
	})
	auth := &Auth{ID: "auth-cf-1010", Provider: "claude"}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	model := "test-model-cf-1010"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	_, errExecute := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected the 1010 error to reach the client")
	}
	if statusCodeFromError(errExecute) != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", statusCodeFromError(errExecute), http.StatusForbidden)
	}
	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to be present")
	}
	if state := updated.ModelStates[model]; state != nil && !state.NextRetryAfter.IsZero() {
		t.Fatalf("model cooled until %v after a client signature block", state.NextRetryAfter)
	}
	if reg.IsModelSuspendedForClient(auth.ID, model) {
		t.Fatal("model suspended in the registry after a client signature block")
	}
}
