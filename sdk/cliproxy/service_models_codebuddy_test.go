package cliproxy

import (
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestBuildCodeBuddyAuthModelsFromMetadata(t *testing.T) {
	auth := &coreauth.Auth{
		Provider: "codebuddy-cn",
		Metadata: map[string]any{
			"enabled_models": []any{"hy3", "glm-5.2"},
			"models_meta":    `[{"id":"glm-5.2","name":"GLM 5.2","max_input_tokens":200000,"max_output_tokens":64000,"supports_images":true},{"id":"hy3","name":"HY3"}]`,
		},
	}

	models := buildCodeBuddyAuthModels(auth)
	if len(models) != 2 {
		t.Fatalf("unexpected model count: %d", len(models))
	}
	if models[0].ID != "glm-5.2" || models[0].DisplayName != "GLM 5.2" {
		t.Errorf("first model mismatch: %+v", models[0])
	}
	if models[0].ContextLength != 200000 || models[0].MaxCompletionTokens != 64000 {
		t.Errorf("context limits not mapped: %+v", models[0])
	}
	if len(models[0].SupportedInputModalities) != 1 || models[0].SupportedInputModalities[0] != "image" {
		t.Errorf("input modalities not mapped: %v", models[0].SupportedInputModalities)
	}
	if models[1].ID != "hy3" {
		t.Errorf("second model mismatch: %+v", models[1])
	}
}

func TestBuildCodeBuddyAuthModelsFallsBackToIDList(t *testing.T) {
	auth := &coreauth.Auth{
		Provider: "codebuddy-cn",
		Metadata: map[string]any{
			"enabled_models": []any{"hy3", "minimax-m3"},
		},
	}

	models := buildCodeBuddyAuthModels(auth)
	if len(models) != 2 {
		t.Fatalf("unexpected model count: %d", len(models))
	}
	if models[0].ID != "hy3" || models[0].OwnedBy != "codebuddy-cn" || models[0].Type != "codebuddy-cn" {
		t.Errorf("fallback model mismatch: %+v", models[0])
	}
}

func TestBuildCodeBuddyAuthModelsStringSliceMetadata(t *testing.T) {
	auth := &coreauth.Auth{
		Provider: "codebuddy-cn",
		Metadata: map[string]any{
			"enabled_models": []string{"hy3"},
		},
	}
	if models := buildCodeBuddyAuthModels(auth); len(models) != 1 || models[0].ID != "hy3" {
		t.Fatalf("string slice metadata not handled: %+v", models)
	}
}

func TestBuildCodeBuddyAuthModelsEmptyMetadata(t *testing.T) {
	if models := buildCodeBuddyAuthModels(nil); models != nil {
		t.Errorf("nil auth should yield no models, got %+v", models)
	}
	if models := buildCodeBuddyAuthModels(&coreauth.Auth{Provider: "codebuddy-cn"}); models != nil {
		t.Errorf("auth without metadata should yield no models, got %+v", models)
	}
}
