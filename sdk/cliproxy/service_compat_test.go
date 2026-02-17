package cliproxy

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// TestOpenAICompatProviderKeyExtraction verifies that provider keys are correctly
// extracted from OpenAI compatibility auth entries.
func TestOpenAICompatProviderKeyExtraction(t *testing.T) {
	tests := []struct {
		name                string
		auth                *coreauth.Auth
		wantProviderKey     string
		wantCompatName      string
		wantIsCompat        bool
	}{
		{
			name: "GLM provider with prefix",
			auth: &coreauth.Auth{
				ID:       "config:zai[key1]",
				Provider: "zai",
				Label:    "zai",
				Prefix:   "glm",
				Attributes: map[string]string{
					"provider_key": "zai",
					"compat_name":  "zai",
					"base_url":     "https://api.z.ai/api/paas/v4",
				},
			},
			wantProviderKey: "zai",
			wantCompatName:  "zai",
			wantIsCompat:    true,
		},
		{
			name: "MiniMax provider without prefix",
			auth: &coreauth.Auth{
				ID:       "config:minimax[key2]",
				Provider: "minimax",
				Label:    "minimax",
				Attributes: map[string]string{
					"provider_key": "minimax",
					"compat_name":  "minimax",
					"base_url":     "https://api.minimax.io/v1",
				},
			},
			wantProviderKey: "minimax",
			wantCompatName:  "minimax",
			wantIsCompat:    true,
		},
		{
			name: "Legacy OpenAI compat provider",
			auth: &coreauth.Auth{
				ID:       "legacy",
				Provider: "openai-compatibility",
				Label:    "openrouter",
				Attributes: map[string]string{},
			},
			wantProviderKey: "openai-compatibility",
			wantCompatName:  "openrouter",
			wantIsCompat:    true,
		},
		{
			name: "Regular Gemini provider",
			auth: &coreauth.Auth{
				ID:       "gemini-oauth-1",
				Provider: "gemini",
				Label:    "user@example.com",
				Attributes: map[string]string{},
			},
			wantProviderKey: "",
			wantCompatName:  "",
			wantIsCompat:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			providerKey, compatName, isCompat := openAICompatInfoFromAuth(tt.auth)
			
			if isCompat != tt.wantIsCompat {
				t.Errorf("openAICompatInfoFromAuth() isCompat = %v, want %v", isCompat, tt.wantIsCompat)
			}
			if isCompat {
				if providerKey != tt.wantProviderKey {
					t.Errorf("openAICompatInfoFromAuth() providerKey = %v, want %v", providerKey, tt.wantProviderKey)
				}
				if compatName != tt.wantCompatName {
					t.Errorf("openAICompatInfoFromAuth() compatName = %v, want %v", compatName, tt.wantCompatName)
				}
			}
		})
	}
}

