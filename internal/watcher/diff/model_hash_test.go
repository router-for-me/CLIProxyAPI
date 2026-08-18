package diff

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestComputeOpenAICompatModelsHash_Deterministic(t *testing.T) {
	models := []config.OpenAICompatibilityModel{
		{Name: "gpt-4", Alias: "gpt4"},
		{Name: "gpt-3.5-turbo"},
	}
	hash1 := ComputeOpenAICompatModelsHash(models)
	hash2 := ComputeOpenAICompatModelsHash(models)
	if hash1 == "" {
		t.Fatal("hash should not be empty")
	}
	if hash1 != hash2 {
		t.Fatalf("hash should be deterministic, got %s vs %s", hash1, hash2)
	}
	changed := ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "gpt-4"}, {Name: "gpt-4.1"}})
	if hash1 == changed {
		t.Fatal("hash should change when model list changes")
	}
}

func TestComputeOpenAICompatModelsHash_IncludesImageFlag(t *testing.T) {
	textModel := ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "gpt-image", Alias: "image"}})
	imageModel := ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "gpt-image", Alias: "image", Image: true}})
	if textModel == "" || imageModel == "" {
		t.Fatal("hashes should not be empty")
	}
	if textModel == imageModel {
		t.Fatal("hash should change when image flag changes")
	}
}

func TestComputeOpenAICompatModelsHashIncludesModalities(t *testing.T) {
	base := []config.OpenAICompatibilityModel{{Name: "model", InputModalities: []string{"text"}, OutputModalities: []string{"text"}}}
	inputChanged := []config.OpenAICompatibilityModel{{Name: "model", InputModalities: []string{"text", "image"}, OutputModalities: []string{"text"}}}
	outputChanged := []config.OpenAICompatibilityModel{{Name: "model", InputModalities: []string{"text"}, OutputModalities: []string{"text", "image"}}}
	baseHash := ComputeOpenAICompatModelsHash(base)
	if baseHash == ComputeOpenAICompatModelsHash(inputChanged) {
		t.Fatal("input modalities did not change model hash")
	}
	if baseHash == ComputeOpenAICompatModelsHash(outputChanged) {
		t.Fatal("output modalities did not change model hash")
	}
}

func TestComputeOpenAICompatModelsHashPreservesRoutingOrderAndDuplicates(t *testing.T) {
	a := []config.OpenAICompatibilityModel{
		{Name: "gpt-4", Alias: "gpt4"},
		{Name: " "},
		{Name: "GPT-4", Alias: "GPT4"},
		{Alias: "a1"},
	}
	b := []config.OpenAICompatibilityModel{
		{Alias: "A1"},
		{Name: "gpt-4", Alias: "gpt4"},
	}
	h1 := ComputeOpenAICompatModelsHash(a)
	h2 := ComputeOpenAICompatModelsHash(b)
	if h1 == "" || h2 == "" {
		t.Fatal("expected non-empty hashes for non-empty model sets")
	}
	if h1 == h2 {
		t.Fatalf("expected routing order and duplicates to change hashes, got %s", h1)
	}
}

func TestComputeVertexCompatModelsHash_DifferentInputs(t *testing.T) {
	models := []config.VertexCompatModel{{Name: "gemini-pro", Alias: "pro"}}
	hash1 := ComputeVertexCompatModelsHash(models)
	hash2 := ComputeVertexCompatModelsHash([]config.VertexCompatModel{{Name: "gemini-1.5-pro", Alias: "pro"}})
	if hash1 == "" || hash2 == "" {
		t.Fatal("hashes should not be empty for non-empty models")
	}
	if hash1 == hash2 {
		t.Fatal("hash should differ when model content differs")
	}
}

func TestComputeVertexCompatModelsHashPreservesDuplicates(t *testing.T) {
	a := []config.VertexCompatModel{
		{Name: "m1", Alias: "a1"},
		{Name: " "},
		{Name: "M1", Alias: "A1"},
	}
	b := []config.VertexCompatModel{
		{Name: "m1", Alias: "a1"},
	}
	if h1, h2 := ComputeVertexCompatModelsHash(a), ComputeVertexCompatModelsHash(b); h1 == "" || h1 == h2 {
		t.Fatalf("expected duplicate routing entries to change hash, got %q / %q", h1, h2)
	}
}

func TestComputeClaudeModelsHash_Empty(t *testing.T) {
	if got := ComputeClaudeModelsHash(nil); got != "" {
		t.Fatalf("expected empty hash for nil models, got %q", got)
	}
	if got := ComputeClaudeModelsHash([]config.ClaudeModel{}); got != "" {
		t.Fatalf("expected empty hash for empty slice, got %q", got)
	}
}

func TestComputeCodexModelsHash_Empty(t *testing.T) {
	if got := ComputeCodexModelsHash(nil); got != "" {
		t.Fatalf("expected empty hash for nil models, got %q", got)
	}
	if got := ComputeCodexModelsHash([]config.CodexModel{}); got != "" {
		t.Fatalf("expected empty hash for empty slice, got %q", got)
	}
}

