package cliproxy

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestRegisterModelsForAuth_VertexServiceAccount_OpenAPIModelsAndAliases(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"vertex": {
				{
					Name:  "deepseek-ai/deepseek-v4.1-flash-maas",
					Alias: "deepseek-flash",
				},
			},
		},
	}
	cfg.SanitizeOAuthModelAlias()

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetOAuthModelAlias(cfg.OAuthModelAlias)
	service := &Service{
		cfg:         cfg,
		coreManager: manager,
	}

	auth := &coreauth.Auth{
		ID:       "vertex-sa-maas-test",
		Provider: "vertex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{
			"type":       "vertex",
			"project_id": "test-proj",
			"location":   "global",
			"openapi_models": []any{
				"qwen/qwen3-coder-480b-a35b-instruct-maas",
			},
		},
	}
	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(auth.ID)
	})

	registered, errReg := service.coreManager.Register(context.Background(), auth)
	if errReg != nil {
		t.Fatalf("Register auth error: %v", errReg)
	}
	service.registerModelsForAuth(context.Background(), registered)

	if !GlobalModelRegistry().ClientSupportsModel(auth.ID, "deepseek-flash") {
		t.Fatalf("expected Vertex SA auth to support aliased MaaS model 'deepseek-flash'")
	}
	if !GlobalModelRegistry().ClientSupportsModel(auth.ID, "qwen/qwen3-coder-480b-a35b-instruct-maas") {
		t.Fatalf("expected Vertex SA auth to support openapi_models entry 'qwen/qwen3-coder-480b-a35b-instruct-maas'")
	}
	if !GlobalModelRegistry().ClientSupportsModel(auth.ID, "gemini-2.5-pro") {
		t.Fatalf("expected Vertex SA auth to retain native Gemini model 'gemini-2.5-pro'")
	}
	if gotUpstream := service.coreManager.ResolveExecutionModel(registered, "deepseek-flash"); gotUpstream != "deepseek-ai/deepseek-v4.1-flash-maas" {
		t.Fatalf("ResolveExecutionModel(deepseek-flash) = %q, want deepseek-ai/deepseek-v4.1-flash-maas", gotUpstream)
	}
}

func TestSynthesizeAuthFile_VertexModelsAndAliases(t *testing.T) {
	rawJSON := []byte(`{
		"type": "vertex",
		"project_id": "test-proj",
		"location": "global",
		"models": [
			{"name": "deepseek-ai/deepseek-v4.1-flash-maas", "alias": "ds-maas"},
			{"name": "xai/grok-4-maas"}
		],
		"service_account": {"client_email": "sa@test.iam.gserviceaccount.com"}
	}`)

	auths, errSynth := synthesizer.SynthesizeAuthFile(&synthesizer.SynthesisContext{
		Now: time.Unix(1700000000, 0),
	}, "/tmp/vertex-test.json", rawJSON)
	if errSynth != nil {
		t.Fatalf("SynthesizeAuthFile error: %v", errSynth)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 synthesized auth, got %d", len(auths))
	}

	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}
	auth := auths[0]
	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(auth.ID)
	})

	registered, errReg := service.coreManager.Register(context.Background(), auth)
	if errReg != nil {
		t.Fatalf("Register auth error: %v", errReg)
	}
	service.registerModelsForAuth(context.Background(), registered)

	if !GlobalModelRegistry().ClientSupportsModel(auth.ID, "ds-maas") {
		t.Fatalf("expected synthesized Vertex SA auth to support alias 'ds-maas'")
	}
	if !GlobalModelRegistry().ClientSupportsModel(auth.ID, "xai/grok-4-maas") {
		t.Fatalf("expected synthesized Vertex SA auth to support direct model 'xai/grok-4-maas'")
	}
	if gotUpstream := service.coreManager.ResolveExecutionModel(registered, "ds-maas"); gotUpstream != "deepseek-ai/deepseek-v4.1-flash-maas" {
		t.Fatalf("ResolveExecutionModel(ds-maas) = %q, want deepseek-ai/deepseek-v4.1-flash-maas", gotUpstream)
	}
}
