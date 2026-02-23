# CLIProxyAPI Ecosystem 2000-Item Execution Board

- Generated: 2026-02-22
- Scope: `router-for-me/CLIProxyAPIPlus` + `router-for-me/CLIProxyAPI` Issues, PRs, Discussions
- Objective: Implementation-ready backlog (up to 2000), including CLI extraction, bindings/API integration, docs quickstarts, and dev-runtime refresh

## Coverage
- generated_items: 2000
- sources_total_unique: 1865
- issues_plus: 81
- issues_core: 880
- prs_plus: 169
- prs_core: 577
- discussions_plus: 3
- discussions_core: 155

## Distribution
### Priority
- P1: 1112
- P2: 786
- P3: 102

### Wave
- wave-1: 1114
- wave-2: 784
- wave-3: 102

### Effort
- S: 1048
- M: 949
- L: 3

### Theme
- thinking-and-reasoning: 444
- general-polish: 296
- responses-and-chat-compat: 271
- provider-model-registry: 249
- docs-quickstarts: 142
- oauth-and-authentication: 122
- websocket-and-streaming: 104
- go-cli-extraction: 99
- integration-api-bindings: 78
- dev-runtime-refresh: 60
- cli-ux-dx: 55
- error-handling-retries: 40
- install-and-ops: 26
- testing-and-quality: 12
- platform-architecture: 1
- project-frontmatter: 1

## Top 250 (Execution Order)

### [CP2K-0011] Follow up "kiro账号被封" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: general-polish
- Source: router-for-me/CLIProxyAPIPlus issue#221
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/221
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0014] Generalize "Add support for proxying models from kilocode CLI" into provider-agnostic translation/utilities to reduce duplicate logic.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPIPlus issue#213
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/213
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0015] Improve CLI UX around "[Bug] Kiro 与 Ampcode 的 Bash 工具参数不兼容" with clearer commands, flags, and immediate validation feedback.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPIPlus issue#210
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/210
- Implementation note: Instrument structured logs/metrics around request normalize->translate->dispatch lifecycle.

### [CP2K-0016] Extend docs for "[Feature Request] Add default oauth-model-alias for Kiro channel (like Antigravity)" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: provider-model-registry
- Source: router-for-me/CLIProxyAPIPlus issue#208
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/208
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0017] Create or refresh provider quickstart derived from "bug: Nullable type arrays in tool schemas cause 400 error on Antigravity/Droid Factory" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPIPlus issue#206
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/206
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0018] Refactor internals touched by "GitHub Copilot CLI 使用方法" to reduce coupling and improve maintainability.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPIPlus issue#202
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/202
- Implementation note: Benchmark p50/p95 latency and memory; reject regressions in CI quality gate.

### [CP2K-0021] Follow up "Cursor CLI \ Auth Support" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: provider-model-registry
- Source: router-for-me/CLIProxyAPIPlus issue#198
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/198
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0022] Harden "Why no opus 4.6 on github copilot auth" with stricter validation, safer defaults, and explicit fallback semantics.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPIPlus issue#196
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/196
- Implementation note: Add failing-before/failing-after regression tests and update golden fixtures for each supported provider.

### [CP2K-0025] Improve CLI UX around "Claude thought_signature forwarded to Gemini causes Base64 decode error" with clearer commands, flags, and immediate validation feedback.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPIPlus issue#178
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/178
- Implementation note: Instrument structured logs/metrics around request normalize->translate->dispatch lifecycle.

### [CP2K-0030] Standardize naming/metadata affected by "fix(kiro): handle empty content in messages to prevent Bad Request errors" across both repos and docs.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPIPlus issue#163
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/163
- Implementation note: Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.

### [CP2K-0031] Follow up "在配置文件中支持为所有 OAuth 渠道自定义上游 URL" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPIPlus issue#158
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/158
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0034] Create or refresh provider quickstart derived from "请求docker部署支持arm架构的机器！感谢。" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPIPlus issue#147
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/147
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0036] Extend docs for "[Bug]进一步完善 openai兼容模式对 claude 模型的支持（完善 协议格式转换 ）" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPIPlus issue#145
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/145
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0037] Add robust stream/non-stream parity tests for "完善 claude openai兼容渠道的格式转换" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPIPlus issue#142
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/142
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0039] Prepare safe rollout for "kiro idc登录需要手动刷新状态" via flags, migration docs, and backward-compat tests.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPIPlus issue#136
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/136
- Implementation note: Expand quickstart and troubleshooting docs with copy-paste examples and expected outputs.

### [CP2K-0040] Standardize naming/metadata affected by "[Bug Fix] 修复 Kiro 的Claude模型非流式请求 output_tokens 为 0 导致的用量统计缺失" across both repos and docs.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPIPlus issue#134
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/134
- Implementation note: Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.

### [CP2K-0045] Improve CLI UX around "Error 403" with clearer commands, flags, and immediate validation feedback.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPIPlus issue#125
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/125
- Implementation note: Instrument structured logs/metrics around request normalize->translate->dispatch lifecycle.

### [CP2K-0047] Add robust stream/non-stream parity tests for "enterprise 账号 Kiro不是很稳定，很容易就403不可用了" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPIPlus issue#118
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/118
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0048] Refactor internals touched by "-kiro-aws-login 登录后一直封号" to reduce coupling and improve maintainability.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPIPlus issue#115
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/115
- Implementation note: Benchmark p50/p95 latency and memory; reject regressions in CI quality gate.

### [CP2K-0050] Standardize naming/metadata affected by "Antigravity authentication failed" across both repos and docs.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPIPlus issue#111
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/111
- Implementation note: Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.

### [CP2K-0051] Create or refresh provider quickstart derived from "大佬，什么时候搞个多账号管理呀" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPIPlus issue#108
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/108
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0052] Harden "日志中,一直打印auth file changed (WRITE)" with stricter validation, safer defaults, and explicit fallback semantics.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPIPlus issue#105
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/105
- Implementation note: Add failing-before/failing-after regression tests and update golden fixtures for each supported provider.

### [CP2K-0053] Operationalize "登录incognito参数无效" with observability, runbook updates, and deployment safeguards.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPIPlus issue#102
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/102
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0054] Generalize "OpenAI-compat provider hardcodes /v1/models (breaks Z.ai v4: /api/coding/paas/v4/models)" into provider-agnostic translation/utilities to reduce duplicate logic.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPIPlus issue#101
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/101
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0056] Extend docs for "Kiro currently has no authentication available" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPIPlus issue#96
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/96
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0059] Prepare safe rollout for "Bug: Kiro/BuilderId tokens can collide when email/profile_arn are empty; refresh token lifecycle not handled" via flags, migration docs, and backward-compat tests.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPIPlus issue#90
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/90
- Implementation note: Expand quickstart and troubleshooting docs with copy-paste examples and expected outputs.

