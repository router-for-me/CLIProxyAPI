# 标准动态库插件示例

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
- `request-lifecycle/`：仅 Go 实现的请求生命周期插件，演示并发控制、主动终止 HTTP 请求和终态回调。
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

## 请求生命周期

`request-lifecycle` 同时声明 `request_interceptor` 和 `request_lifecycle_plugin`。它会在认证选择前占用并发槽位，可以直接返回自定义 `403` 或 `429` 响应而不请求上游模型，并在成功、失败、拒绝或取消时通过 `request.complete` 释放已接入请求的槽位。

```yaml
plugins:
  configs:
    request-lifecycle:
      enabled: true
      priority: 100
      max_concurrency: 2
      reject_keyword: "blocked"
```

构建方式和生命周期语义详见 `request-lifecycle/README.md`。

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

## 插件执行器错误处理与 HTTP 状态码

当插件执行器（Executor）遇到上游调用失败（如因凭据无效返回 `401 Unauthorized`、因模型权限或配额限制返回 `403 Forbidden`，或因限流返回 `429 Too Many Requests`）时，应在错误信封中设置 HTTP 状态码：

- 在 JSON RPC 错误信封中，设置 `error` 对象内的 `http_status` 字段（对应 `pluginabi.Error.HTTPStatus`）。
- 若省略 `http_status` 或设置为 `0`，CPA 会默认将错误降级为 HTTP `500 Internal Server Error`（`server_error` / `internal_server_error`），导致客户端将其误判为网关故障并进行退避重试。
- 显式设置 `http_status` 后，CPA 会将状态码正确映射为结构化的客户端错误响应：
  - `401` -> HTTP 401，`type: "authentication_error"`，`code: "invalid_api_key"`
  - `403` -> HTTP 403，`type: "permission_error"`，`code: "insufficient_quota"`
  - `429` -> HTTP 429，`type: "rate_limit_error"`，`code: "rate_limit_exceeded"`
  - `404` -> HTTP 404，`type: "invalid_request_error"`，`code: "model_not_found"`
  - `>=500` -> HTTP 5xx，`type: "server_error"`，`code: "internal_server_error"`
- **注意**：非流式执行（`executor.execute`）和流式执行（`executor.execute_stream`）两处报错路径均需要设置 `http_status`，以保证异常分类行为一致。
- 原生动态库插件通过 C ABI 交换序列化的 JSON 缓冲区通信，因此状态码必须编码到序列化的 JSON 信封中（例如使用 `pluginabi.NewErrorEnvelope` 或自定义带 `http_status` 字段的信封结构体）。Go 语言原生的 error 对象无法跨越 C ABI 边界传递。

Go 语言编写的插件可导入 `github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi` 并直接使用 `pluginabi.NewErrorEnvelope(code, message, httpStatus)`：

```go
// 推荐方式：直接使用 sdk/pluginabi 构造错误信封
rawEnvelope, errMarshal := pluginabi.NewErrorEnvelope("insufficient_quota", "plan limit reached", http.StatusForbidden)
```

也可以在自定义信封结构体中声明 `HTTPStatus` 字段（`json:"http_status,omitempty"`）：

```go
type envelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

func errorEnvelope(code, message string, httpStatus ...int) []byte {
	status := 0
	if len(httpStatus) > 0 {
		status = httpStatus[0]
	}
	raw, _ := json.Marshal(envelope{
		OK: false,
		Error: &envelopeError{
			Code:       code,
			Message:    message,
			HTTPStatus: status,
		},
	})
	return raw
}
```

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
