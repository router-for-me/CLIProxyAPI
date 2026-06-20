# @sdk/access 开发指引

`github.com/router-for-me/CLIProxyAPI/v6/sdk/access` 包负责代理的入站访问认证。它提供一个轻量的管理器，用于按顺序链接多种凭证校验实现，让服务器在 CLI 运行时内外都能复用相同的访问控制逻辑。

## 引用方式

```go
import (
    sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
)
```

通过 `go get github.com/router-for-me/CLIProxyAPI/v6/sdk/access` 添加依赖。

## Provider Registry

访问提供者是全局注册，然后以快照形式挂到 `Manager` 上：

- `RegisterProvider(type, provider)` 注册一个已经初始化好的 provider 实例。
- 每个 `type` 第一次出现时会记录其注册顺序。
- `RegisteredProviders()` 会按该顺序返回 provider 列表。

## 管理器生命周期

```go
manager := sdkaccess.NewManager()
manager.SetProviders(sdkaccess.RegisteredProviders())
```

- `NewManager` 创建空管理器。
- `SetProviders` 替换提供者切片并做防御性拷贝。
- `Providers` 返回适合并发读取的快照。

如果管理器本身为 `nil` 或未配置任何 provider，调用会返回 `nil, nil`，可视为关闭访问控制。

## 认证请求

```go
result, authErr := manager.Authenticate(ctx, req)
switch {
case authErr == nil:
    // Authentication succeeded; result carries provider and principal.
case sdkaccess.IsAuthErrorCode(authErr, sdkaccess.AuthErrorCodeNoCredentials):
    // No recognizable credentials were supplied.
case sdkaccess.IsAuthErrorCode(authErr, sdkaccess.AuthErrorCodeInvalidCredential):
    // Credentials were present but rejected.
default:
    // Provider surfaced a transport-level failure.
}
```

`Manager.Authenticate` 会按顺序遍历 provider：遇到成功立即返回，`AuthErrorCodeNotHandled` 会继续尝试下一个；`AuthErrorCodeNoCredentials` / `AuthErrorCodeInvalidCredential` 会在遍历结束后汇总给调用方。

`Result` 提供认证提供者标识、解析出的主体以及可选元数据（例如凭证来源）。

## 内建 `config-api-key` Provider

代理内置一个访问提供者：

- `config-api-key`：校验 `config.yaml` 顶层的 `api-keys`。
  - 凭证来源：`Authorization: Bearer`、`X-Goog-Api-Key`、`X-Api-Key`、`?key=`、`?auth_token=`
  - 元数据：`Result.Metadata["source"]` 会写入匹配到的来源标识

在 CLI 服务端与 `sdk/cliproxy` 中，该 provider 会根据加载到的配置自动注册。

```yaml
api-keys:
  - sk-test-123
  - sk-prod-456
```

## 引入外部 Go 模块提供者

若要消费其它 Go 模块输出的访问提供者，直接用空白标识符导入以触发其 `init` 注册即可：

```go
import (
    _ "github.com/acme/xplatform/sdk/access/providers/partner" // registers partner-token
    sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
)
```

空白导入可确保 `init` 先执行，从而在你调用 `RegisteredProviders()`（或 `cliproxy.NewBuilder().Build()`）之前完成 `sdkaccess.RegisterProvider`。

### 元数据与审计

`Result.Metadata` 用于携带提供者特定的上下文信息。内建的 `config-api-key` 会记录凭证来源（`authorization`、`x-goog-api-key`、`x-api-key`、`query-key`、`query-auth-token`）。自定义提供者同样可以填充该 Map，以便丰富日志与审计场景。

## 编写自定义提供者

```go
type customProvider struct{}

func (p *customProvider) Identifier() string { return "my-provider" }

func (p *customProvider) Authenticate(ctx context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
    token := r.Header.Get("X-Custom")
    if token == "" {
        return nil, sdkaccess.NewNotHandledError()
    }
    if token != "expected" {
        return nil, sdkaccess.NewInvalidCredentialError()
    }
    return &sdkaccess.Result{
        Provider:  p.Identifier(),
        Principal: "service-user",
        Metadata:  map[string]string{"source": "x-custom"},
    }, nil
}

func init() {
    sdkaccess.RegisterProvider("custom", &customProvider{})
}
```

自定义提供者需要实现 `Identifier()` 与 `Authenticate()`。在 `init` 中用已初始化实例调用 `RegisterProvider` 注册到全局 registry。

## 错误语义

- `NewNoCredentialsError()`（`AuthErrorCodeNoCredentials`）：未提供或未识别到凭证。（HTTP 401）
- `NewInvalidCredentialError()`（`AuthErrorCodeInvalidCredential`）：凭证存在但校验失败。（HTTP 401）
- `NewNotHandledError()`（`AuthErrorCodeNotHandled`）：告诉管理器跳到下一个 provider。
- `NewInternalAuthError(message, cause)`（`AuthErrorCodeInternal`）：网络/系统错误。（HTTP 500）

除可汇总的 `not_handled` / `no_credentials` / `invalid_credential` 外，其它错误会立即冒泡返回。

## 与 cliproxy 集成

使用 `sdk/cliproxy` 构建服务时会自动接入 `@sdk/access`。如果希望在宿主进程里复用同一个 `Manager` 实例，可传入自定义管理器：

```go
coreCfg, _ := config.LoadConfig("config.yaml")
accessManager := sdkaccess.NewManager()

svc, _ := cliproxy.NewBuilder().
  WithConfig(coreCfg).
  WithConfigPath("config.yaml").
  WithRequestAccessManager(accessManager).
  Build()
```

请在调用 `Build()` 之前完成自定义 provider 的注册（通常通过空白导入触发 `init`），以确保它们被包含在全局 registry 的快照中。

### 动态热更新提供者

当配置发生变化时，刷新依赖配置的 provider，然后重置 manager 的 provider 链：

```go
// configaccess is github.com/router-for-me/CLIProxyAPI/v6/internal/access/config_access
configaccess.Register(&newCfg.SDKConfig)
accessManager.SetProviders(sdkaccess.RegisteredProviders())
```

这一流程与 `internal/access.ApplyAccessProviders` 保持一致，避免为更新访问策略而重启进程。
