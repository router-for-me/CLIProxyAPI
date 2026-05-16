package cliproxy

import (
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestResolveOpenAICompatibilityThinking_UsesExplicitConfig(t *testing.T) {
	model := config.OpenAICompatibilityModel{
		Name:     "gpt-5.5",
		Alias:    "team/gpt-5.5",
		Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high"}},
	}

	got := resolveOpenAICompatibilityThinking(model)
	if got == nil {
		t.Fatal("expected thinking support")
	}
	if !reflect.DeepEqual(got.Levels, []string{"low", "high"}) {
		t.Fatalf("levels mismatch: got %v", got.Levels)
	}
}

func TestResolveOpenAICompatibilityThinking_InheritsStaticModelSupport(t *testing.T) {
	model := config.OpenAICompatibilityModel{
		Name:  "gpt-5.5",
		Alias: "team/gpt-5.5",
	}

	got := resolveOpenAICompatibilityThinking(model)
	if got == nil {
		t.Fatal("expected thinking support")
	}
	want := []string{"low", "medium", "high", "xhigh"}
	if !reflect.DeepEqual(got.Levels, want) {
		t.Fatalf("levels mismatch: got %v, want %v", got.Levels, want)
	}
}

func TestResolveOpenAICompatibilityThinking_FallsBackToDefaultLevels(t *testing.T) {
	model := config.OpenAICompatibilityModel{
		Name:  "custom-openai-model",
		Alias: "team/custom-openai-model",
	}

	got := resolveOpenAICompatibilityThinking(model)
	if got == nil {
		t.Fatal("expected thinking support")
	}
	want := []string{"low", "medium", "high"}
	if !reflect.DeepEqual(got.Levels, want) {
		t.Fatalf("levels mismatch: got %v, want %v", got.Levels, want)
	}
}
