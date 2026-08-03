package config

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMaxContextLengthConfigDecoding(t *testing.T) {
	const want = 1048576
	const yamlConfig = `codex-api-key:
  - models:
      - name: codex-upstream
        alias: codex-alias
        max-context-length: 1048576
claude-api-key:
  - models:
      - name: claude-upstream
        alias: claude-alias
        max-context-length: 1048576
gemini-api-key:
  - models:
      - name: gemini-upstream
        alias: gemini-alias
        max-context-length: 1048576
interactions-api-key:
  - models:
      - name: interactions-upstream
        alias: interactions-alias
        max-context-length: 1048576
xai-api-key:
  - models:
      - name: xai-upstream
        alias: xai-alias
        max-context-length: 1048576
openai-compatibility:
  - models:
      - name: compat-upstream
        alias: compat-alias
        max-context-length: 1048576
`
	const jsonConfig = `{"codex-api-key":[{"models":[{"name":"codex-upstream","alias":"codex-alias","max-context-length":1048576}]}],"claude-api-key":[{"models":[{"name":"claude-upstream","alias":"claude-alias","max-context-length":1048576}]}],"gemini-api-key":[{"models":[{"name":"gemini-upstream","alias":"gemini-alias","max-context-length":1048576}]}],"interactions-api-key":[{"models":[{"name":"interactions-upstream","alias":"interactions-alias","max-context-length":1048576}]}],"xai-api-key":[{"models":[{"name":"xai-upstream","alias":"xai-alias","max-context-length":1048576}]}],"openai-compatibility":[{"models":[{"name":"compat-upstream","alias":"compat-alias","max-context-length":1048576}]}]}`

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

			models := []struct {
				name string
				got  int
			}{
				{name: "codex", got: cfg.CodexKey[0].Models[0].MaxContextLength},
				{name: "claude", got: cfg.ClaudeKey[0].Models[0].MaxContextLength},
				{name: "gemini", got: cfg.GeminiKey[0].Models[0].MaxContextLength},
				{name: "interactions", got: cfg.InteractionsKey[0].Models[0].MaxContextLength},
				{name: "xai", got: cfg.XAIKey[0].Models[0].MaxContextLength},
				{name: "openai compatibility", got: cfg.OpenAICompatibility[0].Models[0].MaxContextLength},
			}
			for _, model := range models {
				if model.got != want {
					t.Errorf("%s max-context-length = %d, want %d", model.name, model.got, want)
				}
			}
		})
	}
}