### [CP2K-0060] Standardize naming/metadata affected by "[Bug] Amazon Q endpoint returns HTTP 400 ValidationException (wrong CLI/KIRO_CLI origin)" across both repos and docs.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPIPlus issue#89
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/89
- Implementation note: Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.

### [CP2K-0062] Harden "Cursor Issue" with stricter validation, safer defaults, and explicit fallback semantics.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPIPlus issue#86
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/86
- Implementation note: Add failing-before/failing-after regression tests and update golden fixtures for each supported provider.

### [CP2K-0063] Operationalize "Feature request: Configurable HTTP request timeout for Extended Thinking models" with observability, runbook updates, and deployment safeguards.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPIPlus issue#84
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/84
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0064] Generalize "kiro请求偶尔报错event stream fatal" into provider-agnostic translation/utilities to reduce duplicate logic.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: websocket-and-streaming
- Source: router-for-me/CLIProxyAPIPlus issue#83
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/83
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0066] Extend docs for "[建议] 技术大佬考虑可以有机会新增一堆逆向平台" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPIPlus issue#79
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/79
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0068] Create or refresh provider quickstart derived from "kiro请求的数据好像一大就会出错,导致cc写入文件失败" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPIPlus issue#77
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/77
- Implementation note: Benchmark p50/p95 latency and memory; reject regressions in CI quality gate.

### [CP2K-0073] Operationalize "How to use KIRO with IAM?" with observability, runbook updates, and deployment safeguards.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPIPlus issue#56
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/56
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0074] Generalize "[Bug] Models from Codex (openai) are not accessible when Copilot is added" into provider-agnostic translation/utilities to reduce duplicate logic.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: provider-model-registry
- Source: router-for-me/CLIProxyAPIPlus issue#43
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/43
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0075] Improve CLI UX around "model gpt-5.1-codex-mini is not accessible via the /chat/completions endpoint" with clearer commands, flags, and immediate validation feedback.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPIPlus issue#41
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/41
- Implementation note: Instrument structured logs/metrics around request normalize->translate->dispatch lifecycle.

### [CP2K-0079] Prepare safe rollout for "lack of thinking signature in kiro's non-stream response cause incompatibility with some ai clients (specifically cherry studio)" via flags, migration docs, and backward-compat tests.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPIPlus issue#27
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/27
- Implementation note: Expand quickstart and troubleshooting docs with copy-paste examples and expected outputs.

### [CP2K-0080] Standardize naming/metadata affected by "I did not find the Kiro entry in the Web UI" across both repos and docs.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPIPlus issue#26
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/26
- Implementation note: Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.

### [CP2K-0081] Follow up "Kiro (AWS CodeWhisperer) - Stream error, status: 400" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPIPlus issue#7
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/7
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0251] Follow up "Why a separate repo?" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPIPlus discussion#170
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/discussions/170
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0252] Harden "How do I perform GitHub OAuth authentication? I can't find the entrance." with stricter validation, safer defaults, and explicit fallback semantics.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPIPlus discussion#215
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/discussions/215
- Implementation note: Add failing-before/failing-after regression tests and update golden fixtures for each supported provider.

### [CP2K-0255] Create or refresh provider quickstart derived from "feat: support image content in tool result messages (OpenAI ↔ Claude translation)" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#1670
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1670
- Implementation note: Instrument structured logs/metrics around request normalize->translate->dispatch lifecycle.

### [CP2K-0257] Add robust stream/non-stream parity tests for "Need maintainer-handled codex translator compatibility for Responses compaction fields" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1667
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1667
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0258] Refactor internals touched by "codex: usage_limit_reached (429) should honor resets_at/resets_in_seconds as next_retry_after" to reduce coupling and improve maintainability.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1666
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1666
- Implementation note: Benchmark p50/p95 latency and memory; reject regressions in CI quality gate.

### [CP2K-0260] Standardize naming/metadata affected by "fix(claude): token exchange blocked by Cloudflare managed challenge on console.anthropic.com" across both repos and docs.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1659
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1659
- Implementation note: Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.

### [CP2K-0263] Operationalize "All credentials for model claude-sonnet-4-6 are cooling down" with observability, runbook updates, and deployment safeguards.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1655
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1655
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0265] Improve CLI UX around "Claude Sonnet 4.5 models are deprecated - please remove from panel" with clearer commands, flags, and immediate validation feedback.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1651
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1651
- Implementation note: Instrument structured logs/metrics around request normalize->translate->dispatch lifecycle.

### [CP2K-0267] Add robust stream/non-stream parity tests for "codex 返回 Unsupported parameter: response_format" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1647
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1647
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0268] Refactor internals touched by "Bug: Invalid JSON payload when tool_result has no content field (antigravity translator)" to reduce coupling and improve maintainability.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1646
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1646
- Implementation note: Benchmark p50/p95 latency and memory; reject regressions in CI quality gate.

### [CP2K-0272] Create or refresh provider quickstart derived from "是否支持微软账号的反代？" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#1632
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1632
- Implementation note: Add failing-before/failing-after regression tests and update golden fixtures for each supported provider.

### [CP2K-0274] Generalize "Claude Sonnet 4.5 is no longer available. Please switch to Claude Sonnet 4.6." into provider-agnostic translation/utilities to reduce duplicate logic.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1630
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1630
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0277] Add robust stream/non-stream parity tests for "Question: applyClaudeHeaders() — how were these defaults chosen?" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1621
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1621
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0278] Refactor internals touched by "[BUG] claude code 接入 cliproxyapi 使用时，模型的输出没有呈现流式，而是一下子蹦出来回答结果" to reduce coupling and improve maintainability.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: provider-model-registry
- Source: router-for-me/CLIProxyAPI issue#1620
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1620
- Implementation note: Benchmark p50/p95 latency and memory; reject regressions in CI quality gate.

### [CP2K-0281] Follow up "[bug] codex oauth登录流程失败" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: provider-model-registry
- Source: router-for-me/CLIProxyAPI issue#1612
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1612
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0282] Harden "qwen auth 里获取到了 qwen3.5，但是 ai 客户端获取不到这个模型" with stricter validation, safer defaults, and explicit fallback semantics.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#1611
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1611
- Implementation note: Add failing-before/failing-after regression tests and update golden fixtures for each supported provider.

