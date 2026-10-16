package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
)

func TestOpenAIModelsExposesContextLength(t *testing.T) {
	const modelID = "openai-models-context-length-test"
	const contextLength = 1048576

	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(modelID, "openai-compatibility", []*registry.ModelInfo{
		{
			ID:            modelID,
			ContextLength: contextLength,
		},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(modelID)
	})

	base := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, nil)
	router := gin.New()
	router.GET("/v1/models", NewOpenAIAPIHandler(base).OpenAIModels)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	response, err := http.Get(server.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	defer response.Body.Close()

	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode /v1/models response: %v", err)
	}

	for _, model := range body.Data {
		if model["id"] != modelID {
			continue
		}
		value, exists := model["context_length"]
		if !exists {
			t.Fatalf("model %s has no context_length: %#v", modelID, model)
		}
		if got, _ := value.(float64); got != contextLength {
			t.Fatalf("context_length = %v, want %d", value, contextLength)
		}
		return
	}

	t.Fatalf("model %s not present in /v1/models: %#v", modelID, body.Data)
}
