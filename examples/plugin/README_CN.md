# 标准动态库插件示例

本目录包含 CLIProxyAPI C ABI 的标准动态库插件示例。

## 目录布局

- `simple/`：声明全部支持能力的完整骨架示例。
- `model/`：只演示模型能力。
- `auth/`：只演示认证提供方能力。
- `frontend-auth/`：只演示前端认证提供方能力。
- `executor/`：只演示执行器能力。
- `protocol-format/`：使用最小执行器重点演示输入和输出格式声明。
- `request-translator/`：只演示请求转换能力。
- `request-normalizer/`：只演示请求规整能力。
- `codex-service-tier/`：仅 Go 实现的请求规整插件，启用后会将 Codex `gpt-5.4` 请求设置为 priority service tier。
- `response-translator/`：只演示响应转换能力。
- `response-normalizer/`：只演示响应规整能力。
- `thinking/`：只演示 Thinking 处理能力。
- `usage/`：只演示 Usage 观察能力。
- `cli/`：只演示命令行扩展能力。
- `management-api/`：只演示 Management API 扩展能力。
- `host-callback/`：使用最小 Management API 路由演示宿主回调。

多数标准能力示例都包含 `go/`、`c/` 和 `rust/` 三个子目录。专用示例可能只提供所需的实现语言。

## Codex Service Tier

`codex-service-tier` 声明请求规整能力。当 `fast` 为 `true` 时，如果 `req.ToFormat` 为 `codex` 且 `req.Model` 为 `gpt-5.4`，它会将 `service_tier` 设置为 `priority`。

```yaml
plugins:
  configs:
    codex-service-tier:
      enabled: true
      priority: 1
      fast: false
```

## 构建全部示例

```bash
make -C examples/plugin list
make -C examples/plugin build
```

构建产物会写入 `examples/plugin/bin`。

## 说明

`protocol-format` 使用最小执行器承载，因为格式声明属于执行器能力。

`host-callback` 使用最小 Management API 路由承载，因为宿主回调只能从插件方法内部发起，不是独立能力。