### [CP2K-0283] Operationalize "fix: handle response.function_call_arguments.done in codex→claude streaming translator" with observability, runbook updates, and deployment safeguards.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1609
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1609
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0286] Extend docs for "[Feature Request] Antigravity channel should support routing claude-haiku-4-5-20251001 model (used by Claude Code pre-flight checks)" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1596
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1596
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0289] Create or refresh provider quickstart derived from "[Bug] Claude Code 2.1.37 random cch in x-anthropic-billing-header causes severe prompt-cache miss on third-party upstreams" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#1592
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1592
- Implementation note: Expand quickstart and troubleshooting docs with copy-paste examples and expected outputs.

### [CP2K-0291] Follow up "配额管理可以刷出额度，但是调用的时候提示额度不足" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1590
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1590
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0293] Operationalize "iflow GLM 5 时不时会返回 406" with observability, runbook updates, and deployment safeguards.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1588
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1588
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0296] Extend docs for "bug: Invalid thinking block signature when switching from Gemini CLI to Claude OAuth mid-conversation" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1584
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1584
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0297] Add robust stream/non-stream parity tests for "I saved 10M tokens (89%) on my Claude Code sessions with a CLI proxy" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1583
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1583
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0298] Refactor internals touched by "[bug]? gpt-5.3-codex-spark 在 team 账户上报错 400" to reduce coupling and improve maintainability.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1582
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1582
- Implementation note: Benchmark p50/p95 latency and memory; reject regressions in CI quality gate.

### [CP2K-0302] Harden "Port 8317 becomes unreachable after running for some time, recovers immediately after SSH login" with stricter validation, safer defaults, and explicit fallback semantics.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#1575
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1575
- Implementation note: Add failing-before/failing-after regression tests and update golden fixtures for each supported provider.

### [CP2K-0303] Operationalize "Support for gpt-5.3-codex-spark" with observability, runbook updates, and deployment safeguards.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1573
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1573
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0306] Create or refresh provider quickstart derived from "能否再难用一点?!" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#1564
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1564
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0307] Add robust stream/non-stream parity tests for "Cache usage through Claude oAuth always 0" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1562
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1562
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0308] Refactor internals touched by "antigravity 无法使用" to reduce coupling and improve maintainability.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#1561
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1561
- Implementation note: Benchmark p50/p95 latency and memory; reject regressions in CI quality gate.

### [CP2K-0310] Standardize naming/metadata affected by "Claude Code 调用 nvidia 发现 无法正常使用bash grep类似的工具" across both repos and docs.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1557
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1557
- Implementation note: Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.

### [CP2K-0311] Follow up "Gemini CLI: 额度获取失败：请检查凭证状态" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#1556
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1556
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0314] Generalize "Kimi的OAuth无法使用" into provider-agnostic translation/utilities to reduce duplicate logic.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#1553
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1553
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0315] Improve CLI UX around "grok的OAuth登录认证可以支持下吗？ 谢谢！" with clearer commands, flags, and immediate validation feedback.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#1552
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1552
- Implementation note: Instrument structured logs/metrics around request normalize->translate->dispatch lifecycle.

### [CP2K-0316] Extend docs for "iflow executor: token refresh failed" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1551
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1551
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0317] Add robust stream/non-stream parity tests for "为什么gemini3会报错" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1549
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1549
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0323] Create or refresh provider quickstart derived from "佬们，隔壁很多账号403啦，这里一切正常吗？" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#1541
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1541
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0324] Generalize "feat(thinking): support Claude output_config.effort parameter (Opus 4.6)" into provider-agnostic translation/utilities to reduce duplicate logic.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1540
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1540
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0327] Add robust stream/non-stream parity tests for "[Bug] Persistent 400 "Invalid Argument" error with claude-opus-4-6-thinking model (with and without thinking budget)" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1533
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1533
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0329] Prepare safe rollout for "bug: proxy_ prefix applied to tool_choice.name but not tools[].name causes 400 errors on OAuth requests" via flags, migration docs, and backward-compat tests.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1530
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1530
- Implementation note: Expand quickstart and troubleshooting docs with copy-paste examples and expected outputs.

### [CP2K-0333] Operationalize "The account has available credit, but a 503 or 429 error is occurring." with observability, runbook updates, and deployment safeguards.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: websocket-and-streaming
- Source: router-for-me/CLIProxyAPI issue#1521
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1521
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0334] Generalize "openclaw调用CPA 中的codex5.2 报错。" into provider-agnostic translation/utilities to reduce duplicate logic.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1517
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1517
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0336] Extend docs for "Token refresh logic fails with generic 500 error ("server busy") from iflow provider" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1514
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1514
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0337] Add robust stream/non-stream parity tests for "bug: Nullable type arrays in tool schemas cause 400 error on Antigravity/Droid Factory" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1513
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1513
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0340] Create or refresh provider quickstart derived from "反重力 claude-opus-4-6-thinking 模型如何通过 () 实现强行思考" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#1509
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1509
- Implementation note: Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.

### [CP2K-0341] Follow up "Feature: Per-OAuth-Account Outbound Proxy Enforcement for Google (Gemini/Antigravity) + OpenAI Codex – incl. Token Refresh and optional Strict/Fail-Closed Mode" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1508
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1508
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0353] Operationalize "Feature request [allow to configure RPM, TPM, RPD, TPD]" with observability, runbook updates, and deployment safeguards.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: provider-model-registry
- Source: router-for-me/CLIProxyAPI issue#1493
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1493
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0354] Generalize "Antigravity using Ultra plan: Opus 4.6 gets 429 on CLIProxy but runs with Opencode-Auth" into provider-agnostic translation/utilities to reduce duplicate logic.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1486
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1486
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0357] Create or refresh provider quickstart derived from "Amp code doesn't route through CLIProxyAPI" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#1481
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1481
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0358] Refactor internals touched by "导入kiro账户，过一段时间就失效了" to reduce coupling and improve maintainability.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1480
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1480
- Implementation note: Benchmark p50/p95 latency and memory; reject regressions in CI quality gate.

### [CP2K-0359] Prepare safe rollout for "openai-compatibility: streaming response empty when translating Codex protocol (/v1/responses) to OpenAI chat/completions" via flags, migration docs, and backward-compat tests.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1478
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1478
- Implementation note: Expand quickstart and troubleshooting docs with copy-paste examples and expected outputs.