func TestComputeClaudeModelsHashPreservesDuplicates(t *testing.T) {
	a := []config.ClaudeModel{
		{Name: "m1", Alias: "a1"},
		{Name: " "},
		{Name: "M1", Alias: "A1"},
	}
	b := []config.ClaudeModel{
		{Name: "m1", Alias: "a1"},
	}
	if h1, h2 := ComputeClaudeModelsHash(a), ComputeClaudeModelsHash(b); h1 == "" || h1 == h2 {
		t.Fatalf("expected duplicate routing entries to change hash, got %q / %q", h1, h2)
	}
}

func TestComputeCodexModelsHashPreservesDuplicates(t *testing.T) {
	a := []config.CodexModel{
		{Name: "m1", Alias: "a1"},
		{Name: " "},
		{Name: "M1", Alias: "A1"},
	}
	b := []config.CodexModel{
		{Name: "m1", Alias: "a1"},
	}
	if h1, h2 := ComputeCodexModelsHash(a), ComputeCodexModelsHash(b); h1 == "" || h1 == h2 {
		t.Fatalf("expected duplicate routing entries to change hash, got %q / %q", h1, h2)
	}
}

func TestComputeModelHashesIncludeDisplayName(t *testing.T) {
	tests := []struct {
		name    string
		base    string
		changed string
	}{
		{
			name:    "openai compatibility",
			base:    ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "m", Alias: "a", DisplayName: "One"}}),
			changed: ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "m", Alias: "a", DisplayName: "Two"}}),
		},
		{
			name:    "vertex",
			base:    ComputeVertexCompatModelsHash([]config.VertexCompatModel{{Name: "m", Alias: "a", DisplayName: "One"}}),
			changed: ComputeVertexCompatModelsHash([]config.VertexCompatModel{{Name: "m", Alias: "a", DisplayName: "Two"}}),
		},
		{
			name:    "claude",
			base:    ComputeClaudeModelsHash([]config.ClaudeModel{{Name: "m", Alias: "a", DisplayName: "One"}}),
			changed: ComputeClaudeModelsHash([]config.ClaudeModel{{Name: "m", Alias: "a", DisplayName: "Two"}}),
		},
		{
			name:    "codex",
			base:    ComputeCodexModelsHash([]config.CodexModel{{Name: "m", Alias: "a", DisplayName: "One"}}),
			changed: ComputeCodexModelsHash([]config.CodexModel{{Name: "m", Alias: "a", DisplayName: "Two"}}),
		},
		{
			name:    "gemini",
			base:    ComputeGeminiModelsHash([]config.GeminiModel{{Name: "m", Alias: "a", DisplayName: "One"}}),
			changed: ComputeGeminiModelsHash([]config.GeminiModel{{Name: "m", Alias: "a", DisplayName: "Two"}}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.base == "" || tt.base == tt.changed {
				t.Fatalf("display name must change model hash: %q / %q", tt.base, tt.changed)
			}
		})
	}
}

func TestComputeCodexModelsHashIncludesForceMapping(t *testing.T) {
	withoutForceMapping := ComputeCodexModelsHash([]config.CodexModel{{Name: "m", Alias: "a"}})
	withForceMapping := ComputeCodexModelsHash([]config.CodexModel{{Name: "m", Alias: "a", ForceMapping: true}})
	if withoutForceMapping == "" || withoutForceMapping == withForceMapping {
		t.Fatalf("force-mapping must change model hash: %q / %q", withoutForceMapping, withForceMapping)
	}
}

func TestComputeOtherModelHashesIncludeForceMapping(t *testing.T) {
	if ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "m"}}) == ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "m", ForceMapping: true}}) {
		t.Fatal("OpenAI compatibility force-mapping did not change model hash")
	}
	if ComputeVertexCompatModelsHash([]config.VertexCompatModel{{Name: "m"}}) == ComputeVertexCompatModelsHash([]config.VertexCompatModel{{Name: "m", ForceMapping: true}}) {
		t.Fatal("Vertex force-mapping did not change model hash")
	}
	if ComputeClaudeModelsHash([]config.ClaudeModel{{Name: "m"}}) == ComputeClaudeModelsHash([]config.ClaudeModel{{Name: "m", ForceMapping: true}}) {
		t.Fatal("Claude force-mapping did not change model hash")
	}
	if ComputeGeminiModelsHash([]config.GeminiModel{{Name: "m"}}) == ComputeGeminiModelsHash([]config.GeminiModel{{Name: "m", ForceMapping: true}}) {
		t.Fatal("Gemini force-mapping did not change model hash")
	}
}

