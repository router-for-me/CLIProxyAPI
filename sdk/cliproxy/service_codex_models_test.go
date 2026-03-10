package cliproxy

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestRegisterModelsForAuth_CodexFreePlanSkipsHigherTierModels(t *testing.T) {
	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       "auth-codex-free",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"plan_type": "free"},
	}
	models := registerCodexModelsForTest(t, service, auth)
	assertMissingModel(t, models, "gpt-5.3-codex")
	assertMissingModel(t, models, "gpt-5.4")
	assertMissingModel(t, models, "gpt-5.3-codex-spark")
}

func TestRegisterModelsForAuth_CodexTeamPlanIncludes54ButNotSpark(t *testing.T) {
	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       "auth-codex-team",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"plan_type": "team"},
	}
	models := registerCodexModelsForTest(t, service, auth)
	assertHasModel(t, models, "gpt-5.3-codex")
	assertHasModel(t, models, "gpt-5.4")
	assertMissingModel(t, models, "gpt-5.3-codex-spark")
}

func TestRegisterModelsForAuth_CodexPlusPlanIncludesSpark(t *testing.T) {
	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       "auth-codex-plus",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"plan_type": "plus"},
	}
	models := registerCodexModelsForTest(t, service, auth)
	assertHasModel(t, models, "gpt-5.3-codex")
	assertHasModel(t, models, "gpt-5.4")
	assertHasModel(t, models, "gpt-5.3-codex-spark")
}

func TestRegisterModelsForAuth_CodexUnknownPlanFallsBackToUnion(t *testing.T) {
	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       "auth-codex-unknown",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{},
	}
	models := registerCodexModelsForTest(t, service, auth)
	assertHasModel(t, models, "gpt-5.3-codex")
	assertHasModel(t, models, "gpt-5.4")
	assertHasModel(t, models, "gpt-5.3-codex-spark")
}

func TestRegisterModelsForAuth_CodexAPIKeyWithoutModelsUsesUnion(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			CodexKey: []config.CodexKey{{
				APIKey: "sk-test",
			}},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-codex-apikey",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "apikey",
			"api_key":   "sk-test",
		},
		Metadata: map[string]any{},
	}
	models := registerCodexModelsForTest(t, service, auth)
	assertHasModel(t, models, "gpt-5.3-codex")
	assertHasModel(t, models, "gpt-5.4")
	assertHasModel(t, models, "gpt-5.3-codex-spark")
}

func TestRegisterModelsForAuth_CodexAPIKeyExplicitModelsOverrideUnion(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			CodexKey: []config.CodexKey{{
				APIKey: "sk-test-explicit",
				Models: []internalconfig.CodexModel{{
					Name:  "gpt-5.4",
					Alias: "gpt-5.4",
				}},
			}},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-codex-apikey-explicit",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "apikey",
			"api_key":   "sk-test-explicit",
		},
		Metadata: map[string]any{},
	}
	models := registerCodexModelsForTest(t, service, auth)
	assertHasModel(t, models, "gpt-5.4")
	assertMissingModel(t, models, "gpt-5.3-codex")
	assertMissingModel(t, models, "gpt-5.3-codex-spark")
}

func registerCodexModelsForTest(t *testing.T, service *Service, auth *coreauth.Auth) []*registry.ModelInfo {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	reg.UnregisterClient(auth.ID)
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	service.registerModelsForAuth(auth)
	return reg.GetModelsForClient(auth.ID)
}

func assertHasModel(t *testing.T, models []*registry.ModelInfo, id string) {
	t.Helper()
	for _, model := range models {
		if model != nil && model.ID == id {
			return
		}
	}
	t.Fatalf("expected model %q, got %+v", id, models)
}

func assertMissingModel(t *testing.T, models []*registry.ModelInfo, id string) {
	t.Helper()
	for _, model := range models {
		if model != nil && model.ID == id {
			t.Fatalf("did not expect model %q, got %+v", id, models)
		}
	}
}
