# CLIProxyAPI 项目总览（会话速查）

> 目标：给后续开发/讨论会话一个统一的“快速上下文”，减少重复梳理成本。
> 最后更新：2026-03-03

## 1. 项目定位

CLIProxyAPI 是一个面向 AI Coding 工具链的统一代理层：

- 对下游暴露 **OpenAI / Gemini / Claude / Codex 兼容接口**
- 对上游统一管理 **OAuth 账号、API Key、OpenAI 兼容提供商**
- 提供 **管理 API + Web 控制面板 + TUI**
- 支持 **多账号路由、热更新、使用统计、扩展 SDK**

适合场景：本地或团队内统一接入多家模型能力，并在不中断服务情况下持续调整配置与凭据。

---

## 2. 主要功能清单

### 2.1 协议与路由能力

- OpenAI 兼容接口（含 Responses）
- Gemini 兼容接口
- Claude 兼容接口
- Gemini CLI 内部接口
- WebSocket 路由支持

### 2.2 认证与凭据能力

- OAuth 登录：Claude、Codex、Gemini CLI、Antigravity、Qwen、Kimi、iFlow
- API Key 账户管理：Gemini / Claude / Codex / Vertex / OpenAI-compat
- 多账号轮询与路由策略（round-robin / fill-first）
- 模型前缀、模型排除、OAuth 模型别名

### 2.3 管理与运维能力

- Management API（配置、凭据、日志、使用统计等）
- Web 管理面板（管理增强脚本可注入 OAuth 可用性组件）
- TUI 管理界面（Dashboard / Config / Auth Files / API Keys / OAuth / Usage / Logs）
- 配置热更新与 auth 目录变更自动生效
- 自动刷新机制（凭据自动刷新）

### 2.4 统计与可观测能力

- 请求与 Token 统计（总量、按小时、按天）
- 按 API / 模型明细统计
- 使用统计导入导出（备份/迁移）
- 请求日志与错误日志控制（可动态开关）

### 2.5 SDK 可扩展能力

- 可内嵌运行 `sdk/cliproxy`
- 自定义 Access Provider（`sdk/access`）
- 自定义 Executor 与 Translator
- Watcher 增量更新队列接入

---

## 3. 当前代码中的关键“特性更新”

以下是当前代码已体现、且对后续会话最有价值的特性点：

1. **OAuth 可用性监控增强（Web + TUI）**
   - Web：在 Usage 页面注入 OAuth 可用率组件（10 秒采样、近 5 分钟趋势）
   - TUI：Usage 页新增 OAuth Provider 可用性、趋势和凭证明细

2. **Usage 数据可导入/导出**
   - 支持管理端快照导出与合并导入，便于迁移和排障复盘

3. **Access Provider 动态对账/热更新**
   - 配置变化后可重建 provider 链并比较新增/更新/删除，避免全量重启

4. **Watcher 增量队列与高频变更合并**
   - auth 变更通过队列增量分发，支持高频更新下的去抖与合并

5. **TUI 独立模式（standalone）**
   - 可在 TUI 内嵌启动本地服务，减少外部依赖并提升管理闭环

6. **多后端存储适配**
   - 默认文件存储外，支持 Postgres / Git / Object Store 作为凭据与配置后端

---

## 4. 统计口径速记（避免沟通歧义）

### 4.1 “请求趋势”指什么？

“请求趋势（按小时/按天）”来自 usage 聚合数据（`/v0/management/usage`），本质是**代理执行器上报的模型调用请求**。

### 4.2 OAuth 请求是否包含在请求趋势里？

**OAuth 登录流程本身不包含在请求趋势里。**

- OAuth 登录/回调属于 management OAuth 流程
- OAuth 可用性展示来自 `/v0/management/auth-files` 聚合结果

结论：
- 请求趋势 = 模型调用流量
- OAuth 可用性 = 凭据健康状态

---

## 5. 代码结构速查（按职责）

- 启动入口与运行模式：`cmd/server/main.go`
- HTTP 路由与管理路由注册：`internal/api/server.go`
- 管理 API 处理器：`internal/api/handlers/management/`
- 执行器与上游调用：`internal/runtime/executor/`
- 使用统计聚合：`internal/usage/logger_plugin.go`
- TUI 主框架：`internal/tui/app.go`
- TUI 使用统计页：`internal/tui/usage_tab.go`
- Web 管理增强（OAuth 可用性组件）：`internal/api/management_enhancements.js`
- Access Provider 对账：`internal/access/reconcile.go`
- SDK 文档入口：`docs/sdk-usage_CN.md` / `docs/sdk-advanced_CN.md` / `docs/sdk-access_CN.md` / `docs/sdk-watcher_CN.md`

---

## 6. 会话前 60 秒 Checklist（建议）

开始新一轮需求前，先确认：

1. 目标改动在以下哪层：路由层 / 管理层 / 执行器层 / TUI 层 / SDK 层
2. 是否涉及配置项变更（`config.example.yaml`）
3. 是否涉及统计口径（usage vs oauth availability）
4. 是否需要热更新兼容（watcher / access provider reconcile）
5. 是否需要补充测试（特别是 management handler 和 executor）

---

## 7. 常见任务 → 快速定位

- 新增管理接口：`internal/api/server.go` + `internal/api/handlers/management/*`
- 调整 Usage 统计口径：`internal/runtime/executor/usage_helpers.go` + `internal/usage/logger_plugin.go`
- 调整 OAuth 可用性展示：
  - TUI：`internal/tui/usage_tab.go`
  - Web：`internal/api/management_enhancements.js`
- 新增访问认证方式：`sdk/access` + `internal/access/config_access`
- 新增上游 Provider：`internal/runtime/executor/*` + 模型注册/翻译逻辑
- 调整 TUI 交互与页签：`internal/tui/app.go` + 对应 tab 文件

---

## 8. 备注

该文档是“会话上下文索引”，优先保证可定位与可执行，不替代详细 API/SDK 文档。
