package management

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

func TestValidateModelRoutes(t *testing.T) {
	cases := []struct {
		name    string
		policy  store.Policy
		wantErr bool
	}{
		{
			name:    "no_routes_ok",
			policy:  store.Policy{AllowedModels: []string{"gpt-4o"}},
			wantErr: false,
		},
		{
			name: "route_for_allowed_model_ok",
			policy: store.Policy{
				AllowedModels: []string{"deepseek-v4-pro"},
				ModelRoutes:   []store.ModelRoute{{Model: "deepseek-v4-pro", Providers: []string{"semutssh"}}},
			},
			wantErr: false,
		},
		{
			name: "route_covered_by_wildcard_ok",
			policy: store.Policy{
				AllowedModels: []string{"gpt-4*"},
				ModelRoutes:   []store.ModelRoute{{Model: "gpt-4o", Providers: []string{"opencode"}}},
			},
			wantErr: false,
		},
		{
			name: "route_not_in_allowed_rejected",
			policy: store.Policy{
				AllowedModels: []string{"deepseek-v4-pro"},
				ModelRoutes:   []store.ModelRoute{{Model: "gpt-4o", Providers: []string{"opencode"}}},
			},
			wantErr: true,
		},
		{
			name: "route_with_no_providers_rejected",
			policy: store.Policy{
				AllowedModels: []string{"gpt-4o"},
				ModelRoutes:   []store.ModelRoute{{Model: "gpt-4o", Providers: nil}},
			},
			wantErr: true,
		},
		{
			name: "duplicate_route_model_rejected",
			policy: store.Policy{
				AllowedModels: []string{"gpt-4o"},
				ModelRoutes: []store.ModelRoute{
					{Model: "gpt-4o", Providers: []string{"opencode"}},
					{Model: "gpt-4o", Providers: []string{"cometapi"}},
				},
			},
			wantErr: true,
		},
		{
			name: "empty_model_route_rejected",
			policy: store.Policy{
				AllowedModels: []string{"gpt-4o"},
				ModelRoutes:   []store.ModelRoute{{Model: "", Providers: []string{"opencode"}}},
			},
			wantErr: true,
		},
		{
			name: "strategy_priority_with_priorities_ok",
			policy: store.Policy{
				AllowedModels: []string{"gpt-4o"},
				ModelRoutes: []store.ModelRoute{{
					Model:      "gpt-4o",
					Providers:  []string{"opencode", "cometapi"},
					Strategy:   "priority",
					Priorities: []store.ProviderPriority{{Provider: "opencode", Priority: 10}, {Provider: "cometapi", Priority: 1}},
				}},
			},
			wantErr: false,
		},
		{
			name: "strategy_failover_ok",
			policy: store.Policy{
				AllowedModels: []string{"gpt-4o"},
				ModelRoutes: []store.ModelRoute{{
					Model:     "gpt-4o",
					Providers: []string{"opencode", "cometapi"},
					Strategy:  "failover",
				}},
			},
			wantErr: false,
		},
		{
			name: "strategy_unknown_rejected",
			policy: store.Policy{
				AllowedModels: []string{"gpt-4o"},
				ModelRoutes: []store.ModelRoute{{
					Model:     "gpt-4o",
					Providers: []string{"opencode"},
					Strategy:  "weighted",
				}},
			},
			wantErr: true,
		},
		{
			name: "priority_for_non_allowlisted_provider_rejected",
			policy: store.Policy{
				AllowedModels: []string{"gpt-4o"},
				ModelRoutes: []store.ModelRoute{{
					Model:      "gpt-4o",
					Providers:  []string{"opencode"},
					Strategy:   "priority",
					Priorities: []store.ProviderPriority{{Provider: "ghost", Priority: 5}},
				}},
			},
			wantErr: true,
		},
		{
			name: "duplicate_priority_for_provider_rejected",
			policy: store.Policy{
				AllowedModels: []string{"gpt-4o"},
				ModelRoutes: []store.ModelRoute{{
					Model:      "gpt-4o",
					Providers:  []string{"opencode"},
					Strategy:   "priority",
					Priorities: []store.ProviderPriority{{Provider: "opencode", Priority: 5}, {Provider: "opencode", Priority: 3}},
				}},
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := validateModelRoutes(tc.policy)
			if tc.wantErr && msg == "" {
				t.Fatalf("validateModelRoutes() = %q, want non-empty error", msg)
			}
			if !tc.wantErr && msg != "" {
				t.Fatalf("validateModelRoutes() = %q, want empty", msg)
			}
		})
	}
}
