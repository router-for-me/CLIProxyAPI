package openai_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	openai "github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers/openai"
)

// 验证：当存在 Zhipu 与 MiniMax 的 Claude 兼容配置时，/v1/models 返回包含 glm-4.6 与 MiniMax-M2。
func TestOpenAIModels_ContainsZhipuAndMiniMaxModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 使用 Service.registerModelsForAuth 注入两条 auth：Zhipu 与 MiniMax 兼容端点
	// 直接向全局注册表注册最小模型集，以避免跨包访问未导出方法
	zID := "test:provider:zhipu"
	mID := "test:provider:minimax"
	// 仅 glm-4.6
	onlyZ := make([]*registry.ModelInfo, 0, 1)
	for _, m := range registry.GetZhipuModels() {
		if m != nil && m.ID == "glm-4.6" {
			onlyZ = append(onlyZ, m)
			break
		}
	}
	registry.GetGlobalRegistry().RegisterClient(zID, "zhipu", onlyZ)
	// 仅 MiniMax-M2
	onlyM := make([]*registry.ModelInfo, 0, 1)
	for _, m := range registry.GetMiniMaxModels() {
		if m != nil && m.ID == "MiniMax-M2" {
			onlyM = append(onlyM, m)
			break
		}
	}
	registry.GetGlobalRegistry().RegisterClient(mID, "minimax", onlyM)
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(zID)
		registry.GetGlobalRegistry().UnregisterClient(mID)
	})

	base := handlers.NewBaseAPIHandlers(nil, nil)
	h := openai.NewOpenAIAPIHandler(base)

	r := gin.New()
	r.GET("/v1/models", h.OpenAIModels)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}
	var body struct {
		Object string           `json:"object"`
		Data   []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if body.Object != "list" {
		t.Fatalf("expected object=list, got %s", body.Object)
	}
	var seenGLM46, seenM2 bool
	for _, m := range body.Data {
		if id, _ := m["id"].(string); id == "glm-4.6" {
			seenGLM46 = true
		}
		if id, _ := m["id"].(string); id == "MiniMax-M2" {
			seenM2 = true
		}
	}
	if !seenGLM46 {
		t.Fatalf("expected glm-4.6 present in /v1/models")
	}
	if !seenM2 {
		t.Fatalf("expected MiniMax-M2 present in /v1/models")
	}
}
