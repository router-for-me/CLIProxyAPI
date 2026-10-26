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

func TestOpenAIModelsPreservesInputModalities(t *testing.T) {
	clientID := "openai-model-capabilities-test"
	registry.GetGlobalRegistry().RegisterClient(clientID, "openai", []*registry.ModelInfo{{
		ID:                        "gpt-vision",
		SupportedInputModalities:  []string{"text", "image"},
		SupportedOutputModalities: []string{"text"},
	}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(clientID) })

	h := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&config.SDKConfig{}, nil))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/v1/models", nil)
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = request
	h.OpenAIModels(ctx)
	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, recorder.Body.String())
	}
	for _, model := range payload.Data {
		if model["id"] == "gpt-vision" {
			modalities, ok := model["input_modalities"].([]any)
			if !ok || len(modalities) != 2 || modalities[1] != "image" {
				t.Fatalf("input_modalities = %#v, want [text image]", model["input_modalities"])
			}
			return
		}
	}
	t.Fatalf("gpt-vision missing from response: %s", recorder.Body.String())
}
