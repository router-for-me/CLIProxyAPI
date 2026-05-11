package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestOpenAIModelsPreservesModelMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)

	clientID := "openai-models-metadata-test-client-" + time.Now().Format("20060102150405.000000000")
	modelID := "openai-models-metadata-test-model-" + time.Now().Format("20060102150405.000000000")

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, "openai", []*registry.ModelInfo{{
		ID:                  modelID,
		Object:              "model",
		Created:             1776902400,
		OwnedBy:             "openai",
		Type:                "openai",
		DisplayName:         "Metadata Test Model",
		Version:             modelID,
		Description:         "Model with optional metadata.",
		ContextLength:       272000,
		MaxCompletionTokens: 128000,
		SupportedParameters: []string{"tools"},
	}})
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	router := gin.New()
	handler := &OpenAIAPIHandler{}
	router.GET("/v1/models", handler.OpenAIModels)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	var response struct {
		Object string           `json:"object"`
		Data   []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if response.Object != "list" {
		t.Fatalf("expected object list, got %q", response.Object)
	}

	var found map[string]any
	for _, model := range response.Data {
		if model["id"] == modelID {
			found = model
			break
		}
	}
	if found == nil {
		t.Fatalf("expected to find model %q in response", modelID)
	}

	assertJSONNumber(t, found, "context_length", 272000)
	assertJSONNumber(t, found, "max_completion_tokens", 128000)
	assertJSONNumber(t, found, "created", 1776902400)
	assertStringField(t, found, "id", modelID)
	assertStringField(t, found, "object", "model")
	assertStringField(t, found, "owned_by", "openai")
	assertStringField(t, found, "type", "openai")
	assertStringField(t, found, "display_name", "Metadata Test Model")
	assertStringField(t, found, "version", modelID)
	assertStringField(t, found, "description", "Model with optional metadata.")

	params, ok := found["supported_parameters"].([]any)
	if !ok || len(params) != 1 || params[0] != "tools" {
		t.Fatalf("expected supported_parameters [tools], got %#v", found["supported_parameters"])
	}
}

func assertStringField(t *testing.T, model map[string]any, key, want string) {
	t.Helper()
	got, ok := model[key].(string)
	if !ok || got != want {
		t.Fatalf("expected %s=%q, got %#v", key, want, model[key])
	}
}

func assertJSONNumber(t *testing.T, model map[string]any, key string, want float64) {
	t.Helper()
	got, ok := model[key].(float64)
	if !ok || got != want {
		t.Fatalf("expected %s=%v, got %#v", key, want, model[key])
	}
}
