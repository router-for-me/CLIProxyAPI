# Example Go Dynamic Plugin

This directory is the reference skeleton for writing a provider plugin against the current `sdk/pluginapi` ABI. It is intentionally deterministic and small, but it demonstrates the host integration points that a real provider plugin needs: provider-owned auth parsing, model discovery, execution, HTTP bridging, request/response transforms, thinking config, usage observation, command-line flags, and diagnostic Management API routes.

The example uses the provider key `plugin-example` and the plugin ID `example`.

## What the sample implements

`examples/plugin/main.go` exports the required Go plugin entrypoints:

```go
func Register(configYAML []byte) pluginapi.Plugin
func Reconfigure(configYAML []byte) pluginapi.Plugin
```

`Register` is called the first time the host loads the `.so` file. `Reconfigure` is called on config hot reload for a plugin that has already been opened and is still enabled. Both functions must return a `pluginapi.Plugin` value with valid metadata and at least one capability.

Required metadata fields:

- `Metadata.Name`
- `Metadata.Version`
- `Metadata.Author`
- `Metadata.GitHubRepository`

The sample declares these capabilities:

| Capability | Interface | What this sample shows |
| --- | --- | --- |
| Static and per-auth models | `ModelProvider` | Returns `plugin-example-model` for both static registration and auth-bound discovery. |
| Auth parsing and refresh | `AuthProvider` | Parses auth JSON whose `type` is `plugin-example`, exposes non-interactive login methods, and returns refreshed storage unchanged. |
| Frontend auth | `FrontendAuthProvider` | Accepts inbound requests only when `X-Plugin-Example: allow` is present. |
| Provider execution | `ProviderExecutor` | Implements non-streaming execution, streaming execution, token counting, and raw HTTP passthrough. |
| Executor model scope | `ExecutorModelScope` | Uses `pluginapi.ExecutorModelScopeBoth` so the executor can serve static models and OAuth/auth-bound models. |
| Request conversion | `RequestTranslator`, `RequestNormalizer` | Shows where canonical and provider-specific request payload transforms live. |
| Response conversion | `ResponseTranslator`, `ResponseBeforeTranslator`, `ResponseAfterTranslator` | Shows the response transform hooks before and after native translation. |
| Thinking config | `ThinkingApplier` | Receives canonical thinking config and writes provider-specific payload fields. |
| Usage observation | `UsagePlugin` | Counts completed usage records in memory for diagnostics. |
| Command-line flags | `CommandLinePlugin` | Adds plugin-owned CLI flags and receives all parsed flag values at execution time. |
| Management API | `ManagementAPI` | Adds exact diagnostic routes under `/v0/management/`. |

`ModelRegistrar` is still present in `sdk/pluginapi` for simple model-only plugins. New provider plugins should normally prefer `ModelProvider`, because it supports both static model metadata and per-auth model discovery through the same provider-native path.

## Platform and ABI rules

CLIProxyAPI loads standard Go plugins built with:

```bash
go build -buildmode=plugin
```

The Go standard `plugin` package is supported on Linux, FreeBSD, and macOS. On unsupported platforms, plugin loading is disabled and the service continues with native logic.

Go plugin ABI compatibility is strict. Build the plugin for the target service binary with the same:

- `GOOS` and `GOARCH`
- CPU feature target, when you use CPU-specific directories
- Go toolchain version
- build tags and CGO settings
- module path
- shared dependency versions

If any of these differ, `plugin.Open` can fail or the loaded symbols can have incompatible types.

## Build and install

Build from the repository root:

```bash
mkdir -p plugins/$(go env GOOS)/$(go env GOARCH)
go build -buildmode=plugin -o plugins/$(go env GOOS)/$(go env GOARCH)/example.so ./examples/plugin
```

The plugin ID is the `.so` file basename without the final `.so` suffix. `example.so` maps to `plugins.configs.example`.

