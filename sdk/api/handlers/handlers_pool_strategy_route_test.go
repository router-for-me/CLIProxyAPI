package handlers

import (
	"context"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/api/middleware"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

// newPoolStrategyHandler builds a handler whose auth manager carries pool auths
// stamped with the given per-pool strategies. Each entry of poolAuths registers
// one claude-api-key-style auth per pool (compound provider_key like "claude:7",
// per-entry entry_provider_key like "claude:7:key-71") and registers the pool
// row as the registry provider for the model, mirroring how a rendered pool row
// surfaces to the per-model router.
func newPoolStrategyHandler(t *testing.T, model string, poolStrategy map[string]string) (*BaseAPIHandler, func()) {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	modelRegistry := registry.GetGlobalRegistry()

	var auths []*coreauth.Auth
	for pool, strategy := range poolStrategy {
		auth := &coreauth.Auth{
			ID:       "pool-auth-" + pool,
			Provider: "claude",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"provider_key":                     pool,
				coreauth.AttributeEntryProviderKey: pool + ":key-71",
				coreauth.AttributeAuthKind:         coreauth.AuthKindAPIKey,
				coreauth.AttributeAPIKey:           "k-" + pool,
			},
		}
		if strategy != "" {
			auth.Attributes[coreauth.AttributePoolStrategy] = strategy
		}
		auths = append(auths, auth)
		modelRegistry.RegisterClient(auth.ID, pool, []*registry.ModelInfo{{ID: model}})
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
	}
	cleanup := func() {
		for _, auth := range auths {
			modelRegistry.UnregisterClient(auth.ID)
		}
	}
	return NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager), cleanup
}

// applyRouteOnGinContext runs applyPinnedRoute with the supplied route against
// a gin context and returns the stashed route strategy so tests assert both the
// provider intersection and the strategy transfer in one place.
func applyRouteOnGinContext(t *testing.T, h *BaseAPIHandler, providers []string, modelName string, route *store.ModelRoute) ([]string, string) {
	t.Helper()
	ginCtx, _ := gin.CreateTestContext(nil)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	filtered, errMsg := h.applyPinnedRoute(ctx, providers, modelName, route)
	if errMsg != nil {
		t.Fatalf("applyPinnedRoute error = %+v", errMsg)
	}
	return filtered, middleware.RouteStrategyFor(ginCtx)
}

// TestApplyPinnedRoute_PoolStrategyBecomesRouteDefault pins the Task 7
// behavior: a route pinning a pool without its own strategy inherits the pool
// row's strategy, the route's own strategy still wins, and mixed pools keep
// the global strategy (nothing stashed).
func TestApplyPinnedRoute_PoolStrategyBecomesRouteDefault(t *testing.T) {
	const model = "glm-5.3-pool-strategy-route"
	handler, cleanup := newPoolStrategyHandler(t, model, map[string]string{
		"claude:7":  "round-robin",
		"claude:9":  "fill-first",
		"claude:11": "weighted-round-robin",
	})
	t.Cleanup(cleanup)

	cases := []struct {
		name         string
		route        *store.ModelRoute
		wantStrategy string
	}{
		{
			name:         "route_without_strategy_inherits_pool_row_strategy",
			route:        &store.ModelRoute{Model: model, Providers: []string{"claude:7"}},
			wantStrategy: "round-robin",
		},
		{
			name:         "route_explicit_strategy_wins_over_pool_strategy",
			route:        &store.ModelRoute{Model: model, Providers: []string{"claude:7"}, Strategy: "priority"},
			wantStrategy: "priority",
		},
		{
			// weighted-round-robin is reachable only through the pool fallback
			// at this layer (the per-key route input boundary still validates
			// routes to ""/priority/failover), so this case is the sole guard
			// for that stash-switch arm.
			name:         "route_without_strategy_inherits_weighted_pool_strategy",
			route:        &store.ModelRoute{Model: model, Providers: []string{"claude:11"}},
			wantStrategy: "weighted-round-robin",
		},
		{
			name: "mixed_pools_with_different_strategies_keep_global",
			route: &store.ModelRoute{
				Model:     model,
				Providers: []string{"claude:7", "claude:9"},
			},
			wantStrategy: "",
		},
		{
			name:         "nil_route_stashes_nothing",
			route:        nil,
			wantStrategy: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filtered, strategy := applyRouteOnGinContext(t, handler, []string{"claude:7", "claude:9", "claude:11"}, model, tc.route)
			if strategy != tc.wantStrategy {
				t.Fatalf("RouteStrategyFor = %q, want %q (providers=%v)", strategy, tc.wantStrategy, filtered)
			}
		})
	}
}

// TestApplyPinnedRoute_NoPoolStrategyStashesNothing covers today's behavior
// for pools that never opted into a row strategy: the global routing strategy
// applies unchanged.
func TestApplyPinnedRoute_NoPoolStrategyStashesNothing(t *testing.T) {
	const model = "glm-5.3-pool-no-strategy"
	handler, cleanup := newPoolStrategyHandler(t, model, map[string]string{
		"claude:7": "",
		"claude:9": "",
	})
	t.Cleanup(cleanup)

	_, strategy := applyRouteOnGinContext(t, handler, []string{"claude:7"}, model,
		&store.ModelRoute{Model: model, Providers: []string{"claude:7"}})
	if strategy != "" {
		t.Fatalf("RouteStrategyFor = %q, want empty (pools without strategy keep the global routing strategy)", strategy)
	}
}
