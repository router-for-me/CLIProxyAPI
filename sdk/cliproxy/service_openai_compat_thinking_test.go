package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestRegisterModelsForAuth_OpenAICompatibility_DefaultThinkingPreservesLegacyPassthrough(t *testing.T) {
	modelID := "compat-default-" + t.Name()
	authID := "auth-" + t.Name()
	service := &Service{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name: "compat-default",
				Models: []config.OpenAICompatibilityModel{{
					Name:  "upstream-default",
					Alias: modelID,
				}},
			}},
		},
	}
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "openai-compatibility",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"compat_name": "compat-default",
		},
	}

	reg := GlobalModelRegistry()
	reg.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)

	got := registry.LookupModelInfo(modelID, "compat-default")
	if got == nil {
		t.Fatalf("expected model %q to be registered", modelID)
	}
	if !got.UserDefined {
		t.Fatalf("expected model %q to remain user-defined when thinking is omitted", modelID)
	}
	if got.Thinking != nil {
		t.Fatalf("expected model %q to keep nil thinking support when omitted, got %+v", modelID, got.Thinking)
	}
}

func TestRegisterModelsForAuth_OpenAICompatibility_ExplicitThinkingEnablesManagedSupport(t *testing.T) {
	modelID := "compat-thinking-" + t.Name()
	authID := "auth-" + t.Name()
	service := &Service{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name: "compat-thinking",
				Models: []config.OpenAICompatibilityModel{{
					Name:     "upstream-thinking",
					Alias:    modelID,
					Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}},
				}},
			}},
		},
	}
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "openai-compatibility",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"compat_name": "compat-thinking",
		},
	}

	reg := GlobalModelRegistry()
	reg.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)

	got := registry.LookupModelInfo(modelID, "compat-thinking")
	if got == nil {
		t.Fatalf("expected model %q to be registered", modelID)
	}
	if got.UserDefined {
		t.Fatalf("expected model %q to use managed thinking when explicitly configured", modelID)
	}
	if got.Thinking == nil {
		t.Fatalf("expected model %q to expose configured thinking support", modelID)
	}
	if len(got.Thinking.Levels) != 3 || got.Thinking.Levels[0] != "low" || got.Thinking.Levels[1] != "medium" || got.Thinking.Levels[2] != "high" {
		t.Fatalf("unexpected thinking levels: %+v", got.Thinking)
	}
}
