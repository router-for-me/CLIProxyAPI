package openai

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/registry"
)

func TestOpenAIModels_ExposesProviderPinnedAliasesForCollidingIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)

	reg := registry.GetGlobalRegistry()
	openaiClientID := "openai-model-alias-test-client-openai"
	copilotClientID := "openai-model-alias-test-client-copilot"
	modelID := "gpt-5.2"

	reg.RegisterClient(openaiClientID, "openai", []*registry.ModelInfo{{
		ID:      modelID,
		Object:  "model",
		Created: 1763424000,
		OwnedBy: "openai",
		Type:    "openai",
	}})
	reg.RegisterClient(copilotClientID, "github-copilot", []*registry.ModelInfo{{
		ID:      modelID,
		Object:  "model",
		Created: 1732752000,
		OwnedBy: "github-copilot",
		Type:    "github-copilot",
	}})
	t.Cleanup(func() {
		reg.UnregisterClient(openaiClientID)
		reg.UnregisterClient(copilotClientID)
	})

	h := &OpenAIAPIHandler{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	h.OpenAIModels(c)

	if w.Code != 200 {
		t.Fatalf("OpenAIModels status = %d, want 200", w.Code)
	}

	var resp struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	seen := make(map[string]string, len(resp.Data))
	for _, model := range resp.Data {
		seen[model.ID] = model.OwnedBy
	}

	if _, ok := seen[modelID]; !ok {
		t.Fatalf("expected base model %q in /v1/models listing", modelID)
	}
	if got := seen["openai/"+modelID]; got != "openai" {
		t.Fatalf("expected openai/%s owned_by=openai, got %q", modelID, got)
	}
	if got := seen["github-copilot/"+modelID]; got != "github-copilot" {
		t.Fatalf("expected github-copilot/%s owned_by=github-copilot, got %q", modelID, got)
	}
}
