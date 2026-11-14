package helps

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestShouldExecuteVertexOpenAPIForRequest_ModelSettings(t *testing.T) {
	for _, kind := range []string{"service-account", "api-key"} {
		for _, source := range []string{"models", "model_aliases", "model-aliases", "vertex_model_openapi", "oauth-attribute", "global-alias", "global-setting", "api-key-config"} {
			if source == "api-key-config" && kind != "api-key" {
				continue
			}
			if kind == "api-key" && (source == "oauth-attribute" || source == "global-alias" || source == "global-setting") {
				continue
			}
			for _, enabled := range []bool{true, false} {
				for _, withAlias := range []bool{true, false} {
					if !withAlias && (source == "oauth-attribute" || source == "global-alias") {
						continue
					}
					t.Run(kind+"/"+source+"/"+strconv.FormatBool(enabled)+"/alias="+strconv.FormatBool(withAlias), func(t *testing.T) {
						name := "custom-gemini"
						if !enabled {
							name = "partner/custom-model"
						}
						rule := config.OAuthModelAlias{Name: name, OpenAPI: &enabled}
						requested := name
						if withAlias {
							rule.Alias = "public-model"
							requested = "PUBLIC-MODEL(high)"
						}
						fallback := !enabled
						auth := &cliproxyauth.Auth{
							Provider:   "vertex",
							Attributes: map[string]string{"openapi": strconv.FormatBool(fallback)},
							Metadata:   map[string]any{"service_account": map[string]any{"client_email": "test@example.com"}},
						}
						cfg := &config.Config{}
						if kind == "api-key" {
							auth.Attributes["api_key"] = "test-key"
							auth.Attributes["base_url"] = "https://relay.example.com"
							auth.Metadata = map[string]any{}
							cfg.VertexCompatAPIKey = []config.VertexCompatKey{{APIKey: "test-key", BaseURL: "https://relay.example.com", OpenAPI: &fallback}}
						}
						switch source {
						case "models", "model_aliases", "model-aliases":
							auth.Metadata[source] = []config.OAuthModelAlias{rule}
						case "vertex_model_openapi":
							data, errMarshal := json.Marshal([]config.OAuthModelAlias{rule})
							if errMarshal != nil {
								t.Fatal(errMarshal)
							}
							auth.Attributes[source] = string(data)
						case "oauth-attribute":
							cliproxyauth.SetOAuthModelAliasesAttribute(auth, []config.OAuthModelAlias{rule})
						case "global-alias":
							cfg.OAuthModelAlias = map[string][]config.OAuthModelAlias{"vertex": {rule}}
							cfg.SanitizeOAuthModelAlias()
						case "global-setting":
							cfg.OAuthSettings = map[string][]config.OAuthModelSetting{"vertex": {{Name: name, Alias: rule.Alias, OpenAPI: &enabled}}}
							cfg.SanitizeOAuthSettings()
						case "api-key-config":
							cfg.VertexCompatAPIKey[0].Models = []config.VertexCompatModel{{Name: name, Alias: rule.Alias, OpenAPI: &enabled}}
							// The config model takes precedence over per-auth model rules.
							auth.Metadata["models"] = []config.OAuthModelAlias{{Name: name, Alias: rule.Alias, OpenAPI: &fallback}}
						}
						opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: requested}}
						if got := ShouldExecuteVertexOpenAPIForRequest(cfg, auth, name+"(high)", opts); got != enabled {
							t.Fatalf("model setting = %v, want %v", got, enabled)
						}
						if withAlias {
							opts.Metadata[cliproxyexecutor.RequestedModelMetadataKey] = "sibling-alias"
							if got := ShouldExecuteVertexOpenAPIForRequest(cfg, auth, name, opts); got != fallback {
								t.Fatalf("setting leaked to sibling alias: %v, want %v", got, fallback)
							}
						}
						opts.Metadata[cliproxyexecutor.RequestedModelMetadataKey] = name
						if got := ShouldExecuteVertexOpenAPIForRequest(cfg, auth, name, opts); got != enabled {
							t.Fatalf("direct name setting = %v, want %v", got, enabled)
						}
					})
				}
			}
		}
	}
}

