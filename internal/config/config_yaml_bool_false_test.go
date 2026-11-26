package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSaveConfigPreserveComments_PointerBoolExplicitFalse(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("port: 8317\n"), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	falseVal := false
	cfg := &Config{
		Port: 8317,
		VertexCompatAPIKey: []VertexCompatKey{
			{
				APIKey:       "vertex-key-1",
				OpenAPI:      &falseVal,
				Interactions: &falseVal,
				Models: []VertexCompatModel{
					{
						Name:    "gemini-2.5-pro",
						Alias:   "g25",
						OpenAPI: &falseVal,
					},
				},
			},
		},
		OAuthModelAlias: map[string][]OAuthModelAlias{
			"vertex": {
				{
					Name:    "gemini-2.5-pro",
					Alias:   "g25-oauth",
					OpenAPI: &falseVal,
				},
			},
		},
		OAuthSettings: map[string][]OAuthModelSetting{
			"vertex": {
				{
					Name:    "gemini-2.5-pro",
					OpenAPI: &falseVal,
				},
			},
		},
		CodexKey: []CodexKey{
			{
				APIKey:               "codex-key-1",
				BaseURL:              "https://example.invalid",
				DisableCodexCloaking: &falseVal,
			},
		},
	}

	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}

	savedBytes, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}

	var doc yaml.Node
	if errUnmarshal := yaml.Unmarshal(savedBytes, &doc); errUnmarshal != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", errUnmarshal)
	}
	if len(doc.Content) == 0 || doc.Content[0] == nil {
		t.Fatalf("saved YAML produced empty document:\n%s", savedBytes)
	}
	root := doc.Content[0]

	// Default non-pointer codex.disable-codex-cloaking must not pollute the saved file.
	if yamlPath(root, "codex") != nil {
		t.Fatalf("saved YAML unexpectedly contains default codex section:\n%s", savedBytes)
	}

	loaded, errLoad := LoadConfigOptional(configPath, false)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	if len(loaded.VertexCompatAPIKey) != 1 {
		t.Fatalf("len(loaded.VertexCompatAPIKey) = %d, want 1", len(loaded.VertexCompatAPIKey))
	}
	if loaded.VertexCompatAPIKey[0].OpenAPI == nil || *loaded.VertexCompatAPIKey[0].OpenAPI {
		t.Fatalf("loaded.VertexCompatAPIKey[0].OpenAPI = %v, want explicit false\nsaved YAML:\n%s", loaded.VertexCompatAPIKey[0].OpenAPI, savedBytes)
	}
	if loaded.VertexCompatAPIKey[0].Interactions == nil || *loaded.VertexCompatAPIKey[0].Interactions {
		t.Fatalf("loaded.VertexCompatAPIKey[0].Interactions = %v, want explicit false\nsaved YAML:\n%s", loaded.VertexCompatAPIKey[0].Interactions, savedBytes)
	}
	if len(loaded.VertexCompatAPIKey[0].Models) != 1 || loaded.VertexCompatAPIKey[0].Models[0].OpenAPI == nil || *loaded.VertexCompatAPIKey[0].Models[0].OpenAPI {
		t.Fatalf("loaded.VertexCompatAPIKey[0].Models[0].OpenAPI = %+v, want explicit false\nsaved YAML:\n%s", loaded.VertexCompatAPIKey[0].Models, savedBytes)
	}
	if alias := loaded.OAuthModelAlias["vertex"]; len(alias) != 1 || alias[0].OpenAPI == nil || *alias[0].OpenAPI {
		t.Fatalf("loaded.OAuthModelAlias[vertex] = %+v, want explicit false OpenAPI\nsaved YAML:\n%s", alias, savedBytes)
	}
	if settings := loaded.OAuthSettings["vertex"]; len(settings) != 1 || settings[0].OpenAPI == nil || *settings[0].OpenAPI {
		t.Fatalf("loaded.OAuthSettings[vertex] = %+v, want explicit false OpenAPI\nsaved YAML:\n%s", settings, savedBytes)
	}
	if len(loaded.CodexKey) != 1 {
		t.Fatalf("len(loaded.CodexKey) = %d, want 1", len(loaded.CodexKey))
	}
	if loaded.CodexKey[0].DisableCodexCloaking == nil || *loaded.CodexKey[0].DisableCodexCloaking {
		t.Fatalf("loaded.CodexKey[0].DisableCodexCloaking = %v, want explicit false\nsaved YAML:\n%s", loaded.CodexKey[0].DisableCodexCloaking, savedBytes)
	}
}

