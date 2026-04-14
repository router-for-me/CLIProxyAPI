package cliproxy

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestRegisterModelsForAuth_OpenAICompatUsesStaticThinkingSupportWhenUnset(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name: "compat-static",
					Models: []config.OpenAICompatibilityModel{
						{Name: "gpt-5.2-codex"},
					},
				},
			},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-compat-static",
		Provider: "compat-static",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"compat_name":  "compat-static",
			"provider_key": "compat-static",
		},
	}

	reg := GlobalModelRegistry()
	reg.UnregisterClient(auth.ID)
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	service.registerModelsForAuth(auth)

	info := registry.GetGlobalRegistry().GetModelInfo("gpt-5.2-codex", "compat-static")
	if info == nil {
		t.Fatalf("expected model to be registered")
	}
	if info.UserDefined {
		t.Fatalf("expected static-known model to use strict capability (UserDefined=false)")
	}
	if info.Thinking == nil {
		t.Fatalf("expected thinking support inherited from static registry")
	}
	if !containsThinkingLevel(info.Thinking, "xhigh") {
		t.Fatalf("expected xhigh support, got levels=%v", info.Thinking.Levels)
	}
}

func TestRegisterModelsForAuth_OpenAICompatUnknownModelDefaultsToPassthrough(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name: "compat-unknown",
					Models: []config.OpenAICompatibilityModel{
						{Name: "unknown-upstream-model", Alias: "unknown-local-model"},
					},
				},
			},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-compat-unknown",
		Provider: "compat-unknown",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"compat_name":  "compat-unknown",
			"provider_key": "compat-unknown",
		},
	}

	reg := GlobalModelRegistry()
	reg.UnregisterClient(auth.ID)
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	service.registerModelsForAuth(auth)

	info := registry.GetGlobalRegistry().GetModelInfo("unknown-local-model", "compat-unknown")
	if info == nil {
		t.Fatalf("expected model to be registered")
	}
	if !info.UserDefined {
		t.Fatalf("expected unknown model to passthrough validation (UserDefined=true)")
	}
	if info.Thinking != nil {
		t.Fatalf("expected unknown model to keep nil thinking support, got %+v", info.Thinking)
	}
}

func containsThinkingLevel(support *registry.ThinkingSupport, level string) bool {
	if support == nil {
		return false
	}
	want := strings.ToLower(strings.TrimSpace(level))
	for _, item := range support.Levels {
		if strings.ToLower(strings.TrimSpace(item)) == want {
			return true
		}
	}
	return false
}
