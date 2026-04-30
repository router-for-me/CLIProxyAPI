package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
)

func TestOpenAIModelsIncludesContextLength(t *testing.T) {
	modelID := "gpt-context-length-test"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient("context-length-test-client", "openai", []*registry.ModelInfo{{
		ID:            modelID,
		Object:        "model",
		Created:       123,
		OwnedBy:       "openai",
		ContextLength: 1050000,
	}})
	t.Cleanup(func() { modelRegistry.UnregisterClient("context-length-test-client") })

	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewOpenAIAPIHandler(&handlers.BaseAPIHandler{})
	router.GET("/v1/models", handler.OpenAIModels)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Object string `json:"object"`
		Data   []struct {
			ID            string `json:"id"`
			Object        string `json:"object"`
			Created       int64  `json:"created"`
			OwnedBy       string `json:"owned_by"`
			ContextLength int    `json:"context_length"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if response.Object != "list" {
		t.Fatalf("expected object=list, got %q", response.Object)
	}
	var found bool
	for _, model := range response.Data {
		if model.ID != modelID {
			continue
		}
		found = true
		if model.ContextLength != 1050000 {
			t.Fatalf("expected context_length 1050000, got %d", model.ContextLength)
		}
	}
	if !found {
		t.Fatalf("expected model %q in /v1/models response", modelID)
	}
}
