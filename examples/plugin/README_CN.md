- ：仅 Go 实现的插件资源，演示 host 凭证文件回调（、、、）。
- # 标准动态库插件示例

本目录包含 CLIProxyAPI C ABI 的标准动态库插件示例。

## 目录布局

- `simple/`：声明全部支持能力的完整骨架示例。
- `model/`：只演示模型能力。
- `auth/`：只演示认证提供方能力。
- `frontend-auth/`：只演示前端认证提供方能力。
- `frontend-auth-exclusive/`：演示被选中后成为唯一请求认证方式的前端认证提供方。
- `executor/`：只演示执行器能力。
- `protocol-format/`：使用最小执行器重点演示输入和输出格式声明。
- `request-translator/`：只演示请求转换能力。
- `request-normalizer/`：只演示请求规整能力。
- `codex-service-tier/`：仅 Go 实现的请求规整插件，启用后会将 Codex `gpt-5.5` 请求设置为 priority service tier。
- `scheduler/`：仅 Go 实现的调度插件，可选择指定 auth ID、委托内置调度器或拒绝调度。
- `response-translator/`：只演示响应转换能力。
- `response-normalizer/`：只演示响应规整能力。
- `thinking/`：只演示 Thinking 处理能力。
- `usage/`：只演示 Usage 观察能力。
- `cli/`：只演示命令行扩展能力。
- `management-api/`：只演示 Management API 和资源扩展能力。
- `host-callback/`：使用最小插件资源演示宿主回调。
- `host-callback-auth-files/`：仅 Go 实现的插件资源，演示 host 凭证文件回调。
- `host-model-callback/`：仅 Go 实现的插件资源，演示调用宿主模型执行回调。

多数标准能力示例都包含 `go/`、`c/` 和 `rust/` 三个子目录。专用示例可能只提供所需的实现语言。

## Codex Service Tier

`codex-service-tier` 声明请求规整能力。当 `fast` 为 `true` 时，如果 `req.ToFormat` 为 `codex` 且 `req.Model` 为 `gpt-5.5`，它会将 `service_tier` 设置为 `priority`。

```yaml
plugins:
  configs:
    codex-service-tier:
      enabled: true
      priority: 1
      fast: false
```



## Host Auth Files 回调

`host-callback-auth-files` 声明 Management API 能力，并暴露名为 `Host Auth Files` 的浏览器资源，演示 `host.auth.list`、`host.auth.get`（物理 JSON 文件）、`host.auth.get_runtime` 与 `host.auth.save`。

```yaml
plugins:
  configs:
    host-callback-auth-files:
      enabled: true
      priority: 1
```

详见 `host-callback-auth-files/README.md`。

## Host Model Callback

`host-model-callback` 声明 Management API 能力，并暴露名为 `Host Model Callback` 的浏览器资源。该资源在非流式请求中调用 `host.model.execute`，在流式请求中调用 `host.model.execute_stream` 和 `host.model.stream_read`。它演示了通过 `host.model.stream_close` 显式关闭流，也提供 `implicit_close=true` 用于演示 RPC 作用域结束时的宿主隐式清理。

当该资源转发自身收到的 `host_callback_id` 时，CPA 会识别发起宿主模型回调的插件，并在嵌套模型执行中跳过同一个插件的拦截器。因此宿主模型回调不会递归调用发起插件自身，但其他已启用插件仍可拦截这次嵌套请求。

```yaml
plugins:
  configs:
    host-model-callback:
      enabled: true
      priority: 1
```

默认示例模型是 `gpt-5.5`，但请求能否成功取决于当前 CPA 模型和认证配置是否可以路由该模型。

## Scheduler

`scheduler` 声明调度能力。它可以从候选列表中选择配置的 auth ID，委托内置的 `fill-first` 或 `round-robin` 调度器，或在 `deny` 为 `true` 时拒绝调度。

```yaml
plugins:
  configs:
    scheduler:
      enabled: true
      priority: 1
      auth_id: ""
      delegate: ""
      deny: false
```

`auth_id` 会在 `delegate` 为空时选择匹配候选。`delegate` 支持 `""`、`fill-first` 和 `round-robin`；其他非空值会让本插件不处理本次调度。`deny` 会返回调度错误。

## 构建全部示例

```bash
make -C examples/plugin list
make -C examples/plugin build
```

构建产物会写入 `examples/plugin/bin`。

## 说明

`protocol-format` 使用最小执行器承载，因为格式声明属于执行器能力。

`host-callback` 使用最小插件资源承载，因为宿主回调只能从插件方法内部发起，不是独立能力。

`management.register` 通过 `resources` 字段返回的菜单资源会由 CPA 暴露在 `/v0/resource/plugins/<pluginID>/...` 下。需要认证的插件自有 Management API 路由仍保留在 `/v0/management/...` 下。
