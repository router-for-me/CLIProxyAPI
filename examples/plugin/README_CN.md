# Go 动态插件示例

这个目录是基于当前 `sdk/pluginapi` ABI 编写 provider 插件的参考骨架。它保持确定性和小规模实现，但覆盖真实 provider 插件通常需要接入的宿主能力：provider 自有 auth 解析、模型发现、执行器、HTTP bridge、请求/响应转换、thinking 配置、usage 观察、命令行参数和诊断 Management API 路由。

示例使用 provider key `plugin-example`，插件 ID 为 `example`。

## 示例实现内容

`examples/plugin/main.go` 导出了 Go 插件必须提供的入口函数：

```go
func Register(configYAML []byte) pluginapi.Plugin
func Reconfigure(configYAML []byte) pluginapi.Plugin
```

宿主第一次加载 `.so` 文件时调用 `Register`。如果插件已经打开并且仍处于启用状态，配置热重载时调用 `Reconfigure`。两个函数都必须返回包含有效 metadata 且至少带有一个能力的 `pluginapi.Plugin`。

必须填写的 metadata 字段：

- `Metadata.Name`
- `Metadata.Version`
- `Metadata.Author`
- `Metadata.GitHubRepository`

这个示例声明了以下能力：

| 能力 | 接口 | 示例展示内容 |
| --- | --- | --- |
| 静态模型和按 auth 发现模型 | `ModelProvider` | 为静态注册和 auth 绑定发现都返回 `plugin-example-model`。 |
| Auth 解析和刷新 | `AuthProvider` | 解析 `type` 为 `plugin-example` 的 auth JSON，暴露非交互式登录方法，并原样返回刷新后的存储数据。 |
| 前端鉴权 | `FrontendAuthProvider` | 仅当请求包含 `X-Plugin-Example: allow` 时接受前端请求。 |
| Provider 执行器 | `ProviderExecutor` | 实现非流式执行、流式执行、token 统计和原始 HTTP 透传。 |
| 执行器模型范围 | `ExecutorModelScope` | 使用 `pluginapi.ExecutorModelScopeBoth`，表示执行器同时支持静态模型和 OAuth/auth 绑定模型。 |
| 请求转换 | `RequestTranslator`, `RequestNormalizer` | 展示 canonical 请求和 provider 专属请求 payload 的转换位置。 |
| 响应转换 | `ResponseTranslator`, `ResponseBeforeTranslator`, `ResponseAfterTranslator` | 展示原生翻译前后的响应转换 hook。 |
| Thinking 配置 | `ThinkingApplier` | 接收 canonical thinking 配置，并写入 provider 专属 payload 字段。 |
| Usage 观察 | `UsagePlugin` | 在内存中统计已完成 usage record，供诊断接口展示。 |
| 命令行参数 | `CommandLinePlugin` | 添加插件自有 CLI 参数，并在执行时接收全部解析后的 flag 值。 |
| Management API | `ManagementAPI` | 在 `/v0/management/` 下添加精确匹配的诊断路由。 |

`sdk/pluginapi` 中仍保留 `ModelRegistrar`，用于简单的纯模型插件。新的 provider 插件通常应优先使用 `ModelProvider`，因为它通过同一条 provider-native 路径同时支持静态模型元数据和按 auth 发现模型。

## 平台和 ABI 规则

CLIProxyAPI 加载使用以下命令构建的标准 Go 插件：

```bash
go build -buildmode=plugin
```

Go 标准库 `plugin` 包支持 Linux、FreeBSD 和 macOS。在不支持的平台上，插件加载会被禁用，服务会继续使用原生逻辑运行。

Go plugin ABI 兼容性非常严格。请使用与目标服务二进制一致的环境构建插件：

- `GOOS` 和 `GOARCH`
- 使用 CPU 专属目录时的 CPU feature target
- Go 工具链版本
- build tags 和 CGO 设置
- module path
- 共享依赖版本

如果这些条件不一致，`plugin.Open` 可能失败，或者加载出的符号类型不兼容。

