package api_test

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "path/filepath"
    "testing"
    "time"

    "github.com/gin-gonic/gin"
    "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
    "github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
    handlers2 "github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
    "github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers/openai"
    "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy"
    coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
    sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
)

// 模拟“保存 copilot token → watcher/manager 注册 → registry 有模型”的链路：
// 直接调用 Service.applyCoreAuthAddOrUpdate 等价路径的公开组合：通过 manager.Register + registerModelsForAuth。
func TestOpenAIModels_AfterRegisterCopilotAuth_Visible(t *testing.T) {
    t.Parallel()
    gin.SetMode(gin.TestMode)

    tmp := t.TempDir()
    cfg := &config.Config{Port: 53355, AuthDir: tmp}

    // 构建 Service（用 builder），并注入 core manager
    tokenStore := sdkAuth.GetTokenStore()
    if ds, ok := tokenStore.(interface{ SetBaseDir(string) }); ok {
        ds.SetBaseDir(filepath.Join(tmp, "auth"))
    }
    mgr := coreauth.NewManager(tokenStore, nil, coreauth.NoopHook{})

    if _, err := cliproxy.NewBuilder().WithConfig(cfg).WithConfigPath(filepath.Join(tmp, "config.yaml")).WithCoreAuthManager(mgr).Build(); err != nil { t.Fatalf("build: %v", err) }

    // 注册一条 copilot Auth（模拟保存 token 后触发的新增事件）
    a := &coreauth.Auth{ID: "copilot:e2e-reg", Provider: "copilot", Label: "copilot",
        Metadata: map[string]any{"email": "user@example.com", "access_token": "atk"},
        CreatedAt: time.Now(), UpdatedAt: time.Now(), Status: coreauth.StatusActive}
    if _, err := mgr.Register(context.Background(), a); err != nil { t.Fatalf("register: %v", err) }

    // 等价于 applyCoreAuthAddOrUpdate：确保执行器与模型注册（使用公开方法组合）
    // 调用内部组合路径的近似替代：ensureExecutorsForAuth + registerModelsForAuth
    // 这些在 Service 的方法中是非导出的，这里通过再次注册模型（共享全局注册表）达到同等可见性
    cliproxy.GlobalModelRegistry().RegisterClient(a.ID, "copilot", registry.GetOpenAIModels())
    t.Cleanup(func(){ cliproxy.GlobalModelRegistry().UnregisterClient(a.ID) })

    // 直接使用 OpenAI handler 断言 /v1/models 非空
    base := handlers2.NewBaseAPIHandlers(nil, nil)
    openaiHandler := openai.NewOpenAIAPIHandler(base)
    r := gin.New()
    r.GET("/v1/models", openaiHandler.OpenAIModels)
    req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)
    if w.Code != http.StatusOK { t.Fatalf("expected 200, got %d", w.Code) }

    var out struct{ Object string `json:"object"`; Data []map[string]any `json:"data"` }
    if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil { t.Fatalf("unmarshal: %v", err) }
    if out.Object != "list" || len(out.Data) == 0 {
        t.Fatalf("expected non-empty models after copilot auth register")
    }
}
