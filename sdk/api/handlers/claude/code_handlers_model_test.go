package claude

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/tidwall/gjson"
)

func TestClaudeModelsResponseUsesConfiguredDisplayName(t *testing.T) {
	const clientID = "claude-display-name-catalog-test"
	const modelID = "claude-display-name-catalog-test"
	const responseID = "claude/" + modelID
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
		if model.ID == responseID {
			if model.DisplayName != "Configured Claude Name" {
				t.Fatalf("display_name = %q, want Configured Claude Name", model.DisplayName)
			}
			return
		}
	}
	t.Fatalf("model %q not found in response", responseID)
}

func TestClaudeModelsResponseIncludesCodexProviderModels(t *testing.T) {
	const clientID = "claude-codex-provider-catalog-test"
	want := map[string]string{
		"claude/gpt-5.6-sol":   "GPT-5.6-Sol",
		"claude/gpt-5.6-terra": "GPT-5.6-Terra",
		"claude/gpt-5.6-luna":  "GPT-5.6-Luna",
	}
	models := make([]*registry.ModelInfo, 0, len(want))
	for id, displayName := range map[string]string{
		"gpt-5.6-sol": "GPT-5.6-Sol", "gpt-5.6-terra": "GPT-5.6-Terra", "gpt-5.6-luna": "GPT-5.6-Luna",
	} {
		models = append(models, &registry.ModelInfo{
			ID: id, Object: "model", OwnedBy: "openai", DisplayName: displayName,
			ContextLength: 272000, MaxCompletionTokens: 128000,
		})
	}
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient(clientID, "codex", models)
	t.Cleanup(func() { registryRef.UnregisterClient(clientID) })

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{}).ClaudeModels(ctx)

	var response struct {
		Data []struct {
			ID             string `json:"id"`
			DisplayName    string `json:"display_name"`
			MaxInputTokens int    `json:"max_input_tokens"`
			MaxTokens      int    `json:"max_tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, model := range response.Data {
		displayName, ok := want[model.ID]
		if !ok {
			continue
		}
		if model.DisplayName != displayName || model.MaxInputTokens != 272000 || model.MaxTokens != 128000 {
			t.Fatalf("model %s metadata = %#v", model.ID, model)
		}
		delete(want, model.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing Codex models from Claude discovery: %v", want)
	}
}

func TestCanonicalizeClaudeNamespacedRequest(t *testing.T) {
	tests := []struct {
		name            string
		body            string
		wantModel       string
		wantModelExists bool
	}{
		{
			name:            "namespaced model is resolved",
			body:            `{"model":"claude/gpt-4o","messages":[]}`,
			wantModel:       "gpt-4o",
			wantModelExists: true,
		},
		{
			name:            "every namespace is resolved",
			body:            `{"model":"claude/claude/claude/gpt-4o","messages":[]}`,
			wantModel:       "gpt-4o",
			wantModelExists: true,
		},
		{
			name:            "wrapped catalog Claude model is resolved",
			body:            `{"model":"claude/claude/claude-opus-5","messages":[]}`,
			wantModel:       "claude-opus-5",
			wantModelExists: true,
		},
		{
			name:            "plain catalog Claude model unchanged",
			body:            `{"model":"claude-opus-5","messages":[]}`,
			wantModel:       "claude-opus-5",
			wantModelExists: true,
		},
		{
			name:            "unprefixed translated model unchanged",
			body:            `{"model":"gpt-4o","messages":[]}`,
			wantModel:       "gpt-4o",
			wantModelExists: true,
		},
		{
			name:            "wrapped native model preserves thinking suffix",
			body:            `{"model":"claude/claude-opus-5(high)","stream":true}`,
			wantModel:       "claude-opus-5(high)",
			wantModelExists: true,
		},
		{
			name:      "missing model field unchanged",
			body:      `{"messages":[]}`,
			wantModel: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model, body := canonicalizeClaudeNamespacedRequest([]byte(tt.body))
			if model != tt.wantModel {
				t.Fatalf("route model = %q, want %q", model, tt.wantModel)
			}
			bodyModel := gjson.GetBytes(body, "model")
			if bodyModel.Exists() != tt.wantModelExists {
				t.Fatalf("body model exists = %t, want %t; body=%s", bodyModel.Exists(), tt.wantModelExists, body)
			}
			if bodyModel.Exists() && bodyModel.String() != tt.wantModel {
				t.Fatalf("body model = %q, want %q; body=%s", bodyModel.String(), tt.wantModel, body)
			}
			if !tt.wantModelExists && string(body) != tt.body {
				t.Fatalf("body changed without a model: got %s, want %s", body, tt.body)
			}
		})
	}
}
