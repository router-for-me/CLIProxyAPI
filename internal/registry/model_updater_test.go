package registry

import (
	"slices"
	"testing"
)

func TestMergeModelsCatalogWithFallback_PreservesMissingSections(t *testing.T) {
	existing := &staticModelsJSON{
		Claude: []*ModelInfo{
			{ID: "claude-old"},
		},
		AWSBedrock: []*ModelInfo{
			{ID: "deepseek.r1-v1:0", SupportedParameters: []string{"max_tokens", "thinking"}},
		},
	}
	incoming := &staticModelsJSON{
		Claude: []*ModelInfo{
			{ID: "claude-new"},
		},
		AWSBedrock: []*ModelInfo{},
	}

	merged := mergeModelsCatalogWithFallback(existing, incoming)
	if merged == nil {
		t.Fatal("merged catalog is nil")
	}
	if len(merged.Claude) != 1 || merged.Claude[0].ID != "claude-new" {
		t.Fatalf("claude section should use incoming data, got: %#v", merged.Claude)
	}
	if len(merged.AWSBedrock) != 1 || merged.AWSBedrock[0].ID != "deepseek.r1-v1:0" {
		t.Fatalf("aws-bedrock section should fallback to existing data, got: %#v", merged.AWSBedrock)
	}
}

func TestDetectChangedProviders_IncludesAWSBedrock(t *testing.T) {
	oldData := &staticModelsJSON{
		Claude:      []*ModelInfo{{ID: "claude-a"}},
		Gemini:      []*ModelInfo{{ID: "gemini-a"}},
		Vertex:      []*ModelInfo{{ID: "vertex-a"}},
		GeminiCLI:   []*ModelInfo{{ID: "gemini-cli-a"}},
		AIStudio:    []*ModelInfo{{ID: "aistudio-a"}},
		CodexFree:   []*ModelInfo{{ID: "codex-free-a"}},
		CodexTeam:   []*ModelInfo{{ID: "codex-team-a"}},
		CodexPlus:   []*ModelInfo{{ID: "codex-plus-a"}},
		CodexPro:    []*ModelInfo{{ID: "codex-pro-a"}},
		Qwen:        []*ModelInfo{{ID: "qwen-a"}},
		IFlow:       []*ModelInfo{{ID: "iflow-a"}},
		Kimi:        []*ModelInfo{{ID: "kimi-a"}},
		Antigravity: []*ModelInfo{{ID: "antigravity-a"}},
		AWSBedrock:  []*ModelInfo{{ID: "deepseek.r1-v1:0"}},
	}
	newData := &staticModelsJSON{
		Claude:      []*ModelInfo{{ID: "claude-a"}},
		Gemini:      []*ModelInfo{{ID: "gemini-a"}},
		Vertex:      []*ModelInfo{{ID: "vertex-a"}},
		GeminiCLI:   []*ModelInfo{{ID: "gemini-cli-a"}},
		AIStudio:    []*ModelInfo{{ID: "aistudio-a"}},
		CodexFree:   []*ModelInfo{{ID: "codex-free-a"}},
		CodexTeam:   []*ModelInfo{{ID: "codex-team-a"}},
		CodexPlus:   []*ModelInfo{{ID: "codex-plus-a"}},
		CodexPro:    []*ModelInfo{{ID: "codex-pro-a"}},
		Qwen:        []*ModelInfo{{ID: "qwen-a"}},
		IFlow:       []*ModelInfo{{ID: "iflow-a"}},
		Kimi:        []*ModelInfo{{ID: "kimi-a"}},
		Antigravity: []*ModelInfo{{ID: "antigravity-a"}},
		AWSBedrock:  []*ModelInfo{{ID: "deepseek.v3.2"}},
	}

	changed := detectChangedProviders(oldData, newData)
	if !slices.Contains(changed, "aws-bedrock") {
		t.Fatalf("changed providers should contain aws-bedrock, got: %v", changed)
	}
}
