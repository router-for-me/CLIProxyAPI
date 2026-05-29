package registry

import "testing"

func TestGitHubCopilotGeminiModelsAreChatOnly(t *testing.T) {
	models := GetGitHubCopilotModels()
	required := map[string]bool{
		"gemini-2.5-pro":         false,
		"gemini-3-pro-preview":   false,
		"gemini-3.1-pro-preview": false,
		"gemini-3-flash-preview": false,
	}

	for _, model := range models {
		if _, ok := required[model.ID]; !ok {
			continue
		}
		required[model.ID] = true
		if len(model.SupportedEndpoints) != 1 || model.SupportedEndpoints[0] != "/chat/completions" {
			t.Fatalf("model %q supported endpoints = %v, want [/chat/completions]", model.ID, model.SupportedEndpoints)
		}
	}

	for modelID, found := range required {
		if !found {
			t.Fatalf("expected GitHub Copilot model %q in definitions", modelID)
		}
	}
}

func TestCodexStaticModelsIncludeGPT55(t *testing.T) {
	tierModels := map[string][]*ModelInfo{
		"free": GetCodexFreeModels(),
		"team": GetCodexTeamModels(),
		"plus": GetCodexPlusModels(),
		"pro":  GetCodexProModels(),
	}

	for tier, models := range tierModels {
		t.Run(tier, func(t *testing.T) {
			model := findModelInfo(models, "gpt-5.5")
			if model == nil {
				t.Fatalf("expected codex %s tier to include gpt-5.5", tier)
			}
			assertGPT55ModelInfo(t, tier, model)
		})
	}

	model := LookupStaticModelInfo("gpt-5.5")
	if model == nil {
		t.Fatal("expected LookupStaticModelInfo to find gpt-5.5")
	}
	assertGPT55ModelInfo(t, "lookup", model)
}

func TestKiroStaticModelsIncludeOpus48(t *testing.T) {
	model := findModelInfo(GetKiroModels(), "kiro-claude-opus-4-8")
	if model == nil {
		t.Fatal("expected Kiro models to include kiro-claude-opus-4-8")
	}
	assertKiroOpus48ModelInfo(t, model, "Kiro Claude Opus 4.8", "Claude Opus 4.8 via Kiro (2.2x credit)")

	agentic := findModelInfo(GetKiroModels(), "kiro-claude-opus-4-8-agentic")
	if agentic == nil {
		t.Fatal("expected Kiro models to include kiro-claude-opus-4-8-agentic")
	}
	assertKiroOpus48ModelInfo(t, agentic, "Kiro Claude Opus 4.8 (Agentic)", "Claude Opus 4.8 optimized for coding agents (chunked writes)")

	lookup := LookupStaticModelInfo("kiro-claude-opus-4-8")
	if lookup == nil {
		t.Fatal("expected LookupStaticModelInfo to find kiro-claude-opus-4-8")
	}
	assertKiroOpus48ModelInfo(t, lookup, "Kiro Claude Opus 4.8", "Claude Opus 4.8 via Kiro (2.2x credit)")
}

func TestAmazonQStaticModelsIncludeOpus48(t *testing.T) {
	model := findModelInfo(GetAmazonQModels(), "amazonq-claude-opus-4.8")
	if model == nil {
		t.Fatal("expected Amazon Q models to include amazonq-claude-opus-4.8")
	}
	assertAmazonQOpus48ModelInfo(t, model, "Amazon Q Claude Opus 4.8", "Claude Opus 4.8 via Amazon Q (2.2x credit)")

	hyphenModel := findModelInfo(GetAmazonQModels(), "amazonq-claude-opus-4-8")
	if hyphenModel == nil {
		t.Fatal("expected Amazon Q models to include amazonq-claude-opus-4-8")
	}
	assertAmazonQOpus48ModelInfo(t, hyphenModel, "Amazon Q Claude Opus 4.8", "Claude Opus 4.8 via Amazon Q (2.2x credit)")

	lookup := LookupStaticModelInfo("amazonq-claude-opus-4.8")
	if lookup == nil {
		t.Fatal("expected LookupStaticModelInfo to find amazonq-claude-opus-4.8")
	}
	assertAmazonQOpus48ModelInfo(t, lookup, "Amazon Q Claude Opus 4.8", "Claude Opus 4.8 via Amazon Q (2.2x credit)")

	hyphenLookup := LookupStaticModelInfo("amazonq-claude-opus-4-8")
	if hyphenLookup == nil {
		t.Fatal("expected LookupStaticModelInfo to find amazonq-claude-opus-4-8")
	}
	assertAmazonQOpus48ModelInfo(t, hyphenLookup, "Amazon Q Claude Opus 4.8", "Claude Opus 4.8 via Amazon Q (2.2x credit)")
}