// TestOpenAICompatModelRegistrationWithPrefix verifies that models with prefixes
// are registered under both prefixed and non-prefixed names.
func TestOpenAICompatModelRegistrationWithPrefix(t *testing.T) {
	// Create a test config with GLM provider that has a prefix
	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:     "zai",
				Prefix:   "glm",
				BaseURL:  "https://api.z.ai/api/paas/v4",
				Models: []config.OpenAICompatibilityModel{
					{Name: "glm-5", Alias: "glm-5"},
					{Name: "glm-4.7-flash", Alias: "glm-4.7-flash"},
				},
			},
			{
				Name:    "minimax",
				BaseURL: "https://api.minimax.io/v1",
				Models: []config.OpenAICompatibilityModel{
					{Name: "MiniMax-M2.5", Alias: "minimax-m2.5"},
				},
			},
		},
	}

	tests := []struct {
		name          string
		compat        config.OpenAICompatibility
		authPrefix    string
		wantModelIDs  []string
	}{
		{
			name:       "GLM with config prefix and matching auth prefix",
			compat:     cfg.OpenAICompatibility[0],
			authPrefix: "glm",
			wantModelIDs: []string{
				"glm-5",
				"glm/glm-5",
				"glm-4.7-flash",
				"glm/glm-4.7-flash",
			},
		},
		{
			name:       "GLM with config prefix and empty auth prefix",
			compat:     cfg.OpenAICompatibility[0],
			authPrefix: "",
			wantModelIDs: []string{
				"glm-5",
				"glm/glm-5",
				"glm-4.7-flash",
				"glm/glm-4.7-flash",
			},
		},
		{
			name:       "MiniMax without prefix",
			compat:     cfg.OpenAICompatibility[1],
			authPrefix: "",
			wantModelIDs: []string{
				"minimax-m2.5",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create model infos like registerModelsForAuth does
			ms := make([]*ModelInfo, 0, len(tt.compat.Models))
			for _, m := range tt.compat.Models {
				modelID := m.Alias
				if modelID == "" {
					modelID = m.Name
				}
				ms = append(ms, &ModelInfo{
					ID:          modelID,
					Object:      "model",
					OwnedBy:     tt.compat.Name,
					Type:        "openai-compatibility",
					DisplayName: modelID,
					UserDefined: true,
				})
			}

			// Apply auth prefix
			ms = applyModelPrefixes(ms, tt.authPrefix, false)

			// Apply compat prefix if different from auth prefix
			if tt.compat.Prefix != "" && tt.compat.Prefix != tt.authPrefix {
				ms = applyModelPrefixes(ms, tt.compat.Prefix, false)
			}

			// Collect all model IDs
			gotModelIDs := make([]string, len(ms))
			for i, m := range ms {
				gotModelIDs[i] = m.ID
			}

			// Check that all expected model IDs are present
			for _, wantID := range tt.wantModelIDs {
				found := false
				for _, gotID := range gotModelIDs {
					if gotID == wantID {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected model ID %q not found in registered models: %v", wantID, gotModelIDs)
				}
			}

			// Check that we have the expected number of models
			if len(gotModelIDs) != len(tt.wantModelIDs) {
				t.Errorf("Got %d model IDs, want %d: got=%v, want=%v", 
					len(gotModelIDs), len(tt.wantModelIDs), gotModelIDs, tt.wantModelIDs)
			}
		})
	}
}

// TestProviderKeyConsistency verifies that the same provider key is used
// for both executor registration and model registration.
func TestProviderKeyConsistency(t *testing.T) {
	// Simulate an auth entry for GLM provider
	auth := &coreauth.Auth{
		ID:       "config:zai[test-key]",
		Provider: "zai",
		Label:    "zai",
		Prefix:   "glm",
		Attributes: map[string]string{
			"provider_key": "zai",
			"compat_name":  "zai",
			"base_url":     "https://api.z.ai/api/paas/v4",
			"api_key":      "test-key",
		},
	}

	// Get provider key using the same logic as ensureExecutorsForAuth
	compatProviderKey, _, isCompat := openAICompatInfoFromAuth(auth)
	if !isCompat {
		t.Fatal("Expected auth to be detected as OpenAI compatibility")
	}

	if compatProviderKey == "" {
		compatProviderKey = strings.ToLower(strings.TrimSpace(auth.Provider))
	}
	if compatProviderKey == "" {
		compatProviderKey = "openai-compatibility"
	}

	// This is the key used for executor registration
	executorProviderKey := compatProviderKey

	// Simulate the provider key logic from registerModelsForAuth
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	compatProviderKey2, compatDisplayName, compatDetected := openAICompatInfoFromAuth(auth)
	if compatDetected {
		provider = "openai-compatibility"
	}

	providerKey := provider
	compatName := strings.TrimSpace(auth.Provider)
	if compatDetected {
		if compatProviderKey2 != "" {
			providerKey = compatProviderKey2
		}
		if compatDisplayName != "" {
			compatName = compatDisplayName
		}
	}

	// For OpenAI compat, providerKey should come from attributes
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["provider_key"]); v != "" {
			providerKey = strings.ToLower(v)
		}
		// compatName is used for matching against config.OpenAICompatibility
		_ = compatName
	}

	// This is the key used for model registration
	modelProviderKey := providerKey

	// Verify they match
	if executorProviderKey != modelProviderKey {
		t.Errorf("Provider key mismatch: executor uses %q but model registration uses %q", 
			executorProviderKey, modelProviderKey)
	}

	// Expected provider key is "zai" from attributes
	expectedProviderKey := "zai"
	if executorProviderKey != expectedProviderKey {
		t.Errorf("Executor provider key = %q, want %q", executorProviderKey, expectedProviderKey)
	}
	if modelProviderKey != expectedProviderKey {
		t.Errorf("Model provider key = %q, want %q", modelProviderKey, expectedProviderKey)
	}
}
