package diff

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestModelHashesIncludeIsCompat(t *testing.T) {
	if ComputeClaudeModelsHash([]config.ClaudeModel{{Name: "m"}}) == ComputeClaudeModelsHash([]config.ClaudeModel{{Name: "m", IsCompat: true}}) {
		t.Fatal("Claude model hash did not change when IsCompat changed")
	}
	if ComputeGeminiModelsHash([]config.GeminiModel{{Name: "m"}}) == ComputeGeminiModelsHash([]config.GeminiModel{{Name: "m", IsCompat: true}}) {
		t.Fatal("Gemini model hash did not change when IsCompat changed")
	}
	if ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "m"}}) == ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "m", IsCompat: true}}) {
		t.Fatal("OpenAI compatibility model hash did not change when IsCompat changed")
	}
}
