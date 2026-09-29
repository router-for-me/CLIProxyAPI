package openai

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
)

func TestOpenAIModelsIncludesContextLength(t *testing.T) {
	const clientID = "openai-context-catalog-test"
	const modelID = "gemini-image-context-catalog-test"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, "gemini", []*registry.ModelInfo{{
		ID: modelID, Object: "model", InputTokenLimit: 131072, MaxContextLength: 120000,
	}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest("GET", "/v1/models", nil)
	base := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, nil)
	NewOpenAIAPIHandler(base).OpenAIModels(ctx)

	var response struct {
		Data []struct {
			ID               string `json:"id"`
			ContextLength    int    `json:"context_length"`
			MaxContextLength int    `json:"max_context_length"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	for _, model := range response.Data {
		if model.ID == modelID {
			if model.ContextLength != 131072 || model.MaxContextLength != 120000 {
				t.Fatalf("context limits = %d/%d, want 131072/120000", model.ContextLength, model.MaxContextLength)
			}
			return
		}
	}
	t.Fatalf("model %q not found in response", modelID)
}
