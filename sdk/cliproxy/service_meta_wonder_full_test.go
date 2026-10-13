package cliproxy

import (
	"context"
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestRegisterModelsForAuth_MetaWonderFullEntitlement(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]any
		wantFull bool
	}{
		{
			name:     "maximum usage active",
			metadata: map[string]any{"subs_tier_name": "Muse Code Max Usage", "subs_tier_id": "28367280949604159", "is_subs_active": true},
			wantFull: true,
		},
		{
			name:     "power usage active",
			metadata: map[string]any{"subs_tier_name": "Power Usage", "is_subs_active": true},
			wantFull: true,
		},
		{
			name:     "power usage tier id slug",
			metadata: map[string]any{"subs_tier_id": "power_usage", "is_subs_active": true},
			wantFull: true,
		},
		{
			name:     "high usage active",
			metadata: map[string]any{"subs_tier_name": "High Usage", "subs_tier_id": "high_usage", "is_subs_active": true},
			wantFull: false,
		},
		{
			name:     "everyday usage active",
			metadata: map[string]any{"subs_tier_name": "Everyday Usage", "is_subs_active": true},
			wantFull: false,
		},
		{
			name:     "maximum usage inactive",
			metadata: map[string]any{"subs_tier_name": "Muse Code Max Usage", "is_subs_active": false},
			wantFull: false,
		},
		{
			name:     "unknown subscription state",
			metadata: nil,
			wantFull: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &Service{cfg: &config.Config{}}
			auth := &coreauth.Auth{
				ID:       "auth-meta-wonder-full-" + strings.ReplaceAll(strings.ToLower(test.name), " ", "-"),
				Provider: "meta",
				Status:   coreauth.StatusActive,
				Attributes: map[string]string{
					"auth_kind": "oauth",
					"api_key":   "LLM|minted",
				},
				Metadata: test.metadata,
			}

			registry := GlobalModelRegistry()
			registry.UnregisterClient(auth.ID)
			t.Cleanup(func() {
				registry.UnregisterClient(auth.ID)
			})

			service.registerModelsForAuth(context.Background(), auth)

			models := registry.GetModelsForClient(auth.ID)
			seen := make(map[string]bool)
			for _, model := range models {
				if model != nil {
					seen[strings.TrimSpace(model.ID)] = true
				}
			}
			if !seen["muse-wonder-1.0"] || !seen["muse-spark-1.3"] {
				t.Fatalf("expected standard Meta models to stay registered, got %v", seen)
			}
			for _, fullID := range []string{"muse-wonder-full-1.0", "muse-wonder-full-1.0-contributor"} {
				if seen[fullID] != test.wantFull {
					t.Fatalf("%s registered = %v, want %v (metadata %v)", fullID, seen[fullID], test.wantFull, test.metadata)
				}
			}
		})
	}
}

func TestRegisterModelsForAuth_MetaWonderFullExplicitConfigModelsKept(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			MetaKey: []config.CodexKey{
				{
					BaseURL: "https://api.meta.ai/v1",
					Models: []config.MetaModel{
						{Name: "muse-wonder-full-1.0"},
					},
				},
			},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-meta-wonder-full-config",
		Provider: "meta",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "apikey",
			"base_url":  "https://api.meta.ai/v1",
		},
	}

	registry := GlobalModelRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(context.Background(), auth)

	seen := make(map[string]bool)
	for _, model := range registry.GetModelsForClient(auth.ID) {
		if model != nil {
			seen[strings.TrimSpace(model.ID)] = true
		}
	}
	if !seen["muse-wonder-full-1.0"] {
		t.Fatalf("expected explicitly configured muse-wonder-full-1.0 to stay registered, got %v", seen)
	}
}
