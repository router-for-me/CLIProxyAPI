package claude

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
)

// 验证：Claude handler 的模型列表在注册了 Zhipu 与 MiniMax 后包含 glm-4.6 与 MiniMax-M2。
// 该 handler 即为服务在 User-Agent=claude-cli 情况下使用的模型列表。
func TestClaudeModels_ContainsZhipuAndMiniMaxModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 最小注册：仅 glm-4.6 与 MiniMax-M2
	zID := "test:provider:zhipu:claude"
	mID := "test:provider:minimax:claude"
	onlyZ := make([]*registry.ModelInfo, 0, 1)
	for _, m := range registry.GetZhipuModels() {
		if m != nil && m.ID == "glm-4.6" {
			onlyZ = append(onlyZ, m)
			break
		}
	}
	registry.GetGlobalRegistry().RegisterClient(zID, "zhipu", onlyZ)
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
	h := NewClaudeCodeAPIHandler(base)

	r := gin.New()
	r.GET("/v1/models", h.ClaudeModels)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	// 模拟 Claude CLI 的 UA（路由在集成层判断，这里直接调用 Claude handler）
	req.Header.Set("User-Agent", "claude-cli/1.0")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal error: %v", err)
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
		t.Fatalf("expected glm-4.6 present in Claude models")
	}
	if !seenM2 {
		t.Fatalf("expected MiniMax-M2 present in Claude models")
	}
}
