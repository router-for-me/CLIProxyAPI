package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestBuildOpenAICompatibilityConfigModelsIncludesContextLength(t *testing.T) {
	models := buildOpenAICompatibilityConfigModels(&config.OpenAICompatibility{
		Name: "heybox-deepseek",
		Models: []config.OpenAICompatibilityModel{{
			Name:          "deepseek-v4-pro",
			ContextLength: 1000000,
		}},
	})

	if len(models) != 1 {
		t.Fatalf("models len = %d, want 1", len(models))
	}
	if got := models[0].ContextLength; got != 1000000 {
		t.Fatalf("ContextLength = %d, want 1000000", got)
	}
}
