package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestOpenAIModelsIncludesModelDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)

	modelRegistry := registry.GetGlobalRegistry()
	clientID := "openai-model-details-test-client"
	modelRegistry.RegisterClient(clientID, "openai", []*registry.ModelInfo{{
		ID:                  "test-model-details",
		Object:              "model",
		Created:             123,
		OwnedBy:             "test-owner",
		Type:                "openai",
		DisplayName:         "Test Model Details",
		ContextLength:       128000,
		MaxCompletionTokens: 32768,
		InputTokenLimit:     128000,
		OutputTokenLimit:    32768,
		SupportedParameters: []string{"tools"},
		Thinking: &registry.ThinkingSupport{
			Min:            128,
			Max:            32768,
			ZeroAllowed:    true,
			DynamicAllowed: true,
			Levels:         []string{"minimal", "low", "high", "xhigh"},
		},
	}})
	defer modelRegistry.UnregisterClient(clientID)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.GET("/v1/models", handler.OpenAIModels)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Object string           `json:"object"`
		Data   []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	var model map[string]any
	for _, candidate := range response.Data {
		if candidate["id"] == "test-model-details" {
			model = candidate
			break
		}
	}
	if model == nil {
		t.Fatalf("model test-model-details not found in response: %#v", response.Data)
	}

	if got := model["context_length"]; got != float64(128000) {
		t.Fatalf("context_length = %#v, want 128000", got)
	}
	if got := model["max_completion_tokens"]; got != float64(32768) {
		t.Fatalf("max_completion_tokens = %#v, want 32768", got)
	}
	if got := model["inputTokenLimit"]; got != float64(128000) {
		t.Fatalf("inputTokenLimit = %#v, want 128000", got)
	}
	if got := model["outputTokenLimit"]; got != float64(32768) {
		t.Fatalf("outputTokenLimit = %#v, want 32768", got)
	}

	thinking, ok := model["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking = %#v, want object", model["thinking"])
	}
	if got := thinking["max"]; got != float64(32768) {
		t.Fatalf("thinking.max = %#v, want 32768", got)
	}
	levels, ok := thinking["levels"].([]any)
	if !ok || len(levels) != 4 || levels[0] != "minimal" {
		t.Fatalf("thinking.levels = %#v, want [minimal low high xhigh]", thinking["levels"])
	}
	supportedLevels, ok := thinking["supported_levels"].([]any)
	if !ok || len(supportedLevels) != 6 || supportedLevels[0] != "none" || supportedLevels[1] != "auto" || supportedLevels[2] != "minimal" {
		t.Fatalf("thinking.supported_levels = %#v, want [none auto minimal low high xhigh]", thinking["supported_levels"])
	}
	levelBudgets, ok := thinking["level_budgets"].(map[string]any)
	if !ok {
		t.Fatalf("thinking.level_budgets = %#v, want object", thinking["level_budgets"])
	}
	if got := levelBudgets["xhigh"]; got != float64(32768) {
		t.Fatalf("thinking.level_budgets.xhigh = %#v, want 32768", got)
	}
}
