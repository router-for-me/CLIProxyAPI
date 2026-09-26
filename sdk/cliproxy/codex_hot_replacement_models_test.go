package cliproxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

// Replacing an invalidated Codex OAuth credential through the hot-update path
// must restore the same state a fresh start would: the auth becomes available
// again, the full plan catalog stays registered, and no model remains
// suspended in the registry (#5736).
func TestCodexHotReplacementRestoresModelsAndAvailability(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	svc := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	ctx := context.Background()

	teamAuth := &coreauth.Auth{
		ID:       "codex-team-5736",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"plan_type": "team",
		},
	}
	if _, errRegister := svc.coreManager.Register(ctx, teamAuth); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	svc.registerModelsForAuth(ctx, teamAuth)

	// Credential invalidated upstream.
	svc.coreManager.MarkResult(ctx, coreauth.Result{
		AuthID: teamAuth.ID, Provider: teamAuth.Provider, Model: "",
		Success: false,
		Error:   &coreauth.Error{HTTPStatus: http.StatusUnauthorized, Message: "Encountered invalidated oauth token: REDACTED"},
	})

	// The credential is replaced with a fresh one under the same auth ID.
	fresh := &coreauth.Auth{
		ID:       "codex-team-5736",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"plan_type": "team",
		},
	}
	svc.applyCoreAuthAddOrUpdate(ctx, fresh)

	updated, ok := svc.coreManager.GetByID(teamAuth.ID)
	if !ok || updated == nil {
		t.Fatal("auth missing after replacement")
	}
	if updated.Unavailable || !updated.NextRetryAfter.IsZero() {
		t.Fatalf("replaced auth still carries the invalidated-credential cooldown: Unavailable=%v NextRetryAfter=%v", updated.Unavailable, updated.NextRetryAfter)
	}

	registered := reg.GetModelsForClient(teamAuth.ID)
	teamModels := registry.GetCodexTeamModels()
	if len(registered) != len(teamModels) {
		ids := make([]string, 0, len(registered))
		for _, m := range registered {
			ids = append(ids, m.ID)
		}
		t.Fatalf("registered models = %v, want the full team catalog (%d models)", ids, len(teamModels))
	}
	for _, model := range teamModels {
		if reg.IsModelSuspendedForClient(teamAuth.ID, model.ID) {
			t.Fatalf("team model %q suspended after credential replacement", model.ID)
		}
	}
}
