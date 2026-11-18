package config_test

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

func TestVertexOpenAPIConfigDecoding(t *testing.T) {
	for _, layout := range []struct{ name, prefix, indent string }{
		{name: "v8", prefix: "api-keys:\n  vertex:\n    - keys:\n", indent: "        "},
		{name: "legacy", prefix: "vertex-api-key:\n", indent: "  "},
	} {
		t.Run(layout.name, func(t *testing.T) {
			keyConfig := `- api-key: test-key
  openapi: true
  openapi-location: " global "
  models:
    - name: partner/model
      alias: native-model
      openapi: false
    - name: gemini-2.5-pro
      alias: openapi-model
      openapi: true
    - name: automatic-model`
			raw := layout.prefix + layout.indent + strings.ReplaceAll(keyConfig, "\n", "\n"+layout.indent) + `
oauth:
  model-alias:
    vertex:
      - name: gemini-2.5-pro
        alias: public-model
        openapi: true
  settings:
    vertex:
      - name: partner/model
        openapi: false
`
			var cfg config.Config
			if errUnmarshal := yaml.Unmarshal([]byte(raw), &cfg); errUnmarshal != nil {
				t.Fatal(errUnmarshal)
			}
			cfg.SanitizeVertexCompatKeys()
			cfg.SanitizeOAuthModelAlias()
			cfg.SanitizeOAuthSettings()
			if len(cfg.VertexCompatAPIKey) != 1 {
				t.Fatalf("Vertex key count = %d", len(cfg.VertexCompatAPIKey))
			}
			entry := cfg.VertexCompatAPIKey[0]
			if entry.OpenAPI == nil || !*entry.OpenAPI || entry.OpenAPILocation != "global" {
				t.Fatalf("credential settings = %+v", entry)
			}
			if len(entry.Models) != 3 || entry.Models[0].OpenAPI == nil || *entry.Models[0].OpenAPI || entry.Models[1].OpenAPI == nil || !*entry.Models[1].OpenAPI || entry.Models[2].OpenAPI != nil {
				t.Fatalf("model settings = %+v", entry.Models)
			}
			if setting := cfg.OAuthModelAlias["vertex"][0].OpenAPI; setting == nil || !*setting {
				t.Fatalf("OAuth alias setting = %v", setting)
			}
			if setting := cfg.OAuthSettings["vertex"][0].OpenAPI; setting == nil || *setting {
				t.Fatalf("OAuth model setting = %v", setting)
			}
		})
	}
}

func TestSanitizeOAuthOpenAPISettingsClonesOptionalBools(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		original := enabled
		cfg := &config.Config{
			OAuthModelAlias: map[string][]config.OAuthModelAlias{" VERTEX ": {{Name: "model", Alias: "alias", OpenAPI: &original}}},
			OAuthSettings:   map[string][]config.OAuthModelSetting{" VERTEX ": {{Name: "model", OpenAPI: &original}}},
		}
		cfg.SanitizeOAuthModelAlias()
		cfg.SanitizeOAuthSettings()
		original = !enabled
		for _, setting := range []*bool{cfg.OAuthModelAlias["vertex"][0].OpenAPI, cfg.OAuthSettings["vertex"][0].OpenAPI} {
			if setting == nil || *setting != enabled || setting == &original {
				t.Fatalf("normalized setting shares or loses the original value: %v", setting)
			}
		}
	}
}
