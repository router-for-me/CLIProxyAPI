# SDK 高级指南：执行器与翻译器

本文介绍如何使用 SDK 扩展内嵌代理：
- 实现自定义 Provider 执行器以调用你的上游 API
- 注册请求/响应翻译器进行协议转换
- 注册模型以出现在 `/v1/models`

示例基于 Go 1.24+ 与 v6 模块路径。

## 概念

- Provider 执行器：实现 `auth.ProviderExecutor` 的运行时组件，负责某个 provider key（如 `gemini`、`claude`、`codex`）的真正出站调用。若实现 `RequestPreparer` 接口，可在原始 HTTP 请求上注入凭据。
- 翻译器注册表：由 `sdk/translator` 驱动的协议转换函数。内置了 OpenAI/Gemini/Claude/Codex 的互转；你也可以注册新的格式转换。
- 模型注册表：对外发布可用模型列表，供 `/v1/models` 与路由参考。

## 1) 实现 Provider 执行器

创建类型满足 `auth.ProviderExecutor` 接口。

```go
package myprov

import (
    "context"
    "net/http"

    coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
    clipexec "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type Executor struct{}

func (Executor) Identifier() string { return "myprov" }

// 可选：在原始 HTTP 请求上注入凭据
func (Executor) PrepareRequest(req *http.Request, a *coreauth.Auth) error {
    // 例如：req.Header.Set("Authorization", "Bearer "+a.Attributes["api_key"]) 
    return nil
}

func (Executor) Execute(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (clipexec.Response, error) {
    // 基于 req.Payload 构造上游请求，返回上游 JSON 负载
    return clipexec.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (Executor) ExecuteStream(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (<-chan clipexec.StreamChunk, error) {
    ch := make(chan clipexec.StreamChunk, 1)
    go func() { defer close(ch); ch <- clipexec.StreamChunk{Payload: []byte("data: {\\"done\\":true}\\n\\n")} }()
    return ch, nil
}

func (Executor) Refresh(ctx context.Context, a *coreauth.Auth) (*coreauth.Auth, error) { return a, nil }
```

在启动服务前将执行器注册到核心管理器：

```go
core := coreauth.NewManager(coreauth.NewFileStore(cfg.AuthDir), nil, nil)
core.RegisterExecutor(myprov.Executor{})
svc, _ := cliproxy.NewBuilder().WithConfig(cfg).WithConfigPath(cfgPath).WithCoreAuthManager(core).Build()
```

当凭据的 `Provider` 为 `"myprov"` 时，管理器会将请求路由到你的执行器。

## 2) 注册翻译器

内置处理器接受 OpenAI/Gemini/Claude/Codex 的入站格式。要支持新的 provider 协议，需要在 `sdk/translator` 的默认注册表中注册转换函数。

方向很重要：
- 请求：从“入站格式”转换为“provider 格式”
- 响应：从“provider 格式”转换回“入站格式”

示例：OpenAI Chat → MyProv Chat 及其反向。

```go
package myprov

import (
  "context"
  sdktr "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

const (
  FOpenAI = sdktr.Format("openai.chat")
  FMyProv = sdktr.Format("myprov.chat")
)

func init() {
  sdktr.Register(FOpenAI, FMyProv,
    func(model string, raw []byte, stream bool) []byte { return convertOpenAIToMyProv(model, raw, stream) },
    sdktr.ResponseTransform{
      Stream: func(ctx context.Context, model string, originalReq, translatedReq, raw []byte, param *any) []string {
        return convertStreamMyProvToOpenAI(model, originalReq, translatedReq, raw)
      },
      NonStream: func(ctx context.Context, model string, originalReq, translatedReq, raw []byte, param *any) string {
        return convertMyProvToOpenAI(model, originalReq, translatedReq, raw)
      },
    },
  )
}
```

当 OpenAI 处理器接到需要路由到 `myprov` 的请求时，流水线会自动应用已注册的转换。

## 3) 注册模型

通过全局模型注册表将模型暴露到 `/v1/models`：

```go
models := []*cliproxy.ModelInfo{
  { ID: "myprov-pro-1", Object: "model", Type: "myprov", DisplayName: "MyProv Pro 1" },
}
cliproxy.GlobalModelRegistry().RegisterClient(authID, "myprov", models)
```

内置 Provider 会自动注册；自定义 Provider 建议在启动时（例如加载到 Auth 后）或在 Auth 注册钩子中调用。

## 凭据与传输

- 使用 `Manager.SetRoundTripperProvider` 注入按账户的 `*http.Transport`（例如代理）：
  ```go
  core.SetRoundTripperProvider(myProvider) // 按账户返回 transport
  ```
- 对于原始 HTTP 请求，若实现了 `PrepareRequest`，或通过 `Manager.InjectCredentials(req, authID)` 进行头部注入。

## 测试建议

- 启用请求日志：管理 API GET/PUT `/v0/management/request-log`
- 切换调试日志：管理 API GET/PUT `/v0/management/debug`
- 热更新：`config.yaml` 与 `auths/` 变化会自动被侦测并应用