### [CP2K-0360] Standardize naming/metadata affected by "bug: request-level metadata fields injected into contents[] causing Gemini API rejection (v6.8.4)" across both repos and docs.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1477
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1477
- Implementation note: Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.

### [CP2K-0366] Extend docs for "model not found for gpt-5.3-codex" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1463
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1463
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0370] Standardize naming/metadata affected by "When I don’t add the authentication file, opening Claude Code keeps throwing a 500 error, instead of directly using the AI provider I’ve configured." across both repos and docs.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: provider-model-registry
- Source: router-for-me/CLIProxyAPI issue#1455
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1455
- Implementation note: Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.

### [CP2K-0371] Follow up "6.7.53版本反重力无法看到opus-4.6模型" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#1453
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1453
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0372] Harden "Codex OAuth failed" with stricter validation, safer defaults, and explicit fallback semantics.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#1451
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1451
- Implementation note: Add failing-before/failing-after regression tests and update golden fixtures for each supported provider.

### [CP2K-0373] Operationalize "Google asking to Verify account" with observability, runbook updates, and deployment safeguards.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1447
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1447
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0374] Create or refresh provider quickstart derived from "API Error" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#1445
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1445
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0375] Improve CLI UX around "Unable to use GPT 5.3 codex (model_not_found)" with clearer commands, flags, and immediate validation feedback.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1443
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1443
- Implementation note: Instrument structured logs/metrics around request normalize->translate->dispatch lifecycle.

### [CP2K-0376] Extend docs for "gpt-5.3-codex 请求400 显示不存在该模型" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1442
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1442
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0381] Follow up "[BUG] Invalid JSON payload with large requests (~290KB) - truncated body" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1433
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1433
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0384] Generalize "[v6.7.47] 接入智谱 Plan 计划后请求报错" into provider-agnostic translation/utilities to reduce duplicate logic.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1430
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1430
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0387] Add robust stream/non-stream parity tests for "bug: Claude → Gemini translation fails due to unsupported JSON Schema fields ($id, patternProperties)" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1424
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1424
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0390] Standardize naming/metadata affected by "Security Review: Apply Lessons from Supermemory Security Findings" across both repos and docs.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1418
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1418
- Implementation note: Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.

### [CP2K-0391] Create or refresh provider quickstart derived from "Add Webhook Support for Document Lifecycle Events" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#1417
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1417
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0394] Generalize "Add Document Processor for PDF and URL Content Extraction" into provider-agnostic translation/utilities to reduce duplicate logic.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: provider-model-registry
- Source: router-for-me/CLIProxyAPI issue#1414
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1414
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0398] Refactor internals touched by "Implement MCP Server for Memory Operations" to reduce coupling and improve maintainability.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1410
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1410
- Implementation note: Benchmark p50/p95 latency and memory; reject regressions in CI quality gate.

### [CP2K-0400] Standardize naming/metadata affected by "Bug: /v1/responses returns 400 "Input must be a list" when input is string (regression 6.7.42, Droid auto-compress broken)" across both repos and docs.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1403
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1403
- Implementation note: Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.

### [CP2K-0401] Follow up "Factory Droid CLI got 404" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1401
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1401
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0403] Operationalize "Feature request: Cursor CLI support" with observability, runbook updates, and deployment safeguards.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#1399
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1399
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0404] Generalize "bug: Invalid signature in thinking block (API 400) on follow-up requests" into provider-agnostic translation/utilities to reduce duplicate logic.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1398
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1398
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0407] Add robust stream/non-stream parity tests for "Session title generation fails for Claude models via Antigravity provider (OpenCode)" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1394
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1394
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0408] Create or refresh provider quickstart derived from "反代反重力请求gemini-3-pro-image-preview接口报错" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#1393
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1393
- Implementation note: Benchmark p50/p95 latency and memory; reject regressions in CI quality gate.

### [CP2K-0409] Prepare safe rollout for "[Feature Request] Implement automatic account rotation on VALIDATION_REQUIRED errors" via flags, migration docs, and backward-compat tests.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1392
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1392
- Implementation note: Expand quickstart and troubleshooting docs with copy-paste examples and expected outputs.

### [CP2K-0413] Operationalize "在codex运行报错" with observability, runbook updates, and deployment safeguards.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: websocket-and-streaming
- Source: router-for-me/CLIProxyAPI issue#1406
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1406
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0415] Improve CLI UX around "Claude authentication failed in v6.7.41 (works in v6.7.25)" with clearer commands, flags, and immediate validation feedback.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#1383
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1383
- Implementation note: Instrument structured logs/metrics around request normalize->translate->dispatch lifecycle.

### [CP2K-0416] Extend docs for "Question: Does load balancing work with 2 Codex accounts for the Responses API?" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1382
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1382
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0417] Add robust stream/non-stream parity tests for "登陆提示“登录失败: 访问被拒绝，权限不足”" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#1381
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1381
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0419] Prepare safe rollout for "antigravity无法登录" via flags, migration docs, and backward-compat tests.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1376
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1376
- Implementation note: Expand quickstart and troubleshooting docs with copy-paste examples and expected outputs.

### [CP2K-0421] Follow up "API Error: 403" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1374
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1374
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0424] Generalize "Bad processing of Claude prompt caching that is already implemented by client app" into provider-agnostic translation/utilities to reduce duplicate logic.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1366
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1366
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0425] Create or refresh provider quickstart derived from "[Bug] OpenAI-compatible provider: message_start.usage always returns 0 tokens (kimi-for-coding)" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#1365
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1365
- Implementation note: Instrument structured logs/metrics around request normalize->translate->dispatch lifecycle.

### [CP2K-0426] Extend docs for "iflow Cli官方针对terminal有Oauth 登录方式" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#1364
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1364
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0428] Refactor internals touched by "“Error 404: Requested entity was not found" for gemini 3 by gemini-cli" to reduce coupling and improve maintainability.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1325
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1325
- Implementation note: Benchmark p50/p95 latency and memory; reject regressions in CI quality gate.

### [CP2K-0430] Standardize naming/metadata affected by "Feature Request: Add generateImages endpoint support for Gemini API" across both repos and docs.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1322
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1322
- Implementation note: Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.

### [CP2K-0431] Follow up "iFlow Error: LLM returned 200 OK but response body was empty (possible rate limit)" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#1321
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1321
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0432] Harden "feat: add code_execution and url_context tool passthrough for Gemini" with stricter validation, safer defaults, and explicit fallback semantics.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1318
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1318
- Implementation note: Add failing-before/failing-after regression tests and update golden fixtures for each supported provider.