Plugin IDs must match this shape:

```text
[A-Za-z0-9][A-Za-z0-9._-]{0,127}
```

The host searches these directories in order and keeps the first `.so` found for each plugin ID:

```text
plugins/<GOOS>/<GOARCH>-<variant>/*.so
plugins/<GOOS>/<GOARCH>/*.so
plugins/*.so
```

For `amd64`, `<variant>` is selected from CPU capabilities as `v4`, `v3`, `v2`, or `v1`. CPU-specific builds therefore belong under paths such as `plugins/linux/amd64-v3/`.

Replacing an already opened `.so` file requires a process restart. Go plugins cannot be unloaded from the current process.

## Configure the host

Dynamic plugins are disabled by default. Enable them in `config.yaml`:

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

Configuration rules:

- `plugins.enabled=false` skips all plugin loading and execution.
- `plugins.dir` defaults to `plugins` when omitted or empty.
- `plugins.configs.<pluginID>` is the per-plugin YAML subtree passed to `Register` or `Reconfigure`.
- `enabled` defaults to `true` for a configured plugin instance.
- `priority` defaults to `0`.
- The host injects normalized `enabled` and `priority` into the YAML bytes passed to the plugin when they are missing.
- Higher `priority` plugins run before lower `priority` plugins. Equal priorities are ordered by plugin ID.

Hot reload updates the runtime plugin snapshot. Already opened plugin binaries stay in memory, but disabled plugins are removed from the active capability set. If a loaded plugin remains enabled, the host calls `Reconfigure(configYAML)` instead of `Register(configYAML)`.

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

## Add auth material

Executor-backed plugin models need a matching auth record so the scheduler can select the provider. The auth `type` must match the provider returned by `ModelProvider`, `AuthProvider.Identifier`, and `ProviderExecutor.Identifier`.

For this sample:

```json
{
  "type": "plugin-example",
  "api_key": "plugin-or-upstream-secret"
}
```

Place the file under the configured auth directory, for example:

```text
auths/plugin-example.json
```

Do not configure `base_url`, `compat_name`, or an `openai-compatibility` entry for the same provider unless you intentionally want the native OpenAI-compatible executor to own that provider. Native executors always win over plugin executors.

Auth provider behavior in this sample:

- `ParseAuth` accepts JSON offered by the host auth loader and returns `pluginapi.AuthData`.
- `StartLogin` and `PollLogin` are present but return non-interactive errors in this sample.
- `RefreshAuth` returns the current auth data unchanged.
- A real plugin can return `AuthData` from command-line execution or login polling; the host persists it through the normal auth store.

## Model registration and executor scope

The current provider-native model path is `ModelProvider`:

- `StaticModels` returns provider models that are available without inspecting a specific auth record.
- `ModelsForAuth` returns models discovered for one selected auth record and can return an `AuthUpdate` when discovery refreshes persisted provider state.

The host applies normal model processing after plugin discovery: aliases, excluded models, prefixes, registry reconciliation, and scheduler rules.

`ExecutorModelScope` controls which model-registration paths are allowed when `Capabilities.Executor` is present:

| Scope | Meaning |
| --- | --- |
| `pluginapi.ExecutorModelScopeBoth` | The executor supports both static models and auth-bound OAuth-style models. This is the default when the scope is empty or invalid. |
| `pluginapi.ExecutorModelScopeStatic` | The executor supports only non-OAuth static models. `ModelsForAuth` is skipped for executor-backed registration. |
| `pluginapi.ExecutorModelScopeOAuth` | The executor supports only auth-bound models. Static executor model clients are not registered. |

Use the narrowest scope that matches the provider. This avoids exposing models through the wrong registration path.

## Execution flow

A plugin executor runs only when:

- global plugins are enabled,
- the specific plugin is enabled,
- the plugin has not been panic-fused,
- the selected auth provider matches the executor provider,
- no native executor owns the same provider or selected model,
- and no higher-priority plugin has already claimed the same provider/model.

