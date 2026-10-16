package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// TestOpenAIModelsExposesThinkingLevels locks in the contract the OpenCode
// CLIProxyAPI plugin relies on: /v1/models must keep the provider-specific
// thinking capabilities so clients can build reasoning-effort controls.
func TestOpenAIModelsExposesThinkingLevels(t *testing.T) {
	clientID := "openai-models-thinking-test"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, "codex", []*registry.ModelInfo{
		{
			ID:          "cliproxy-thinking-model",
			Object:      "model",
			OwnedBy:     "openai",
			DisplayName: "Thinking Model",
			Thinking: &registry.ThinkingSupport{
				Levels:      []string{"low", "medium", "high"},
				ZeroAllowed: true,
			},
		},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
	})

	gin.SetMode(gin.TestMode)
	handler := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
	router := gin.New()
	router.GET("/v1/models", handler.OpenAIModels)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var response struct {
		Data []map[string]any `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}

	var found map[string]any
	for _, model := range response.Data {
		if model["id"] == "cliproxy-thinking-model" {
			found = model
			break
		}
	}
	if found == nil {
		t.Fatalf("model cliproxy-thinking-model missing from %v", response.Data)
	}

	thinking, ok := found["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking metadata = %#v, want object", found["thinking"])
	}
	levels, ok := thinking["levels"].([]any)
	if !ok {
		t.Fatalf("thinking.levels = %#v, want array", thinking["levels"])
	}
	got := make([]string, 0, len(levels))
	for _, level := range levels {
		got = append(got, level.(string))
	}
	want := []string{"low", "medium", "high"}
	if len(got) != len(want) {
		t.Fatalf("thinking.levels = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("thinking.levels = %v, want %v", got, want)
		}
	}
	if zeroAllowed, ok := thinking["zero_allowed"].(bool); !ok || !zeroAllowed {
		t.Fatalf("thinking.zero_allowed = %#v, want true", thinking["zero_allowed"])
	}
}

// TestOpenAIModelsOmitsThinkingWhenUnset keeps the extension additive: models
// without reasoning metadata must not gain an empty thinking field.
func TestOpenAIModelsOmitsThinkingWhenUnset(t *testing.T) {
	clientID := "openai-models-no-thinking-test"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, "codex", []*registry.ModelInfo{
		{ID: "cliproxy-plain-model", Object: "model", OwnedBy: "openai"},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
	})

	gin.SetMode(gin.TestMode)
	handler := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
	router := gin.New()
	router.GET("/v1/models", handler.OpenAIModels)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var response struct {
		Data []map[string]any `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	for _, model := range response.Data {
		if model["id"] != "cliproxy-plain-model" {
			continue
		}
		if _, exists := model["thinking"]; exists {
			t.Fatalf("unexpected thinking metadata: %#v", model["thinking"])
		}
		return
	}
	t.Fatal("model cliproxy-plain-model missing from response")
}