func TestComputeExcludedModelsHash_Normalizes(t *testing.T) {
	hash1 := ComputeExcludedModelsHash([]string{" A ", "b", "a"})
	hash2 := ComputeExcludedModelsHash([]string{"a", " b", "A"})
	if hash1 == "" || hash2 == "" {
		t.Fatal("hash should not be empty for non-empty input")
	}
	if hash1 != hash2 {
		t.Fatalf("hash should be order/space insensitive for same multiset, got %s vs %s", hash1, hash2)
	}
	hash3 := ComputeExcludedModelsHash([]string{"c"})
	if hash1 == hash3 {
		t.Fatal("hash should differ for different normalized sets")
	}
}

func TestComputeOpenAICompatModelsHash_Empty(t *testing.T) {
	if got := ComputeOpenAICompatModelsHash(nil); got != "" {
		t.Fatalf("expected empty hash for nil input, got %q", got)
	}
	if got := ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{}); got != "" {
		t.Fatalf("expected empty hash for empty slice, got %q", got)
	}
	if got := ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: " "}, {Alias: ""}}); got != "" {
		t.Fatalf("expected empty hash for blank models, got %q", got)
	}
}

func TestComputeVertexCompatModelsHash_Empty(t *testing.T) {
	if got := ComputeVertexCompatModelsHash(nil); got != "" {
		t.Fatalf("expected empty hash for nil input, got %q", got)
	}
	if got := ComputeVertexCompatModelsHash([]config.VertexCompatModel{}); got != "" {
		t.Fatalf("expected empty hash for empty slice, got %q", got)
	}
	if got := ComputeVertexCompatModelsHash([]config.VertexCompatModel{{Name: " "}}); got != "" {
		t.Fatalf("expected empty hash for blank models, got %q", got)
	}
}

func TestComputeExcludedModelsHash_Empty(t *testing.T) {
	if got := ComputeExcludedModelsHash(nil); got != "" {
		t.Fatalf("expected empty hash for nil input, got %q", got)
	}
	if got := ComputeExcludedModelsHash([]string{}); got != "" {
		t.Fatalf("expected empty hash for empty slice, got %q", got)
	}
	if got := ComputeExcludedModelsHash([]string{"  ", ""}); got != "" {
		t.Fatalf("expected empty hash for whitespace-only entries, got %q", got)
	}
}

func TestComputeClaudeModelsHash_Deterministic(t *testing.T) {
	models := []config.ClaudeModel{{Name: "a", Alias: "A"}, {Name: "b"}}
	h1 := ComputeClaudeModelsHash(models)
	h2 := ComputeClaudeModelsHash(models)
	if h1 == "" || h1 != h2 {
		t.Fatalf("expected deterministic hash, got %s / %s", h1, h2)
	}
	if h3 := ComputeClaudeModelsHash([]config.ClaudeModel{{Name: "a"}}); h3 == h1 {
		t.Fatalf("expected different hash when models change, got %s", h3)
	}
}

func TestComputeCodexModelsHash_Deterministic(t *testing.T) {
	models := []config.CodexModel{{Name: "a", Alias: "A"}, {Name: "b"}}
	h1 := ComputeCodexModelsHash(models)
	h2 := ComputeCodexModelsHash(models)
	if h1 == "" || h1 != h2 {
		t.Fatalf("expected deterministic hash, got %s / %s", h1, h2)
	}
	if h3 := ComputeCodexModelsHash([]config.CodexModel{{Name: "a"}}); h3 == h1 {
		t.Fatalf("expected different hash when models change, got %s", h3)
	}
}

func TestComputeModelHashesIncludeThinking(t *testing.T) {
	low := &registry.ThinkingSupport{Levels: []string{"low"}}
	high := &registry.ThinkingSupport{Levels: []string{"high"}}
	tests := []struct {
		name string
		low  string
		high string
	}{
		{name: "openai compatibility", low: ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "m", Thinking: low}}), high: ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "m", Thinking: high}})},
		{name: "vertex", low: ComputeVertexCompatModelsHash([]config.VertexCompatModel{{Name: "m", Thinking: low}}), high: ComputeVertexCompatModelsHash([]config.VertexCompatModel{{Name: "m", Thinking: high}})},
		{name: "claude", low: ComputeClaudeModelsHash([]config.ClaudeModel{{Name: "m", Thinking: low}}), high: ComputeClaudeModelsHash([]config.ClaudeModel{{Name: "m", Thinking: high}})},
		{name: "codex", low: ComputeCodexModelsHash([]config.CodexModel{{Name: "m", Thinking: low}}), high: ComputeCodexModelsHash([]config.CodexModel{{Name: "m", Thinking: high}})},
		{name: "gemini", low: ComputeGeminiModelsHash([]config.GeminiModel{{Name: "m", Thinking: low}}), high: ComputeGeminiModelsHash([]config.GeminiModel{{Name: "m", Thinking: high}})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.low == "" || tc.low == tc.high {
				t.Fatalf("thinking capability must change model hash: %q / %q", tc.low, tc.high)
			}
		})
	}
}
