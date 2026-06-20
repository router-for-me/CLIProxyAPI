# CLI Proxy SDK 使用指南

`sdk/cliproxy` 模块将代理能力以 Go 库的形式对外暴露，方便在其它服务中内嵌路由、鉴权、热更新与翻译层，而无需依赖可执行的 CLI 程序。

## 安装与导入

```bash
go get github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy
```

```go
import (
    "context"
    "errors"
    "time"

    "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
    "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy"
)
```

注意模块路径包含 `/v6`。

## 最小可用示例

```go
cfg, err := config.LoadConfig("config.yaml")
if err != nil { panic(err) }

svc, err := cliproxy.NewBuilder().
    WithConfig(cfg).
    WithConfigPath("config.yaml"). // 绝对路径或工作目录相对路径
    Build()
if err != nil { panic(err) }

ctx, cancel := context.WithCancel(context.Background())
defer cancel()

if err := svc.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
    panic(err)
}
```

服务内部会管理配置与认证文件的监听、后台令牌刷新与优雅关闭。取消上下文即可停止服务。

## 服务器可选项（中间件、路由、日志）

通过 `WithServerOptions` 自定义：

```go
svc, _ := cliproxy.NewBuilder().
  WithConfig(cfg).
  WithConfigPath("config.yaml").
  WithServerOptions(
    // 追加全局中间件
    cliproxy.WithMiddleware(func(c *gin.Context) { c.Header("X-Embed", "1"); c.Next() }),
    // 提前调整 gin 引擎（如 CORS、trusted proxies）
    cliproxy.WithEngineConfigurator(func(e *gin.Engine) { e.ForwardedByClientIP = true }),
    // 在默认路由之后追加自定义路由
    cliproxy.WithRouterConfigurator(func(e *gin.Engine, _ *handlers.BaseAPIHandler, _ *config.Config) {
      e.GET("/healthz", func(c *gin.Context) { c.String(200, "ok") })
    }),
    // 覆盖请求日志的创建（启用/目录）
    cliproxy.WithRequestLoggerFactory(func(cfg *config.Config, cfgPath string) logging.RequestLogger {
      return logging.NewFileRequestLogger(true, "logs", filepath.Dir(cfgPath))
    }),
  ).
  Build()
```

这些选项与 CLI 服务器内部用法保持一致。

## 管理 API（内嵌时）

- 仅当 `config.yaml` 中设置了 `remote-management.secret-key` 时才会挂载管理端点。
- 远程访问还需要 `remote-management.allow-remote: true`。
- 具体端点见 MANAGEMENT_API_CN.md。内嵌服务器会在配置端口下暴露 `/v0/management`。

## 使用核心鉴权管理器

服务内部使用核心 `auth.Manager` 负责选择、执行、自动刷新。内嵌时可自定义其传输或钩子：

```go
core := coreauth.NewManager(coreauth.NewFileStore(cfg.AuthDir), nil, nil)
core.SetRoundTripperProvider(myRTProvider) // 按账户返回 *http.Transport

svc, _ := cliproxy.NewBuilder().
    WithConfig(cfg).
    WithConfigPath("config.yaml").
    WithCoreAuthManager(core).
    Build()
```

实现每个账户的自定义传输：

```go
type myRTProvider struct{}
func (myRTProvider) RoundTripperFor(a *coreauth.Auth) http.RoundTripper {
    if a == nil || a.ProxyURL == "" { return nil }
    u, _ := url.Parse(a.ProxyURL)
    return &http.Transport{ Proxy: http.ProxyURL(u) }
}
```

管理器提供编程式执行接口：

```go
// 非流式
resp, err := core.Execute(ctx, []string{"gemini"}, req, opts)

// 流式
chunks, err := core.ExecuteStream(ctx, []string{"gemini"}, req, opts)
for ch := range chunks { /* ... */ }
```

说明：运行 `Service` 时会自动注册内置的提供商执行器；若仅单独使用 `Manager` 而不启动 HTTP 服务器，则需要自行实现并注册满足 `auth.ProviderExecutor` 的执行器。

## 自定义凭据来源

当凭据不在本地文件系统时，替换默认加载器：

```go
type memoryTokenProvider struct{}
func (p *memoryTokenProvider) Load(ctx context.Context, cfg *config.Config) (*cliproxy.TokenClientResult, error) {
    // 从内存/远端加载并返回数量统计
    return &cliproxy.TokenClientResult{}, nil
}

svc, _ := cliproxy.NewBuilder().
  WithConfig(cfg).
  WithConfigPath("config.yaml").
  WithTokenClientProvider(&memoryTokenProvider{}).
  WithAPIKeyClientProvider(cliproxy.NewAPIKeyClientProvider()).
  Build()
```

## 启动钩子

无需修改内部代码即可观察生命周期：

```go
hooks := cliproxy.Hooks{
  OnBeforeStart: func(cfg *config.Config) { log.Infof("starting on :%d", cfg.Port) },
  OnAfterStart:  func(s *cliproxy.Service) { log.Info("ready") },
}
svc, _ := cliproxy.NewBuilder().WithConfig(cfg).WithConfigPath("config.yaml").WithHooks(hooks).Build()
```

## 关闭

`Run` 内部会延迟调用 `Shutdown`，因此只需取消父上下文即可。若需手动停止：

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
_ = svc.Shutdown(ctx)
```

## 说明

- 热更新：`config.yaml` 与 `auths/` 变化会被自动侦测并应用。
- 请求日志可通过管理 API 在运行时开关。
- `gemini-web.*` 相关配置在内嵌服务器中会被遵循。

