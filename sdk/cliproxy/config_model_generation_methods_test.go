package cliproxy

import (
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// TestBuildGeminiConfigModelsKeepsNativeGenerationMethods guards the native capability
// metadata when an API-key config supplies an explicit models list: configured entries
// otherwise lose SupportedGenerationMethods and the catalog advertises them as
// generateContent only.
func TestBuildGeminiConfigModelsKeepsNativeGenerationMethods(t *testing.T) {
	models := buildGeminiConfigModels(&config.GeminiKey{Models: []config.GeminiModel{
		{Name: "gemini-embedding-001"},
		{Name: "gemini-embedding-2"},
		{Name: "gemini-decision-1"},
	}})
	if len(models) != 3 {
		t.Fatalf("got %d configured models, want 3", len(models))
	}
	want := map[string][]string{
		"gemini-embedding-001": {"embedContent", "batchEmbedContents"},
		"gemini-embedding-2":   {"embedContent", "batchEmbedContents"},
		"gemini-decision-1":    {"decisions"},
	}
	for _, info := range models {
		expected, ok := want[info.ID]
		if !ok {
			continue
		}
		if !reflect.DeepEqual(info.SupportedGenerationMethods, expected) {
			t.Errorf("%s supportedGenerationMethods = %v, want %v", info.ID, info.SupportedGenerationMethods, expected)
		}
	}
}

func TestBuildGeminiConfigModelsKeepsChatMethodsUnchanged(t *testing.T) {
	models := buildGeminiConfigModels(&config.GeminiKey{Models: []config.GeminiModel{
		{Name: "gemini-2.5-pro"},
		{Name: "gemini-2.5-pro", Alias: "gemini-custom-alias"},
	}})
	for _, info := range models {
		if len(info.SupportedGenerationMethods) != 0 {
			t.Errorf("%s supportedGenerationMethods = %v, want none so chat entries keep the default surface",
				info.ID, info.SupportedGenerationMethods)
		}
	}
}

func TestNativeModelGenerationMethods(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   []string
		want []string
	}{
		{name: "embedding", in: []string{"embedContent", "batchEmbedContents"}, want: []string{"embedContent", "batchEmbedContents"}},
		{name: "decisions", in: []string{"decisions"}, want: []string{"decisions"}},
		{name: "chat only", in: []string{"generateContent", "countTokens"}},
		{name: "mixed", in: []string{"generateContent", "embedContent"}, want: []string{"embedContent"}},
		{name: "empty"},
		{name: "padded", in: []string{"  decisions  "}, want: []string{"decisions"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := registry.NativeModelGenerationMethods(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("registry.NativeModelGenerationMethods(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
