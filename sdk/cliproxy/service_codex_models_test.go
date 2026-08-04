package cliproxy

import (
	"context"
	"fmt"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestRegisterModelsForAuthCodexAPIKeyModels(t *testing.T) {
	defaultModels := internalregistry.GetCodexProModels()
	if len(defaultModels) == 0 {
		t.Fatal("expected Codex Pro default models")
	}

	excludedModelID := defaultModels[0].ID
	tests := []struct {
		name        string
		entry       config.CodexKey
		wantIDs     map[string]struct{}
		wantPresent []string
		wantAbsent  []string
	}{
		{
			name:        "defaults without explicit models",
			entry:       config.CodexKey{APIKey: "default-key"},
			wantIDs:     codexModelIDSet(defaultModels),
			wantPresent: []string{"gpt-image-1.5", "gpt-image-2"},
		},
		{
			name: "only explicitly configured models",
			entry: config.CodexKey{
				APIKey: "configured-key",
				Models: []internalconfig.CodexModel{{
					Name: "upstream-codex", Alias: "configured-codex",
				}},
			},
			wantIDs:    map[string]struct{}{"configured-codex": {}},
			wantAbsent: []string{"gpt-image-1.5", "gpt-image-2"},
		},
		{
			name: "exclusions apply to defaults",
			entry: config.CodexKey{
				APIKey:         "excluded-key",
				ExcludedModels: []string{excludedModelID},
			},
			wantIDs: codexModelIDSet(defaultModels[1:]),
		},
	}

	for index := range tests {
		testCase := tests[index]
		t.Run(testCase.name, func(t *testing.T) {
			authID := fmt.Sprintf("codex-api-key-models-%d", index)
			modelRegistry := internalregistry.GetGlobalRegistry()
			modelRegistry.UnregisterClient(authID)
			t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

			service := &Service{cfg: &config.Config{CodexKey: []config.CodexKey{testCase.entry}}}
			auth := &coreauth.Auth{
				ID:       authID,
				Provider: "codex",
				Status:   coreauth.StatusActive,
				Attributes: map[string]string{
					coreauth.AttributeAPIKey:      testCase.entry.APIKey,
					coreauth.AttributeConfigIndex: "0",
					coreauth.AttributeSource:      "config:codex:test",
				},
			}

			service.registerModelsForAuth(context.Background(), auth)
			gotIDs := codexModelIDSet(modelRegistry.GetModelsForClient(authID))
			if len(gotIDs) != len(testCase.wantIDs) {
				t.Fatalf("registered model IDs = %#v, want %#v", gotIDs, testCase.wantIDs)
			}
			for modelID := range testCase.wantIDs {
				if _, ok := gotIDs[modelID]; !ok {
					t.Errorf("missing registered model %q", modelID)
				}
			}
			for _, modelID := range testCase.wantPresent {
				if _, ok := gotIDs[modelID]; !ok {
					t.Errorf("missing required registered model %q", modelID)
				}
			}
			for _, modelID := range testCase.wantAbsent {
				if _, ok := gotIDs[modelID]; ok {
					t.Errorf("unexpected registered model %q", modelID)
				}
			}
		})
	}
}

func TestRegisterModelsForAuthCodexAPIKeyDefaultRequiresConfigMatch(t *testing.T) {
	defaultIDs := codexModelIDSet(internalregistry.GetCodexProModels())
	tests := []struct {
		name       string
		config     config.Config
		attributes map[string]string
		wantIDs    map[string]struct{}
	}{
		{
			name: "valid index with unmatched API key",
			config: config.Config{CodexKey: []config.CodexKey{{
				APIKey: "configured-key",
			}}},
			attributes: map[string]string{
				coreauth.AttributeAPIKey:      "stale-key",
				coreauth.AttributeConfigIndex: "0",
				coreauth.AttributeSource:      "config:codex:stale",
			},
			wantIDs: map[string]struct{}{},
		},
		{
			name: "valid index with unmatched base URL",
			config: config.Config{CodexKey: []config.CodexKey{{
				APIKey: "configured-key", BaseURL: "https://new.example.com",
			}}},
			attributes: map[string]string{
				coreauth.AttributeAPIKey:      "configured-key",
				coreauth.AttributeConfigIndex: "0",
				coreauth.AttributeSource:      "config:codex:stale",
				"base_url":                    "https://old.example.com",
			},
			wantIDs: map[string]struct{}{},
		},
		{
			name: "stale index falls back to matching credentials",
			config: config.Config{CodexKey: []config.CodexKey{
				{
					APIKey: "wrong-key",
					Models: []internalconfig.CodexModel{{Name: "wrong-model"}},
				},
				{APIKey: "configured-key"},
			}},
			attributes: map[string]string{
				coreauth.AttributeAPIKey:      "configured-key",
				coreauth.AttributeConfigIndex: "0",
				coreauth.AttributeSource:      "config:codex:stale",
			},
			wantIDs: defaultIDs,
		},
		{
			name: "API key ignores OAuth plan type",
			config: config.Config{CodexKey: []config.CodexKey{{
				APIKey: "configured-key",
			}}},
			attributes: map[string]string{
				coreauth.AttributeAPIKey:      "configured-key",
				coreauth.AttributeConfigIndex: "0",
				coreauth.AttributeSource:      "config:codex:test",
				"plan_type":                   "free",
			},
			wantIDs: defaultIDs,
		},
	}

	for index := range tests {
		testCase := tests[index]
		t.Run(testCase.name, func(t *testing.T) {
			authID := fmt.Sprintf("codex-api-key-config-match-%d", index)
			modelRegistry := internalregistry.GetGlobalRegistry()
			modelRegistry.UnregisterClient(authID)
			modelRegistry.RegisterClient(authID, "codex", []*internalregistry.ModelInfo{{ID: "stale-model"}})
			t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

			service := &Service{cfg: &testCase.config}
			auth := &coreauth.Auth{
				ID:         authID,
				Provider:   "codex",
				Status:     coreauth.StatusActive,
				Attributes: testCase.attributes,
			}

			service.registerModelsForAuth(context.Background(), auth)
			gotIDs := codexModelIDSet(modelRegistry.GetModelsForClient(authID))
			if len(gotIDs) != len(testCase.wantIDs) {
				t.Fatalf("registered model IDs = %#v, want %#v", gotIDs, testCase.wantIDs)
			}
			for modelID := range testCase.wantIDs {
				if _, ok := gotIDs[modelID]; !ok {
					t.Errorf("missing registered model %q", modelID)
				}
			}
		})
	}
}

func TestRegisterConfigAPIKeyAuthsCodexModelModes(t *testing.T) {
	defaultIDs := codexModelIDSet(internalregistry.GetCodexProModels())
	tests := []struct {
		name       string
		models     []internalconfig.CodexModel
		wantIDs    map[string]struct{}
		wantImages bool
	}{
		{
			name:       "empty models uses defaults with images",
			wantIDs:    defaultIDs,
			wantImages: true,
		},
		{
			name: "configured models replace defaults",
			models: []internalconfig.CodexModel{{
				Name: "runtime-upstream", Alias: "runtime-configured",
			}},
			wantIDs: map[string]struct{}{"runtime-configured": {}},
		},
	}

	for index := range tests {
		testCase := tests[index]
		t.Run(testCase.name, func(t *testing.T) {
			cfg := &config.Config{CodexKey: []config.CodexKey{{
				APIKey: fmt.Sprintf("runtime-key-%d", index),
				Models: testCase.models,
			}}}
			manager := coreauth.NewManager(nil, nil, nil)
			service := &Service{cfg: cfg, coreManager: manager}
			service.registerConfigAPIKeyAuths(context.Background(), cfg)

			auths := manager.List()
			modelRegistry := internalregistry.GetGlobalRegistry()
			for _, auth := range auths {
				if auth != nil {
					authID := auth.ID
					t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })
				}
			}
			if len(auths) != 1 {
				t.Fatalf("runtime auth count = %d, want 1", len(auths))
			}

			registeredIDs := codexModelIDSet(modelRegistry.GetModelsForClient(auths[0].ID))
			if len(registeredIDs) != len(testCase.wantIDs) {
				t.Fatalf("registered model IDs = %#v, want %#v", registeredIDs, testCase.wantIDs)
			}
			for modelID := range testCase.wantIDs {
				if _, ok := registeredIDs[modelID]; !ok {
					t.Errorf("missing registered model %q", modelID)
				}
			}
			for _, modelID := range []string{"gpt-image-1.5", "gpt-image-2"} {
				_, registered := registeredIDs[modelID]
				if registered != testCase.wantImages {
					t.Errorf("registered model %q = %t, want %t", modelID, registered, testCase.wantImages)
				}
				if testCase.wantImages {
					if _, available := openAIModelIDSet(modelRegistry.GetAvailableModels("openai"))[modelID]; !available {
						t.Errorf("/v1/models source is missing %q", modelID)
					}
				}
			}
		})
	}
}

func codexModelIDSet(models []*internalregistry.ModelInfo) map[string]struct{} {
	ids := make(map[string]struct{}, len(models))
	for _, model := range models {
		if model != nil && model.ID != "" {
			ids[model.ID] = struct{}{}
		}
	}
	return ids
}

func openAIModelIDSet(models []map[string]any) map[string]struct{} {
	ids := make(map[string]struct{}, len(models))
	for _, model := range models {
		if modelID, ok := model["id"].(string); ok && modelID != "" {
			ids[modelID] = struct{}{}
		}
	}
	return ids
}
