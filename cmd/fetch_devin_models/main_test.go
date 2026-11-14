package main

import (
	"reflect"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestSplitDevinUID(t *testing.T) {
	tests := []struct {
		uid        string
		wantBase   string
		wantEffort string
	}{
		{"claude-opus-5-low-fast", "claude-opus-5", "low"},
		{"claude-opus-5-max-fast", "claude-opus-5", "max"},
		{"gpt-6-astra-low-priority", "gpt-6-astra", "low"},
		{"gpt-6-astra-high", "gpt-6-astra", "high"},
		{"MODEL_GPT_5_2_LOW", "MODEL_GPT_5_2", "low"},
		{"MODEL_GPT_5_2_HIGH", "MODEL_GPT_5_2", "high"},
		{"MODEL_GOOGLE_GEMINI_3_0_FLASH_MINIMAL", "MODEL_GOOGLE_GEMINI_3_0_FLASH", "minimal"},
		{"claude-opus-4-6-thinking", "claude-opus-4-6", ""},
		{"claude-opus-4-6-thinking-1m", "claude-opus-4-6-1m", ""},
		{"glm-5-2-max-1m", "glm-5-2-1m", "max"},
		{"swe-1-6-slow", "swe-1-6-slow", ""},
		{"swe-1-6-fast", "swe-1-6", ""},
		{"swe-2", "swe-2", ""},
		{"swe-2-medium-lightning", "swe-2-lightning", "medium"},
		{"swe-2-high-lightning", "swe-2-lightning", "high"},
		{"swe-2-max-lightning", "swe-2-lightning", "max"},
	}

	for _, tt := range tests {
		t.Run(tt.uid, func(t *testing.T) {
			base, effort := splitDevinUID(tt.uid)
			if base != tt.wantBase {
				t.Errorf("splitDevinUID(%q) base = %q, want %q", tt.uid, base, tt.wantBase)
			}
			if effort != tt.wantEffort {
				t.Errorf("splitDevinUID(%q) effort = %q, want %q", tt.uid, effort, tt.wantEffort)
			}
		})
	}
}

func TestAggregateModels_MergesThinkingVariants_Issue6141(t *testing.T) {
	raw := []rawDevinModel{
		{
			UID:           "test-opus-model",
			Label:         "Test Opus Model",
			ContextLength: 1000000,
			Multimodal:    true,
			VendorID:      3,
		},
		{
			UID:           "test-opus-model-low-fast",
			Label:         "Test Opus Model Low Fast",
			ContextLength: 1000000,
			Multimodal:    true,
			VendorID:      3,
		},
		{
			UID:           "test-opus-model-high-fast",
			Label:         "Test Opus Model High Fast",
			ContextLength: 1000000,
			Multimodal:    true,
			VendorID:      3,
		},
		{
			UID:           "test-astra-model-low-priority",
			Label:         "Test Astra Model Low Thinking Fast",
			ContextLength: 1000000,
			Multimodal:    true,
			VendorID:      2,
		},
		{
			UID:           "test-astra-model-high-priority",
			Label:         "Test Astra Model High Thinking Fast",
			ContextLength: 1000000,
			Multimodal:    true,
			VendorID:      2,
		},
	}

	aggregated := aggregateModels(raw)
	if len(aggregated) != 2 {
		t.Fatalf("expected 2 aggregated models, got %d: %+v", len(aggregated), aggregated)
	}

	// Verify test-opus-model
	opus := aggregated[0]
	if opus.ID != "test-opus-model" {
		t.Errorf("expected ID test-opus-model, got %q", opus.ID)
	}
	if opus.DisplayName != "Test Opus Model" {
		t.Errorf("expected DisplayName 'Test Opus Model', got %q", opus.DisplayName)
	}
	if opus.Thinking == nil {
		t.Fatalf("expected Thinking to be populated for test-opus-model")
	}
	wantOpusLevels := []string{"low", "high"}
	if !reflect.DeepEqual(opus.Thinking.Levels, wantOpusLevels) {
		t.Errorf("test-opus-model levels = %v, want %v", opus.Thinking.Levels, wantOpusLevels)
	}

	// Verify test-astra-model
	astra := aggregated[1]
	if astra.ID != "test-astra-model" {
		t.Errorf("expected ID test-astra-model, got %q", astra.ID)
	}
	if astra.DisplayName != "Test Astra Model" {
		t.Errorf("expected DisplayName 'Test Astra Model', got %q", astra.DisplayName)
	}
	if astra.Thinking == nil {
		t.Fatalf("expected Thinking to be populated for test-astra-model")
	}
	wantAstraLevels := []string{"low", "high"}
	if !reflect.DeepEqual(astra.Thinking.Levels, wantAstraLevels) {
		t.Errorf("test-astra-model levels = %v, want %v", astra.Thinking.Levels, wantAstraLevels)
	}
}

func TestAggregateModels_PreservesCatalogThinkingLevels(t *testing.T) {
	raw := []rawDevinModel{
		{
			UID:           "claude-fable-5-1",
			Label:         "Claude Fable 5.1",
			ContextLength: 1000000,
			Multimodal:    true,
			VendorID:      3,
		},
	}
	aggregated := aggregateModels(raw)
	if len(aggregated) != 1 {
		t.Fatalf("expected 1 model, got %d", len(aggregated))
	}
	if aggregated[0].Thinking == nil || len(aggregated[0].Thinking.Levels) == 0 {
		t.Fatalf("expected thinking levels seeded from catalog for claude-fable-5-1, got nil")
	}
}

func TestParseSingleModelConfig_ReadsMaxOutputFromModelConfig(t *testing.T) {
	var cfg []byte
	cfg = protowire.AppendTag(cfg, 4, protowire.VarintType)
	cfg = protowire.AppendVarint(cfg, 1048576)
	cfg = protowire.AppendTag(cfg, 5, protowire.BytesType)
	cfg = protowire.AppendString(cfg, "LLAMA_WITH_SPECIAL")
	cfg = protowire.AppendTag(cfg, 13, protowire.VarintType)
	cfg = protowire.AppendVarint(cfg, 384000)

	var msg []byte
	msg = protowire.AppendTag(msg, 18, protowire.VarintType)
	msg = protowire.AppendVarint(msg, 1048576)
	msg = protowire.AppendTag(msg, 22, protowire.BytesType)
	msg = protowire.AppendString(msg, "deepseek-v4-1-flash-1017-low")
	msg = protowire.AppendTag(msg, 23, protowire.BytesType)
	msg = protowire.AppendBytes(msg, cfg)

	got := parseSingleModelConfig(msg)
	if got.UID != "deepseek-v4-1-flash-1017-low" || got.ContextLength != 1048576 {
		t.Fatalf("parsed %+v", got)
	}
	if got.MaxOutput != 384000 {
		t.Fatalf("MaxOutput = %d, want 384000", got.MaxOutput)
	}
}

func TestAggregateModels_UsesUpstreamMaxOutput(t *testing.T) {
	raw := []rawDevinModel{
		{UID: "grok-4-7-low", Label: "Grok 4.7 Low", ContextLength: 500000, MaxOutput: 100000, VendorID: 5},
		{UID: "grok-4-7-high", Label: "Grok 4.7 High", ContextLength: 500000, MaxOutput: 100000, VendorID: 5},
		{UID: "old-model", Label: "Old Model", ContextLength: 200000},
		{UID: "mixed-model-low", Label: "Mixed Low", ContextLength: 200000, MaxOutput: 128000},
		{UID: "mixed-model-high", Label: "Mixed High", ContextLength: 200000},
	}
	got := map[string]int{}
	for _, m := range aggregateModels(raw) {
		got[m.ID] = m.MaxCompletionTokens
	}
	if got["grok-4-7"] != 100000 {
		t.Fatalf("grok-4-7 max_completion_tokens = %d, want 100000", got["grok-4-7"])
	}
	if got["old-model"] != defaultDevinMaxCompletionTokens {
		t.Fatalf("old-model max_completion_tokens = %d, want default %d", got["old-model"], defaultDevinMaxCompletionTokens)
	}
	// A variant without a reported limit counts as the fallback, so the aggregate never exceeds it.
	if got["mixed-model"] != defaultDevinMaxCompletionTokens {
		t.Fatalf("mixed-model max_completion_tokens = %d, want fallback %d", got["mixed-model"], defaultDevinMaxCompletionTokens)
	}
}
