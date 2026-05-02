# CLIProxyAPI fork 差异说明

本仓库是 `router-for-me/CLIProxyAPI` 的 fork。除下列差异外，其余功能、使用方式和配置项以 upstream 为准。
本仓库需要搭配[前端面板](https://github.com/Fwindy/Cli-Proxy-API-Management-Center)使用

## 与 upstream 的主要差异

### 1. Usage 统计恢复并重构为 SQLite 持久化

本 fork 保留并重构了 built-in usage 统计能力：

- usage 原始请求记录持久化到 SQLite，默认数据库文件为日志目录下的 `usage.db`，不再保留运行期内存聚合统计对象。
- `usage-statistics-enabled` 仍用于控制是否记录 usage。
- Management API 返回精简明细结构，支持时间范围查询和按 `id` 删除；usage import/export 接口已删除。

### 2. Usage 明细字段扩展

每条 usage detail 额外记录：

- `first_byte_latency_ms`：首字延迟。
- `generation_ms`：表示首字之后到记录完成之间的耗时近似值。
- `thinking_effort`：请求的 thinking / reasoning 强度，例如 `low`、`medium`、`high`、`xhigh`、`budget:4096`。

### 3. Thinking effort 提取与透传

本 fork 在请求进入各 executor 时提取 thinking / reasoning 配置，并写入 usage：

- 支持模型 suffix，例如 `model(high)`、`model(4096)`。
- 支持 OpenAI / OpenAI Responses / Codex / Claude / Gemini / Kimi 等请求体格式。
- `openai-response` compact 路径也会应用并记录对应 reasoning effort。

### 4. Fork 自动同步相关 workflow

本 fork 调整了 GitHub workflow，用于跟踪 upstream 更新和标签同步；不完全沿用 upstream 的 workflow 配置。

## 维护注意事项

- 合并 upstream 时，优先保留本 fork 的 SQLite usage 模块、usage API、thinking effort 记录和首字延迟记录能力。

## 友链

[![友链 linux.do](https://img.shields.io/badge/LINUX--DO-Community-blue.svg)](https://linux.do/)

## License

MIT