func TestSaveConfigPreserveComments_PointerBoolExplicitFalse_ExistingEntriesAndReset(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initialYAML := "port: 8317\nvertex-api-key:\n  - api-key: vertex-key-1\ncodex-api-key:\n  - api-key: codex-key-1\n    base-url: https://example.invalid\n"
	if errWrite := os.WriteFile(configPath, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	falseVal := false
	cfg := &Config{
		Port: 8317,
		VertexCompatAPIKey: []VertexCompatKey{
			{
				APIKey:       "vertex-key-1",
				OpenAPI:      &falseVal,
				Interactions: &falseVal,
			},
		},
		CodexKey: []CodexKey{
			{
				APIKey:               "codex-key-1",
				BaseURL:              "https://example.invalid",
				DisableCodexCloaking: &falseVal,
			},
		},
	}

	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}

	loaded, errLoad := LoadConfigOptional(configPath, false)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	if loaded.VertexCompatAPIKey[0].OpenAPI == nil || *loaded.VertexCompatAPIKey[0].OpenAPI {
		t.Fatalf("existing vertex entry OpenAPI = %v, want explicit false", loaded.VertexCompatAPIKey[0].OpenAPI)
	}
	if loaded.VertexCompatAPIKey[0].Interactions == nil || *loaded.VertexCompatAPIKey[0].Interactions {
		t.Fatalf("existing vertex entry Interactions = %v, want explicit false", loaded.VertexCompatAPIKey[0].Interactions)
	}
	if loaded.CodexKey[0].DisableCodexCloaking == nil || *loaded.CodexKey[0].DisableCodexCloaking {
		t.Fatalf("existing codex entry DisableCodexCloaking = %v, want explicit false", loaded.CodexKey[0].DisableCodexCloaking)
	}

	// Clearing overrides back to nil must remove them on subsequent save.
	cfg.VertexCompatAPIKey[0].OpenAPI = nil
	cfg.VertexCompatAPIKey[0].Interactions = nil
	cfg.CodexKey[0].DisableCodexCloaking = nil
	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("second SaveConfigPreserveComments() error = %v", errSave)
	}

	reloaded, errReload := LoadConfigOptional(configPath, false)
	if errReload != nil {
		t.Fatalf("second LoadConfigOptional() error = %v", errReload)
	}
	if reloaded.VertexCompatAPIKey[0].OpenAPI != nil {
		t.Fatalf("reloaded OpenAPI = %v, want nil", reloaded.VertexCompatAPIKey[0].OpenAPI)
	}
	if reloaded.VertexCompatAPIKey[0].Interactions != nil {
		t.Fatalf("reloaded Interactions = %v, want nil", reloaded.VertexCompatAPIKey[0].Interactions)
	}
	if reloaded.CodexKey[0].DisableCodexCloaking != nil {
		t.Fatalf("reloaded DisableCodexCloaking = %v, want nil", reloaded.CodexKey[0].DisableCodexCloaking)
	}
}