func findModelInfo(models []*ModelInfo, id string) *ModelInfo {
	for _, model := range models {
		if model != nil && model.ID == id {
			return model
		}
	}
	return nil
}

func assertKiroOpus48ModelInfo(t *testing.T, model *ModelInfo, displayName, description string) {
	t.Helper()

	assertAWSOpus48ModelInfo(t, model, displayName, description)
	if model.Thinking == nil {
		t.Fatal("missing thinking support")
	}
	if model.Thinking.Min != 1024 || model.Thinking.Max != 32000 || !model.Thinking.ZeroAllowed || !model.Thinking.DynamicAllowed {
		t.Fatalf("thinking support mismatch: %+v", model.Thinking)
	}
}

func assertAmazonQOpus48ModelInfo(t *testing.T, model *ModelInfo, displayName, description string) {
	t.Helper()

	assertAWSOpus48ModelInfo(t, model, displayName, description)
}

func assertAWSOpus48ModelInfo(t *testing.T, model *ModelInfo, displayName, description string) {
	t.Helper()

	if model.Created != 1780012800 {
		t.Fatalf("created timestamp mismatch: got %d", model.Created)
	}
	if model.OwnedBy != "aws" {
		t.Fatalf("owned_by mismatch: got %q", model.OwnedBy)
	}
	if model.Type != "kiro" {
		t.Fatalf("type mismatch: got %q", model.Type)
	}
	if model.DisplayName != displayName {
		t.Fatalf("display name mismatch: got %q, want %q", model.DisplayName, displayName)
	}
	if model.Description != description {
		t.Fatalf("description mismatch: got %q, want %q", model.Description, description)
	}
	if model.ContextLength != 200000 {
		t.Fatalf("context length mismatch: got %d", model.ContextLength)
	}
	if model.MaxCompletionTokens != 64000 {
		t.Fatalf("max completion tokens mismatch: got %d", model.MaxCompletionTokens)
	}
}

func assertGPT55ModelInfo(t *testing.T, source string, model *ModelInfo) {
	t.Helper()

	if model.ID != "gpt-5.5" {
		t.Fatalf("%s id mismatch: got %q", source, model.ID)
	}
	if model.Object != "model" {
		t.Fatalf("%s object mismatch: got %q", source, model.Object)
	}
	if model.Created != 1776902400 {
		t.Fatalf("%s created timestamp mismatch: got %d", source, model.Created)
	}
	if model.OwnedBy != "openai" {
		t.Fatalf("%s owned_by mismatch: got %q", source, model.OwnedBy)
	}
	if model.Type != "openai" {
		t.Fatalf("%s type mismatch: got %q", source, model.Type)
	}
	if model.DisplayName != "GPT 5.5" {
		t.Fatalf("%s display name mismatch: got %q", source, model.DisplayName)
	}
	if model.Version != "gpt-5.5" {
		t.Fatalf("%s version mismatch: got %q", source, model.Version)
	}
	if model.Description != "Frontier model for complex coding, research, and real-world work." {
		t.Fatalf("%s description mismatch: got %q", source, model.Description)
	}
	if model.ContextLength != 272000 {
		t.Fatalf("%s context length mismatch: got %d", source, model.ContextLength)
	}
	if model.MaxCompletionTokens != 128000 {
		t.Fatalf("%s max completion tokens mismatch: got %d", source, model.MaxCompletionTokens)
	}
	if len(model.SupportedParameters) != 1 || model.SupportedParameters[0] != "tools" {
		t.Fatalf("%s supported parameters mismatch: got %v", source, model.SupportedParameters)
	}
	if model.Thinking == nil {
		t.Fatalf("%s missing thinking support", source)
	}

	want := []string{"low", "medium", "high", "xhigh"}
	if len(model.Thinking.Levels) != len(want) {
		t.Fatalf("%s thinking level count mismatch: got %d, want %d", source, len(model.Thinking.Levels), len(want))
	}
	for i, level := range want {
		if model.Thinking.Levels[i] != level {
			t.Fatalf("%s thinking level %d mismatch: got %q, want %q", source, i, model.Thinking.Levels[i], level)
		}
	}
}