### [CP2K-0436] Extend docs for "Claude Opus 4.5 returns "Internal server error" in response body via Anthropic OAuth (Sonnet works)" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1306
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1306
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0439] Prepare safe rollout for "版本: v6.7.27 添加openai-compatibility的时候出现 malformed HTTP response 错误" via flags, migration docs, and backward-compat tests.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1301
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1301
- Implementation note: Expand quickstart and troubleshooting docs with copy-paste examples and expected outputs.

### [CP2K-0440] Standardize naming/metadata affected by "fix(logging): request and API response timestamps are inaccurate in error logs" across both repos and docs.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: websocket-and-streaming
- Source: router-for-me/CLIProxyAPI issue#1299
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1299
- Implementation note: Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.

### [CP2K-0441] Follow up "cpaUsageMetadata leaks to Gemini API responses when using Antigravity backend" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1297
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1297
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0442] Create or refresh provider quickstart derived from "Gemini API error: empty text content causes 'required oneof field data must have one initialized field'" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#1293
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1293
- Implementation note: Add failing-before/failing-after regression tests and update golden fixtures for each supported provider.

### [CP2K-0443] Operationalize "Gemini API error: empty text content causes 'required oneof field data must have one initialized field'" with observability, runbook updates, and deployment safeguards.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1292
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1292
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0446] Extend docs for "Request takes over a minute to get sent with Antigravity" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: provider-model-registry
- Source: router-for-me/CLIProxyAPI issue#1289
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1289
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0447] Add robust stream/non-stream parity tests for "Antigravity auth requires daily re-login - sessions expire unexpectedly" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1288
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1288
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0449] Prepare safe rollout for "429 RESOURCE_EXHAUSTED for Claude Opus 4.5 Thinking with Google AI Pro Account" via flags, migration docs, and backward-compat tests.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1284
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1284
- Implementation note: Expand quickstart and troubleshooting docs with copy-paste examples and expected outputs.

### [CP2K-0452] Harden "Support request: Kimi For Coding (Kimi Code / K2.5) behind CLIProxyAPI" with stricter validation, safer defaults, and explicit fallback semantics.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1280
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1280
- Implementation note: Add failing-before/failing-after regression tests and update golden fixtures for each supported provider.

### [CP2K-0459] Create or refresh provider quickstart derived from "[Improvement] Pre-bundle Management UI in Docker Image" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#1266
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1266
- Implementation note: Expand quickstart and troubleshooting docs with copy-paste examples and expected outputs.

### [CP2K-0467] Add robust stream/non-stream parity tests for "CLIProxyAPI goes down after some time, only recovers when SSH into server" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#1253
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1253
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0468] Refactor internals touched by "kiro hope" to reduce coupling and improve maintainability.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#1252
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1252
- Implementation note: Benchmark p50/p95 latency and memory; reject regressions in CI quality gate.

### [CP2K-0469] Prepare safe rollout for ""Requested entity was not found" for all antigravity models" via flags, migration docs, and backward-compat tests.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1251
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1251
- Implementation note: Expand quickstart and troubleshooting docs with copy-paste examples and expected outputs.

### [CP2K-0476] Create or refresh provider quickstart derived from "GLM Coding Plan" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#1226
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1226
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0479] Prepare safe rollout for "auth_unavailable: no auth available in claude code cli, 使用途中经常500" via flags, migration docs, and backward-compat tests.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1222
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1222
- Implementation note: Expand quickstart and troubleshooting docs with copy-paste examples and expected outputs.

### [CP2K-0482] Harden "openai codex 认证失败: Failed to exchange authorization code for tokens" with stricter validation, safer defaults, and explicit fallback semantics.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1217
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1217
- Implementation note: Add failing-before/failing-after regression tests and update golden fixtures for each supported provider.

### [CP2K-0484] Generalize "Error 403" into provider-agnostic translation/utilities to reduce duplicate logic.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1214
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1214
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0485] Improve CLI UX around "Gemini CLI OAuth 认证失败: failed to start callback server" with clearer commands, flags, and immediate validation feedback.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#1213
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1213
- Implementation note: Instrument structured logs/metrics around request normalize->translate->dispatch lifecycle.

### [CP2K-0486] Extend docs for "bug: Thinking budget ignored in cross-provider conversations (Antigravity)" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1199
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1199
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0490] Standardize naming/metadata affected by "codex总是有失败" across both repos and docs.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1193
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1193
- Implementation note: Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.

### [CP2K-0493] Create or refresh provider quickstart derived from "🚨🔥 CRITICAL BUG REPORT: Invalid Function Declaration Schema in API Request 🔥🚨" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#1189
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1189
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0496] Extend docs for "使用 Antigravity OAuth 使用openai格式调用opencode问题" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#1173
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1173
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0497] Add robust stream/non-stream parity tests for "今天中午开始一直429" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: error-handling-retries
- Source: router-for-me/CLIProxyAPI issue#1172
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1172
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0508] Refactor internals touched by "[Bug] v6.7.x Regression: thinking parameter not recognized, causing Cherry Studio and similar clients to fail displaying extended thinking content" to reduce coupling and improve maintainability.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1155
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1155
- Implementation note: Benchmark p50/p95 latency and memory; reject regressions in CI quality gate.

### [CP2K-0510] Create or refresh provider quickstart derived from "Antigravity OAuth认证失败" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#1153
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1153
- Implementation note: Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.

### [CP2K-0516] Extend docs for "cc 使用 zai-glm-4.7 报错 body.reasoning" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1143
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1143
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0517] Add robust stream/non-stream parity tests for "NVIDIA不支持，转发成claude和gpt都用不了" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1139
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1139
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0520] Standardize naming/metadata affected by "tool_choice not working for Gemini models via Claude API endpoint" across both repos and docs.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1135
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1135
- Implementation note: Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.

