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
	if !reflect.DeepEqual(got.Levels, []string{"low", "high"}) {
		t.Fatalf("levels mismatch: got %v", got.Levels)
	}
}

func TestResolveOpenAICompatibilityThinking_InheritsStaticModelSupport(t *testing.T) {
	upstream := registry.LookupStaticModelInfo("gpt-5.5")
	if upstream == nil || upstream.Thinking == nil {
		t.Fatal("expected gpt-5.5 static model definition with thinking support")
	}

	model := config.OpenAICompatibilityModel{
		Name:  "gpt-5.5",
		Alias: "team/gpt-5.5",
	}

	got := resolveOpenAICompatibilityThinking(model)
	if !reflect.DeepEqual(got, upstream.Thinking) {
		t.Fatalf("thinking mismatch: got %+v, want %+v", got, upstream.Thinking)
	}
}

func TestResolveOpenAICompatibilityThinking_KnownModelWithoutThinkingRemainsNil(t *testing.T) {
	upstream := registry.LookupStaticModelInfo("kimi-k2")
	if upstream == nil {
		t.Fatal("expected kimi-k2 static model definition")
	}
	if upstream.Thinking != nil {
		t.Fatal("expected kimi-k2 to have no static thinking support")
	}

	model := config.OpenAICompatibilityModel{
		Name:  "kimi-k2",
		Alias: "team/kimi-k2",
	}

	if got := resolveOpenAICompatibilityThinking(model); got != nil {
		t.Fatalf("expected nil thinking support, got %+v", got)
	}
}

func TestResolveOpenAICompatibilityThinking_UsesAliasWhenNameIsEmpty(t *testing.T) {
	upstream := registry.LookupStaticModelInfo("gpt-5.5")
	if upstream == nil || upstream.Thinking == nil {
		t.Fatal("expected gpt-5.5 static model definition with thinking support")
	}

	model := config.OpenAICompatibilityModel{
		Alias: "gpt-5.5",
	}

	got := resolveOpenAICompatibilityThinking(model)
	if !reflect.DeepEqual(got, upstream.Thinking) {
		t.Fatalf("thinking mismatch: got %+v, want %+v", got, upstream.Thinking)
	}
}

func TestResolveOpenAICompatibilityThinking_FallsBackToDefaultLevels(t *testing.T) {
	model := config.OpenAICompatibilityModel{
		Name:  "custom-openai-model",
		Alias: "team/custom-openai-model",
	}

	got := resolveOpenAICompatibilityThinking(model)
	want := []string{"low", "medium", "high"}
	if !reflect.DeepEqual(got.Levels, want) {
		t.Fatalf("levels mismatch: got %v, want %v", got.Levels, want)
	}
}
