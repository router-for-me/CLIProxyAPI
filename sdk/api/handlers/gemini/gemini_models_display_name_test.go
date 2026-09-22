package gemini

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
)

func TestGeminiModelsResponseUsesConfiguredDisplayName(t *testing.T) {
	const clientID = "gemini-display-name-catalog-test"
	const modelID = "gemini-display-name-catalog-test"
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient(clientID, "gemini", []*registry.ModelInfo{{
		ID: modelID, Name: modelID, DisplayName: "Configured Gemini Name",
	}})
	t.Cleanup(func() {
		registryRef.UnregisterClient(clientID)
	})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	NewGeminiAPIHandler(&handlers.BaseAPIHandler{}).GeminiModels(ctx)

	var response struct {
		Models []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
	}
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	for _, model := range response.Models {
		if model.Name == "models/"+modelID {
			if model.DisplayName != "Configured Gemini Name" {
				t.Fatalf("displayName = %q, want Configured Gemini Name", model.DisplayName)
			}
			return
		}
	}
	t.Fatalf("model %q not found in response", modelID)
}
