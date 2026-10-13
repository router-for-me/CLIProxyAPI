package cliproxy

import (
	"context"
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func metaRegisteredModelIDs(authID string) map[string]bool {
	seen := make(map[string]bool)
	for _, model := range GlobalModelRegistry().GetModelsForClient(authID) {
		if model != nil {
			seen[strings.TrimSpace(model.ID)] = true
		}
	}
	return seen
}

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
		{
			name:     "catalog evidence entitled overrides lower tier",
			metadata: map[string]any{"subs_tier_name": "High Usage", "is_subs_active": true, "wonder_full_entitled": true},
			wantFull: true,
		},
		{
			name:     "catalog evidence not entitled overrides maximum tier",
			metadata: map[string]any{"subs_tier_name": "Muse Code Max Usage", "is_subs_active": true, "wonder_full_entitled": false},
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

			seen := metaRegisteredModelIDs(auth.ID)
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

	seen := metaRegisteredModelIDs(auth.ID)
	if !seen["muse-wonder-full-1.0"] {
		t.Fatalf("expected explicitly configured muse-wonder-full-1.0 to stay registered, got %v", seen)
	}
}

func TestMetaRegistrationHookReregistersAfterTierUpdate(t *testing.T) {
	coreManager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: &config.Config{}, coreManager: coreManager}
	coreManager.AddHook(metaModelRegistrationHook{service: service})

	auth := &coreauth.Auth{
		ID:       "auth-meta-wonder-full-hook",
		Provider: "meta",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{
			"type":         "meta",
			"auth_kind":    "oauth",
			"access_token": "dca:valid",
			"dca_token":    "dca:valid",
		},
	}

	registry := GlobalModelRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	ctx := context.Background()
	registered, errRegister := coreManager.Register(ctx, auth)
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	registeredIDs := func() map[string]bool { return metaRegisteredModelIDs(auth.ID) }

	if seen := registeredIDs(); !seen["muse-wonder-1.0"] || seen["muse-wonder-full-1.0"] {
		t.Fatalf("before mint: want Wonder without Full, got %v", seen)
	}

	minted := registered.Clone()
	minted.Metadata["api_key"] = "LLM|minted"
	minted.Metadata["access_token"] = "LLM|minted"
	minted.Metadata["subs_tier_name"] = "Muse Code Max Usage"
	minted.Metadata["subs_tier_id"] = "28367280949604159"
	minted.Metadata["is_subs_active"] = true
	saved, errUpdate := coreManager.UpdatePreparedAuth(ctx, registered, minted)
	if errUpdate != nil {
		t.Fatalf("UpdatePreparedAuth() error = %v", errUpdate)
	}
	if saved == nil {
		t.Fatal("UpdatePreparedAuth() returned nil auth")
	}
	if seen := registeredIDs(); !seen["muse-wonder-full-1.0"] || !seen["muse-wonder-full-1.0-contributor"] {
		t.Fatalf("after mint: want Wonder Full registered, got %v", seen)
	}

	downgraded := saved.Clone()
	downgraded.Metadata["subs_tier_name"] = "High Usage"
	downgraded.Metadata["subs_tier_id"] = "high_usage"
	if _, errDowngrade := coreManager.Update(ctx, downgraded); errDowngrade != nil {
		t.Fatalf("Update() downgrade error = %v", errDowngrade)
	}
	if seen := registeredIDs(); seen["muse-wonder-full-1.0"] || !seen["muse-wonder-1.0"] {
		t.Fatalf("after downgrade: want Wonder without Full, got %v", seen)
	}
}

func TestRegisterModelsForAuth_MetaOAuthConfigMatchStaysEntitlementFiltered(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			MetaKey: []config.CodexKey{
				{
					BaseURL: "https://api.meta.ai/v1",
					Models: []config.MetaModel{
						{Name: "muse-wonder-full-1.0", Alias: "muse-full"},
						{Name: "muse-wonder-1.0"},
					},
				},
			},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-meta-wonder-full-oauth-config-match",
		Provider: "meta",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  "https://api.meta.ai/v1",
		},
		Metadata: map[string]any{
			"type":      "meta",
			"auth_kind": "oauth",
			"dca_token": "dca:valid",
		},
	}

	registry := GlobalModelRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(context.Background(), auth)

	seen := metaRegisteredModelIDs(auth.ID)
	if !seen["muse-wonder-1.0"] {
		t.Fatalf("expected configured muse-wonder-1.0 to stay registered, got %v", seen)
	}
	if seen["muse-wonder-full-1.0"] || seen["muse-full"] {
		t.Fatalf("expected Wonder Full (including its muse-full alias) to stay filtered for an OAuth credential matched only by base URL, got %v", seen)
	}
}

func TestMetaRegistrationHookGuardsReentrancyAndCancellation(t *testing.T) {
	coreManager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: &config.Config{}, coreManager: coreManager}
	hook := metaModelRegistrationHook{service: service}
	coreManager.AddHook(hook)

	auth := &coreauth.Auth{
		ID:       "auth-meta-hook-guards",
		Provider: "meta",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{
			"type":      "meta",
			"auth_kind": "oauth",
			"dca_token": "dca:valid",
		},
	}

	registry := GlobalModelRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	registered, errRegister := coreManager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	if seen := metaRegisteredModelIDs(auth.ID); !seen["muse-wonder-1.0"] {
		t.Fatalf("want Wonder registered after Register, got %v", seen)
	}

	registry.UnregisterClient(auth.ID)
	nestedCtx := context.WithValue(context.Background(), metaRegistrationContextKey{}, true)
	hook.reregister(nestedCtx, registered)
	if seen := metaRegisteredModelIDs(auth.ID); len(seen) != 0 {
		t.Fatalf("want nested reregister to no-op, got %v", seen)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	hook.reregister(canceledCtx, registered)
	if seen := metaRegisteredModelIDs(auth.ID); !seen["muse-wonder-1.0"] {
		t.Fatalf("want Wonder registered despite canceled context, got %v", seen)
	}
}

func TestStripUnentitledMetaWonderFullModels(t *testing.T) {
	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       "auth-meta-strip-stale-full",
		Provider: "meta",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{
			"type":                 "meta",
			"auth_kind":            "oauth",
			"subs_tier_name":       "Muse Code Max Usage",
			"is_subs_active":       true,
			"wonder_full_entitled": false,
		},
	}

	registry := GlobalModelRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})
	registry.RegisterClient(auth.ID, "meta", []*ModelInfo{
		{ID: "muse-wonder-1.0"},
		{ID: "muse-wonder-full-1.0"},
		{ID: "muse-full", MetadataModelID: "muse-wonder-full-1.0-contributor"},
	})

	service.stripUnentitledMetaWonderFullModels(auth)
	seen := metaRegisteredModelIDs(auth.ID)
	if !seen["muse-wonder-1.0"] || seen["muse-wonder-full-1.0"] || seen["muse-full"] {
		t.Fatalf("want only muse-wonder-1.0 after strip, got %v", seen)
	}

	registry.RegisterClient(auth.ID, "meta", []*ModelInfo{
		{ID: "muse-wonder-1.0"},
		{ID: "muse-wonder-full-1.0"},
	})
	auth.Metadata["wonder_full_entitled"] = true
	service.stripUnentitledMetaWonderFullModels(auth)
	if seen := metaRegisteredModelIDs(auth.ID); !seen["muse-wonder-full-1.0"] {
		t.Fatalf("want entitled registration untouched, got %v", seen)
	}
}
