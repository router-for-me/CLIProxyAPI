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

// 验证：当注册表存在 provider=copilot 的模型注册时，/v1/models 返回非空列表（OpenAI 视图）。
func TestOpenAIModels_IncludesCopilotModels(t *testing.T) {
    gin.SetMode(gin.TestMode)

    reg := registry.GetGlobalRegistry()
    reg.RegisterClient("copilot:test", "copilot", registry.GetOpenAIModels())
    t.Cleanup(func() { reg.UnregisterClient("copilot:test") })

    baseHandler := handlers2.NewBaseAPIHandlers(nil, nil)
    openaiHandler := openai.NewOpenAIAPIHandler(baseHandler)

    r := gin.New()
    r.GET("/v1/models", openaiHandler.OpenAIModels)

    req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)

    if w.Code != http.StatusOK {
        t.Fatalf("expected 200 OK, got %d", w.Code)
    }
    var resp struct {
        Object string `json:"object"`
        Data   []map[string]any `json:"data"`
    }
    if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
        t.Fatalf("unmarshal error: %v", err)
    }
    if resp.Object != "list" || len(resp.Data) == 0 {
        t.Fatalf("expected non-empty models list for copilot")
    }
}