## 构建和安装

在仓库根目录构建：

```bash
mkdir -p plugins/$(go env GOOS)/$(go env GOARCH)
go build -buildmode=plugin -o plugins/$(go env GOOS)/$(go env GOARCH)/example.so ./examples/plugin
```

插件 ID 来自 `.so` 文件名去掉最后的 `.so` 后缀。`example.so` 对应 `plugins.configs.example`。

插件 ID 必须符合以下格式：

```text
[A-Za-z0-9][A-Za-z0-9._-]{0,127}
```

宿主按以下顺序搜索目录，并对每个插件 ID 保留第一个发现的 `.so`：

```text
plugins/<GOOS>/<GOARCH>-<variant>/*.so
plugins/<GOOS>/<GOARCH>/*.so
plugins/*.so
```

对于 `amd64`，`<variant>` 会根据 CPU 能力选择为 `v4`、`v3`、`v2` 或 `v1`。因此，CPU 专属构建可以放在类似 `plugins/linux/amd64-v3/` 的路径下。

替换已经打开的 `.so` 文件需要重启进程。Go 插件无法从当前进程中卸载。

## 配置宿主

动态插件默认关闭。请在 `config.yaml` 中启用：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    example:
      enabled: true
      priority: 1
      config1: true
      config2: "string"
      config3: 3
