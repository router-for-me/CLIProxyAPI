package codebuddy

import (
	"testing"
)

func TestParseEnabledModelsAggregatesAndFilters(t *testing.T) {
	body := []byte(`{
		"models": [
			{"id": "glm-5.2", "name": "GLM 5.2", "credits": "x2.20 credits", "maxInputTokens": 200000, "maxOutputTokens": 64000, "supportsImages": true},
			{"id": "hy3", "name": "HY3"},
			{"id": "auto", "name": "Auto"}
		],
		"agents": [
			{"name": "agent", "asTool": false, "models": ["glm-5.2", "deepseek-v4-pro", "auto", ""]}
		]
	}`)

	models, err := ParseEnabledModels(body)
	if err != nil {
		t.Fatalf("ParseEnabledModels returned error: %v", err)
	}
	want := []string{"deepseek-v4-pro", "glm-5.2", "hy3"}
	if len(models) != len(want) {
		t.Fatalf("unexpected model count: got %v, want %v", models, want)
	}
	for i, id := range want {
		if models[i] != id {
			t.Errorf("models[%d] = %q, want %q", i, models[i], id)
		}
	}
}

func TestParseEnabledModelsSupportsDataWrapper(t *testing.T) {
	body := []byte(`{"code":0,"msg":"ok","data":{"models":[{"id":"kimi-k2.7"}]}}`)
	models, err := ParseEnabledModels(body)
	if err != nil {
		t.Fatalf("ParseEnabledModels returned error: %v", err)
	}
	if len(models) != 1 || models[0] != "kimi-k2.7" {
		t.Fatalf("unexpected models: %v", models)
	}
}

func TestParseEnabledModelsEmptyBody(t *testing.T) {
	models, err := ParseEnabledModels(nil)
	if err != nil {
		t.Fatalf("ParseEnabledModels(nil) returned error: %v", err)
	}
	if models != nil {
		t.Fatalf("expected nil models, got %v", models)
	}
}

func TestParseModelsMetadata(t *testing.T) {
	body := []byte(`{
		"models": [
			{"id": "glm-5.2", "name": "GLM 5.2", "credits": "x2.20 credits", "maxInputTokens": 200000, "maxOutputTokens": 64000, "supportsImages": true, "supportsReasoning": false, "supportsToolCall": true},
			{"id": "minimax-m3", "name": "MiniMax M3", "credits": ""}
		]
	}`)

	models, err := ParseModels(body)
	if err != nil {
		t.Fatalf("ParseModels returned error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("unexpected model count: %d", len(models))
	}
	if models[0].ID != "glm-5.2" {
		t.Errorf("expected sorted output, first model = %q", models[0].ID)
	}
	if models[0].CreditMultiplier != 2.20 {
		t.Errorf("credit multiplier = %v, want 2.20", models[0].CreditMultiplier)
	}
	if models[0].MaxInputTokens != 200000 || models[0].MaxOutputTokens != 64000 {
		t.Errorf("token limits not parsed: %+v", models[0])
	}
	if !models[0].SupportsImages {
		t.Errorf("supportsImages not parsed")
	}
	if models[1].CreditMultiplier != 0 {
		t.Errorf("empty credits should yield zero multiplier, got %v", models[1].CreditMultiplier)
	}
}