### [CP2K-0527] Create or refresh provider quickstart derived from "gpt-5.2-codex "System messages are not allowed"" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#1122
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1122
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0531] Follow up "gemini-3-pro-high (Antigravity): malformed_function_call error with tools" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1113
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1113
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0533] Operationalize "香蕉pro 图片一下将所有图片额度都消耗没了" with observability, runbook updates, and deployment safeguards.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: error-handling-retries
- Source: router-for-me/CLIProxyAPI issue#1110
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1110
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0536] Extend docs for "gemini-3-pro-high returns empty response when subagent uses tools" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1106
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1106
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0537] Add robust stream/non-stream parity tests for "GitStore local repo fills tmpfs due to accumulating loose git objects (no GC/repack)" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: provider-model-registry
- Source: router-for-me/CLIProxyAPI issue#1104
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1104
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0541] Follow up "Wrong workspace selected for OpenAI accounts" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: provider-model-registry
- Source: router-for-me/CLIProxyAPI issue#1095
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1095
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0543] Operationalize "Antigravity 生图无法指定分辨率" with observability, runbook updates, and deployment safeguards.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1093
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1093
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0544] Create or refresh provider quickstart derived from "文件写方式在docker下容易出现Inode变更问题" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#1092
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1092
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0548] Refactor internals touched by "Streaming Response Translation Fails to Emit Completion Events on `[DONE]` Marker" to reduce coupling and improve maintainability.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1085
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1085
- Implementation note: Benchmark p50/p95 latency and memory; reject regressions in CI quality gate.

### [CP2K-0549] Prepare safe rollout for "Feature Request: Add support for Text Embedding API (/v1/embeddings)" via flags, migration docs, and backward-compat tests.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1084
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1084
- Implementation note: Expand quickstart and troubleshooting docs with copy-paste examples and expected outputs.

### [CP2K-0553] Operationalize "配额管理中可否新增Claude OAuth认证方式号池的配额信息" with observability, runbook updates, and deployment safeguards.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#1079
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1079
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0554] Generalize "Extended thinking model fails with "Expected thinking or redacted_thinking, but found tool_use" on multi-turn conversations" into provider-agnostic translation/utilities to reduce duplicate logic.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1078
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1078
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0555] Improve CLI UX around "functionDeclarations 和 googleSearch 合并到同一个 tool 对象导致 Gemini API 报错" with clearer commands, flags, and immediate validation feedback.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1077
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1077
- Implementation note: Instrument structured logs/metrics around request normalize->translate->dispatch lifecycle.

### [CP2K-0558] Refactor internals touched by "image generation 429" to reduce coupling and improve maintainability.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1073
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1073
- Implementation note: Benchmark p50/p95 latency and memory; reject regressions in CI quality gate.

### [CP2K-0559] Prepare safe rollout for "No Auth Available" via flags, migration docs, and backward-compat tests.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1072
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1072
- Implementation note: Expand quickstart and troubleshooting docs with copy-paste examples and expected outputs.

### [CP2K-0560] Standardize naming/metadata affected by "配置OpenAI兼容格式的API，用Anthropic接口 OpenAI接口都调用不成功" across both repos and docs.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1066
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1066
- Implementation note: Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.

### [CP2K-0561] Create or refresh provider quickstart derived from ""Think Mode" Reasoning models are not visible in GitHub Copilot interface" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#1065
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1065
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0562] Harden "Gemini 和 Claude 多条 system 提示词时，只有最后一条生效 / When Gemini and Claude have multiple system prompt words, only the last one takes effect" with stricter validation, safer defaults, and explicit fallback semantics.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1064
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1064
- Implementation note: Add failing-before/failing-after regression tests and update golden fixtures for each supported provider.

### [CP2K-0563] Operationalize "OAuth issue with Qwen using Google Social Login" with observability, runbook updates, and deployment safeguards.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1063
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1063
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0564] Generalize "[Feature] allow to disable auth files from UI (management)" into provider-agnostic translation/utilities to reduce duplicate logic.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#1062
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1062
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0567] Add robust stream/non-stream parity tests for "OpenAI 兼容提供商 由于客户端没有兼容OpenAI接口，导致调用失败" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1059
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1059
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0569] Prepare safe rollout for "[bug]在 opencode 多次正常请求后出现 500 Unknown Error 后紧接着 No Auth Available" via flags, migration docs, and backward-compat tests.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1057
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1057
- Implementation note: Expand quickstart and troubleshooting docs with copy-paste examples and expected outputs.

### [CP2K-0573] Operationalize "Codex authentication cannot be detected" with observability, runbook updates, and deployment safeguards.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: provider-model-registry
- Source: router-for-me/CLIProxyAPI issue#1052
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1052
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0574] Generalize "v6.7.3 OAuth 模型映射 新增或修改存在问题" into provider-agnostic translation/utilities to reduce duplicate logic.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#1051
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1051
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0576] Extend docs for "最新版本CPA，OAuths模型映射功能失败？" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#1048
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1048
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0577] Add robust stream/non-stream parity tests for "新增的Antigravity文件会报错429" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#1047
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1047
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0578] Create or refresh provider quickstart derived from "Docker部署缺失gemini-web-auth功能" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#1045
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1045
- Implementation note: Benchmark p50/p95 latency and memory; reject regressions in CI quality gate.

### [CP2K-0586] Extend docs for "macos webui Codex OAuth error" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1037
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1037
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0587] Add robust stream/non-stream parity tests for "antigravity 无法获取登录链接" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#1035
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1035
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0590] Standardize naming/metadata affected by "Antigravity auth causes infinite refresh loop when project_id cannot be fetched" across both repos and docs.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1030
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1030
- Implementation note: Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.

### [CP2K-0595] Create or refresh provider quickstart derived from "Vertex Credential Doesn't Work with gemini-3-pro-image-preview" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#1024
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1024
- Implementation note: Instrument structured logs/metrics around request normalize->translate->dispatch lifecycle.

### [CP2K-0601] Follow up "Antigravity Accounts Rate Limited (HTTP 429) Despite Available Quota" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#1015
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1015
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0605] Improve CLI UX around "「建议」希望能添加一个手动控制某 oauth 认证是否参与反代的功能" with clearer commands, flags, and immediate validation feedback.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#1010
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1010
- Implementation note: Instrument structured logs/metrics around request normalize->translate->dispatch lifecycle.

### [CP2K-0607] Add robust stream/non-stream parity tests for "添加openai v1 chat接口，使用responses调用，出现截断，最后几个字不显示" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1008
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1008
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0610] Standardize naming/metadata affected by "Feature: Add Veo 3.1 Video Generation Support" across both repos and docs.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1005
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1005
- Implementation note: Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.

### [CP2K-0611] Follow up "Bug: Streaming response.output_item.done missing function name" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#1004
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1004
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0612] Create or refresh provider quickstart derived from "Close" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#1003
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1003
- Implementation note: Add failing-before/failing-after regression tests and update golden fixtures for each supported provider.

