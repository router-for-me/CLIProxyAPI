package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
)

// TestOpenAIModelsPassesCommandCodeContextFields verifies that /v1/models
// passes context-window metadata through for Command Code models only.
// Other channels keep the historical 4-field listing shape; generalizing the
// passthrough is an upstream design decision.
func TestOpenAIModelsPassesCommandCodeContextFields(t *testing.T) {
	const clientID = "openai-models-ctx-fields-client"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, "commandcode", []*registry.ModelInfo{
		{
			ID:            "meta/muse-spark-1.2-contributor",
			Object:        "model",
			OwnedBy:       "commandcode",
			Type:          "commandcode",
			ContextLength: 1048576,
		},
		{
			// Zero-value ContextLength must not emit the field.
			ID:      "commandcode/no-context-model",
			Object:  "model",
			OwnedBy: "commandcode",
			Type:    "commandcode",
		},
		{
			// Non-commandcode models with a ContextLength must stay on the
			// 4-field shape until upstream decides otherwise.
			ID:            "gemini-3.7-flash-high",
			Object:        "model",
			OwnedBy:       "antigravity",
			Type:          "antigravity",
			ContextLength: 1048576,
		},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
	})

	base := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, nil)
	handler := NewOpenAIAPIHandler(base)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rr := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rr)
	c.Request = req
	handler.OpenAIModels(c)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var resp struct {
		Object string           `json:"object"`
		Data   []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response JSON: %v; body=%s", err, rr.Body.String())
	}

	byID := make(map[string]map[string]any)
	for _, model := range resp.Data {
		id, _ := model["id"].(string)
		byID[id] = model
	}

	muse := byID["meta/muse-spark-1.2-contributor"]
	if muse == nil {
		t.Fatal("missing commandcode model in response")
	}
	if got, ok := muse["context_length"].(float64); !ok || int(got) != 1048576 {
		t.Fatalf("context_length = %#v, want 1048576", muse["context_length"])
	}
	if _, exists := muse["display_name"]; exists {
		t.Fatalf("display_name must stay filtered out, got %#v", muse["display_name"])
	}

	noCtx := byID["commandcode/no-context-model"]
	if noCtx == nil {
		t.Fatal("missing zero-context commandcode model in response")
	}
	if _, exists := noCtx["context_length"]; exists {
		t.Fatal("context_length must be omitted when registry value is 0")
	}

	other := byID["gemini-3.7-flash-high"]
	if other == nil {
		t.Fatal("missing non-commandcode model in response")
	}
	if _, exists := other["context_length"]; exists {
		t.Fatal("context_length must NOT leak for non-commandcode channels")
	}
}
