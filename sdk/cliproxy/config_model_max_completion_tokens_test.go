package cliproxy

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	claudeThinking "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/claude"
	claudeResponses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"

	codexmodels "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/models"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestConfiguredCompletionLimitInModelCatalogs(t *testing.T) {
	const modelID = "configured-completion-limit-test"
	const limit = 32768
	const key = "test-api-key"
	service := &Service{cfg: &config.Config{CodexKey: []config.CodexKey{{
		APIKey: key,
		Models: []config.CodexModel{
			{Name: modelID, MaxCompletionTokens: limit},
			{Name: modelID + "-negative", MaxCompletionTokens: -1},
			{Name: modelID + "-zero", MaxCompletionTokens: 0},
			{Name: modelID + "-omitted"},
		},
	}}}}
	auth := &coreauth.Auth{
		ID: t.Name(), Provider: "codex", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "apikey", "api_key": key},
	}
	modelRegistry := GlobalModelRegistry()
	t.Cleanup(func() { modelRegistry.UnregisterClient(auth.ID) })
	service.registerModelsForAuth(t.Context(), auth)

	registered := modelRegistry.GetModelsForClient(auth.ID)
	if len(registered) != 4 {
		t.Fatalf("registered %d models, want 4", len(registered))
	}
	for _, model := range registered {
		want := 0
		if model.ID == modelID {
			want = limit
		}
		if model.CatalogMaxCompletionTokens != want || model.MaxCompletionTokens != 0 || model.OutputTokenLimit != 0 {
			t.Errorf("%s has catalog/runtime/output limits %d/%d/%d, want %d/0/0", model.ID, model.CatalogMaxCompletionTokens, model.MaxCompletionTokens, model.OutputTokenLimit, want)
		}
	}

	for _, catalog := range []struct {
		handler, idKey, limitKey string
		fallback                 any
	}{
		{"openai", "id", "max_completion_tokens", nil},
		{"claude", "id", "max_tokens", int(registry.DefaultClaudeMaxOutputTokens)},
		{"gemini", "name", "outputTokenLimit", nil},
		{"codex", "slug", "max_tokens", nil},
	} {
		t.Run(catalog.handler, func(t *testing.T) {
			models := modelRegistry.GetAvailableModels(catalog.handler)
			if catalog.handler == "codex" {
				models = codexmodels.BuildResponseForClient(modelRegistry.GetAvailableModels("openai"), nil, false, "cpa")["models"].([]map[string]any)
			}
			for _, suffix := range []string{"", "-negative", "-zero", "-omitted"} {
				found := false
				for _, model := range models {
					if model[catalog.idKey] != modelID+suffix {
						continue
					}
					found = true
					want := catalog.fallback
					if suffix == "" {
						want = limit
					}
					if got := model[catalog.limitKey]; got != want {
						t.Errorf("%s %s = %#v, want %#v", modelID+suffix, catalog.limitKey, got, want)
					}
				}
				if !found {
					t.Errorf("missing model %q", modelID+suffix)
				}
			}
		})
	}
}

func TestOAuthCompletionLimitOverridesCatalogOnly(t *testing.T) {
	const limit = 32768
	base := &ModelInfo{ID: "public", MetadataModelID: "upstream", MaxCompletionTokens: 64000, OutputTokenLimit: 65536}
	for _, tc := range []struct {
		name, authKind          string
		configured, wantCatalog int
	}{
		{"oauth", "oauth", limit, limit},
		{"zero", "oauth", 0, 0},
		{"negative", "oauth", -1, 0},
		{"api key", "apikey", limit, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{OAuthSettings: map[string][]config.OAuthModelSetting{
				"codex": {{Name: "upstream", Alias: "public", MaxCompletionTokens: tc.configured}},
			}}
			got := applyOAuthSettings(cfg, "codex", tc.authKind, []*ModelInfo{base})[0]
			if got.CatalogMaxCompletionTokens != tc.wantCatalog || got.MaxCompletionTokens != 64000 || got.OutputTokenLimit != 65536 {
				t.Fatalf("catalog/runtime/output limits = %d/%d/%d, want %d/64000/65536", got.CatalogMaxCompletionTokens, got.MaxCompletionTokens, got.OutputTokenLimit, tc.wantCatalog)
			}
			if base.CatalogMaxCompletionTokens != 0 || base.MaxCompletionTokens != 64000 || base.OutputTokenLimit != 65536 {
				t.Fatalf("original model mutated: %+v", base)
			}
		})
	}
}

func TestConfiguredClaudeCatalogLimitDoesNotAffectRequests(t *testing.T) {
	const modelID = "configured-claude-completion-limit-test"
	const key = "test-api-key"
	service := &Service{cfg: &config.Config{ClaudeKey: []config.ClaudeKey{{
		APIKey: key,
		Models: []config.ClaudeModel{{Name: modelID, MaxCompletionTokens: 2048}},
	}}}}
	auth := &coreauth.Auth{
		ID: t.Name(), Provider: "claude", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "apikey", "api_key": key},
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
	service.registerModelsForAuth(t.Context(), auth)
	info := registry.LookupModelInfo(modelID, "claude")
	if info == nil || info.CatalogMaxCompletionTokens != 2048 {
		t.Fatalf("configured Claude route = %+v, want catalog limit 2048", info)
	}

	for _, tc := range []struct {
		name, body string
		want       int64
	}{
		{"omitted", `{"input":"hello"}`, 32000},
		{"explicit", `{"input":"hello","max_output_tokens":8192}`, 8192},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := claudeResponses.ConvertOpenAIResponsesRequestToClaude(modelID, []byte(tc.body), false)
			if err != nil {
				t.Fatal(err)
			}
			if got := ojson.GetBytes(body, "max_tokens").Int(); got != tc.want {
				t.Fatalf("translated max_tokens = %d, want %d", got, tc.want)
			}
		})
	}

	// Exercise the thinking applier's known-model path with a native output limit.
	known := &registry.ModelInfo{
		MaxCompletionTokens: 4096, CatalogMaxCompletionTokens: info.CatalogMaxCompletionTokens,
		Thinking: &registry.ThinkingSupport{Min: 1024},
	}
	body, err := claudeThinking.NewApplier().Apply([]byte(`{}`), thinking.ThinkingConfig{
		Mode: thinking.ModeBudget, Budget: 5000,
	}, known)
	if err != nil {
		t.Fatal(err)
	}
	if got := ojson.GetBytes(body, "max_tokens").Int(); got != 4096 {
		t.Errorf("thinking max_tokens = %d, want 4096", got)
	}
	if got := ojson.GetBytes(body, "thinking.budget_tokens").Int(); got != 4095 {
		t.Errorf("thinking budget_tokens = %d, want 4095", got)
	}
}
