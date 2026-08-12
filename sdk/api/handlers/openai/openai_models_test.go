package openai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestOpenAIModelsIncludesRegistryMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)

	suffix := time.Now().UnixNano()
	clientID := fmt.Sprintf("openai-models-full-test-client-%d", suffix)
	modelID := fmt.Sprintf("openai-models-full-test-model-%d", suffix)
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, "openai", []*registry.ModelInfo{{
		ID:                  modelID,
		Object:              "model",
		Created:             1776902400,
		OwnedBy:             "openai",
		ContextLength:       372000,
		MaxCompletionTokens: 128000,
	}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	handler := &OpenAIAPIHandler{}
	model := requestListedModel(t, handler, "/v1/models", modelID)
	assertJSONNumber(t, model, "context_length", 372000)
	assertJSONNumber(t, model, "max_completion_tokens", 128000)
}

func requestListedModel(t *testing.T, handler *OpenAIAPIHandler, target, modelID string) map[string]any {
	t.Helper()

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, target, nil)
	handler.OpenAIModels(context)

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want %d: %s", target, recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode GET %s response: %v", target, err)
	}
	for _, model := range response.Data {
		if model["id"] == modelID {
			return model
		}
	}
	t.Fatalf("GET %s response did not include model %q", target, modelID)
	return nil
}

func assertJSONNumber(t *testing.T, model map[string]any, key string, want float64) {
	t.Helper()
	if got, ok := model[key].(float64); !ok || got != want {
		t.Fatalf("%s = %#v, want %v", key, model[key], want)
	}
}
