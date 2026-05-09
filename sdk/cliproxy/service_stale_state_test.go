package cliproxy

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestServiceApplyCoreAuthAddOrUpdate_DeleteReAddDoesNotInheritStaleRuntimeState(t *testing.T) {
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}

	authID := "service-stale-state-auth"
	modelID := "stale-model"
	lastRefreshedAt := time.Date(2026, time.March, 1, 8, 0, 0, 0, time.UTC)
	nextRefreshAfter := lastRefreshedAt.Add(30 * time.Minute)

	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(authID)
	})

	service.applyCoreAuthAddOrUpdate(context.Background(), &coreauth.Auth{
		ID:               authID,
		Provider:         "claude",
		Status:           coreauth.StatusActive,
		LastRefreshedAt:  lastRefreshedAt,
		NextRefreshAfter: nextRefreshAfter,
		ModelStates: map[string]*coreauth.ModelState{
			modelID: {
				Quota: coreauth.QuotaState{BackoffLevel: 7},
			},
		},
	})

	service.applyCoreAuthRemoval(context.Background(), authID)

	disabled, ok := service.coreManager.GetByID(authID)
	if !ok || disabled == nil {
		t.Fatalf("expected disabled auth after removal")
	}
	if !disabled.Disabled || disabled.Status != coreauth.StatusDisabled {
		t.Fatalf("expected disabled auth after removal, got disabled=%v status=%v", disabled.Disabled, disabled.Status)
	}
	if disabled.LastRefreshedAt.IsZero() {
		t.Fatalf("expected disabled auth to still carry prior LastRefreshedAt for regression setup")
	}
	if disabled.NextRefreshAfter.IsZero() {
		t.Fatalf("expected disabled auth to still carry prior NextRefreshAfter for regression setup")
	}

	// Reconcile prunes unsupported model state during registration, so seed the
	// disabled snapshot explicitly before exercising delete -> re-add behavior.
	disabled.ModelStates = map[string]*coreauth.ModelState{
		modelID: {
			Quota: coreauth.QuotaState{BackoffLevel: 7},
		},
	}
	if _, err := service.coreManager.Update(context.Background(), disabled); err != nil {
		t.Fatalf("seed disabled auth stale ModelStates: %v", err)
	}

	disabled, ok = service.coreManager.GetByID(authID)
	if !ok || disabled == nil {
		t.Fatalf("expected disabled auth after stale state seeding")
	}
	if len(disabled.ModelStates) == 0 {
		t.Fatalf("expected disabled auth to carry seeded ModelStates for regression setup")
	}

	service.applyCoreAuthAddOrUpdate(context.Background(), &coreauth.Auth{
		ID:       authID,
		Provider: "claude",
		Status:   coreauth.StatusActive,
	})

	updated, ok := service.coreManager.GetByID(authID)
	if !ok || updated == nil {
		t.Fatalf("expected re-added auth to be present")
	}
	if updated.Disabled {
		t.Fatalf("expected re-added auth to be active")
	}
	if !updated.LastRefreshedAt.IsZero() {
		t.Fatalf("expected LastRefreshedAt to reset on delete -> re-add, got %v", updated.LastRefreshedAt)
	}
	if !updated.NextRefreshAfter.IsZero() {
		t.Fatalf("expected NextRefreshAfter to reset on delete -> re-add, got %v", updated.NextRefreshAfter)
	}
	if len(updated.ModelStates) != 0 {
		t.Fatalf("expected ModelStates to reset on delete -> re-add, got %d entries", len(updated.ModelStates))
	}
	if models := registry.GetGlobalRegistry().GetModelsForClient(authID); len(models) == 0 {
		t.Fatalf("expected re-added auth to re-register models in global registry")
	}
}

func TestForceHomeRuntimeConfigEnablesUsageStatistics(t *testing.T) {
	cfg := &config.Config{
		UsageStatisticsEnabled: false,
	}

	forceHomeRuntimeConfig(cfg)

	if !cfg.UsageStatisticsEnabled {
		t.Fatal("expected home runtime config to force usage statistics enabled")
	}
}

func TestApplyHomeOverlayForcesUsageStatisticsEnabled(t *testing.T) {
	baseCfg := &config.Config{}
	baseCfg.Home.Enabled = true
	service := &Service{cfg: baseCfg}

	service.applyHomeOverlay(&config.Config{
		UsageStatisticsEnabled: false,
	})

	if service.cfg == nil || !service.cfg.UsageStatisticsEnabled {
		t.Fatal("expected home overlay to force usage statistics enabled")
	}
	if !service.cfg.Home.Enabled {
		t.Fatal("expected home overlay to preserve local home settings")
	}
}
