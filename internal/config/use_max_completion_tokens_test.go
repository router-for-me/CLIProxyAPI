package config

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAICompatibilityUseMaxCompletionTokensYAMLDecoding(t *testing.T) {
	const yamlConfig = `
openai-compatibility:
  - name: test-provider
    models:
      - name: new-reasoning-model
        alias: new-alias
        use-max-completion-tokens: true
      - name: legacy-model
        alias: legacy-alias
`

	var cfg Config
	if errDecode := yaml.Unmarshal([]byte(yamlConfig), &cfg); errDecode != nil {
		t.Fatalf("yaml decode error: %v", errDecode)
	}

	if len(cfg.OpenAICompatibility) != 1 {
		t.Fatalf("OpenAICompatibility len = %d, want 1", len(cfg.OpenAICompatibility))
	}
	models := cfg.OpenAICompatibility[0].Models
	if len(models) != 2 {
		t.Fatalf("models len = %d, want 2", len(models))
	}

	if !models[0].UseMaxCompletionTokens {
		t.Errorf("models[0].UseMaxCompletionTokens = false, want true")
	}
	if !models[0].GetUseMaxCompletionTokens() {
		t.Errorf("models[0].GetUseMaxCompletionTokens() = false, want true")
	}

	if models[1].UseMaxCompletionTokens {
		t.Errorf("models[1].UseMaxCompletionTokens = true, want default false")
	}
	if models[1].GetUseMaxCompletionTokens() {
		t.Errorf("models[1].GetUseMaxCompletionTokens() = true, want default false")
	}
}

func TestOpenAICompatibilityUseMaxCompletionTokensJSONDecoding(t *testing.T) {
	const jsonConfig = `{
		"openai-compatibility": [
			{
				"name": "test-provider",
				"models": [
					{
						"name": "model-true",
						"alias": "alias-true",
						"use-max-completion-tokens": true
					},
					{
						"name": "model-false",
						"alias": "alias-false",
						"use-max-completion-tokens": false
					},
					{
						"name": "model-omitted",
						"alias": "alias-omitted"
					}
				]
			}
		]
	}`

	var cfg Config
	if errDecode := json.Unmarshal([]byte(jsonConfig), &cfg); errDecode != nil {
		t.Fatalf("json decode error: %v", errDecode)
	}

	models := cfg.OpenAICompatibility[0].Models
	if !models[0].GetUseMaxCompletionTokens() {
		t.Errorf("models[0].GetUseMaxCompletionTokens() = false, want true")
	}
	if models[1].GetUseMaxCompletionTokens() {
		t.Errorf("models[1].GetUseMaxCompletionTokens() = true, want false")
	}
	if models[2].GetUseMaxCompletionTokens() {
		t.Errorf("models[2].GetUseMaxCompletionTokens() = true, want default false")
	}
}
