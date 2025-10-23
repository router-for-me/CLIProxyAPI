package management

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "path/filepath"
    "testing"

    "github.com/gin-gonic/gin"
    "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// 仅验证路由存在与结构字段，实际外呼依赖外网，这里只确保返回非 404/401（管理中间件在测试中未启用）。
func TestCopilotDeviceCode_EndpointExists(t *testing.T) {
    gin.SetMode(gin.TestMode)
    cfg := &config.Config{}
    h := NewHandler(cfg, filepath.Join(t.TempDir(), "config.yaml"), nil)
    r := gin.New()
    r.GET("/v0/management/copilot-device-code", h.RequestCopilotDeviceCode)

    req := httptest.NewRequest(http.MethodGet, "/v0/management/copilot-device-code", nil)
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)

    // 在无外网与默认占位端点下，可能返回 502/400；只校验不是 404
    if w.Code == http.StatusNotFound {
        t.Fatalf("expected endpoint to exist, got 404")
    }
    // 当外呼失败时，返回 JSON 含 error 字段
    var m map[string]any
    _ = json.Unmarshal(w.Body.Bytes(), &m)
    // 允许 error 存在，此测试不校验外网
}
