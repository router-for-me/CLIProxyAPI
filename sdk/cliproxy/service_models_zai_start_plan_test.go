package cliproxy

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestBuildZaiStartPlanModelsRestrictsCatalog(t *testing.T) {
	models := buildZaiStartPlanModels(registry.GetZAIModels(), []string{" glm-4.6 ", "glm-5v-turbo", "GLM-4.6", ""})
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d: %#v", len(models), models)
	}
	if models[0].ID != "glm-4.6" {
		t.Fatalf("expected static glm-4.6 entry, got %s", models[0].ID)
	}
	if models[0].Thinking == nil {
		t.Fatal("static entry must keep its thinking support")
	}
	if models[1].ID != "glm-5v-turbo" || models[1].Type != "openai-compatibility" || models[1].OwnedBy != "zai" {
		t.Fatalf("unexpected synthesized entry: %#v", models[1])
	}
}

func TestBuildZaiStartPlanModelsEmptyWhitelist(t *testing.T) {
	if models := buildZaiStartPlanModels(registry.GetZAIModels(), nil); len(models) != 0 {
		t.Fatalf("expected no models for an empty whitelist, got %d", len(models))
	}
}

func TestZaiStartPlanModelsAttribute(t *testing.T) {
	if got := zaiStartPlanModelsAttribute(nil); got != "" {
		t.Fatalf("nil auth must yield empty list, got %q", got)
	}
	auth := &coreauth.Auth{Metadata: map[string]any{"start_plan_models": "glm-5.3-flash"}}
	if got := zaiStartPlanModelsAttribute(auth); got != "glm-5.3-flash" {
		t.Fatalf("metadata whitelist not read: %q", got)
	}
	auth = &coreauth.Auth{Attributes: map[string]string{"start_plan_models": "glm-5.3-flash"}}
	if got := zaiStartPlanModelsAttribute(auth); got != "glm-5.3-flash" {
		t.Fatalf("attribute whitelist not read: %q", got)
	}
	auth = &coreauth.Auth{Metadata: map[string]any{"start_plan_models": "  "}, Attributes: map[string]string{"start_plan_models": "glm-5.3"}}
	if got := zaiStartPlanModelsAttribute(auth); !strings.Contains(got, "glm-5.3") {
		t.Fatalf("blank metadata must fall through to attributes, got %q", got)
	}
}