func TestShouldExecuteVertexOpenAPIForRequest_CredentialSettings(t *testing.T) {
	for _, source := range []string{"attribute", "metadata", "api-key-config"} {
		for _, raw := range []any{true, false, "true", "false", "1", "0"} {
			if source == "api-key-config" {
				if _, ok := raw.(bool); !ok {
					continue
				}
			}
			enabled, _ := parseVertexOpenAPIBool(raw)
			t.Run(source+"/"+strconv.FormatBool(enabled), func(t *testing.T) {
				auth := &cliproxyauth.Auth{Provider: "vertex", Attributes: map[string]string{}, Metadata: map[string]any{}}
				cfg := &config.Config{}
				switch source {
				case "attribute":
					if value, ok := raw.(string); ok {
						auth.Attributes["openapi"] = value
					} else {
						auth.Attributes["openapi"] = strconv.FormatBool(enabled)
					}
					auth.Metadata["openapi"] = !enabled
				case "metadata":
					auth.Metadata["openapi"] = raw
				case "api-key-config":
					auth.Attributes["api_key"] = "test-key"
					cfg.VertexCompatAPIKey = []config.VertexCompatKey{{APIKey: "test-key", OpenAPI: &enabled}}
				}
				for _, model := range []string{"gemini-2.5-pro", "partner/model"} {
					if got := ShouldExecuteVertexOpenAPIForRequest(cfg, auth, model, cliproxyexecutor.Options{}); got != enabled {
						t.Fatalf("%s: credential setting = %v, want %v", model, got, enabled)
					}
				}
				if ShouldExecuteVertexOpenAPIForRequest(cfg, auth, "partner/imagen-4(high)", cliproxyexecutor.Options{}) {
					t.Fatal("Imagen must not use OpenAPI")
				}
			})
		}
	}
}

func TestShouldExecuteVertexOpenAPIForRequest_RequestedModelAndAliasPriority(t *testing.T) {
	enabled, disabled := true, false
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"models": []config.OAuthModelAlias{
		{Name: "gemini-2.5-pro", OpenAPI: &disabled},
		{Name: "gemini-2.5-pro", Alias: "public-model", OpenAPI: &enabled},
	}}}
	for _, opts := range []cliproxyexecutor.Options{
		{OriginalRequest: []byte(`{"model":" public-model(high) "}`)},
		{Metadata: map[string]any{"model": " public-model "}},
		{OriginalRequest: []byte(`{"model":"other"}`), Metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: "public-model"}},
	} {
		if !ShouldExecuteVertexOpenAPIForRequest(nil, auth, "gemini-2.5-pro", opts) {
			t.Fatal("alias match must precede name match and use the original requested model")
		}
	}
	for _, model := range []string{"gemini-2.5-pro", "partner/model"} {
		if got := ShouldExecuteVertexOpenAPIForRequest(nil, nil, model, cliproxyexecutor.Options{}); got != IsVertexOpenAPIModel(model) {
			t.Fatalf("automatic selection for %s = %v", model, got)
		}
	}
}