`ProviderExecutor` receives a `pluginapi.ExecutorRequest` with:

- `Model`: the host-resolved model identifier after alias handling,
- `Format`: the target provider format,
- `SourceFormat`: the original client format,
- `OriginalRequest`: the raw client payload,
- `Payload`: the translated provider payload,
- `StorageJSON`, `AuthMetadata`, and `AuthAttributes`: selected auth state,
- `HTTPClient`: the host HTTP bridge.

Executor upstream HTTP calls must use `req.HTTPClient.Do` or `req.HTTPClient.DoStream`. Do not build a separate proxy-aware client inside the plugin. The host bridge preserves host transport policy and lets `request-log` capture the outbound upstream request and the raw upstream response before plugin-side translation.

The sample methods are intentionally deterministic:

- `Execute` returns one OpenAI-shaped JSON response.
- `ExecuteStream` emits one stream chunk and closes the channel.
- `CountTokens` returns zero token counts.
- `HttpRequest` forwards raw HTTP through the host bridge.

For real providers, use `req.Model` for provider routing and model rewriting decisions. Do not assume every protocol payload has a trustworthy top-level `model` field.

## Translators, normalizers, and thinking

Native logic is authoritative. Plugin transforms fill gaps instead of replacing built-in provider support.

Request and response behavior:

- Request normalizers run from higher priority to lower priority and are chained.
- Response normalizers before and after translation follow the same priority ordering.
- Request translators and response translators run only when no native translator exists for the format pair.
- Only the highest-priority plugin translator is selected for a missing translation path.

Thinking behavior:

- The host parses, normalizes, and validates thinking config centrally.
- `ThinkingApplier` receives canonical `pluginapi.ThinkingConfig`.
- A plugin thinking applier only applies provider keys that are not owned by native thinking providers.
- When a plugin is disabled, removed from the active snapshot, or panic-fused, its thinking applier is removed.

The sample writes these provider-specific fields into the payload:

```json
{
  "plugin_example_thinking": {
    "mode": "budget",
    "budget": 1024,
    "level": ""
  }
}
```

## Command-line flags

The sample declares two plugin-owned flags:

```bash
./cli-proxy-api -config config.yaml -plugin-example-command
./cli-proxy-api -config config.yaml -plugin-example-command -plugin-example-message "custom message"
```

Plugin command-line flags are registered before normal flag parsing so they appear in `-help`.

Rules:

- Supported flag types are `bool`, `string`, `int`, `int64`, `float64`, and `duration`.
- Flag names cannot start with `-`, contain whitespace, contain `=`, or be `help` / `h`.
- Native flags cannot be replaced.
- Higher-priority plugin flags cannot be replaced by lower-priority plugins.
- When any plugin-owned flag is provided, the host passes every argument, every visible parsed flag, and the triggered plugin-owned flags to `ExecuteCommandLine`.
- If final config disables global plugins or this plugin, the flag can still be parsed but plugin execution is skipped.
- If `ExecuteCommandLine` returns `Auths`, the host persists them through the configured auth store and appends saved paths to stdout.

## Management API routes

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

The sample routes are:

```text
GET /v0/management/plugins/example/status
GET /v0/management/plugins/example/capabilities
```

Management API route rules:

- Routes are exact method/path matches under `/v0/management/`.
- A plugin may return relative paths such as `/plugins/example/status`; the host resolves them under `/v0/management`.
- Paths cannot contain whitespace, `:`, or `*`.
- Native Management API routes cannot be replaced.
- Higher-priority plugin routes cannot be replaced by lower-priority plugins.
- Routes require the normal Management API authentication.
- Routes are unavailable when Home mode or Management API availability disables local Management routes.
- The route table is rebuilt on config reload.

## Frontend authentication

The sample `FrontendAuthProvider` accepts a request only when this header is present:

