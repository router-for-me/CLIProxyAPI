package cliproxy

import (
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestRegisterModelsForAuth_UsesPreMergedExcludedModelsAttribute(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			OAuthExcludedModels: map[string][]string{
				"gemini-cli": {"gemini-2.5-pro"},
			},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-gemini-cli",
		Provider: "gemini-cli",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind":       "oauth",
			"excluded_models": "gemini-2.5-flash",
		},
	}

	registry := GlobalModelRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)

	models := registry.GetAvailableModelsByProvider("gemini-cli")
	if len(models) == 0 {
		t.Fatal("expected gemini-cli models to be registered")
	}

	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := strings.TrimSpace(model.ID)
		if strings.EqualFold(modelID, "gemini-2.5-flash") {
			t.Fatalf("expected model %q to be excluded by auth attribute", modelID)
		}
	}

	seenGlobalExcluded := false
	for _, model := range models {
		if model == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(model.ID), "gemini-2.5-pro") {
			seenGlobalExcluded = true
			break
		}
	}
	if !seenGlobalExcluded {
		t.Fatal("expected global excluded model to be present when attribute override is set")
	}
}

func TestRegisterModelsForAuth_AWSBedrockAPIKeyAppliesExcludedModels(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			AWSBedrockKey: []config.AWSBedrockKey{
				{
					APIKey: "bedrock-key",
					Models: []config.AWSBedrockModel{
						{Name: "us.deepseek.r1-v1:0", Alias: "deepseek-r1"},
						{Name: "us.deepseek.v3-v1:0", Alias: "deepseek-v3"},
					},
					ExcludedModels: []string{"deepseek-r1"},
				},
			},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-bedrock-apikey",
		Provider: "aws-bedrock",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "apikey",
			"api_key":   "bedrock-key",
		},
	}

	registry := GlobalModelRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)
	models := registry.GetAvailableModelsByProvider("aws-bedrock")
	if len(models) == 0 {
		t.Fatal("expected aws-bedrock models to be registered")
	}

	hasExcluded := false
	hasAllowed := false
	for _, model := range models {
		if model == nil {
			continue
		}
		switch strings.TrimSpace(model.ID) {
		case "deepseek-r1":
			hasExcluded = true
		case "deepseek-v3":
			hasAllowed = true
		}
	}
	if hasExcluded {
		t.Fatal("expected excluded aws-bedrock model deepseek-r1 to be filtered out")
	}
	if !hasAllowed {
		t.Fatal("expected non-excluded aws-bedrock model deepseek-v3 to remain")
	}
}
