package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestBuildOpenAICompatibilityConfigModels_DeepSeekV4SupportsMaxThinking(t *testing.T) {
	models := buildOpenAICompatibilityConfigModels(&config.OpenAICompatibility{
		Name: "deepseek",
		Models: []config.OpenAICompatibilityModel{
			{Name: "deepseek-v4-pro", Alias: "deepseek-v4-pro"},
			{Name: "deepseek-v4-flash", Alias: "custom-deepseek-flash"},
			{Name: "other-model", Alias: "other-model"},
		},
	})

	if len(models) != 3 {
		t.Fatalf("len(models) = %d, want 3", len(models))
	}

	for _, model := range models[:2] {
		if model.Thinking == nil {
			t.Fatalf("%s thinking = nil", model.ID)
		}
		if !hasString(model.Thinking.Levels, "max") {
			t.Fatalf("%s levels = %v, want max", model.ID, model.Thinking.Levels)
		}
	}
	if hasString(models[2].Thinking.Levels, "max") {
		t.Fatalf("non-DeepSeek levels = %v, did not expect max", models[2].Thinking.Levels)
	}
}

func hasString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
