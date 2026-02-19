package cliproxy

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestRegisterModelsForAuth_CodexOAuthPlanFilter_OnlyProSupportsSpark(t *testing.T) {
	service := &Service{cfg: &config.Config{}}

	registry := GlobalModelRegistry()
	defaultAuthID := "codex-default-auth"
	proAuthID := "codex-pro-auth"
	registry.UnregisterClient(defaultAuthID)
	registry.UnregisterClient(proAuthID)
	t.Cleanup(func() {
		registry.UnregisterClient(defaultAuthID)
		registry.UnregisterClient(proAuthID)
	})

	defaultAuth := &coreauth.Auth{
		ID:       defaultAuthID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
	}
	service.registerModelsForAuth(defaultAuth)

	if registry.ClientSupportsModel(defaultAuthID, "gpt-5.3-codex-spark") {
		t.Fatal("expected codex oauth account to default exclude gpt-5.3-codex-spark")
	}

	proAuth := &coreauth.Auth{
		ID:       proAuthID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{
			"id_token": fakeCodexIDToken(t, "pro"),
		},
	}
	service.registerModelsForAuth(proAuth)

	if !registry.ClientSupportsModel(proAuthID, "gpt-5.3-codex-spark") {
		t.Fatal("expected pro codex oauth account to include gpt-5.3-codex-spark")
	}
}

func TestRegisterModelsForAuth_CodexOAuthPlanFilter_FallbackFilenamePro(t *testing.T) {
	service := &Service{cfg: &config.Config{}}

	registry := GlobalModelRegistry()
	authID := "codex-user@example.com-pro.json"
	registry.UnregisterClient(authID)
	t.Cleanup(func() {
		registry.UnregisterClient(authID)
	})

	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
	}
	service.registerModelsForAuth(auth)

	if !registry.ClientSupportsModel(authID, "gpt-5.3-codex-spark") {
		t.Fatal("expected codex oauth filename with pro suffix to include gpt-5.3-codex-spark")
	}
}

func fakeCodexIDToken(t *testing.T, planType string) string {
	t.Helper()

	headerBytes, err := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	payloadBytes, err := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_plan_type": planType,
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	header := base64.RawURLEncoding.EncodeToString(headerBytes)
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	return header + "." + payload + "."
}
