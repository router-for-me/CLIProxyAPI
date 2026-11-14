package handlers

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestExecuteStreamWithAuthManagerRoutesUltrafastFromRequestBody(t *testing.T) {
	executor := &authAwareStreamExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	model := "ultrafast-handler-model"
	for _, auth := range []*coreauth.Auth{
		{ID: "handler-pro", Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"plan_type": "pro", "priority": "10"}, Metadata: map[string]any{"access_token": "pro-token"}},
		{ID: "handler-promax", Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"plan_type": "promax"}, Metadata: map[string]any{"access_token": "promax-token"}},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register(%s): %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		id := auth.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)

	for _, body := range []string{
		`{"model":"` + model + `","service_tier":"ultrafast"}`,
		`{"model":"` + model + `","service_tier":"priority"}`,
	} {
		dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai-response", model, []byte(body), "")
		for range dataChan {
		}
		for errMsg := range errChan {
			if errMsg != nil {
				t.Fatalf("%s: unexpected error %v", body, errMsg.Error)
			}
		}
	}
	if got := executor.AuthIDs(); len(got) != 2 || got[0] != "handler-promax" || got[1] != "handler-pro" {
		t.Fatalf("selected auths = %v, want [handler-promax handler-pro]", got)
	}
}
