package cliproxy

import (
	"context"
	"testing"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

// modelIDSet collects the IDs of a model list into a set.
func modelIDSet(models []*internalregistry.ModelInfo) map[string]struct{} {
	out := make(map[string]struct{}, len(models))
	for _, m := range models {
		if m != nil && m.ID != "" {
			out[m.ID] = struct{}{}
		}
	}
	return out
}

// TestRegisterConfigAPIKeyAuthsOpenCodeModels mirrors TestRegisterConfigAPIKeyAuthsCodexModelModes
// for the native OpenCode (Zen) provider. It exercises the REAL dc8 startup path:
// RegisterConfigAPIKeyAuths synthesizes the OpenCode key into an auth, routes it
// through the native `case constant.OpenCode` switch (GetOpenCodeModels("zen")), and
// registers the models into the global registry. /v1/models reads
// GetAvailableModelInfos(), so the final assertion gates on the user-facing slice.
//
// On dc8 the opencs key survives sanitization (banner reports 1 OpenCode key) yet
// /v1/models surfaces no opencs models; this test reproduces that contract in-process.
func TestRegisterConfigAPIKeyAuthsOpenCodeModels(t *testing.T) {
	zenModels := internalregistry.GetOpenCodeModels("zen")
	if len(zenModels) == 0 {
		t.Fatal("expected non-empty OpenCode (zen) model catalog from registry")
	}
	wantIDs := modelIDSet(zenModels)

	cfg := &config.Config{
		OpenCodeKey: []config.OpenCodeKey{{
			APIKey: "opencode-runtime-key",
		}},
	}
	modelRegistry := internalregistry.GetGlobalRegistry()
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: cfg, coreManager: manager}

	service.registerConfigAPIKeyAuths(context.Background(), cfg)

	auths := manager.List()
	if len(auths) != 1 {
		t.Fatalf("runtime auth count = %d, want 1 (opencode key present)", len(auths))
	}
	t.Cleanup(func() { modelRegistry.UnregisterClient(auths[0].ID) })

	gotIDs := modelIDSet(modelRegistry.GetModelsForClient(auths[0].ID))
	if len(gotIDs) != len(wantIDs) {
		t.Fatalf("registered opencode model IDs count = %d, want %d (ids=%v)", len(gotIDs), len(wantIDs), gotIDs)
	}
	for modelID := range wantIDs {
		if _, ok := gotIDs[modelID]; !ok {
			t.Errorf("missing registered OpenCode model %q (provider=%s, client=%s)", modelID, "opencode", auths[0].ID)
		}
	}

	available := modelRegistry.GetAvailableModelInfos()
	foundOpenCode := false
	for _, m := range available {
		if m != nil && m.OwnedBy == "opencode" {
			foundOpenCode = true
			break
		}
	}
	if !foundOpenCode {
		t.Fatalf("OpenCode models registered for client %s (count=%d) but absent from GetAvailableModelInfos() source used by /v1/models", auths[0].ID, len(gotIDs))
	}
}
