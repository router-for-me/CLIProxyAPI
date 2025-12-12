# PR Title / 拉取请求标题

`feat(kiro): Add Thinking Mode support & enhance reliability with multi-quota failover`
`feat(kiro): 支持思考模型 (Thinking Mode) 并通过多配额故障转移增强稳定性`

---

# PR Description / 拉取请求描述

## 📝 Summary / 摘要

This PR introduces significant upgrades to the Kiro (AWS CodeWhisperer/Amazon Q) module. It adds native support for **Thinking/Reasoning models** (similar to OpenAI o1/Claude 3.7), implements a robust **Multi-Endpoint Failover** system to handle rate limits (429), and optimizes configuration flexibility.

本次 PR 对 Kiro (AWS CodeWhisperer/Amazon Q) 模块进行了重大升级。它增加了对 **思考/推理模型 (Thinking/Reasoning models)** 的原生支持（类似 OpenAI o1/Claude 3.7），实现了一套健壮的 **多端点故障转移 (Multi-Endpoint Failover)** 系统以应对速率限制 (429)，并优化了配置灵活性。

## ✨ Key Changes / 主要变更

### 1. 🧠 Thinking Mode Support / 思考模式支持
- **OpenAI Compatibility**: Automatically maps OpenAI's `reasoning_effort` parameter (low/medium/high) to Claude's `budget_tokens` (4k/16k/32k).
  - **OpenAI 兼容性**：自动将 OpenAI 的 `reasoning_effort` 参数（low/medium/high）映射为 Claude 的 `budget_tokens`（4k/16k/32k）。
- **Stream Parsing**: Implemented advanced stream parsing logic to detect and extract content within `<thinking>...</thinking>` tags, even across chunk boundaries.
  - **流式解析**：实现了高级流式解析逻辑，能够检测并提取 `<thinking>...</thinking>` 标签内的内容，即使标签跨越了数据块边界。
- **Protocol Translation**: Converts Kiro's internal thinking content into OpenAI-compatible `reasoning_content` fields (for non-stream) or `thinking_delta` events (for stream).
  - **协议转换**：将 Kiro 内部的思考内容转换为兼容 OpenAI 的 `reasoning_content` 字段（非流式）或 `thinking_delta` 事件（流式）。

### 2. 🛡️ Robustness & Failover / 稳健性与故障转移
- **Dual Quota System**: Explicitly defined `kiroEndpointConfig` to distinguish between **IDE (CodeWhisperer)** and **CLI (Amazon Q)** quotas.
  - **双配额系统**：显式定义了 `kiroEndpointConfig` 结构，明确区分 **IDE (CodeWhisperer)** 和 **CLI (Amazon Q)** 的配额来源。
- **Auto Failover**: Implemented automatic failover logic. If one endpoint returns `429 Too Many Requests`, the request seamlessly retries on the next available endpoint/quota.
  - **自动故障转移**：实现了自动故障转移逻辑。如果一个端点返回 `429 Too Many Requests`，请求将无缝在下一个可用端点/配额上重试。
- **Strict Protocol Compliance**: Enforced strict matching of `Origin` and `X-Amz-Target` headers for each endpoint to prevent `403 Forbidden` errors due to protocol mismatches.
  - **严格协议合规**：强制每个端点严格匹配 `Origin` 和 `X-Amz-Target` 头信息，防止因协议不匹配导致的 `403 Forbidden` 错误。

### 3. ⚙️ Configuration & Models / 配置与模型
- **New Config Options**: Added `KiroPreferredEndpoint` (global) and `PreferredEndpoint` (per-key) settings to allow users to prioritize specific quotas (e.g., "ide" or "cli").
  - **新配置项**：添加了 `KiroPreferredEndpoint`（全局）和 `PreferredEndpoint`（单 Key）设置，允许用户优先选择特定的配额（如 "ide" 或 "cli"）。
- **Model Registry**: Normalized model IDs (replaced dots with hyphens) and added `-agentic` variants optimized for large code generation tasks.
  - **模型注册表**：规范化了模型 ID（将点号替换为连字符），并添加了针对大型代码生成任务优化的 `-agentic` 变体。

### 4. 🔧 Fixes / 修复
- **AMP Proxy**: Downgraded client-side context cancellation logs from `Error` to `Debug` to reduce log noise.
  - **AMP 代理**：将客户端上下文取消的日志级别从 `Error` 降级为 `Debug`，减少日志噪音。

## ⚠️ Impact / 影响

- **Authentication**: **No changes** to the login/OAuth process. Existing tokens work as is.
- **认证**：登录/OAuth 流程 **无变更**。现有 Token 可直接使用。
- **Compatibility**: Fully backward compatible. The new failover logic is transparent to the user.
- **兼容性**：完全向后兼容。新的故障转移逻辑对用户是透明的。