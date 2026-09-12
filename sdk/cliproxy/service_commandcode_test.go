package cliproxy

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestCommandCode_ExecutorAndModelRegistration(t *testing.T) {
	cfg := &config.Config{}
	svc := &Service{
		cfg:         cfg,
		coreManager: coreauth.NewManager(nil, nil, nil),
	}

	auth := &coreauth.Auth{
		ID:       "cmdc-test",
		Provider: constant.CommandCode,
		Attributes: map[string]string{
			"api_key": "user_test_key",
		},
	}

	ctx := context.Background()

	// 1. Check executor registration
	svc.ensureExecutorsForAuthWithContext(ctx, auth, false)
	exec, ok := svc.coreManager.Executor(constant.CommandCode)
	if !ok || exec == nil {
		t.Fatalf("expected commandcode executor registered, got ok=%v", ok)
	}
	if _, isCmdc := exec.(*executor.CommandCodeExecutor); !isCmdc {
		t.Fatalf("expected *executor.CommandCodeExecutor, got %T", exec)
	}

	// 2. Check model registration
	svc.registerModelsForAuthWithCache(ctx, auth, nil)
	providers := registry.GetGlobalRegistry().GetModelProviders("deepseek/deepseek-v4-flash")
	found := false
	for _, p := range providers {
		if p == constant.CommandCode {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected deepseek/deepseek-v4-flash to have commandcode provider, got providers: %v", providers)
	}

	available := GlobalModelRegistry().GetAvailableModels("")
	hasModel := false
	for _, m := range available {
		if id, ok := m["id"].(string); ok && id == "deepseek/deepseek-v4-flash" {
			hasModel = true
			break
		}
	}
	if !hasModel {
		t.Fatalf("expected deepseek/deepseek-v4-flash in AvailableModels, got: %v", available)
	}

	// Verify model count
	models := registry.GetCommandCodeModels()
	if len(models) == 0 {
		t.Fatalf("expected builtin Command Code models > 0, got 0")
	}
}
