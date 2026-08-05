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

func TestOrderProvidersByPriority(t *testing.T) {
	cases := []struct {
		name       string
		providers  []string
		priorities []store.ProviderPriority
		want       []string
	}{
		{
			name:       "descending_priority_orders_primary_first",
			providers:  []string{"opencode", "cometapi", "semutssh"},
			priorities: []store.ProviderPriority{{Provider: "opencode", Priority: 10}, {Provider: "semutssh", Priority: 5}, {Provider: "cometapi", Priority: 1}},
			want:       []string{"opencode", "semutssh", "cometapi"},
		},
		{
			name:       "unlisted_defaults_to_zero_so_stays_last",
			providers:  []string{"opencode", "cometapi"},
			priorities: []store.ProviderPriority{{Provider: "cometapi", Priority: 7}},
			want:       []string{"cometapi", "opencode"},
		},
		{
			name:       "same_priority_preserves_registry_order",
			providers:  []string{"opencode", "cometapi", "semutssh"},
			priorities: []store.ProviderPriority{{Provider: "opencode", Priority: 5}, {Provider: "semutssh", Priority: 5}},
			want:       []string{"opencode", "semutssh", "cometapi"},
		},
		{
			name:       "case_insensitive_provider_match",
			providers:  []string{"OpenCode", "CometAPI"},
			priorities: []store.ProviderPriority{{Provider: "opencode", Priority: 1}, {Provider: "cometapi", Priority: 9}},
			want:       []string{"CometAPI", "OpenCode"},
		},
		{
			name:       "no_priorities_returns_input_unchanged",
			providers:  []string{"opencode", "cometapi"},
			priorities: nil,
			want:       []string{"opencode", "cometapi"},
		},
		{
			name:       "single_provider_returns_unchanged",
			providers:  []string{"opencode"},
			priorities: []store.ProviderPriority{{Provider: "opencode", Priority: 99}},
			want:       []string{"opencode"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := orderProvidersByPriority(tc.providers, tc.priorities)
			if len(got) != len(tc.want) {
				t.Fatalf("orderProvidersByPriority = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("orderProvidersByPriority[%d] = %q, want %q (got=%v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

func TestIntersectProviders(t *testing.T) {
	cases := []struct {
		name      string
		providers []string
		pinned    []string
		want      []string
	}{
		{
			name:      "subset_intersection_preserves_order",
			providers: []string{"semutssh", "opencode", "cometapi"},
			pinned:    []string{"opencode", "cometapi"},
			want:      []string{"opencode", "cometapi"},
		},
		{
			name:      "single_pin_no_failover_set",
			providers: []string{"semutssh", "opencode", "cometapi"},
			pinned:    []string{"semutssh"},
			want:      []string{"semutssh"},
		},
		{
			name:      "case_insensitive",
			providers: []string{"OpenCode", "CometAPI"},
			pinned:    []string{"opencode", "cometapi"},
			want:      []string{"OpenCode", "CometAPI"},
		},
		{
			name:      "empty_intersection_returns_nil",
			providers: []string{"opencode", "cometapi"},
			pinned:    []string{"semutssh"},
			want:      nil,
		},
		{
			name:      "no_pin_returns_all",
			providers: []string{"opencode", "cometapi"},
			pinned:    nil,
			want:      []string{"opencode", "cometapi"},
		},
		{
			name:      "dedupes_provider_duplicates",
			providers: []string{"opencode", "opencode", "cometapi"},
			pinned:    []string{"opencode", "cometapi"},
			want:      []string{"opencode", "cometapi"},
		},
		{
			name:      "trims_whitespace",
			providers: []string{" opencode ", "cometapi"},
			pinned:    []string{"opencode"},
			want:      []string{" opencode "},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := intersectProviders(tc.providers, tc.pinned)
			if len(got) != len(tc.want) {
				t.Fatalf("intersectProviders(%v, %v) = %v, want %v", tc.providers, tc.pinned, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("intersectProviders(%v, %v)[%d] = %q, want %q", tc.providers, tc.pinned, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestIntersectProvidersEmptyProviders(t *testing.T) {
	if got := intersectProviders(nil, []string{"opencode"}); got != nil {
		t.Fatalf("intersectProviders(nil, pinned) = %v, want nil", got)
	}
}

// stubGlobalRouter is a fixed GlobalModelRouteResolver for tests.
type stubGlobalRouter struct {
	route *store.ModelRoute
}

func (s stubGlobalRouter) GlobalModelRoute(_ context.Context, _ string) *store.ModelRoute {
	return s.route
}

func TestGetRequestDetails_GlobalRoutePinsProviders(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-global-route"
	modelRegistry.RegisterClient(clientID, "openai", []*registry.ModelInfo{{ID: "gpt-5.2"}})
	modelRegistry.RegisterClient(clientID+"-anthropic", "anthropic", []*registry.ModelInfo{{ID: "claude-sonnet-4-5"}})
	modelRegistry.RegisterClient(clientID+"-gemini", "gemini", []*registry.ModelInfo{{ID: "gemini-2.5-pro"}})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
		modelRegistry.UnregisterClient(clientID + "-anthropic")
		modelRegistry.UnregisterClient(clientID + "-gemini")
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))
	handler.SetGlobalModelRouter(stubGlobalRouter{route: &store.ModelRoute{
		Model: "gpt-5.2", Providers: []string{"openai"},
	}})

	providers, _, errMsg := handler.getRequestDetails("gpt-5.2")
	if errMsg != nil {
		t.Fatalf("getRequestDetails error = %+v", errMsg)
	}
	if len(providers) != 1 || providers[0] != "openai" {
		t.Fatalf("providers = %v, want [openai]", providers)
	}
}

func TestGetRequestDetails_PerKeyRouteOverridesGlobal(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-global-vs-key-route"
	modelRegistry.RegisterClient(clientID, "openai", []*registry.ModelInfo{{ID: "gpt-5.2"}})
	modelRegistry.RegisterClient(clientID+"-anthropic", "anthropic", []*registry.ModelInfo{{ID: "gpt-5.2"}})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
		modelRegistry.UnregisterClient(clientID + "-anthropic")
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))
	// Global route pins to anthropic.
	handler.SetGlobalModelRouter(stubGlobalRouter{route: &store.ModelRoute{
		Model: "gpt-5.2", Providers: []string{"anthropic"},
	}})

	// Per-key route (stashed on the gin context) pins to openai. The per-key
	// route must win.
	ginCtx, _ := gin.CreateTestContext(nil)
	ginCtx.Set(middleware.CtxPolicyModelRoutes, []store.ModelRoute{
		{Model: "gpt-5.2", Providers: []string{"openai"}},
	})
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	providers, _, errMsg := handler.getRequestDetailsWithOptions(ctx, "gpt-5.2", false)
	if errMsg != nil {
		t.Fatalf("getRequestDetailsWithOptions error = %+v", errMsg)
	}
	if len(providers) != 1 || providers[0] != "openai" {
		t.Fatalf("providers = %v, want [openai] (per-key route overrides global)", providers)
	}
}

func TestGetRequestDetails_NoGlobalOrKeyRouteUsesRegistry(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-no-global-route"
	modelRegistry.RegisterClient(clientID, "openai", []*registry.ModelInfo{{ID: "gpt-5.2"}})
	modelRegistry.RegisterClient(clientID+"-anthropic", "anthropic", []*registry.ModelInfo{{ID: "gpt-5.2"}})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
		modelRegistry.UnregisterClient(clientID + "-anthropic")
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))
	// No global router wired, no per-key route.
	providers, _, errMsg := handler.getRequestDetails("gpt-5.2")
	if errMsg != nil {
		t.Fatalf("getRequestDetails error = %+v", errMsg)
	}
	if len(providers) != 2 {
		t.Fatalf("providers = %v, want both registry providers (no pin)", providers)
	}
}