```text
X-Plugin-Example: allow
```

The registered frontend provider key is namespaced by the host as:

```text
plugin:<pluginID>:<providerIdentifier>
```

For this sample, the provider identifier is `plugin-example`, so downstream auth metadata is kept separate from native frontend auth providers.

## Usage plugin

`UsagePlugin.HandleUsage` receives completed usage records after request execution. The sample increments an in-memory counter that is visible through the diagnostic Management API status route.

Usage records include provider, executor type, model, alias, selected auth, source, requested reasoning effort, service tier, latency, TTFT, failure details, token counters, and selected response headers.

Keep this hook lightweight. Usage dispatch is part of the request accounting path, and the host will recover from panics by fusing the plugin.

## Priority, native precedence, and panic fuse

The plugin system is additive:

- Native providers, executors, translators, thinking appliers, flags, and Management routes have priority over plugins.
- Plugins fill provider gaps and add plugin-owned surfaces.
- Higher-priority plugins are considered before lower-priority plugins.
- Plugin executors do not override native executors.
- Plugin Management routes and command-line flags do not override native routes or flags.

Every lifecycle and capability call is protected by panic recovery. If a plugin panics during `Register`, `Reconfigure`, or any capability method, the host marks that plugin fused for the current process lifetime. A fused plugin is no longer called, even if config reload enables it again. Restart the service to clear the fused state.

Go plugins are trusted in-process code, not a sandbox. Panic recovery cannot prevent a plugin from calling `os.Exit`, mutating shared process state, starting background work, or leaking secrets. Treat plugin binaries as code with the same trust level as the service binary.

## Extending this sample

When turning this sample into a real provider plugin:

1. Keep `package main` and the exported `Register` / `Reconfigure` functions.
2. Rename metadata, provider keys, model IDs, command-line flags, and Management paths consistently.
3. Build the `.so` filename to match the desired plugin ID.
4. Choose the narrowest `ExecutorModelScope`.
5. Use `HostHTTPClient` for all upstream provider calls.
6. Return `AuthData` instead of writing directly to auth storage when the host is already managing login or command-line persistence.
7. Keep provider-specific payload rewriting inside the plugin boundary.
8. Avoid logging secrets, tokens, raw auth JSON, or signed request headers.
9. Keep background goroutines tied to context or explicit lifecycle state, because Go plugins cannot be unloaded.
10. Add plugin-local tests and build the plugin with the same toolchain as the service.

## Verification

Compile the sample plugin:

```bash
go build -buildmode=plugin -o /tmp/cliproxy-example-plugin.so ./examples/plugin && rm -f /tmp/cliproxy-example-plugin.so
```

Check Markdown whitespace after editing docs:

```bash
git diff --check -- examples/plugin/README.md examples/plugin/README_CN.md
```

If you changed Go code as part of a plugin implementation, also run the repository-required server compile:

```bash
go build -o test-output ./cmd/server && rm test-output
```

## Troubleshooting

`plugin.Open` fails with a type or version error:

Build the plugin with the same Go version, module path, build tags, and dependency versions as the service binary.

The plugin is not loaded:

Confirm `plugins.enabled=true`, the `.so` file is under the selected plugin directory, the plugin ID is valid, and the per-plugin config is not disabled.

The plugin loads but no capability is active:

Confirm `Register` or `Reconfigure` returns valid metadata and at least one non-nil capability.

The executor is not used:

Confirm a matching auth record exists, the auth `type` matches the provider key, the executor scope allows the desired model path, and no native executor owns the provider or model.

The command-line flag appears but does nothing:

Confirm the final loaded config still enables global plugins and this plugin. CLI flags are registered before final config dispatch, but execution is checked against the final active plugin snapshot.

The Management route returns 404:

Confirm local Management API routes are available, the route path is exact, the plugin is enabled, and no native or higher-priority route claimed the same method/path.
