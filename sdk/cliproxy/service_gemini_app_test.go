package cliproxy

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestGeminiAppServiceRegistration(t *testing.T) {
	svc := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	auth := &coreauth.Auth{ID: "gemini-app-service-test", Provider: "gemini-app", Status: coreauth.StatusActive}
	if _, err := svc.coreManager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	models := GlobalModelRegistry()
	t.Cleanup(func() { models.UnregisterClient(auth.ID) })
	svc.ensureExecutorsForAuth(auth)
	bound, ok := svc.coreManager.Executor("gemini-app")
	if !ok {
		t.Fatal("Gemini App executor was not registered")
	}
	if _, ok := bound.(*executor.GeminiAppExecutor); !ok {
		t.Fatalf("unexpected executor: %T", bound)
	}
	svc.registerModelsForAuth(context.Background(), auth)
	if !models.ClientSupportsModel(auth.ID, "gemini-app") {
		t.Fatal("Gemini App model was not published")
	}
	auth.Attributes = map[string]string{"excluded_models": "gemini-app"}
	svc.registerModelsForAuth(context.Background(), auth)
	if models.ClientSupportsModel(auth.ID, "gemini-app") {
		t.Fatal("excluded Gemini App model is still published")
	}
}

func TestGeminiAppStaticModelLookup(t *testing.T) {
	model := registry.LookupStaticModelInfoByChannel("gemini-app", "gemini-app")
	if model == nil || model.Type != "gemini-app" {
		t.Fatal("Gemini App static lookup failed")
	}
	model.DisplayName = "changed"
	if registry.GetGeminiAppModels()[0].DisplayName != "Gemini App" {
		t.Fatal("caller mutated static model state")
	}
	if registry.LookupStaticModelInfo("gemini-app") == nil {
		t.Fatal("global Gemini App model lookup failed")
	}
}