func TestShouldExecuteVertexOpenAPIForRequest_PrefixExactSuffixAndOAuthIsolation(t *testing.T) {
	enabled, disabled := true, false

	// 1. Credential prefix is stripped from requestedModel before matching model alias rules
	prefixedAuth := &cliproxyauth.Auth{
		Provider: "vertex",
		Prefix:   "tenant",
		Metadata: map[string]any{
			"service_account": map[string]any{"client_email": "sa@example.com"},
			"models": []config.OAuthModelAlias{
				{Name: "gemini-2.5-pro", Alias: "public-model", OpenAPI: &enabled},
			},
		},
	}
	prefixedOpts := cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: "tenant/public-model"},
	}
	if !ShouldExecuteVertexOpenAPIForRequest(nil, prefixedAuth, "gemini-2.5-pro", prefixedOpts) {
		t.Fatal("expected prefixed request 'tenant/public-model' to match alias 'public-model' with openapi=true")
	}

	// 2. Exact suffixed alias takes precedence over an earlier unsuffixed base alias
	suffixAuth := &cliproxyauth.Auth{
		Provider: "vertex",
		Metadata: map[string]any{
			"service_account": map[string]any{"client_email": "sa@example.com"},
			"models": []config.OAuthModelAlias{
				{Name: "gemini-2.5-pro", Alias: "public-model", OpenAPI: &disabled},
				{Name: "gemini-2.5-pro(high)", Alias: "public-model(high)", OpenAPI: &enabled},
			},
		},
	}
	highOpts := cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: "public-model(high)"},
	}
	if !ShouldExecuteVertexOpenAPIForRequest(nil, suffixAuth, "gemini-2.5-pro(high)", highOpts) {
		t.Fatal("expected exact suffix alias 'public-model(high)' to take precedence over base alias 'public-model'")
	}
	lowOpts := cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: "public-model(low)"},
	}
	if ShouldExecuteVertexOpenAPIForRequest(nil, suffixAuth, "gemini-2.5-pro(low)", lowOpts) {
		t.Fatal("expected 'public-model(low)' to fall back to base alias 'public-model' (openapi=false), not match 'public-model(high)'")
	}

	// 3. Global OAuth model aliases and OAuth settings must not affect API-key Vertex credentials
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"vertex": {{Name: "gemini-2.5-pro", Alias: "gemini-oauth-openapi", OpenAPI: &enabled}},
		},
		OAuthSettings: map[string][]config.OAuthModelSetting{
			"vertex": {{Name: "gemini-2.5-pro", OpenAPI: &enabled}},
		},
	}
	apiKeyAuth := &cliproxyauth.Auth{
		Provider: "vertex",
		Attributes: map[string]string{
			"api_key":  "vertex-static-key",
			"base_url": "https://aiplatform.googleapis.com",
		},
	}
	if ShouldExecuteVertexOpenAPIForRequest(cfg, apiKeyAuth, "gemini-2.5-pro", cliproxyexecutor.Options{}) {
		t.Fatal("expected global OAuth settings/aliases not to route API-key Vertex credential through OpenAPI")
	}

	// 4. Resolved upstream model is not stripped when auth.Prefix matches publisher name
	publisherPrefixAuth := &cliproxyauth.Auth{
		Provider: "vertex",
		Prefix:   "deepseek-ai",
		Metadata: map[string]any{
			"service_account": map[string]any{"client_email": "sa@example.com"},
		},
	}
	publisherPrefixOpts := cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: "deepseek-ai/ds"},
	}
	if !ShouldExecuteVertexOpenAPIForRequest(nil, publisherPrefixAuth, "deepseek-ai/deepseek-v4.1-flash-maas", publisherPrefixOpts) {
		t.Fatal("expected resolved upstream model 'deepseek-ai/deepseek-v4.1-flash-maas' to keep publisher prefix even when auth.Prefix is 'deepseek-ai'")
	}

	// 5. model_aliases precedes models when both define the same alias with conflicting openapi flags
	precedenceAuth := &cliproxyauth.Auth{
		Provider: "vertex",
		Metadata: map[string]any{
			"service_account": map[string]any{"client_email": "sa@example.com"},
			"model_aliases": []config.OAuthModelAlias{
				{Name: "deepseek-ai/deepseek-v4.1-flash-maas", Alias: "shared-alias", OpenAPI: &enabled},
			},
			"models": []config.OAuthModelAlias{
				{Name: "gemini-2.5-pro", Alias: "shared-alias", OpenAPI: &disabled},
			},
		},
	}
	sharedAliasOpts := cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: "shared-alias"},
	}
	if !ShouldExecuteVertexOpenAPIForRequest(nil, precedenceAuth, "deepseek-ai/deepseek-v4.1-flash-maas", sharedAliasOpts) {
		t.Fatal("expected model_aliases entry to take precedence over models entry for duplicate alias")
	}

	// 6. Per-auth alias without explicit openapi shadows global alias with openapi=false
	shadowAuth := &cliproxyauth.Auth{
		Provider: "vertex",
		Metadata: map[string]any{
			"service_account": map[string]any{"client_email": "sa@example.com"},
		},
	}
	cliproxyauth.SetOAuthModelAliasesAttribute(shadowAuth, []config.OAuthModelAlias{
		{Name: "deepseek-ai/deepseek-v4.1-flash-maas", Alias: "shared-model"},
	})
	shadowCfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"vertex": {{Name: "gemini-2.5-pro", Alias: "shared-model", OpenAPI: &disabled}},
		},
	}
	shadowOpts := cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: "shared-model"},
	}
	if !ShouldExecuteVertexOpenAPIForRequest(shadowCfg, shadowAuth, "deepseek-ai/deepseek-v4.1-flash-maas", shadowOpts) {
		t.Fatal("expected per-auth alias without explicit openapi to shadow global alias and fall back to automatic OpenAPI detection")
	}
}
