package cliproxy

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestBackfillAntigravityModels_RegistersMissingAuth(t *testing.T) {
	source := &coreauth.Auth{
		ID:       "ag-backfill-source",
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
	}
	target := &coreauth.Auth{
		ID:       "ag-backfill-target",
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
	}

	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), source); err != nil {
		t.Fatalf("register source auth: %v", err)
	}
	if _, err := manager.Register(context.Background(), target); err != nil {
		t.Fatalf("register target auth: %v", err)
	}

	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
	}

	reg := registry.GetGlobalRegistry()
	reg.UnregisterClient(source.ID)
	reg.UnregisterClient(target.ID)
	t.Cleanup(func() {
		reg.UnregisterClient(source.ID)
		reg.UnregisterClient(target.ID)
	})

	primary := []*ModelInfo{
		{ID: "claude-sonnet-4-5"},
		{ID: "gemini-2.5-pro"},
	}
	reg.RegisterClient(source.ID, "antigravity", primary)

	service.backfillAntigravityModels(source, primary)

	got := reg.GetModelsForClient(target.ID)
	if len(got) != 2 {
		t.Fatalf("expected target auth to be backfilled with 2 models, got %d", len(got))
	}

	ids := make(map[string]struct{}, len(got))
	for _, model := range got {
		if model == nil {
			continue
		}
		ids[strings.ToLower(strings.TrimSpace(model.ID))] = struct{}{}
	}
	if _, ok := ids["claude-sonnet-4-5"]; !ok {
		t.Fatal("expected backfilled model claude-sonnet-4-5")
	}
	if _, ok := ids["gemini-2.5-pro"]; !ok {
		t.Fatal("expected backfilled model gemini-2.5-pro")
	}
}

func TestBackfillAntigravityModels_RespectsExcludedModels(t *testing.T) {
	source := &coreauth.Auth{
		ID:       "ag-backfill-source-excluded",
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
	}
	target := &coreauth.Auth{
		ID:       "ag-backfill-target-excluded",
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind":       "oauth",
			"excluded_models": "gemini-2.5-pro",
		},
	}

	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), source); err != nil {
		t.Fatalf("register source auth: %v", err)
	}
	if _, err := manager.Register(context.Background(), target); err != nil {
		t.Fatalf("register target auth: %v", err)
	}

	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
	}

	reg := registry.GetGlobalRegistry()
	reg.UnregisterClient(source.ID)
	reg.UnregisterClient(target.ID)
	t.Cleanup(func() {
		reg.UnregisterClient(source.ID)
		reg.UnregisterClient(target.ID)
	})

	primary := []*ModelInfo{
		{ID: "claude-sonnet-4-5"},
		{ID: "gemini-2.5-pro"},
	}
	reg.RegisterClient(source.ID, "antigravity", primary)

	service.backfillAntigravityModels(source, primary)

	got := reg.GetModelsForClient(target.ID)
	if len(got) != 1 {
		t.Fatalf("expected 1 model after exclusion, got %d", len(got))
	}
	if got[0] == nil || !strings.EqualFold(strings.TrimSpace(got[0].ID), "claude-sonnet-4-5") {
		t.Fatalf("expected remaining model %q, got %+v", "claude-sonnet-4-5", got[0])
	}
}