### [CP2K-0614] Generalize "[Bug] Codex Responses API: item_reference in `input` not cleaned, causing 404 errors and incorrect client suspension" into provider-agnostic translation/utilities to reduce duplicate logic.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#999
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/999
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0615] Improve CLI UX around "[Bug] Codex Responses API: `input` 中的 item_reference 未清理，导致 404 错误和客户端被误暂停" with clearer commands, flags, and immediate validation feedback.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#998
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/998
- Implementation note: Instrument structured logs/metrics around request normalize->translate->dispatch lifecycle.

### [CP2K-0616] Extend docs for "【建议】保留Gemini格式请求的思考签名" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#997
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/997
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0624] Generalize "New OpenAI API: /responses/compact" into provider-agnostic translation/utilities to reduce duplicate logic.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#986
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/986
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0625] Improve CLI UX around "Bug Report: OAuth Login Failure on Windows due to Port 51121 Conflict" with clearer commands, flags, and immediate validation feedback.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#985
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/985
- Implementation note: Instrument structured logs/metrics around request normalize->translate->dispatch lifecycle.

### [CP2K-0626] Extend docs for "Claude model reports wrong/unknown model when accessed via API (Claude Code OAuth)" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#984
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/984
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0628] Refactor internals touched by "［建议］Codex渠道将System角色映射为Developer角色" to reduce coupling and improve maintainability.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#982
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/982
- Implementation note: Benchmark p50/p95 latency and memory; reject regressions in CI quality gate.

### [CP2K-0629] Create or refresh provider quickstart derived from "No Image Generation Models Available After Gemini CLI Setup" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#978
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/978
- Implementation note: Expand quickstart and troubleshooting docs with copy-paste examples and expected outputs.

### [CP2K-0631] Follow up "GPT5.2模型异常报错 auth_unavailable: no auth available" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#976
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/976
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0633] Operationalize "Auth files permanently deleted from S3 on service restart due to race condition" with observability, runbook updates, and deployment safeguards.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#973
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/973
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0637] Add robust stream/non-stream parity tests for "初次运行运行.exe文件报错" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#966
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/966
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0641] Follow up "Antigravity using Flash 2.0 Model for Sonet" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#960
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/960
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0645] Improve CLI UX around "[Feature] Allow define log filepath in config" with clearer commands, flags, and immediate validation feedback.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#954
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/954
- Implementation note: Instrument structured logs/metrics around request normalize->translate->dispatch lifecycle.

### [CP2K-0646] Create or refresh provider quickstart derived from "[建议]希望OpenAI 兼容提供商支持启用停用功能" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#953
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/953
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0647] Add robust stream/non-stream parity tests for "Reasoning field missing for gpt-5.1-codex-max at xhigh reasoning level (while gpt-5.2-codex works as expected)" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#952
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/952
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0650] Standardize naming/metadata affected by "Internal Server Error: {"error":{"message":"auth_unavailable: no auth available"... (click to expand) [retrying in 8s attempt #4]" across both repos and docs.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#949
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/949
- Implementation note: Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.

### [CP2K-0651] Follow up "[BUG] Multi-part Gemini response loses content - only last part preserved in OpenAI translation" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#948
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/948
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0653] Operationalize "接入openroute成功，但是下游使用异常" with observability, runbook updates, and deployment safeguards.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#942
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/942
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0654] Generalize "fix: use original request JSON for echoed fields in OpenAI Responses translator" into provider-agnostic translation/utilities to reduce duplicate logic.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#941
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/941
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0656] Extend docs for "[Feature Request] Support Priority Failover Strategy (Priority Queue) Instead of all Round-Robin" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: provider-model-registry
- Source: router-for-me/CLIProxyAPI issue#937
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/937
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0657] Add robust stream/non-stream parity tests for "[Feature Request] Support multiple aliases for a single model name in oauth-model-mappings" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#936
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/936
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0658] Refactor internals touched by "新手登陆认证问题" to reduce coupling and improve maintainability.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#934
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/934
- Implementation note: Benchmark p50/p95 latency and memory; reject regressions in CI quality gate.

### [CP2K-0661] Follow up "Gemini 3 Pro cannot perform native tool calls in Roo Code" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#931
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/931
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0662] Harden "Qwen OAuth Request Error" with stricter validation, safer defaults, and explicit fallback semantics.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#930
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/930
- Implementation note: Add failing-before/failing-after regression tests and update golden fixtures for each supported provider.

### [CP2K-0663] Create or refresh provider quickstart derived from "无法在 api 代理中使用 Anthropic 模型，报错 429" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#929
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/929
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0666] Extend docs for "同一个chatgpt账号加入了多个工作空间，同时个人账户也有gptplus，他们的codex认证文件在cliproxyapi不能同时使用" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#926
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/926
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0669] Prepare safe rollout for "Help for setting mistral" via flags, migration docs, and backward-compat tests.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#920
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/920
- Implementation note: Expand quickstart and troubleshooting docs with copy-paste examples and expected outputs.

### [CP2K-0671] Follow up "How to run this?" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#917
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/917
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0677] Add robust stream/non-stream parity tests for "Antigravity models return 429 RESOURCE_EXHAUSTED via cURL, but Antigravity IDE still works (started ~18:00 GMT+7)" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#910
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/910
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0678] Refactor internals touched by "gemini3p报429，其他的都好好的" to reduce coupling and improve maintainability.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#908
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/908
- Implementation note: Benchmark p50/p95 latency and memory; reject regressions in CI quality gate.

### [CP2K-0680] Create or refresh provider quickstart derived from "新版本运行闪退" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#906
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/906
- Implementation note: Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.

### [CP2K-0682] Harden "⎿ 429 {"error":{"code":"model_cooldown","message":"All credentials for model gemini-claude-opus-4-5-thinking are cooling down via provider antigravity","model":"gemini-claude-opus-4-5-thinking","provider":"antigravity","reset_seconds" with stricter validation, safer defaults, and explicit fallback semantics.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#904
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/904
- Implementation note: Add failing-before/failing-after regression tests and update golden fixtures for each supported provider.

### [CP2K-0685] Improve CLI UX around "OpenAI Codex returns 400: Unsupported parameter: prompt_cache_retention" with clearer commands, flags, and immediate validation feedback.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#897
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/897
- Implementation note: Instrument structured logs/metrics around request normalize->translate->dispatch lifecycle.