```

配置规则：

- `plugins.enabled=false` 会跳过所有插件加载和执行。
- `plugins.dir` 为空或未配置时默认使用 `plugins`。
- `plugins.configs.<pluginID>` 是传给 `Register` 或 `Reconfigure` 的插件专属 YAML 子树。
- 已配置插件实例的 `enabled` 默认值为 `true`。
- `priority` 默认值为 `0`。
- 如果插件配置中缺少 `enabled` 或 `priority`，宿主会把规整后的值注入到传给插件的 YAML 字节中。
- `priority` 越高，插件越先执行。相同优先级按插件 ID 排序。

热重载会更新运行时插件快照。已经打开的插件二进制仍然留在内存中，但被禁用的插件会从当前活动能力集合中移除。如果已加载插件仍处于启用状态，宿主会调用 `Reconfigure(configYAML)`，而不是再次调用 `Register(configYAML)`。

## 插件 metadata、Logo 和配置字段

插件通过 `pluginapi.Metadata` 向宿主管理接口提供展示信息：

```go
type Metadata struct {
    Name             string
    Version          string
    Author           string
    GitHubRepository string
    Logo             string
    ConfigFields     []ConfigField
}
```

`Logo` 是给管理端展示的字符串。宿主只透传该值，不校验它是 URL、data URI、文件路径或其他格式。

`ConfigFields` 描述 `plugins.configs.<pluginID>` 下的插件自定义配置字段。它只用于管理端展示和生成配置表单，宿主不会用它校验插件配置。字段结构如下：

```go
type ConfigField struct {
    Name        string
    Type        ConfigFieldType
    EnumValues  []string
    Description string
}
```

支持的 `ConfigFieldType` 值包括 `string`、`number`、`integer`、`boolean`、`enum`、`array` 和 `object`。当类型是 `enum` 时，`EnumValues` 应列出所有可选值。

## 添加 auth 材料

带执行器的插件模型需要匹配的 auth 记录，这样调度器才能选择对应 provider。auth 的 `type` 必须匹配 `ModelProvider`、`AuthProvider.Identifier` 和 `ProviderExecutor.Identifier` 返回的 provider。

这个示例对应：

```json
{
  "type": "plugin-example",
  "api_key": "plugin-or-upstream-secret"
}
```

把文件放入已配置的 auth 目录，例如：

```text
auths/plugin-example.json
```

除非你有意让原生 OpenAI-compatible 执行器拥有这个 provider，否则不要为同一个 provider 配置 `base_url`、`compat_name` 或 `openai-compatibility`。原生执行器始终优先于插件执行器。

这个示例中的 auth provider 行为：

- `ParseAuth` 接收宿主 auth loader 提供的 JSON，并返回 `pluginapi.AuthData`。
- `StartLogin` 和 `PollLogin` 存在，但在示例中返回非交互式错误。
- `RefreshAuth` 原样返回当前 auth 数据。
- 真实插件可以从命令行执行或登录轮询中返回 `AuthData`；宿主会通过正常 auth store 持久化这些数据。

## 模型注册和执行器范围

当前 provider-native 模型路径是 `ModelProvider`：

- `StaticModels` 返回不依赖具体 auth 记录即可使用的 provider 模型。
- `ModelsForAuth` 返回为某个选中 auth 记录发现的模型；如果发现过程刷新了 provider 状态，也可以返回 `AuthUpdate`。

插件发现模型后，宿主会继续应用正常模型处理流程：别名、排除模型、前缀、registry reconcile 和调度规则。

当 `Capabilities.Executor` 存在时，`ExecutorModelScope` 控制允许的模型注册路径：

| Scope | 含义 |
| --- | --- |
| `pluginapi.ExecutorModelScopeBoth` | 执行器同时支持静态模型和 auth 绑定的 OAuth 风格模型。scope 为空或非法时默认使用这个值。 |
| `pluginapi.ExecutorModelScopeStatic` | 执行器只支持非 OAuth 的静态模型。执行器模型注册会跳过 `ModelsForAuth`。 |
| `pluginapi.ExecutorModelScopeOAuth` | 执行器只支持 auth 绑定模型。不会注册静态 executor model client。 |

请使用与 provider 匹配的最窄 scope，避免通过错误的注册路径暴露模型。

## 执行流程

插件执行器只会在以下条件全部满足时运行：

- 全局插件已启用；
- 当前插件已启用；
- 当前插件没有被 panic fuse；
- 选中的 auth provider 匹配执行器 provider；
- 没有原生执行器拥有同一个 provider 或选中的模型；
- 没有更高优先级插件已经声明同一个 provider/model。

`ProviderExecutor` 会收到 `pluginapi.ExecutorRequest`，其中包括：

- `Model`：经过宿主别名处理后的模型 ID；
- `Format`：目标 provider 格式；
- `SourceFormat`：客户端原始格式；
- `OriginalRequest`：客户端原始 payload；
- `Payload`：已经翻译到 provider 侧的 payload；
- `StorageJSON`、`AuthMetadata` 和 `AuthAttributes`：选中 auth 的状态；
- `HTTPClient`：宿主 HTTP bridge。

执行器访问上游 HTTP 时必须使用 `req.HTTPClient.Do` 或 `req.HTTPClient.DoStream`。不要在插件内部自行构造 proxy-aware client。宿主 bridge 会保持宿主传输策略，并且让 `request-log` 在插件转换响应前记录发往上游的请求和上游返回的原始响应。

示例方法刻意保持确定性：

- `Execute` 返回一个 OpenAI 形态的 JSON 响应。
- `ExecuteStream` 输出一个 stream chunk 后关闭 channel。
- `CountTokens` 返回 0 token 统计。
- `HttpRequest` 通过宿主 bridge 转发原始 HTTP。

真实 provider 中应使用 `req.Model` 做 provider 路由和模型改写判断。不要假设每种协议 payload 都有可信的顶层 `model` 字段。

## Translator、Normalizer 和 Thinking

原生逻辑是权威实现。插件转换用于补齐空白，而不是替换内置 provider 支持。

请求和响应行为：

- 请求 normalizer 按优先级从高到低链式执行。
- 翻译前和翻译后的响应 normalizer 也遵循同样的优先级顺序。
- 只有当某个格式转换不存在原生 translator 时，请求 translator 和响应 translator 才会运行。
- 对于缺失的翻译路径，只会选择优先级最高的一个插件 translator。

Thinking 行为：

- 宿主集中解析、规整并验证 thinking 配置。
- `ThinkingApplier` 接收 canonical `pluginapi.ThinkingConfig`。
- 插件 thinking applier 只会处理没有原生 thinking provider 拥有的 provider key。
- 插件被禁用、从活动快照中移除或被 panic fuse 后，它的 thinking applier 会被移除。

示例会向 payload 写入这些 provider 专属字段：

```json
{
  "plugin_example_thinking": {
    "mode": "budget",
    "budget": 1024,
    "level": ""
  }
}
```

## 命令行参数

示例声明了两个插件自有参数：

```bash
./cli-proxy-api -config config.yaml -plugin-example-command
./cli-proxy-api -config config.yaml -plugin-example-command -plugin-example-message "custom message"
```

插件命令行参数会在正常 flag 解析前注册，因此会显示在 `-help` 中。

规则：

- 支持的 flag 类型为 `bool`、`string`、`int`、`int64`、`float64` 和 `duration`。
- flag 名称不能以 `-` 开头，不能包含空白字符，不能包含 `=`，也不能是 `help` / `h`。
- 原生 flag 不能被替换。
- 更高优先级插件的 flag 不能被低优先级插件替换。
- 当提供了任意插件自有 flag 时，宿主会把所有参数、所有可见的已解析 flag，以及触发执行的插件自有 flag 传给 `ExecuteCommandLine`。
- 如果最终配置禁用了全局插件或当前插件，flag 仍可能被解析，但插件执行会被跳过。
- 如果 `ExecuteCommandLine` 返回 `Auths`，宿主会通过已配置的 auth store 持久化它们，并把保存路径追加到 stdout。

## Management API 路由

宿主提供原生插件管理接口：

```text
GET /v0/management/plugins
PATCH /v0/management/plugins/{pluginID}/enabled
PUT /v0/management/plugins/{pluginID}/config
PATCH /v0/management/plugins/{pluginID}/config
```

`GET /v0/management/plugins` 会按宿主当前扫描规则列出插件目录中的 `.so` 文件，也会列出只存在于 `plugins.configs` 中的配置项。已成功注册的插件会返回 `logo`、`config_fields` 和 `supports_oauth`。

如果插件注册的 Management API 路由是 `GET` 方法，并且 `ManagementRoute.Menu` 不为空，`GET /v0/management/plugins` 会在该插件条目的 `menus` 数组中返回 `path`、`menu` 和 `description`。`Menu` 用作管理端菜单名称，`Description` 用作菜单说明。

`PATCH /v0/management/plugins/{pluginID}/enabled` 只更新 `plugins.configs.<pluginID>.enabled`，不会隐式修改全局 `plugins.enabled`。因此当 `plugins.enabled=false` 时，单插件可以显示为启用，但实际运行时仍不会加载插件能力。

`PUT /v0/management/plugins/{pluginID}/config` 会替换整个插件配置子树。`PATCH /v0/management/plugins/{pluginID}/config` 会做浅层合并；请求中的 `null` 会删除对应字段。

示例路由：

```text
GET /v0/management/plugins/example/status
GET /v0/management/plugins/example/capabilities
```

Management API 路由规则：

- 路由是 `/v0/management/` 下按 method/path 精确匹配的路由。
- 插件可以返回类似 `/plugins/example/status` 的相对路径；宿主会把它解析到 `/v0/management` 下。
- 路径不能包含空白字符、`:` 或 `*`。
- 原生 Management API 路由不能被替换。
- 更高优先级插件的路由不能被低优先级插件替换。
- 路由仍需要正常的 Management API 鉴权。
- 当 Home 模式或 Management API 可用性禁用本地 Management 路由时，这些路由不可用。
- 路由表会在配置热重载时重建。

## 前端鉴权

示例 `FrontendAuthProvider` 只接受带有以下 header 的请求：

```text
X-Plugin-Example: allow
```

注册后的前端 provider key 会被宿主命名空间化：

```text
plugin:<pluginID>:<providerIdentifier>
```

这个示例的 provider identifier 是 `plugin-example`，因此下游 auth metadata 会与原生前端鉴权 provider 隔离。

## Usage 插件

`UsagePlugin.HandleUsage` 会在请求执行完成后收到 usage record。示例会递增内存计数器，并通过诊断 Management API status 路由展示。

Usage record 包含 provider、executor type、model、alias、选中 auth、source、请求的 reasoning effort、service tier、latency、TTFT、失败详情、token 计数和选定响应头。

这个 hook 应保持轻量。Usage 派发属于请求计费/统计路径，宿主会从 panic 中恢复并 fuse 插件。

## 优先级、原生优先和 panic fuse

插件系统是增量扩展机制：

- 原生 provider、executor、translator、thinking applier、flag 和 Management route 都优先于插件。
- 插件用于补齐 provider 空白并增加插件自有能力面。
- 高优先级插件先于低优先级插件被考虑。
- 插件执行器不会覆盖原生执行器。
- 插件 Management 路由和命令行 flag 不会覆盖原生路由或 flag。

每个生命周期调用和能力调用都带有 panic recovery。如果插件在 `Register`、`Reconfigure` 或任意能力方法中 panic，宿主会在当前进程生命周期内把该插件标记为 fused。fused 插件不会再被调用，即使后续配置热重载重新启用它也一样。重启服务后才会清除 fused 状态。

Go 插件是可信的进程内代码，不是沙箱。panic recovery 无法阻止插件调用 `os.Exit`、修改共享进程状态、启动后台任务或泄露 secret。请把插件二进制视为与服务二进制同等信任级别的代码。

## 扩展示例

把这个示例改造成真实 provider 插件时：

1. 保留 `package main` 和导出的 `Register` / `Reconfigure` 函数。
2. 统一修改 metadata、provider key、model ID、命令行 flag 和 Management path。
3. 让 `.so` 文件名匹配期望的插件 ID。
4. 选择最窄的 `ExecutorModelScope`。
5. 所有上游 provider 调用都使用 `HostHTTPClient`。
6. 当宿主已经负责登录或命令行持久化时，返回 `AuthData`，不要直接写 auth storage。
7. 把 provider 专属 payload 改写保持在插件边界内。
8. 不要记录 secret、token、原始 auth JSON 或签名请求头。
9. 后台 goroutine 需要绑定 context 或显式生命周期状态，因为 Go 插件无法卸载。
10. 添加插件本地测试，并使用与服务相同的工具链构建插件。

## 验证

编译示例插件：

```bash
go build -buildmode=plugin -o /tmp/cliproxy-example-plugin.so ./examples/plugin && rm -f /tmp/cliproxy-example-plugin.so
```

编辑文档后检查 Markdown 空白问题：

```bash
git diff --check -- examples/plugin/README.md examples/plugin/README_CN.md
```

如果插件实现过程中修改了 Go 代码，还需要执行仓库要求的服务端编译：

```bash
go build -o test-output ./cmd/server && rm test-output
```

## 排障

`plugin.Open` 因类型或版本错误失败：

请使用与服务二进制一致的 Go 版本、module path、build tags 和依赖版本构建插件。

插件没有被加载：

确认 `plugins.enabled=true`，`.so` 文件位于被选中的插件目录下，插件 ID 合法，并且单插件配置没有禁用它。

插件加载了，但没有能力生效：

确认 `Register` 或 `Reconfigure` 返回有效 metadata，并且至少有一个非 nil capability。

执行器没有被使用：

确认存在匹配的 auth 记录，auth 的 `type` 匹配 provider key，执行器 scope 允许目标模型路径，并且没有原生执行器拥有该 provider 或模型。

命令行 flag 出现了但没有执行：

确认最终加载的配置仍启用了全局插件和当前插件。CLI flag 会在最终配置分发之前注册，但执行时会检查最终活动插件快照。

Management 路由返回 404：

确认本地 Management API 路由可用，路由路径完全匹配，插件处于启用状态，并且没有原生或更高优先级路由声明了同一个 method/path。