func TestSaveConfigPreserveComments_PointerBoolExplicitFalse_V8Layout(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initialYAML := "config-version: 8\nserver:\n  port: 8317\n"
	if errWrite := os.WriteFile(configPath, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	falseVal := false
	cfg := &Config{
		Port: 8317,
		VertexCompatAPIKey: []VertexCompatKey{
			{
				APIKey:       "vertex-key-1",
				OpenAPI:      &falseVal,
				Interactions: &falseVal,
				Models: []VertexCompatModel{
					{
						Name:    "gemini-2.5-pro",
						Alias:   "g25",
						OpenAPI: &falseVal,
					},
				},
			},
		},
		CodexKey: []CodexKey{
			{
				APIKey:               "codex-key-1",
				BaseURL:              "https://example.invalid",
				DisableCodexCloaking: &falseVal,
			},
		},
	}

	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}

	savedBytes, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	if errValidate := ValidateV8Config(savedBytes); errValidate != nil {
		t.Fatalf("ValidateV8Config() error = %v\nsaved YAML:\n%s", errValidate, savedBytes)
	}

	var doc yaml.Node
	if errUnmarshal := yaml.Unmarshal(savedBytes, &doc); errUnmarshal != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", errUnmarshal)
	}
	if yamlPath(doc.Content[0], "upstream.codex.disable-codex-cloaking") != nil {
		t.Fatalf("v8 saved YAML unexpectedly contains default upstream.codex.disable-codex-cloaking:\n%s", savedBytes)
	}

	loaded, errLoad := LoadConfigOptional(configPath, false)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	if len(loaded.VertexCompatAPIKey) != 1 || loaded.VertexCompatAPIKey[0].OpenAPI == nil || *loaded.VertexCompatAPIKey[0].OpenAPI {
		t.Fatalf("v8 loaded VertexCompatAPIKey OpenAPI = %+v, want explicit false\nsaved YAML:\n%s", loaded.VertexCompatAPIKey, savedBytes)
	}
	if loaded.VertexCompatAPIKey[0].Interactions == nil || *loaded.VertexCompatAPIKey[0].Interactions {
		t.Fatalf("v8 loaded VertexCompatAPIKey Interactions = %v, want explicit false\nsaved YAML:\n%s", loaded.VertexCompatAPIKey[0].Interactions, savedBytes)
	}
	if len(loaded.VertexCompatAPIKey[0].Models) != 1 || loaded.VertexCompatAPIKey[0].Models[0].OpenAPI == nil || *loaded.VertexCompatAPIKey[0].Models[0].OpenAPI {
		t.Fatalf("v8 loaded VertexCompatAPIKey Model OpenAPI = %+v, want explicit false\nsaved YAML:\n%s", loaded.VertexCompatAPIKey[0].Models, savedBytes)
	}
	if len(loaded.CodexKey) != 1 || loaded.CodexKey[0].DisableCodexCloaking == nil || *loaded.CodexKey[0].DisableCodexCloaking {
		t.Fatalf("v8 loaded CodexKey DisableCodexCloaking = %+v, want explicit false\nsaved YAML:\n%s", loaded.CodexKey, savedBytes)
	}
}

func TestIsKnownDefaultValue_PointerBoolMappingSurvivalAndScope(t *testing.T) {
	makeFalseMap := func(key string) *yaml.Node {
		return &yaml.Node{
			Kind: yaml.MappingNode,
			Tag:  "!!map",
			Content: []*yaml.Node{
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
				{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"},
			},
		}
	}

	if isKnownDefaultValue([]string{"vertex-api-key"}, makeFalseMap("openapi")) {
		t.Fatalf("vertex-api-key mapping carrying only openapi: false should not be pruned")
	}
	if isKnownDefaultValue([]string{"vertex-api-key"}, makeFalseMap("interactions")) {
		t.Fatalf("vertex-api-key mapping carrying only interactions: false should not be pruned")
	}
	if isKnownDefaultValue([]string{"codex-api-key"}, makeFalseMap("disable-codex-cloaking")) {
		t.Fatalf("codex-api-key mapping carrying only disable-codex-cloaking: false should not be pruned")
	}
	if !isKnownDefaultValue([]string{"codex"}, makeFalseMap("disable-codex-cloaking")) {
		t.Fatalf("global codex mapping carrying only non-pointer disable-codex-cloaking: false should be pruned as default")
	}
	if !isKnownDefaultValue([]string{"codex", "disable-codex-cloaking"}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"}) {
		t.Fatalf("global codex.disable-codex-cloaking: false should be treated as known default")
	}
	if !isKnownDefaultValue([]string{"payload", "override", "params", "openapi"}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"}) {
		t.Fatalf("dynamic payload.override.params.openapi: false should not match pointer-backed openapi exemption")
	}
	if !isKnownDefaultValue([]string{"payload", "override", "params"}, makeFalseMap("openapi")) {
		t.Fatalf("dynamic payload.override.params mapping with only openapi: false should not match pointer-backed openapi exemption")
	}
}