### [CP2K-0687] Add robust stream/non-stream parity tests for "Apply Routing Strategy also to Auth Files" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#893
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/893
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0689] Prepare safe rollout for "Cursor subscription support" via flags, migration docs, and backward-compat tests.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#891
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/891
- Implementation note: Expand quickstart and troubleshooting docs with copy-paste examples and expected outputs.

### [CP2K-0691] Follow up "[Bug] Codex auth file overwritten when account has both Plus and Team plans" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#887
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/887
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0693] Operationalize "can not work with mcp:ncp on antigravity auth" with observability, runbook updates, and deployment safeguards.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#885
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/885
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0694] Generalize "Gemini Cli Oauth 认证失败" into provider-agnostic translation/utilities to reduce duplicate logic.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#884
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/884
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0697] Create or refresh provider quickstart derived from "同时使用GPT账号个人空间和团队空间" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#875
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/875
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0707] Add robust stream/non-stream parity tests for "[Bug] Infinite hanging and quota surge with gemini-claude-opus-4-5-thinking in Claude Code" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#852
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/852
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0709] Prepare safe rollout for "功能请求：为 OAuth 账户添加独立代理配置支持" via flags, migration docs, and backward-compat tests.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#847
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/847
- Implementation note: Expand quickstart and troubleshooting docs with copy-paste examples and expected outputs.

### [CP2K-0710] Standardize naming/metadata affected by "Promt caching" across both repos and docs.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#845
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/845
- Implementation note: Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.

### [CP2K-0714] Create or refresh provider quickstart derived from "Image Generation 504 Timeout Investigation" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#839
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/839
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0717] Add robust stream/non-stream parity tests for "[Bug] Antigravity token refresh loop caused by metadataEqualIgnoringTimestamps skipping critical field updates" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#833
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/833
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0721] Follow up "windows环境下，认证文件显示重复的BUG" by closing compatibility gaps and locking in regression coverage.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#822
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/822
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0724] Generalize "模型带前缀并开启force_model_prefix后，以gemini格式获取模型列表中没有带前缀的模型" into provider-agnostic translation/utilities to reduce duplicate logic.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: provider-model-registry
- Source: router-for-me/CLIProxyAPI issue#816
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/816
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0726] Extend docs for "代理的codex 404" with quickstart snippets and troubleshooting decision trees.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#812
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/812
- Implementation note: Add staged rollout controls (feature flags) with safe defaults and migration notes.

### [CP2K-0728] Refactor internals touched by "Request for maintenance team intervention: Changes in internal/translator needed" to reduce coupling and improve maintainability.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#806
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/806
- Implementation note: Benchmark p50/p95 latency and memory; reject regressions in CI quality gate.

### [CP2K-0729] Prepare safe rollout for "feat(translator): integrate SanitizeFunctionName across Claude translators" via flags, migration docs, and backward-compat tests.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: responses-and-chat-compat
- Source: router-for-me/CLIProxyAPI issue#804
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/804
- Implementation note: Expand quickstart and troubleshooting docs with copy-paste examples and expected outputs.

### [CP2K-0731] Create or refresh provider quickstart derived from "在cherry-studio中的流失响应似乎未生效" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#798
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/798
- Implementation note: Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.

### [CP2K-0732] Harden "Bug: ModelStates (BackoffLevel) lost when auth is reloaded or refreshed" with stricter validation, safer defaults, and explicit fallback semantics.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#797
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/797
- Implementation note: Add failing-before/failing-after regression tests and update golden fixtures for each supported provider.

### [CP2K-0733] Operationalize "[Bug] Stream usage data is merged with finish_reason: "stop", causing Letta AI to crash (OpenAI Stream Options incompatibility)" with observability, runbook updates, and deployment safeguards.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: provider-model-registry
- Source: router-for-me/CLIProxyAPI issue#796
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/796
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0734] Generalize "[BUG] Codex 默认回调端口 1455 位于 Hyper-v 保留端口段内" into provider-agnostic translation/utilities to reduce duplicate logic.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: provider-model-registry
- Source: router-for-me/CLIProxyAPI issue#793
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/793
- Implementation note: Refactor translation layer to isolate provider transform logic from transport concerns.

### [CP2K-0735] Improve CLI UX around "【Bug】: High CPU usage when managing 50+ OAuth accounts" with clearer commands, flags, and immediate validation feedback.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#792
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/792
- Implementation note: Instrument structured logs/metrics around request normalize->translate->dispatch lifecycle.

### [CP2K-0737] Add robust stream/non-stream parity tests for "当在codex exec 中使用gemini 或claude 模型时 codex 无输出结果" across supported providers.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#790
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/790
- Implementation note: Harden edge-case parsing for stream and non-stream payload variants.

### [CP2K-0739] Prepare safe rollout for "[Bug]: Gemini Models Output Truncated - Database Schema Exceeds Maximum Allowed Tokens (140k+ chars) in Claude Code" via flags, migration docs, and backward-compat tests.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#788
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/788
- Implementation note: Expand quickstart and troubleshooting docs with copy-paste examples and expected outputs.

### [CP2K-0743] Operationalize "当认证账户消耗完之后，不会自动切换到 AI 提供商账户" with observability, runbook updates, and deployment safeguards.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: websocket-and-streaming
- Source: router-for-me/CLIProxyAPI issue#777
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/777
- Implementation note: Improve error diagnostics and add actionable remediation text in CLI and docs.

### [CP2K-0748] Create or refresh provider quickstart derived from "support proxy for opencode" with setup/auth/model/sanity-check flow.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: docs-quickstarts
- Source: router-for-me/CLIProxyAPI issue#753
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/753
- Implementation note: Benchmark p50/p95 latency and memory; reject regressions in CI quality gate.

### [CP2K-0749] Prepare safe rollout for "[BUG] thinking/思考链在 antigravity 反代下被截断/丢失（stream 分块处理过严）" via flags, migration docs, and backward-compat tests.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: thinking-and-reasoning
- Source: router-for-me/CLIProxyAPI issue#752
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/752
- Implementation note: Expand quickstart and troubleshooting docs with copy-paste examples and expected outputs.

### [CP2K-0750] Standardize naming/metadata affected by "api-keys 필드에 placeholder 값이 있으면 invalid api key 에러 발생" across both repos and docs.
- Priority: P1
- Wave: wave-1
- Effort: S
- Theme: oauth-and-authentication
- Source: router-for-me/CLIProxyAPI issue#751
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/751
- Implementation note: Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.

## Full 2000 Items
- Use the CSV/JSON artifacts for full import and sorting.
