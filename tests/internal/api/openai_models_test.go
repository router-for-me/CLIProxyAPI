package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	handlers2 "github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers/openai"
)

func TestOpenAIModels_IncludesPackycodeModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 模拟注册表中已有 packycode 模型注册
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("packycode:test", "packycode", registry.GetOpenAIModels())
	t.Cleanup(func() {
		reg.UnregisterClient("packycode:test")
	})

	baseHandler := handlers2.NewBaseAPIHandlers(nil, nil)
	openaiHandler := openai.NewOpenAIAPIHandler(baseHandler)

	router := gin.New()
	// 直接注册 OpenAI models handler，避免依赖 server 内部细节
	router.GET("/v1/models", openaiHandler.OpenAIModels)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}

	var response struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}

	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if response.Object != "list" {
		t.Fatalf("expected object=list, got %s", response.Object)
	}

	foundPackycode := false
	for _, model := range response.Data {
		if model.OwnedBy == "openai" { // packycode models reuse OpenAI metadata
			foundPackycode = true
			break
		}
	}

	if !foundPackycode {
		t.Fatalf("expected packycode models present in response")
	}
}
