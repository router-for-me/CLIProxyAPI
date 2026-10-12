package claude

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

func TestClaudeModelsResponseUsesConfiguredDisplayName(t *testing.T) {
	const clientID = "claude-display-name-catalog-test"
	const modelID = "claude-display-name-catalog-test"
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient(clientID, "claude", []*registry.ModelInfo{{
		ID: modelID, Object: "model", OwnedBy: "test", DisplayName: "Configured Claude Name",
	}})
	t.Cleanup(func() {
		registryRef.UnregisterClient(clientID)
	})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{}).ClaudeModels(ctx)

	var response struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	for _, model := range response.Data {
		if model.ID == modelID {
			if model.DisplayName != "Configured Claude Name" {
				t.Fatalf("display_name = %q, want Configured Claude Name", model.DisplayName)
			}
			return
		}
	}
	t.Fatalf("model %q not found in response", modelID)
}

func TestClaudeModelsResponseDisablesModelListCloaking(t *testing.T) {
	const clientID = "claude-disable-model-list-cloaking-test"
	const modelID = "gpt-disable-model-list-cloaking-test"
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient(clientID, "claude", []*registry.ModelInfo{{
		ID: modelID, Object: "model", OwnedBy: "test",
	}})
	t.Cleanup(func() {
		registryRef.UnregisterClient(clientID)
	})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	baseHandler := &handlers.BaseAPIHandler{Cfg: &sdkconfig.SDKConfig{
		ClaudeCode: sdkconfig.ClaudeCodeConfig{DisableCloakingModelList: true},
	}}
	NewClaudeCodeAPIHandler(baseHandler).ClaudeModels(ctx)

	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	for _, model := range response.Data {
		if model.ID == modelID {
			return
		}
	}
	t.Fatalf("uncloaked model %q not found in response", modelID)
}

func TestClaudeModelsResponseNativeModelLists(t *testing.T) {
	const claudeClientID = "claude-native-model-lists-test"
	const codexClientID = "codex-native-model-lists-test"
	const claudeModelID = "claude-native-model-lists-test"
	const codexModelID = "gpt-native-model-lists-test"
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient(claudeClientID, "claude", []*registry.ModelInfo{{ID: claudeModelID, Object: "model", OwnedBy: "test"}})
	registryRef.RegisterClient(codexClientID, "codex", []*registry.ModelInfo{{ID: codexModelID, Object: "model", OwnedBy: "test"}})
	t.Cleanup(func() {
		registryRef.UnregisterClient(claudeClientID)
		registryRef.UnregisterClient(codexClientID)
	})

	listed := func(nativeModelLists bool) map[string]bool {
		cfg := &sdkconfig.SDKConfig{ClaudeCode: sdkconfig.ClaudeCodeConfig{DisableCloakingModelList: true}}
		cfg.Client.NativeModelLists = nativeModelLists
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{Cfg: cfg}).ClaudeModels(ctx)

		var response struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
			t.Fatalf("decode response: %v", errUnmarshal)
		}
		ids := make(map[string]bool, len(response.Data))
		for _, model := range response.Data {
			ids[model.ID] = true
		}
		return ids
	}

	if ids := listed(false); !ids[claudeModelID] || !ids[codexModelID] {
		t.Fatalf("setting off: Claude model listed = %t, Codex model listed = %t, want both", ids[claudeModelID], ids[codexModelID])
	}
	if ids := listed(true); !ids[claudeModelID] || ids[codexModelID] {
		t.Fatalf("setting on: Claude model listed = %t, Codex model listed = %t, want only the Claude model", ids[claudeModelID], ids[codexModelID])
	}
}

func TestRewriteClaudeDDModelInBody(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantModel string
	}{
		{
			name:      "encoded model is decoded",
			body:      `{"model":"claude-fable-5-dd-o4-tpg","messages":[]}`,
			wantModel: "gpt-4o",
		},
		{
			name:      "plain claude model unchanged",
			body:      `{"model":"claude-sonnet-4-6","messages":[]}`,
			wantModel: "claude-sonnet-4-6",
		},
		{
			name:      "encoded model with thinking suffix",
			body:      `{"model":"claude-fable-5-dd-o4-tpg(high)","stream":true}`,
			wantModel: "gpt-4o(high)",
		},
		{
			name:      "missing model field unchanged",
			body:      `{"messages":[]}`,
			wantModel: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteClaudeDDModelInBody([]byte(tt.body))
			if model := gjson.GetBytes(got, "model").String(); model != tt.wantModel {
				t.Fatalf("model = %q, want %q; body=%s", model, tt.wantModel, string(got))
			}
		})
	}
}
