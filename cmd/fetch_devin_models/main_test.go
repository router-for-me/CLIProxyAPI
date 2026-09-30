package main

import (
	"reflect"
	"testing"
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
