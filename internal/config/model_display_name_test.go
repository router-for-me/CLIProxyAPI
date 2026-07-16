package config

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestModelDisplayNameConfigDecoding(t *testing.T) {
	const yamlConfig = `codex-api-key:
  - models:
      - name: codex-upstream
        alias: codex-alias
        display-name: Codex Name
xai-api-key:
  - models:
      - name: xai-upstream
        alias: xai-alias
        display-name: xAI Name
claude-api-key:
  - models:
      - name: claude-upstream
        alias: claude-alias
        display-name: Claude Name
gemini-api-key:
  - models:
      - name: gemini-upstream
        alias: gemini-alias
        display-name: Gemini Name
vertex-api-key:
  - models:
      - name: vertex-upstream
        alias: vertex-alias
        display-name: Vertex Name
openai-compatibility:
  - models:
      - name: compat-upstream
        alias: compat-alias
        display-name: Compatibility Name
`
	const jsonConfig = `{"codex-api-key":[{"models":[{"name":"codex-upstream","alias":"codex-alias","display-name":"Codex Name"}]}],"xai-api-key":[{"models":[{"name":"xai-upstream","alias":"xai-alias","display-name":"xAI Name"}]}],"claude-api-key":[{"models":[{"name":"claude-upstream","alias":"claude-alias","display-name":"Claude Name"}]}],"gemini-api-key":[{"models":[{"name":"gemini-upstream","alias":"gemini-alias","display-name":"Gemini Name"}]}],"vertex-api-key":[{"models":[{"name":"vertex-upstream","alias":"vertex-alias","display-name":"Vertex Name"}]}],"openai-compatibility":[{"models":[{"name":"compat-upstream","alias":"compat-alias","display-name":"Compatibility Name"}]}]}`

	for _, tt := range []struct {
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
		t.Run(tt.name, func(t *testing.T) {
			var cfg Config
			if errDecode := tt.decode(&cfg); errDecode != nil {
				t.Fatalf("decode config: %v", errDecode)
			}
			if got := cfg.CodexKey[0].Models[0].DisplayName; got != "Codex Name" {
				t.Fatalf("Codex display name = %q", got)
			}
			if got := cfg.XAIKey[0].Models[0].DisplayName; got != "xAI Name" {
				t.Fatalf("xAI display name = %q", got)
			}
			if got := cfg.ClaudeKey[0].Models[0].DisplayName; got != "Claude Name" {
				t.Fatalf("Claude display name = %q", got)
			}
			if got := cfg.GeminiKey[0].Models[0].DisplayName; got != "Gemini Name" {
				t.Fatalf("Gemini display name = %q", got)
			}
			if got := cfg.VertexCompatAPIKey[0].Models[0].DisplayName; got != "Vertex Name" {
				t.Fatalf("Vertex display name = %q", got)
			}
			if got := cfg.OpenAICompatibility[0].Models[0].DisplayName; got != "Compatibility Name" {
				t.Fatalf("OpenAI compatibility display name = %q", got)
			}
		})
	}
}
