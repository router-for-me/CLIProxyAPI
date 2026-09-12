package config

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCodexWebSearchConfigDecoding(t *testing.T) {
	const yamlConfig = `codex-api-key:
  - models:
      - name: codex-upstream
        alias: codex-alias
        codex-web-search: true
xai-api-key:
  - models:
      - name: xai-upstream
        alias: xai-alias
        codex-web-search: false
claude-api-key:
  - models:
      - name: claude-upstream
        alias: claude-alias
        codex-web-search: true
gemini-api-key:
  - models:
      - name: gemini-upstream
        alias: gemini-alias
        codex-web-search: false
interactions-api-key:
  - models:
      - name: interactions-upstream
        alias: interactions-alias
        codex-web-search: true
vertex-api-key:
  - models:
      - name: vertex-upstream
        alias: vertex-alias
        codex-web-search: false
openai-compatibility:
  - models:
      - name: compat-upstream
        alias: compat-alias
        codex-web-search: true
`
	const jsonConfig = `{"codex-api-key":[{"models":[{"name":"codex-upstream","alias":"codex-alias","codex-web-search":true}]}],"xai-api-key":[{"models":[{"name":"xai-upstream","alias":"xai-alias","codex-web-search":false}]}],"claude-api-key":[{"models":[{"name":"claude-upstream","alias":"claude-alias","codex-web-search":true}]}],"gemini-api-key":[{"models":[{"name":"gemini-upstream","alias":"gemini-alias","codex-web-search":false}]}],"interactions-api-key":[{"models":[{"name":"interactions-upstream","alias":"interactions-alias","codex-web-search":true}]}],"vertex-api-key":[{"models":[{"name":"vertex-upstream","alias":"vertex-alias","codex-web-search":false}]}],"openai-compatibility":[{"models":[{"name":"compat-upstream","alias":"compat-alias","codex-web-search":true}]}]}`

	for _, testCase := range []struct {
		name   string
		decode func(*Config) error
	}{
		{
			name: "YAML",
			decode: func(cfg *Config) error {
				return yaml.Unmarshal([]byte(yamlConfig), cfg)
			},
		},
		{
			name: "JSON",
			decode: func(cfg *Config) error {
				return json.Unmarshal([]byte(jsonConfig), cfg)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var cfg Config
			if errDecode := testCase.decode(&cfg); errDecode != nil {
				t.Fatalf("decode config: %v", errDecode)
			}

			assertBoolPointer(t, "codex", cfg.CodexKey[0].Models[0].CodexWebSearch, true)
			assertBoolPointer(t, "xai", cfg.XAIKey[0].Models[0].CodexWebSearch, false)
			assertBoolPointer(t, "claude", cfg.ClaudeKey[0].Models[0].CodexWebSearch, true)
			assertBoolPointer(t, "gemini", cfg.GeminiKey[0].Models[0].CodexWebSearch, false)
			assertBoolPointer(t, "interactions", cfg.InteractionsKey[0].Models[0].CodexWebSearch, true)
			assertBoolPointer(t, "vertex", cfg.VertexCompatAPIKey[0].Models[0].CodexWebSearch, false)
			assertBoolPointer(t, "openai-compatibility", cfg.OpenAICompatibility[0].Models[0].CodexWebSearch, true)
		})
	}
}

func assertBoolPointer(t *testing.T, name string, got *bool, want bool) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s codex-web-search = nil, want %v", name, want)
	}
	if *got != want {
		t.Fatalf("%s codex-web-search = %v, want %v", name, *got, want)
	}
}
