package config

import (
	"fmt"
	"testing"

	"github.com/MoeForge/moego/json"
	"gopkg.in/yaml.v3"
)

func TestMaxCompletionTokensConfigRoundTrip(t *testing.T) {
	type configuredModel interface {
		GetMaxCompletionTokens() int
	}
	for _, newModel := range []func() configuredModel{
		func() configuredModel { return &ClaudeModel{} },
		func() configuredModel { return &CodexModel{} },
		func() configuredModel { return &GeminiModel{} },
		func() configuredModel { return &OpenAICompatibilityModel{} },
		func() configuredModel { return &OAuthModelSetting{} },
	} {
		for _, format := range []struct {
			name      string
			field     string
			empty     string
			marshal   func(any) ([]byte, error)
			unmarshal func([]byte, any) error
		}{
			{"YAML", "max-completion-tokens: %d", "{}", yaml.Marshal, yaml.Unmarshal},
			{"JSON", `{"max-completion-tokens":%d}`, "{}", json.Marshal, json.Unmarshal},
		} {
			for _, value := range []struct {
				name string
				want int
			}{
				{"positive", 32768}, {"zero", 0}, {"negative", -1}, {"omitted", 0},
			} {
				t.Run(fmt.Sprintf("%T/%s/%s", newModel(), format.name, value.name), func(t *testing.T) {
					input := format.empty
					if value.name != "omitted" {
						input = fmt.Sprintf(format.field, value.want)
					}
					model := newModel()
					if err := format.unmarshal([]byte(input), model); err != nil {
						t.Fatal(err)
					}
					data, err := format.marshal(model)
					if err != nil {
						t.Fatal(err)
					}
					roundTrip := newModel()
					if err := format.unmarshal(data, roundTrip); err != nil {
						t.Fatal(err)
					}
					if model.GetMaxCompletionTokens() != value.want || roundTrip.GetMaxCompletionTokens() != value.want {
						t.Fatalf("decoded limit = %d, round-trip limit = %d, want %d", model.GetMaxCompletionTokens(), roundTrip.GetMaxCompletionTokens(), value.want)
					}
				})
			}
		}
	}
}

func TestSanitizeOAuthSettingsCompletionLimit(t *testing.T) {
	cfg := &Config{OAuthSettings: map[string][]OAuthModelSetting{
		" Codex ": {{Name: " model ", MaxCompletionTokens: 4096}, {Name: "other", MaxCompletionTokens: -1}, {Name: "zero", MaxCompletionTokens: 0}, {Name: "omitted"}},
	}}
	cfg.SanitizeOAuthSettings()
	settings := cfg.OAuthSettings["codex"]
	if len(settings) != 4 || settings[0].Name != "model" || settings[0].MaxCompletionTokens != 4096 || settings[1].MaxCompletionTokens != 0 || settings[2].MaxCompletionTokens != 0 || settings[3].MaxCompletionTokens != 0 {
		t.Fatalf("sanitized settings = %+v", settings)
	}
}
