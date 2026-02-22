# CLIProxyAPI Ecosystem 1000-Item Board

- Generated: 2026-02-22
- Scope: `router-for-me/CLIProxyAPIPlus` issues/PRs/discussions + `router-for-me/CLIProxyAPI` issues/PRs/discussions
- Goal: prioritized quality, compatibility, docs, CLI extraction, integration, dev-runtime, and UX/DX polish workboard

## Source Coverage
- sources_total_unique: 1865
- issues_plus: 81
- issues_core: 880
- prs_plus: 169
- prs_core: 577
- discussions_plus: 3
- discussions_core: 155

## Theme Distribution (Board)
- thinking-and-reasoning: 228
- responses-and-chat-compat: 163
- general-polish: 111
- provider-model-registry: 110
- websocket-and-streaming: 72
- docs-quickstarts: 65
- oauth-and-authentication: 58
- go-cli-extraction: 49
- integration-api-bindings: 39
- cli-ux-dx: 34
- dev-runtime-refresh: 30
- error-handling-retries: 17
- install-and-ops: 16
- testing-and-quality: 5
- platform-architecture: 2
- project-frontmatter: 1

## Priority Bands
- `P1`: interoperability, auth, translation correctness, stream stability, install/setup, migration safety
- `P2`: maintainability, test depth, runtime ergonomics, model metadata consistency
- `P3`: polish, docs expansion, optional ergonomics, non-critical UX improvements

## 1000 Items

### [CPB-0001] Extract a standalone Go mgmt CLI from thegent-owned cliproxy flows (`install`, `doctor`, `login`, `models`, `watch`, `reload`).
- Priority: P1
- Effort: L
- Theme: platform-architecture
- Status: proposed
- Source: cross-repo synthesis
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0002] Define non-subprocess integration surface for thegent: local Go bindings (preferred) and HTTP API fallback with capability negotiation.
- Priority: P1
- Effort: L
- Theme: platform-architecture
- Status: proposed
- Source: cross-repo synthesis
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0003] Add `cliproxy dev` process-compose profile with hot reload, config regeneration watch, and explicit `refresh` command.
- Priority: P1
- Effort: M
- Theme: install-and-ops
- Status: proposed
- Source: cross-repo synthesis
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0004] Ship provider-specific quickstarts (Codex, Claude, Gemini, Copilot, Kiro, MiniMax, OpenAI-compat) with 5-minute success path.
- Priority: P1
- Effort: M
- Theme: docs-quickstarts
- Status: proposed
- Source: cross-repo synthesis
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0005] Create troubleshooting matrix: auth failures, model not found, reasoning mismatch, stream parse faults, timeout classes.
- Priority: P1
- Effort: M
- Theme: docs-quickstarts
- Status: proposed
- Source: cross-repo synthesis
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0006] Introduce interactive first-run setup wizard in Go CLI with profile detection, auth choice, and post-check summary.
- Priority: P1
- Effort: M
- Theme: cli-ux-dx
- Status: proposed
- Source: cross-repo synthesis
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0007] Add `cliproxy doctor --fix` with deterministic remediation steps and machine-readable JSON report mode.
- Priority: P1
- Effort: M
- Theme: cli-ux-dx
- Status: proposed
- Source: cross-repo synthesis
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0008] Establish conformance suite for OpenAI Responses + Chat Completions translation across all providers.
- Priority: P1
- Effort: L
- Theme: testing-and-quality
- Status: proposed
- Source: cross-repo synthesis
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0009] Add golden fixture tests for reasoning controls (`variant`, `reasoning_effort`, `reasoning.effort`, model suffix).
- Priority: P1
- Effort: M
- Theme: testing-and-quality
- Status: proposed
- Source: cross-repo synthesis
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0010] Rewrite repo frontmatter: mission, architecture, support policy, compatibility matrix, release channels, contribution path.
- Priority: P2
- Effort: M
- Theme: project-frontmatter
- Status: proposed
- Source: cross-repo synthesis
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0011] Follow up on "kiro账号被封" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#221
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/221
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0012] Harden "Opus 4.6" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#219
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/219
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0013] Operationalize "Bug: MergeAdjacentMessages drops tool_calls from assistant messages" with observability, alerting thresholds, and runbook updates.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#217
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/217
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0014] Convert "Add support for proxying models from kilocode CLI" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#213
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/213
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0015] Add DX polish around "[Bug] Kiro 与 Ampcode 的 Bash 工具参数不兼容" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#210
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/210
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0016] Expand docs and examples for "[Feature Request] Add default oauth-model-alias for Kiro channel (like Antigravity)" with copy-paste quickstart and troubleshooting section.
- Priority: P1
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#208
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/208
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0017] Create/refresh provider quickstart derived from "bug: Nullable type arrays in tool schemas cause 400 error on Antigravity/Droid Factory" including setup, auth, model select, and sanity-check commands.
- Priority: P1
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#206
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/206
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0018] Refactor implementation behind "GitHub Copilot CLI 使用方法" to reduce complexity and isolate transformation boundaries.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#202
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/202
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0019] Port relevant thegent-managed flow implied by "failed to save config: open /CLIProxyAPI/config.yaml: read-only file system" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#201
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/201
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0020] Standardize metadata and naming conventions touched by "gemini能不能设置配额,自动禁用 ,自动启用?" across both repos.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#200
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/200
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0021] Follow up on "Cursor CLI \ Auth Support" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#198
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/198
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0022] Harden "Why no opus 4.6 on github copilot auth" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#196
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/196
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0023] Define non-subprocess integration path related to "why no kiro in dashboard" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#183
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/183
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0024] Convert "OpenAI-MLX-Server and vLLM-MLX Support?" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#179
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/179
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0025] Add DX polish around "Claude thought_signature forwarded to Gemini causes Base64 decode error" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#178
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/178
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0026] Expand docs and examples for "Kiro Token 导入失败: Refresh token is required" with copy-paste quickstart and troubleshooting section.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#177
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/177
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0027] Add QA scenarios for "Kimi Code support" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#169
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/169
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0028] Refactor implementation behind "kiro如何看配额？" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#165
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/165
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0029] Add process-compose/HMR refresh workflow tied to "kiro反代的Write工具json截断问题，返回的文件路径经常是错误的" so local config and runtime can be reloaded deterministically.
- Priority: P2
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#164
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/164
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0030] Standardize metadata and naming conventions touched by "fix(kiro): handle empty content in messages to prevent Bad Request errors" across both repos.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#163
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/163
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0031] Follow up on "在配置文件中支持为所有 OAuth 渠道自定义上游 URL" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#158
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/158
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0032] Harden "kiro反代出现重复输出的情况" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#160
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/160
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0033] Operationalize "kiro IDC 刷新 token 失败" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#149
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/149
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0034] Create/refresh provider quickstart derived from "请求docker部署支持arm架构的机器！感谢。" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#147
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/147
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0035] Add DX polish around "[Feature Request] 请求增加 Kiro 配额的展示功能" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#146
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/146
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0036] Expand docs and examples for "[Bug]进一步完善 openai兼容模式对 claude 模型的支持（完善 协议格式转换 ）" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#145
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/145
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0037] Add QA scenarios for "完善 claude openai兼容渠道的格式转换" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#142
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/142
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0038] Port relevant thegent-managed flow implied by "Kimi For Coding Support / 请求为 Kimi 添加编程支持" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#141
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/141
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0039] Ensure rollout safety for "kiro idc登录需要手动刷新状态" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#136
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/136
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0040] Standardize metadata and naming conventions touched by "[Bug Fix] 修复 Kiro 的Claude模型非流式请求 output_tokens 为 0 导致的用量统计缺失" across both repos.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#134
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/134
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0041] Follow up on "Routing strategy "fill-first" is not working as expected" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#133
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/133
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0042] Harden "WARN kiro_executor.go:1189 kiro: received 400 error (attempt 1/3), body: {"message":"Improperly formed request.","reason":null}" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#131
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/131
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0043] Operationalize "CLIProxyApiPlus不支持像CLIProxyApi一样使用ClawCloud云部署吗？" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#129
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/129
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0044] Convert "kiro的social凭证无法刷新过期时间。" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#128
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/128
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0045] Add DX polish around "Error 403" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#125
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/125
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0046] Define non-subprocess integration path related to "Gemini3无法生图" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#122
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/122
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0047] Add QA scenarios for "enterprise 账号 Kiro不是很稳定，很容易就403不可用了" including stream/non-stream parity and edge-case payloads.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#118
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/118
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0048] Refactor implementation behind "-kiro-aws-login 登录后一直封号" to reduce complexity and isolate transformation boundaries.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#115
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/115
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0049] Ensure rollout safety for "[Bug]Copilot Premium usage significantly amplified when using amp" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#113
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/113
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0050] Standardize metadata and naming conventions touched by "Antigravity authentication failed" across both repos.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#111
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/111
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0051] Create/refresh provider quickstart derived from "大佬，什么时候搞个多账号管理呀" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#108
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/108
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0052] Harden "日志中,一直打印auth file changed (WRITE)" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#105
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/105
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0053] Operationalize "登录incognito参数无效" with observability, alerting thresholds, and runbook updates.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#102
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/102
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0054] Convert "OpenAI-compat provider hardcodes /v1/models (breaks Z.ai v4: /api/coding/paas/v4/models)" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#101
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/101
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0055] Add DX polish around "ADD TRAE IDE support" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#97
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/97
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0056] Expand docs and examples for "Kiro currently has no authentication available" with copy-paste quickstart and troubleshooting section.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#96
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/96
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0057] Port relevant thegent-managed flow implied by "GitHub Copilot Model Call Failure" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#99
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/99
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0058] Add process-compose/HMR refresh workflow tied to "Feature: Add Veo Video Generation Support (Similar to Image Generation)" so local config and runtime can be reloaded deterministically.
- Priority: P2
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#94
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/94
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0059] Ensure rollout safety for "Bug: Kiro/BuilderId tokens can collide when email/profile_arn are empty; refresh token lifecycle not handled" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#90
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/90
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0060] Standardize metadata and naming conventions touched by "[Bug] Amazon Q endpoint returns HTTP 400 ValidationException (wrong CLI/KIRO_CLI origin)" across both repos.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#89
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/89
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0061] Follow up on "UI 上没有 Kiro 配置的入口，或者说想添加 Kiro 支持，具体该怎么做" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#87
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/87
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0062] Harden "Cursor Issue" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#86
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/86
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0063] Operationalize "Feature request: Configurable HTTP request timeout for Extended Thinking models" with observability, alerting thresholds, and runbook updates.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#84
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/84
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0064] Convert "kiro请求偶尔报错event stream fatal" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#83
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/83
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0065] Add DX polish around "failed to load config: failed to read config file: read /CLIProxyAPI/config.yaml: is a directory" through improved command ergonomics and faster feedback loops.
- Priority: P3
- Effort: S
- Theme: error-handling-retries
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#81
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/81
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0066] Expand docs and examples for "[建议] 技术大佬考虑可以有机会新增一堆逆向平台" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#79
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/79
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0067] Add QA scenarios for "Issue with removed parameters - Sequential Thinking Tool Failure (nextThoughtNeeded undefined)" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#78
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/78
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0068] Create/refresh provider quickstart derived from "kiro请求的数据好像一大就会出错,导致cc写入文件失败" including setup, auth, model select, and sanity-check commands.
- Priority: P1
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#77
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/77
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0069] Define non-subprocess integration path related to "[Bug] Kiro multi-account support broken - auth file overwritten on re-login" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P1
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#76
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/76
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0070] Standardize metadata and naming conventions touched by "Claude Code WebSearch fails with 400 error when using Kiro/Amazon Q backend" across both repos.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#72
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/72
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0071] Follow up on "[BUG] Vision requests fail for ZAI (glm) and Copilot models with missing header / invalid parameter errors" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#69
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/69
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0072] Harden "怎么更新iflow的模型列表。" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#66
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/66
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0073] Operationalize "How to use KIRO with IAM?" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#56
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/56
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0074] Convert "[Bug] Models from Codex (openai) are not accessible when Copilot is added" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#43
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/43
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0075] Add DX polish around "model gpt-5.1-codex-mini is not accessible via the /chat/completions endpoint" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#41
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/41
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0076] Port relevant thegent-managed flow implied by "GitHub Copilot models seem to be hardcoded" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#37
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/37
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0077] Add QA scenarios for "plus版本只能自己构建吗？" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#34
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/34
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0078] Refactor implementation behind "kiro命令登录没有端口" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: install-and-ops
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#30
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/30
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0079] Ensure rollout safety for "lack of thinking signature in kiro's non-stream response cause incompatibility with some ai clients (specifically cherry studio)" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#27
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/27
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0080] Standardize metadata and naming conventions touched by "I did not find the Kiro entry in the Web UI" across both repos.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#26
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/26
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0081] Follow up on "Kiro (AWS CodeWhisperer) - Stream error, status: 400" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus issue#7
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/issues/7
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0082] Harden "BUG: Cannot use Claude Models in Codex CLI" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1671
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1671
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0083] Operationalize "feat: support image content in tool result messages (OpenAI ↔ Claude translation)" with observability, alerting thresholds, and runbook updates.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1670
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1670
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0084] Convert "docker镜像及docker相关其它优化建议" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P3
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1669
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1669
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0085] Create/refresh provider quickstart derived from "Need maintainer-handled codex translator compatibility for Responses compaction fields" including setup, auth, model select, and sanity-check commands.
- Priority: P1
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1667
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1667
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0086] Expand docs and examples for "codex: usage_limit_reached (429) should honor resets_at/resets_in_seconds as next_retry_after" with copy-paste quickstart and troubleshooting section.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1666
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1666
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0087] Add process-compose/HMR refresh workflow tied to "Concerns regarding the removal of Gemini Web support in the early stages of the project" so local config and runtime can be reloaded deterministically.
- Priority: P2
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1665
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1665
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0088] Refactor implementation behind "fix(claude): token exchange blocked by Cloudflare managed challenge on console.anthropic.com" to reduce complexity and isolate transformation boundaries.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1659
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1659
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0089] Ensure rollout safety for "Qwen Oauth fails" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1658
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1658
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0090] Standardize metadata and naming conventions touched by "logs-max-total-size-mb does not account for per-day subdirectories" across both repos.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1657
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1657
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0091] Follow up on "All credentials for model claude-sonnet-4-6 are cooling down" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1655
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1655
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0092] Define non-subprocess integration path related to ""Please add claude-sonnet-4-6 to registered Claude models. Released 2026-02-15."" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1653
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1653
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0093] Operationalize "Claude Sonnet 4.5 models are deprecated - please remove from panel" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1651
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1651
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0094] Convert "Gemini API integration: incorrect renaming of 'parameters' to 'parametersJsonSchema'" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1649
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1649
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0095] Port relevant thegent-managed flow implied by "codex 返回 Unsupported parameter: response_format" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P1
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1647
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1647
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0096] Expand docs and examples for "Bug: Invalid JSON payload when tool_result has no content field (antigravity translator)" with copy-paste quickstart and troubleshooting section.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1646
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1646
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0097] Add QA scenarios for "Docker Image Error" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: error-handling-retries
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1641
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1641
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0098] Refactor implementation behind "Google blocked my 3 email id at once" to reduce complexity and isolate transformation boundaries.
- Priority: P1
- Effort: S
- Theme: error-handling-retries
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1637
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1637
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0099] Ensure rollout safety for "不同思路的 Antigravity 代理" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1633
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1633
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0100] Standardize metadata and naming conventions touched by "是否支持微软账号的反代？" across both repos.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1632
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1632
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0101] Follow up on "Google官方好像已经有检测并稳定封禁CPA反代Antigravity的方案了？" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1631
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1631
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0102] Create/refresh provider quickstart derived from "Claude Sonnet 4.5 is no longer available. Please switch to Claude Sonnet 4.6." including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1630
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1630
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0103] Operationalize "codex 中 plus/team错误支持gpt-5.3-codex-spark 但实际上不支持" with observability, alerting thresholds, and runbook updates.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1623
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1623
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0104] Convert "Please add support for Claude Sonnet 4.6" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1622
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1622
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0105] Add DX polish around "Question: applyClaudeHeaders() — how were these defaults chosen?" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1621
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1621
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0106] Expand docs and examples for "[BUG] claude code 接入 cliproxyapi 使用时，模型的输出没有呈现流式，而是一下子蹦出来回答结果" with copy-paste quickstart and troubleshooting section.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1620
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1620
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0107] Add QA scenarios for "[Feature Request] Session-Aware Hybrid Routing Strategy" including stream/non-stream parity and edge-case payloads.
- Priority: P3
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1617
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1617
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0108] Refactor implementation behind "Any Plans to support Jetbrains IDE?" to reduce complexity and isolate transformation boundaries.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1615
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1615
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0109] Ensure rollout safety for "[bug] codex oauth登录流程失败" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1612
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1612
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0110] Standardize metadata and naming conventions touched by "qwen auth 里获取到了 qwen3.5，但是 ai 客户端获取不到这个模型" across both repos.
- Priority: P2
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1611
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1611
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0111] Follow up on "fix: handle response.function_call_arguments.done in codex→claude streaming translator" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1609
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1609
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0112] Harden "不能正确统计minimax-m2.5/kimi-k2.5的Token" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1607
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1607
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0113] Operationalize "速速支持qwen code的qwen3.5" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1603
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1603
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0114] Port relevant thegent-managed flow implied by "[Feature Request] Antigravity channel should support routing claude-haiku-4-5-20251001 model (used by Claude Code pre-flight checks)" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P1
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1596
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1596
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0115] Define non-subprocess integration path related to "希望为提供商添加请求优先级功能，最好是以模型为基础来进行请求" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1594
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1594
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0116] Add process-compose/HMR refresh workflow tied to "gpt-5.3-codex-spark error" so local config and runtime can be reloaded deterministically.
- Priority: P2
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1593
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1593
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0117] Add QA scenarios for "[Bug] Claude Code 2.1.37 random cch in x-anthropic-billing-header causes severe prompt-cache miss on third-party upstreams" including stream/non-stream parity and edge-case payloads.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1592
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1592
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0118] Refactor implementation behind "()强制思考会在2m左右时返回500错误" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1591
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1591
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0119] Create/refresh provider quickstart derived from "配额管理可以刷出额度，但是调用的时候提示额度不足" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1590
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1590
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0120] Standardize metadata and naming conventions touched by "每次更新或者重启 使用统计数据都会清空" across both repos.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1589
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1589
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0121] Follow up on "iflow GLM 5 时不时会返回 406" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1588
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1588
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0122] Harden "封号了，pro号没了，又找了个免费认证bot分享出来" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1587
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1587
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0123] Operationalize "gemini-cli 不能自定请求头吗？" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1586
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1586
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0124] Convert "bug: Invalid thinking block signature when switching from Gemini CLI to Claude OAuth mid-conversation" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1584
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1584
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0125] Add DX polish around "I saved 10M tokens (89%) on my Claude Code sessions with a CLI proxy" through improved command ergonomics and faster feedback loops.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1583
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1583
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0126] Expand docs and examples for "[bug]? gpt-5.3-codex-spark 在 team 账户上报错 400" with copy-paste quickstart and troubleshooting section.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1582
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1582
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0127] Add QA scenarios for "希望能加一个一键清理失效的认证文件功能" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1580
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1580
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0128] Refactor implementation behind "GPT Team认证似乎获取不到5.3 Codex" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1577
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1577
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0129] Ensure rollout safety for "iflow渠道调用会一直返回406状态码" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1576
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1576
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0130] Standardize metadata and naming conventions touched by "Port 8317 becomes unreachable after running for some time, recovers immediately after SSH login" across both repos.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1575
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1575
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0131] Follow up on "Support for gpt-5.3-codex-spark" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1573
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1573
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0132] Harden "Reasoning Error" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1572
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1572
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0133] Port relevant thegent-managed flow implied by "iflow MiniMax-2.5 is online，please add" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1567
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1567
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0134] Convert "能否再难用一点?!" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1564
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1564
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0135] Add DX polish around "Cache usage through Claude oAuth always 0" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1562
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1562
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0136] Create/refresh provider quickstart derived from "antigravity 无法使用" including setup, auth, model select, and sanity-check commands.
- Priority: P1
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1561
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1561
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0137] Add QA scenarios for "GLM-5 return empty" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1560
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1560
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0138] Define non-subprocess integration path related to "Claude Code 调用 nvidia 发现 无法正常使用bash grep类似的工具" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1557
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1557
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0139] Ensure rollout safety for "Gemini CLI: 额度获取失败：请检查凭证状态" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1556
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1556
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0140] Standardize metadata and naming conventions touched by "403 error" across both repos.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1555
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1555
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0141] Follow up on "iflow glm-5 is online，please add" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1554
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1554
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0142] Harden "Kimi的OAuth无法使用" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1553
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1553
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0143] Operationalize "grok的OAuth登录认证可以支持下吗？ 谢谢！" with observability, alerting thresholds, and runbook updates.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1552
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1552
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0144] Convert "iflow executor: token refresh failed" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1551
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1551
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0145] Add process-compose/HMR refresh workflow tied to "为什么gemini3会报错" so local config and runtime can be reloaded deterministically.
- Priority: P1
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1549
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1549
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0146] Expand docs and examples for "cursor报错根源" with copy-paste quickstart and troubleshooting section.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1548
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1548
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0147] Add QA scenarios for "[Claude code] ENABLE_TOOL_SEARCH - MCP not in available tools 400" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1547
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1547
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0148] Refactor implementation behind "自定义别名在调用的时候404" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1546
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1546
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0149] Ensure rollout safety for "删除iflow提供商的过时模型" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1545
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1545
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0150] Standardize metadata and naming conventions touched by "删除iflow提供商的过时模型" across both repos.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1544
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1544
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0151] Follow up on "佬们，隔壁很多账号403啦，这里一切正常吗？" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1541
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1541
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0152] Port relevant thegent-managed flow implied by "feat(thinking): support Claude output_config.effort parameter (Opus 4.6)" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P1
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1540
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1540
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0153] Create/refresh provider quickstart derived from "Gemini-3-pro-high Corrupted thought signature" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1538
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1538
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0154] Convert "bug: "status": "INVALID_ARGUMENT" when using antigravity claude-opus-4-6" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1535
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1535
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0155] Add DX polish around "[Bug] Persistent 400 "Invalid Argument" error with claude-opus-4-6-thinking model (with and without thinking budget)" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1533
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1533
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0156] Expand docs and examples for "Invalid JSON payload received: Unknown name \"deprecated\"" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1531
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1531
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0157] Add QA scenarios for "bug: proxy_ prefix applied to tool_choice.name but not tools[].name causes 400 errors on OAuth requests" including stream/non-stream parity and edge-case payloads.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1530
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1530
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0158] Refactor implementation behind "请求为Windows添加启动自动更新命令" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1528
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1528
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0159] Ensure rollout safety for "反重力逻辑加载失效" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1526
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1526
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0160] Standardize metadata and naming conventions touched by "support openai image generations api(/v1/images/generations)" across both repos.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1525
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1525
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0161] Define non-subprocess integration path related to "The account has available credit, but a 503 or 429 error is occurring." (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1521
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1521
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0162] Harden "openclaw调用CPA 中的codex5.2 报错。" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1517
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1517
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0163] Operationalize "opus4.6都支持1m的上下文了，请求体什么时候从280K调整下，现在也太小了，动不动就报错" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1515
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1515
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0164] Convert "Token refresh logic fails with generic 500 error ("server busy") from iflow provider" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1514
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1514
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0165] Add DX polish around "bug: Nullable type arrays in tool schemas cause 400 error on Antigravity/Droid Factory" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1513
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1513
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0166] Expand docs and examples for "请求体过大280KB限制和opus 4.6无法调用的问题，啥时候可以修复" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1512
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1512
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0167] Add QA scenarios for "502 unknown provider for model gemini-claude-opus-4-6-thinking" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1510
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1510
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0168] Refactor implementation behind "反重力 claude-opus-4-6-thinking 模型如何通过 () 实现强行思考" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1509
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1509
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0169] Ensure rollout safety for "Feature: Per-OAuth-Account Outbound Proxy Enforcement for Google (Gemini/Antigravity) + OpenAI Codex – incl. Token Refresh and optional Strict/Fail-Closed Mode" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1508
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1508
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0170] Create/refresh provider quickstart derived from "[BUG] 反重力 Opus-4.5 在 OpenCode 上搭配 DCP 插件使用时会报错" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1507
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1507
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0171] Port relevant thegent-managed flow implied by "Antigravity使用时，设计额度最小阈值，超过停止使用或者切换账号，因为额度多次用尽，会触发 5 天刷新" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1505
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1505
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0172] Harden "iflow的glm-4.7会返回406" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1504
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1504
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0173] Operationalize "[BUG] sdkaccess.RegisterProvider 逻辑被 syncInlineAccessProvider 破坏" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1503
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1503
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0174] Add process-compose/HMR refresh workflow tied to "iflow部分模型增加了签名" so local config and runtime can be reloaded deterministically.
- Priority: P2
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1501
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1501
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0175] Add DX polish around "Qwen Free allocated quota exceeded" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1500
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1500
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0176] Expand docs and examples for "After logging in with iFlowOAuth, most models cannot be used, only non-CLI models can be used." with copy-paste quickstart and troubleshooting section.
- Priority: P1
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1499
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1499
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0177] Add QA scenarios for "为什么我请求了很多次,但是使用统计里仍然显示使用为0呢?" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1497
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1497
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0178] Refactor implementation behind "为什么配额管理里没有claude pro账号的额度?" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1496
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1496
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0179] Ensure rollout safety for "最近几个版本，好像轮询失效了" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1495
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1495
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0180] Standardize metadata and naming conventions touched by "iFlow error" across both repos.
- Priority: P2
- Effort: S
- Theme: error-handling-retries
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1494
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1494
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0181] Follow up on "Feature request [allow to configure RPM, TPM, RPD, TPD]" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1493
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1493
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0182] Harden "Antigravity using Ultra plan: Opus 4.6 gets 429 on CLIProxy but runs with Opencode-Auth" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1486
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1486
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0183] Operationalize "gemini在cherry studio的openai接口无法控制思考长度" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1484
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1484
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0184] Define non-subprocess integration path related to "codex5.3什么时候能获取到啊" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1482
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1482
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0185] Add DX polish around "Amp code doesn't route through CLIProxyAPI" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1481
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1481
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0186] Expand docs and examples for "导入kiro账户，过一段时间就失效了" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1480
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1480
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0187] Create/refresh provider quickstart derived from "openai-compatibility: streaming response empty when translating Codex protocol (/v1/responses) to OpenAI chat/completions" including setup, auth, model select, and sanity-check commands.
- Priority: P1
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1478
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1478
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0188] Refactor implementation behind "bug: request-level metadata fields injected into contents[] causing Gemini API rejection (v6.8.4)" to reduce complexity and isolate transformation boundaries.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1477
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1477
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0189] Ensure rollout safety for "Roo Code v3.47.0 cannot make Gemini API calls anymore" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1476
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1476
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0190] Port relevant thegent-managed flow implied by "[feat]更新很频繁,可以内置软件更新功能吗" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1475
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1475
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0191] Follow up on "Cannot alias multiple models to single model only on Antigravity" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1472
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1472
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0192] Harden "无法识别图片" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1469
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1469
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0193] Operationalize "Support for Antigravity Opus 4.6" with observability, alerting thresholds, and runbook updates.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1468
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1468
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0194] Convert "model not found for gpt-5.3-codex" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1463
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1463
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0195] Add DX polish around "antigravity用不了" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1461
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1461
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0196] Expand docs and examples for "为啥openai的端点可以添加多个密钥，但是a社的端点不能添加" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1457
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1457
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0197] Add QA scenarios for "轮询会无差别轮询即便某个账号在很久前已经空配额" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1456
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1456
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0198] Refactor implementation behind "When I don’t add the authentication file, opening Claude Code keeps throwing a 500 error, instead of directly using the AI provider I’ve configured." to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1455
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1455
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0199] Ensure rollout safety for "6.7.53版本反重力无法看到opus-4.6模型" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1453
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1453
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0200] Standardize metadata and naming conventions touched by "Codex OAuth failed" across both repos.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1451
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1451
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0201] Follow up on "Google asking to Verify account" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1447
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1447
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0202] Harden "API Error" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1445
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1445
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0203] Add process-compose/HMR refresh workflow tied to "Unable to use GPT 5.3 codex (model_not_found)" so local config and runtime can be reloaded deterministically.
- Priority: P2
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1443
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1443
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0204] Create/refresh provider quickstart derived from "gpt-5.3-codex 请求400 显示不存在该模型" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1442
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1442
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0205] Add DX polish around "The requested model 'gpt-5.3-codex' does not exist." through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1441
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1441
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0206] Expand docs and examples for "Feature request: Add support for claude opus 4.6" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: install-and-ops
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1439
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1439
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0207] Define non-subprocess integration path related to "Feature request: Add support for perplexity" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1438
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1438
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0208] Refactor implementation behind "iflow kimi-k2.5 无法正常统计消耗的token数，一直是0" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1437
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1437
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0209] Port relevant thegent-managed flow implied by "[BUG] Invalid JSON payload with large requests (~290KB) - truncated body" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P3
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1433
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1433
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0210] Standardize metadata and naming conventions touched by "希望支持国产模型如glm kimi minimax 的 proxy" across both repos.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1432
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1432
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0211] Follow up on "关闭某个认证文件后没有持久化处理" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1431
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1431
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0212] Harden "[v6.7.47] 接入智谱 Plan 计划后请求报错" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1430
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1430
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0213] Operationalize "大佬能不能把使用统计数据持久化？" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1427
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1427
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0214] Convert "[BUG] 使用 Google 官方 Python SDK时思考设置无法生效" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1426
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1426
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0215] Add DX polish around "bug: Claude → Gemini translation fails due to unsupported JSON Schema fields ($id, patternProperties)" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1424
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1424
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0216] Expand docs and examples for "Add Container Tags / Project Scoping for Memory Organization" with copy-paste quickstart and troubleshooting section.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1420
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1420
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0217] Add QA scenarios for "Add LangChain/LangGraph Integration for Memory System" including stream/non-stream parity and edge-case payloads.
- Priority: P3
- Effort: S
- Theme: error-handling-retries
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1419
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1419
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0218] Refactor implementation behind "Security Review: Apply Lessons from Supermemory Security Findings" to reduce complexity and isolate transformation boundaries.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1418
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1418
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0219] Ensure rollout safety for "Add Webhook Support for Document Lifecycle Events" via feature flags, staged defaults, and migration notes.
- Priority: P3
- Effort: S
- Theme: install-and-ops
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1417
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1417
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0220] Standardize metadata and naming conventions touched by "Create OpenAI-Compatible Memory Tools Wrapper" across both repos.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1416
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1416
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0221] Create/refresh provider quickstart derived from "Add Google Drive Connector for Memory Ingestion" including setup, auth, model select, and sanity-check commands.
- Priority: P3
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1415
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1415
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0222] Harden "Add Document Processor for PDF and URL Content Extraction" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1414
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1414
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0223] Operationalize "Add Notion Connector for Memory Ingestion" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: error-handling-retries
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1413
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1413
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0224] Convert "Add Strict Schema Mode for OpenAI Function Calling" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P3
- Effort: S
- Theme: error-handling-retries
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1412
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1412
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0225] Add DX polish around "Add Conversation Tracking Support for Chat History" through improved command ergonomics and faster feedback loops.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1411
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1411
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0226] Expand docs and examples for "Implement MCP Server for Memory Operations" with copy-paste quickstart and troubleshooting section.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1410
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1410
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0227] Add QA scenarios for "■ stream disconnected before completion: stream closed before response.completed" including stream/non-stream parity and edge-case payloads.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1407
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1407
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0228] Port relevant thegent-managed flow implied by "Bug: /v1/responses returns 400 "Input must be a list" when input is string (regression 6.7.42, Droid auto-compress broken)" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P1
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1403
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1403
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0229] Ensure rollout safety for "Factory Droid CLI got 404" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1401
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1401
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0230] Define non-subprocess integration path related to "反代反重力的 claude 在 opencode 中使用出现 unexpected EOF 错误" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1400
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1400
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0231] Follow up on "Feature request: Cursor CLI support" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P3
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1399
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1399
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0232] Add process-compose/HMR refresh workflow tied to "bug: Invalid signature in thinking block (API 400) on follow-up requests" so local config and runtime can be reloaded deterministically.
- Priority: P1
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1398
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1398
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0233] Operationalize "在 Visual Studio Code无法使用过工具" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: error-handling-retries
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1405
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1405
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0234] Convert "Vertex AI global 区域端点 URL 格式错误，导致无法访问 Gemini 3 Preview 模型" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1395
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1395
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0235] Add DX polish around "Session title generation fails for Claude models via Antigravity provider (OpenCode)" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1394
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1394
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0236] Expand docs and examples for "反代反重力请求gemini-3-pro-image-preview接口报错" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1393
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1393
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0237] Add QA scenarios for "[Feature Request] Implement automatic account rotation on VALIDATION_REQUIRED errors" including stream/non-stream parity and edge-case payloads.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1392
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1392
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0238] Create/refresh provider quickstart derived from "[antigravity] 500 Internal error and 403 Verification Required for multiple accounts" including setup, auth, model select, and sanity-check commands.
- Priority: P3
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1389
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1389
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0239] Ensure rollout safety for "Antigravity的配额管理,账号没有订阅资格了,还是在显示模型额度" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1388
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1388
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0240] Standardize metadata and naming conventions touched by "大佬，可以加一个apikey的过期时间不" across both repos.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1387
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1387
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0241] Follow up on "在codex运行报错" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1406
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1406
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0242] Harden "[Feature request] Support nested object parameter mapping in payload config" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1384
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1384
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0243] Operationalize "Claude authentication failed in v6.7.41 (works in v6.7.25)" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1383
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1383
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0244] Convert "Question: Does load balancing work with 2 Codex accounts for the Responses API?" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1382
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1382
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0245] Add DX polish around "登陆提示“登录失败: 访问被拒绝，权限不足”" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1381
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1381
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0246] Expand docs and examples for "Gemini 3 Flash includeThoughts参数不生效了" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1378
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1378
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0247] Port relevant thegent-managed flow implied by "antigravity无法登录" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P1
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1376
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1376
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0248] Refactor implementation behind "[Bug] Gemini 400 Error: "defer_loading" field in ToolSearch is not supported by Gemini API" to reduce complexity and isolate transformation boundaries.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1375
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1375
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0249] Ensure rollout safety for "API Error: 403" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1374
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1374
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0250] Standardize metadata and naming conventions touched by "Feature Request: 有没有可能支持Trea中国版？" across both repos.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1373
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1373
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0251] Follow up on "Bug: Auto-injected cache_control exceeds Anthropic API's 4-block limit" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1372
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1372
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0252] Harden "Bad processing of Claude prompt caching that is already implemented by client app" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1366
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1366
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0253] Define non-subprocess integration path related to "[Bug] OpenAI-compatible provider: message_start.usage always returns 0 tokens (kimi-for-coding)" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P1
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1365
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1365
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0254] Convert "iflow Cli官方针对terminal有Oauth 登录方式" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1364
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1364
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0255] Create/refresh provider quickstart derived from "Kimi For Coding 好像被 ban 了" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1327
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1327
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0256] Expand docs and examples for "“Error 404: Requested entity was not found" for gemini 3 by gemini-cli" with copy-paste quickstart and troubleshooting section.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1325
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1325
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0257] Add QA scenarios for "nvidia openai接口连接失败" including stream/non-stream parity and edge-case payloads.
- Priority: P3
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1324
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1324
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0258] Refactor implementation behind "Feature Request: Add generateImages endpoint support for Gemini API" to reduce complexity and isolate transformation boundaries.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1322
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1322
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0259] Ensure rollout safety for "iFlow Error: LLM returned 200 OK but response body was empty (possible rate limit)" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1321
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1321
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0260] Standardize metadata and naming conventions touched by "feat: add code_execution and url_context tool passthrough for Gemini" across both repos.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1318
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1318
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0261] Add process-compose/HMR refresh workflow tied to "This version of Antigravity is no longer supported. Please update to receive the latest features!" so local config and runtime can be reloaded deterministically.
- Priority: P2
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1316
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1316
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0262] Harden "无法轮询请求反重力和gemini cli" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1315
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1315
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0263] Operationalize "400 Bad Request when reasoning_effort="xhigh" with kimi k2.5 (OpenAI-compatible API)" with observability, alerting thresholds, and runbook updates.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1307
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1307
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0264] Convert "Claude Opus 4.5 returns "Internal server error" in response body via Anthropic OAuth (Sonnet works)" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1306
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1306
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0265] Add DX polish around "CLI Proxy API 版本: v6.7.28，OAuth 模型别名里的antigravity项目无法被删除。" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1305
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1305
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0266] Port relevant thegent-managed flow implied by "Feature Request: Add "Sequential" routing strategy to optimize account quota usage" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1304
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1304
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0267] Add QA scenarios for "版本: v6.7.27 添加openai-compatibility的时候出现 malformed HTTP response 错误" including stream/non-stream parity and edge-case payloads.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1301
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1301
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0268] Refactor implementation behind "fix(logging): request and API response timestamps are inaccurate in error logs" to reduce complexity and isolate transformation boundaries.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1299
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1299
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0269] Ensure rollout safety for "cpaUsageMetadata leaks to Gemini API responses when using Antigravity backend" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1297
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1297
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0270] Standardize metadata and naming conventions touched by "Gemini API error: empty text content causes 'required oneof field data must have one initialized field'" across both repos.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1293
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1293
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0271] Follow up on "Gemini API error: empty text content causes 'required oneof field data must have one initialized field'" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1292
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1292
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0272] Create/refresh provider quickstart derived from "gemini-3-pro-image-preview api 返回500 我看log中报500的都基本在1分钟左右" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1291
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1291
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0273] Operationalize "希望代理设置 能为多个不同的认证文件分别配置不同的代理 URL" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1290
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1290
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0274] Convert "Request takes over a minute to get sent with Antigravity" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1289
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1289
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0275] Add DX polish around "Antigravity auth requires daily re-login - sessions expire unexpectedly" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1288
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1288
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0276] Define non-subprocess integration path related to "cpa长时间运行会oom" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P3
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1287
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1287
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0277] Add QA scenarios for "429 RESOURCE_EXHAUSTED for Claude Opus 4.5 Thinking with Google AI Pro Account" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1284
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1284
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0278] Refactor implementation behind "[功能建议] 建议实现统计数据持久化，免去更新时的手动导出导入" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1282
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1282
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0279] Ensure rollout safety for "反重力的banana pro额度一直无法恢复" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1281
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1281
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0280] Standardize metadata and naming conventions touched by "Support request: Kimi For Coding (Kimi Code / K2.5) behind CLIProxyAPI" across both repos.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1280
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1280
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0281] Follow up on "TPM/RPM过载，但是等待半小时后依旧不行" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1278
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1278
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0282] Harden "支持codex的 /personality" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1273
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1273
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0283] Operationalize "Antigravity 可用模型数为 0" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1270
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1270
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0284] Convert "Tool Error on Antigravity Gemini 3 Flash" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1269
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1269
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0285] Port relevant thegent-managed flow implied by "[Improvement] Persist Management UI assets in a dedicated volume" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P3
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1268
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1268
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0286] Expand docs and examples for "[Feature Request] Provide optional standalone UI service in docker-compose" with copy-paste quickstart and troubleshooting section.
- Priority: P3
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1267
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1267
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0287] Add QA scenarios for "[Improvement] Pre-bundle Management UI in Docker Image" including stream/non-stream parity and edge-case payloads.
- Priority: P3
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1266
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1266
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0288] Refactor implementation behind "AMP CLI not working" to reduce complexity and isolate transformation boundaries.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1264
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1264
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0289] Create/refresh provider quickstart derived from "建议增加根据额度阈值跳过轮询凭证功能" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1263
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1263
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0290] Add process-compose/HMR refresh workflow tied to "[Bug] Antigravity Gemini API 报错：enum 仅允许用于 STRING 类型" so local config and runtime can be reloaded deterministically.
- Priority: P2
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1260
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1260
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0291] Follow up on "好像codebuddy也能有命令行也能用，能加进去吗" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1259
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1259
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0292] Harden "Anthropic via OAuth can not callback URL" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1256
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1256
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0293] Operationalize "[Bug] 反重力banana pro 4k 图片生成输出为空，仅思考过程可见" with observability, alerting thresholds, and runbook updates.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1255
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1255
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0294] Convert "iflow Cookies 登陆好像不能用" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1254
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1254
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0295] Add DX polish around "CLIProxyAPI goes down after some time, only recovers when SSH into server" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1253
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1253
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0296] Expand docs and examples for "kiro hope" with copy-paste quickstart and troubleshooting section.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1252
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1252
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0297] Add QA scenarios for ""Requested entity was not found" for all antigravity models" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1251
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1251
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0298] Refactor implementation behind "[BUG] Why does it repeat twice? 为什么他重复了两次？" to reduce complexity and isolate transformation boundaries.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1247
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1247
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0299] Define non-subprocess integration path related to "6.6.109之前的版本都可以开启iflow的deepseek3.2，qwen3-max-preview思考，6.7.xx就不能了" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1245
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1245
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0300] Standardize metadata and naming conventions touched by "Bug: Anthropic API 400 Error - Missing 'thinking' block before 'tool_use'" across both repos.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1244
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1244
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0301] Follow up on "v6.7.24，反重力的gemini-3，调用API有bug" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1243
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1243
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0302] Harden "How to reset /models" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1240
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1240
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0303] Operationalize "Feature Request:Add support for separate proxy configuration with credentials" with observability, alerting thresholds, and runbook updates.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1236
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1236
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0304] Port relevant thegent-managed flow implied by "GLM Coding Plan" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1226
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1226
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0305] Add DX polish around "更新到最新版本之后，出现了503的报错" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1224
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1224
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0306] Create/refresh provider quickstart derived from "能不能增加一个配额保护" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1223
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1223
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0307] Add QA scenarios for "auth_unavailable: no auth available in claude code cli, 使用途中经常500" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1222
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1222
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0308] Refactor implementation behind "无法关闭谷歌的某个具体的账号的使用权限" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1219
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1219
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0309] Ensure rollout safety for "docker中的最新版本不是lastest" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1218
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1218
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0310] Standardize metadata and naming conventions touched by "openai codex 认证失败: Failed to exchange authorization code for tokens" across both repos.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1217
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1217
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0311] Follow up on "tool_use_error InputValidationError: EnterPlanMode failed due to the following issue: An unexpected parameter `reason` was provided" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1215
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1215
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0312] Harden "Error 403" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1214
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1214
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0313] Operationalize "Gemini CLI OAuth 认证失败: failed to start callback server" with observability, alerting thresholds, and runbook updates.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1213
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1213
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0314] Convert "bug: Thinking budget ignored in cross-provider conversations (Antigravity)" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1199
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1199
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0315] Add DX polish around "[功能需求] 认证文件增加屏蔽模型跳过轮询" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1197
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1197
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0316] Expand docs and examples for "可以出个检查更新吗，不然每次都要拉下载然后重启" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1195
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1195
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0317] Add QA scenarios for "antigravity可以增加配额保护吗 剩余额度多少的时候不在使用" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1194
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1194
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0318] Refactor implementation behind "codex总是有失败" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1193
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1193
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0319] Add process-compose/HMR refresh workflow tied to "建议在使用Antigravity 额度时，设计额度阈值自定义功能" so local config and runtime can be reloaded deterministically.
- Priority: P2
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1192
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1192
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0320] Standardize metadata and naming conventions touched by "Antigravity: rev19-uic3-1p (Alias: gemini-2.5-computer-use-preview-10-2025) nolonger useable" across both repos.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1190
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1190
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0321] Follow up on "🚨🔥 CRITICAL BUG REPORT: Invalid Function Declaration Schema in API Request 🔥🚨" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1189
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1189
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0322] Define non-subprocess integration path related to "认证失败: Failed to exchange token" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1186
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1186
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0323] Create/refresh provider quickstart derived from "Model combo support" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1184
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1184
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0324] Convert "使用 Antigravity OAuth 使用openai格式调用opencode问题" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1173
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1173
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0325] Add DX polish around "今天中午开始一直429" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: error-handling-retries
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1172
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1172
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0326] Expand docs and examples for "gemini api 使用openai 兼容的url 使用时 tool_call 有问题" with copy-paste quickstart and troubleshooting section.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1168
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1168
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0327] Add QA scenarios for "linux一键安装的如何更新" including stream/non-stream parity and edge-case payloads.
- Priority: P3
- Effort: S
- Theme: install-and-ops
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1167
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1167
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0328] Refactor implementation behind "新增微软copilot GPT5.2codex模型" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1166
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1166
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0329] Ensure rollout safety for "Tool Calling Not Working in Cursor When Using Claude via CLIPROXYAPI + Antigravity Proxy" via feature flags, staged defaults, and migration notes.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1165
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1165
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0330] Standardize metadata and naming conventions touched by "[Improvement] Allow multiple model mappings to have the same Alias" across both repos.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1163
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1163
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0331] Follow up on "Antigravity模型在Cursor无法使用工具" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1162
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1162
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0332] Harden "Gemini" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1161
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1161
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0333] Operationalize "Add support proxy per account" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1160
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1160
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0334] Convert "[Feature] 添加Github Copilot 的OAuth" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1159
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1159
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0335] Add DX polish around "希望支持claude api" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1157
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1157
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0336] Expand docs and examples for "[Bug] v6.7.x Regression: thinking parameter not recognized, causing Cherry Studio and similar clients to fail displaying extended thinking content" with copy-paste quickstart and troubleshooting section.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1155
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1155
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0337] Add QA scenarios for "nvidia今天开始超时了，昨天刚配置还好好的" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1154
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1154
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0338] Refactor implementation behind "Antigravity OAuth认证失败" to reduce complexity and isolate transformation boundaries.
- Priority: P1
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1153
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1153
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0339] Ensure rollout safety for "日志怎么不记录了" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1152
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1152
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0340] Create/refresh provider quickstart derived from "v6.7.16无法反重力的gemini-3-pro-preview" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1150
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1150
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0341] Follow up on "OpenAI 兼容模型请求失败问题" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1149
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1149
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0342] Port relevant thegent-managed flow implied by "没有单个凭证 启用/禁用 的切换开关吗" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1148
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1148
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0343] Operationalize "[Bug] Internal restart loop causes continuous "address already in use" errors in logs" with observability, alerting thresholds, and runbook updates.
- Priority: P3
- Effort: S
- Theme: error-handling-retries
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1146
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1146
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0344] Convert "cc 使用 zai-glm-4.7 报错 body.reasoning" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1143
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1143
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0345] Define non-subprocess integration path related to "NVIDIA不支持，转发成claude和gpt都用不了" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P1
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1139
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1139
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0346] Expand docs and examples for "Feature Request: Add support for Cursor IDE as a backend/provider" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1138
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1138
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0347] Add QA scenarios for "Claude to OpenAI Translation Generates Empty System Message" including stream/non-stream parity and edge-case payloads.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1136
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1136
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0348] Add process-compose/HMR refresh workflow tied to "tool_choice not working for Gemini models via Claude API endpoint" so local config and runtime can be reloaded deterministically.
- Priority: P1
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1135
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1135
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0349] Ensure rollout safety for "model stops by itself does not proceed to the next step" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1134
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1134
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0350] Standardize metadata and naming conventions touched by "API Error: 400是怎么回事，之前一直能用" across both repos.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1133
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1133
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0351] Follow up on "希望供应商能够加上微软365" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1128
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1128
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0352] Harden "codex的config.toml文件在哪里修改？" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1127
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1127
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0353] Operationalize "[Bug] Antigravity provider intermittently strips `thinking` blocks in multi-turn conversations with extended thinking enabled" with observability, alerting thresholds, and runbook updates.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1124
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1124
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0354] Convert "使用Amp CLI的Painter工具画图显示prompt is too long" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1123
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1123
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0355] Add DX polish around "gpt-5.2-codex "System messages are not allowed"" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1122
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1122
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0356] Expand docs and examples for "kiro使用orchestrator 模式调用的时候会报错400" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1120
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1120
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0357] Create/refresh provider quickstart derived from "Error code: 400 - {'detail': 'Unsupported parameter: user'}" including setup, auth, model select, and sanity-check commands.
- Priority: P3
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1119
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1119
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0358] Refactor implementation behind "添加智谱OpenAI兼容提供商获取模型和测试会失败" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1118
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1118
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0359] Ensure rollout safety for "gemini-3-pro-high (Antigravity): malformed_function_call error with tools" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1113
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1113
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0360] Standardize metadata and naming conventions touched by "该凭证暂无可用模型，这是被封号了的意思吗" across both repos.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1111
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1111
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0361] Port relevant thegent-managed flow implied by "香蕉pro 图片一下将所有图片额度都消耗没了" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1110
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1110
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0362] Harden "Error 'Expected thinking or redacted_thinking' after upgrade to v6.7.12" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1109
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1109
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0363] Operationalize "[Feature Request] whitelist models for specific API KEY" with observability, alerting thresholds, and runbook updates.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1107
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1107
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0364] Convert "gemini-3-pro-high returns empty response when subagent uses tools" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1106
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1106
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0365] Add DX polish around "GitStore local repo fills tmpfs due to accumulating loose git objects (no GC/repack)" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1104
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1104
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0366] Expand docs and examples for "ℹ ⚠️ Response stopped due to malformed function call. 在 Gemini CLI 中 频繁出现这个提示，对话中断" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1100
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1100
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0367] Add QA scenarios for "【功能请求】添加禁用项目按键（或优先级逻辑）" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1098
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1098
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0368] Define non-subprocess integration path related to "有支持豆包的反代吗" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1097
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1097
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0369] Ensure rollout safety for "Wrong workspace selected for OpenAI accounts" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1095
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1095
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0370] Standardize metadata and naming conventions touched by "Anthropic web_search fails in v6.7.x - invalid tool name web_search_20250305" across both repos.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1094
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1094
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0371] Follow up on "Antigravity 生图无法指定分辨率" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1093
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1093
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0372] Harden "文件写方式在docker下容易出现Inode变更问题" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P3
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1092
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1092
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0373] Operationalize "命令行中返回结果一切正常，但是在cherry studio中找不到模型" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1090
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1090
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0374] Create/refresh provider quickstart derived from "[Feedback #1044] 尝试通过 Payload 设置 Gemini 3 宽高比失败 (Google API 400 Error)" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1089
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1089
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0375] Add DX polish around "反重力2API opus模型 Error searching files" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1086
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1086
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0376] Expand docs and examples for "Streaming Response Translation Fails to Emit Completion Events on `[DONE]` Marker" with copy-paste quickstart and troubleshooting section.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1085
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1085
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0377] Add process-compose/HMR refresh workflow tied to "Feature Request: Add support for Text Embedding API (/v1/embeddings)" so local config and runtime can be reloaded deterministically.
- Priority: P1
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1084
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1084
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0378] Refactor implementation behind "大香蕉生图无图片返回" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1083
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1083
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0379] Ensure rollout safety for "修改报错HTTP Status Code" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1082
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1082
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0380] Port relevant thegent-managed flow implied by "反重力2api无法使用工具" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1080
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1080
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0381] Follow up on "配额管理中可否新增Claude OAuth认证方式号池的配额信息" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1079
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1079
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0382] Harden "Extended thinking model fails with "Expected thinking or redacted_thinking, but found tool_use" on multi-turn conversations" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1078
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1078
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0383] Operationalize "functionDeclarations 和 googleSearch 合并到同一个 tool 对象导致 Gemini API 报错" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1077
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1077
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0384] Convert "Antigravity: MCP 工具的数字类型 enum 值导致 INVALID_ARGUMENT 错误" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1075
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1075
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0385] Add DX polish around "认证文件管理可否添加一键导出所有凭证的按钮" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1074
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1074
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0386] Expand docs and examples for "image generation 429" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1073
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1073
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0387] Add QA scenarios for "No Auth Available" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1072
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1072
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0388] Refactor implementation behind "配置OpenAI兼容格式的API，用Anthropic接口 OpenAI接口都调用不成功" to reduce complexity and isolate transformation boundaries.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1066
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1066
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0389] Ensure rollout safety for ""Think Mode" Reasoning models are not visible in GitHub Copilot interface" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1065
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1065
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0390] Standardize metadata and naming conventions touched by "Gemini 和 Claude 多条 system 提示词时，只有最后一条生效 / When Gemini and Claude have multiple system prompt words, only the last one takes effect" across both repos.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1064
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1064
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0391] Create/refresh provider quickstart derived from "OAuth issue with Qwen using Google Social Login" including setup, auth, model select, and sanity-check commands.
- Priority: P1
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1063
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1063
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0392] Harden "[Feature] allow to disable auth files from UI (management)" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P3
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1062
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1062
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0393] Operationalize "最新版claude 2.1.9调用后，会在后台刷出大量warn；持续输出" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1061
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1061
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0394] Convert "Antigravity 针对Pro账号的 Claude/GPT 模型有周限额了吗？" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1060
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1060
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0395] Add DX polish around "OpenAI 兼容提供商 由于客户端没有兼容OpenAI接口，导致调用失败" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1059
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1059
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0396] Expand docs and examples for "希望可以增加antigravity授权的配额保护功能" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1058
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1058
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0397] Add QA scenarios for "[bug]在 opencode 多次正常请求后出现 500 Unknown Error 后紧接着 No Auth Available" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1057
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1057
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0398] Refactor implementation behind "6.7.3报错 claude和cherry 都报错，是配置问题吗？还是模型换名了unknown provider for model gemini-claude-opus-4-" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1056
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1056
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0399] Port relevant thegent-managed flow implied by "codex-instructions-enabled为true时，在codex-cli中使用是否会重复注入instructions?" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1055
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1055
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0400] Standardize metadata and naming conventions touched by "cliproxyapi多个账户切换(因限流/账号问题), 导致客户端直接报错" across both repos.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1053
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1053
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0401] Follow up on "Codex authentication cannot be detected" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1052
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1052
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0402] Harden "v6.7.3 OAuth 模型映射 新增或修改存在问题" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1051
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1051
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0403] Operationalize "【建议】持久化储存使用统计" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1050
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1050
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0404] Convert "最新版本CPA，OAuths模型映射功能失败？" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1048
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1048
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0405] Add DX polish around "新增的Antigravity文件会报错429" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1047
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1047
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0406] Add process-compose/HMR refresh workflow tied to "Docker部署缺失gemini-web-auth功能" so local config and runtime can be reloaded deterministically.
- Priority: P2
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1045
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1045
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0407] Add QA scenarios for "image模型能否在cliproxyapi中直接区分2k，4k" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1044
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1044
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0408] Create/refresh provider quickstart derived from "OpenAI-compatible assistant content arrays dropped in conversion, causing repeated replies" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1043
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1043
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0409] Ensure rollout safety for "qwen进行模型映射时提示 更新模型映射失败: channel not found" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1042
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1042
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0410] Standardize metadata and naming conventions touched by "升级到最新版本后，认证文件页面提示请升级CPA版本" across both repos.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1041
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1041
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0411] Follow up on "服务启动后，终端连续不断打印相同内容" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1040
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1040
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0412] Harden "Issue" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1039
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1039
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0413] Operationalize "Antigravity error to get quota limit" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1038
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1038
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0414] Define non-subprocess integration path related to "macos webui Codex OAuth error" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P1
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1037
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1037
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0415] Add DX polish around "antigravity 无法获取登录链接" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1035
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1035
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0416] Expand docs and examples for "UltraAI Workspace account error: project_id cannot be retrieved" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: error-handling-retries
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1034
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1034
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0417] Add QA scenarios for "额度获取失败：Gemini CLI 凭证缺少 Project ID" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1032
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1032
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0418] Port relevant thegent-managed flow implied by "Antigravity auth causes infinite refresh loop when project_id cannot be fetched" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P1
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1030
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1030
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0419] Ensure rollout safety for "希望能够通过配置文件设定API调用超时时间" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: error-handling-retries
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1029
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1029
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0420] Standardize metadata and naming conventions touched by "Calling gpt-codex-5.2 returns 400 error: “Unsupported parameter: safety_identifier”" across both repos.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1028
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1028
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0421] Follow up on "【建议】能否加一下模型配额优先级？" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1027
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1027
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0422] Harden "求问，配额显示并不准确" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1026
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1026
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0423] Operationalize "Vertex Credential Doesn't Work with gemini-3-pro-image-preview" with observability, alerting thresholds, and runbook updates.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1024
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1024
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0424] Convert "[Feature] 提供更新命令" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: install-and-ops
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1023
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1023
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0425] Create/refresh provider quickstart derived from "授权文件可以拷贝使用" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1022
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1022
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0426] Expand docs and examples for "额度的消耗怎么做到平均分配和限制最多使用量呢？" with copy-paste quickstart and troubleshooting section.
- Priority: P1
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1021
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1021
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0427] Add QA scenarios for "【建议】就算开了日志也无法区别为什么新加的这个账号错误的原因" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1020
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1020
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0428] Refactor implementation behind "每天早上都报错 错误: Failed to call gemini-3-pro-preview model: unknown provider for model gemini-3-pro-preview 要重新删除账号重新登录，" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1019
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1019
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0429] Ensure rollout safety for "Antigravity Accounts Rate Limited (HTTP 429) Despite Available Quota" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1015
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1015
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0430] Standardize metadata and naming conventions touched by "Bug: CLIproxyAPI returns Prompt is too long (need trim history)" across both repos.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1014
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1014
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0431] Follow up on "Management Usage report resets at restart" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1013
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1013
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0432] Harden "使用gemini-3-pro-image-preview 模型，生成不了图片" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1012
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1012
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0433] Operationalize "「建议」希望能添加一个手动控制某 oauth 认证是否参与反代的功能" with observability, alerting thresholds, and runbook updates.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1010
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1010
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0434] Convert "[Bug] Missing mandatory tool_use.id in request payload causing failure on subsequent tool calls" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1009
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1009
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0435] Add process-compose/HMR refresh workflow tied to "添加openai v1 chat接口，使用responses调用，出现截断，最后几个字不显示" so local config and runtime can be reloaded deterministically.
- Priority: P2
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1008
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1008
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0436] Expand docs and examples for "iFlow token刷新失败" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1007
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1007
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0437] Port relevant thegent-managed flow implied by "fix(codex): Codex 流错误格式不符合 OpenAI Responses API 规范导致客户端解析失败" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P1
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1006
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1006
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0438] Refactor implementation behind "Feature: Add Veo 3.1 Video Generation Support" to reduce complexity and isolate transformation boundaries.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1005
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1005
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0439] Ensure rollout safety for "Bug: Streaming response.output_item.done missing function name" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1004
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1004
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0440] Standardize metadata and naming conventions touched by "Close" across both repos.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1003
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1003
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0441] Follow up on "gemini 3 missing field" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#1002
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/1002
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0442] Create/refresh provider quickstart derived from "[Bug] Codex Responses API: item_reference in `input` not cleaned, causing 404 errors and incorrect client suspension" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#999
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/999
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0443] Operationalize "[Bug] Codex Responses API: `input` 中的 item_reference 未清理，导致 404 错误和客户端被误暂停" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#998
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/998
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0444] Convert "【建议】保留Gemini格式请求的思考签名" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#997
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/997
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0445] Add DX polish around "Gemini CLI 认证api，不支持gemini 3" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#996
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/996
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0446] Expand docs and examples for "配额管理显示不正常。" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#995
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/995
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0447] Add QA scenarios for "使用oh my opencode的时候subagent调用不积极" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#992
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/992
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0448] Refactor implementation behind "A tool for AmpCode agent to turn on off free mode to enjoy Oracle, Websearch by free credits without seeing ads to much" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#990
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/990
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0449] Ensure rollout safety for "`tool_use` ids were found without `tool_result` blocks immediately" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#989
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/989
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0450] Standardize metadata and naming conventions touched by "Codex callback URL仅显示：http://localhost:1455/success" across both repos.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#988
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/988
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0451] Follow up on "【建议】在CPA webui中实现禁用某个特定的凭证" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P3
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#987
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/987
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0452] Harden "New OpenAI API: /responses/compact" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#986
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/986
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0453] Operationalize "Bug Report: OAuth Login Failure on Windows due to Port 51121 Conflict" with observability, alerting thresholds, and runbook updates.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#985
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/985
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0454] Convert "Claude model reports wrong/unknown model when accessed via API (Claude Code OAuth)" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#984
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/984
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0455] Add DX polish around "400 Error: Unsupported max_tokens Parameter When Using OpenAI Base URL" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#983
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/983
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0456] Port relevant thegent-managed flow implied by "［建议］Codex渠道将System角色映射为Developer角色" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P1
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#982
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/982
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0457] Add QA scenarios for "No Image Generation Models Available After Gemini CLI Setup" including stream/non-stream parity and edge-case payloads.
- Priority: P1
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#978
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/978
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0458] Refactor implementation behind "When using the amp cli with gemini 3 pro, after thinking, nothing happens" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#977
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/977
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0459] Create/refresh provider quickstart derived from "GPT5.2模型异常报错 auth_unavailable: no auth available" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#976
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/976
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0460] Define non-subprocess integration path related to "fill-first strategy does not take effect (all accounts remain at 99%)" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#974
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/974
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0461] Follow up on "Auth files permanently deleted from S3 on service restart due to race condition" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#973
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/973
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0462] Harden "feat: Enhanced Request Logging with Metadata and Management API for Observability" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#972
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/972
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0463] Operationalize "Antigravity with opus 4,5 keeps giving rate limits error for no reason." with observability, alerting thresholds, and runbook updates.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#970
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/970
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0464] Add process-compose/HMR refresh workflow tied to "exhausted没被重试or跳过，被传下来了" so local config and runtime can be reloaded deterministically.
- Priority: P2
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#968
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/968
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0465] Add DX polish around "初次运行运行.exe文件报错" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#966
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/966
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0466] Expand docs and examples for "登陆后白屏" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: error-handling-retries
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#965
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/965
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0467] Add QA scenarios for "版本：6.6.98 症状：登录成功后白屏，React Error #300 复现：登录后立即崩溃白屏" including stream/non-stream parity and edge-case payloads.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#964
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/964
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0468] Refactor implementation behind "反重力反代在opencode不支持，问话回答一下就断" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#962
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/962
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0469] Ensure rollout safety for "Antigravity using Flash 2.0 Model for Sonet" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#960
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/960
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0470] Standardize metadata and naming conventions touched by "建议优化轮询逻辑，同一账号额度用完刷新后作为第二优先级轮询" across both repos.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#959
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/959
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0471] Follow up on "macOS的webui无法登录" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#957
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/957
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0472] Harden "【bug】三方兼容open ai接口 测试会报这个，如何解决呢？" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#956
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/956
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0473] Operationalize "[Feature] Allow define log filepath in config" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#954
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/954
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0474] Convert "[建议]希望OpenAI 兼容提供商支持启用停用功能" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#953
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/953
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0475] Port relevant thegent-managed flow implied by "Reasoning field missing for gpt-5.1-codex-max at xhigh reasoning level (while gpt-5.2-codex works as expected)" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P1
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#952
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/952
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0476] Create/refresh provider quickstart derived from "[Bug]反代 Antigravity 使用Claude Code 时，特定请求持续无响应导致 504 Gateway Timeout" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#951
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/951
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0477] Add QA scenarios for "README has been replaced by the one from CLIProxyAPIPlus" including stream/non-stream parity and edge-case payloads.
- Priority: P3
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#950
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/950
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0478] Refactor implementation behind "Internal Server Error: {"error":{"message":"auth_unavailable: no auth available"... (click to expand) [retrying in 8s attempt #4]" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#949
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/949
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0479] Ensure rollout safety for "[BUG] Multi-part Gemini response loses content - only last part preserved in OpenAI translation" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#948
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/948
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0480] Standardize metadata and naming conventions touched by "内存占用太高，用了1.5g" across both repos.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#944
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/944
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0481] Follow up on "接入openroute成功，但是下游使用异常" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#942
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/942
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0482] Harden "fix: use original request JSON for echoed fields in OpenAI Responses translator" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#941
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/941
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0483] Define non-subprocess integration path related to "现有指令会让 Gemini 产生误解，无法真正忽略前置系统提示" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#940
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/940
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0484] Convert "[Feature Request] Support Priority Failover Strategy (Priority Queue) Instead of all Round-Robin" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#937
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/937
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0485] Add DX polish around "[Feature Request] Support multiple aliases for a single model name in oauth-model-mappings" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#936
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/936
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0486] Expand docs and examples for "新手登陆认证问题" with copy-paste quickstart and troubleshooting section.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#934
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/934
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0487] Add QA scenarios for "能不能支持UA伪装？" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#933
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/933
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0488] Refactor implementation behind "[features request] 恳请CPA团队能否增加KIRO的反代模式？Could you add a reverse proxy api to KIRO?" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#932
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/932
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0489] Ensure rollout safety for "Gemini 3 Pro cannot perform native tool calls in Roo Code" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#931
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/931
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0490] Standardize metadata and naming conventions touched by "Qwen OAuth Request Error" across both repos.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#930
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/930
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0491] Follow up on "无法在 api 代理中使用 Anthropic 模型，报错 429" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#929
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/929
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0492] Harden "[Bug] 400 error on Claude Code internal requests when thinking is enabled - assistant message missing thinking block" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#928
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/928
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0493] Create/refresh provider quickstart derived from "配置自定义提供商的时候怎么给相同的baseurl一次配置多个API Token呢？" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#927
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/927
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0494] Port relevant thegent-managed flow implied by "同一个chatgpt账号加入了多个工作空间，同时个人账户也有gptplus，他们的codex认证文件在cliproxyapi不能同时使用" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P1
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#926
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/926
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0495] Add DX polish around "iFlow 登录失败" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#923
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/923
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0496] Expand docs and examples for "希望能自定义系统提示，比如自定义前缀" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#922
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/922
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0497] Add QA scenarios for "Help for setting mistral" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#920
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/920
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0498] Refactor implementation behind "能不能添加功能，禁用某些配置文件" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#919
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/919
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0499] Ensure rollout safety for "How to run this?" via feature flags, staged defaults, and migration notes.
- Priority: P3
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#917
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/917
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0500] Standardize metadata and naming conventions touched by "API密钥→特定配额文件" across both repos.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#915
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/915
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0501] Follow up on "增加支持Gemini API v1版本" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P3
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#914
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/914
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0502] Harden "error on claude code" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#913
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/913
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0503] Operationalize "反重力Claude修好后，大香蕉不行了" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#912
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/912
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0504] Convert "看到有人发了一个更短的提示词" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#911
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/911
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0505] Add DX polish around "Antigravity models return 429 RESOURCE_EXHAUSTED via cURL, but Antigravity IDE still works (started ~18:00 GMT+7)" through improved command ergonomics and faster feedback loops.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#910
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/910
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0506] Define non-subprocess integration path related to "gemini3p报429，其他的都好好的" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#908
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/908
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0507] Add QA scenarios for "[BUG] 403 You are currently configured to use a Google Cloud Project but lack a Gemini Code Assist license" including stream/non-stream parity and edge-case payloads.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#907
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/907
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0508] Refactor implementation behind "新版本运行闪退" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#906
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/906
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0509] Ensure rollout safety for "更新到最新版本后，自定义 System Prompt 无效" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#905
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/905
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0510] Create/refresh provider quickstart derived from "⎿ 429 {"error":{"code":"model_cooldown","message":"All credentials for model gemini-claude-opus-4-5-thinking are cooling down via provider antigravity","model":"gemini-claude-opus-4-5-thinking","provider":"antigravity","reset_seconds" including setup, auth, model select, and sanity-check commands.
- Priority: P3
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#904
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/904
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0511] Follow up on "有人遇到相同问题么？Resource has been exhausted (e.g. check quota)" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#903
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/903
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0512] Harden "auth_unavailable: no auth available" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#902
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/902
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0513] Port relevant thegent-managed flow implied by "OpenAI Codex returns 400: Unsupported parameter: prompt_cache_retention" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P1
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#897
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/897
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0514] Convert "[feat]自动优化Antigravity的quota刷新时间选项" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#895
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/895
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0515] Add DX polish around "Apply Routing Strategy also to Auth Files" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#893
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/893
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0516] Expand docs and examples for "支持包含模型配置" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#892
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/892
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0517] Add QA scenarios for "Cursor subscription support" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#891
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/891
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0518] Refactor implementation behind "增加qodercli" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#889
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/889
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0519] Ensure rollout safety for "[Bug] Codex auth file overwritten when account has both Plus and Team plans" via feature flags, staged defaults, and migration notes.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#887
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/887
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0520] Standardize metadata and naming conventions touched by "新版本有超时Bug,切换回老版本没问题" across both repos.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#886
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/886
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0521] Follow up on "can not work with mcp:ncp on antigravity auth" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#885
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/885
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0522] Add process-compose/HMR refresh workflow tied to "Gemini Cli Oauth 认证失败" so local config and runtime can be reloaded deterministically.
- Priority: P1
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#884
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/884
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0523] Operationalize "Claude Code Web Search doesn’t work" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: testing-and-quality
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#883
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/883
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0524] Convert "fix(antigravity): Streaming finish_reason 'tool_calls' overwritten by 'stop' - breaks Claude Code tool detection" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#876
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/876
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0525] Add DX polish around "同时使用GPT账号个人空间和团队空间" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#875
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/875
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0526] Expand docs and examples for "antigravity and gemini cli duplicated model names" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#873
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/873
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0527] Create/refresh provider quickstart derived from "supports stakpak.dev" including setup, auth, model select, and sanity-check commands.
- Priority: P3
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#872
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/872
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0528] Refactor implementation behind "gemini 模型 tool_calls 问题" to reduce complexity and isolate transformation boundaries.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#866
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/866
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0529] Define non-subprocess integration path related to "谷歌授权登录成功，但是额度刷新失败" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#864
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/864
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0530] Standardize metadata and naming conventions touched by "使用统计 每次重启服务就没了，能否重启不丢失，使用手动的方式去清理统计数据" across both repos.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#863
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/863
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0531] Follow up on "代理 iflow 模型服务的时候频繁出现重复调用同一个请求的情况。一直循环" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#856
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/856
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0532] Port relevant thegent-managed flow implied by "请增加对kiro的支持" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#855
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/855
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0533] Operationalize "Reqest for supporting github copilot" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#854
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/854
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0534] Convert "请添加iflow最新模型iFlow-ROME-30BA3B" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#853
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/853
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0535] Add DX polish around "[Bug] Infinite hanging and quota surge with gemini-claude-opus-4-5-thinking in Claude Code" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#852
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/852
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0536] Expand docs and examples for "Would the consumption be greater in Claude Code?" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#848
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/848
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0537] Add QA scenarios for "功能请求：为 OAuth 账户添加独立代理配置支持" including stream/non-stream parity and edge-case payloads.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#847
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/847
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0538] Refactor implementation behind "Promt caching" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#845
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/845
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0539] Ensure rollout safety for "Feature Request: API for fetching Quota stats (remaining, renew time, etc)" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#844
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/844
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0540] Standardize metadata and naming conventions touched by "使用antigravity转为API在claude code中使用不支持web search" across both repos.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#842
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/842
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0541] Follow up on "[Bug] Antigravity countTokens ignores tools field - always returns content-only token count" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#840
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/840
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0542] Harden "Image Generation 504 Timeout Investigation" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#839
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/839
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0543] Operationalize "[Feature Request] Schedule automated requests to AI models" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#838
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/838
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0544] Create/refresh provider quickstart derived from ""Feature Request: Android Binary Support (Termux Build Guide)"" including setup, auth, model select, and sanity-check commands.
- Priority: P3
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#836
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/836
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0545] Add DX polish around "[Bug] Antigravity token refresh loop caused by metadataEqualIgnoringTimestamps skipping critical field updates" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#833
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/833
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0546] Expand docs and examples for "mac使用brew安装的cpa，请问配置文件在哪？" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#831
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/831
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0547] Add QA scenarios for "Feature request" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: testing-and-quality
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#828
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/828
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0548] Refactor implementation behind "长时间运行后会出现`internal_server_error`" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#827
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/827
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0549] Ensure rollout safety for "windows环境下，认证文件显示重复的BUG" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#822
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/822
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0550] Standardize metadata and naming conventions touched by "[FQ]增加telegram bot集成和更多管理API命令刷新Providers周期额度" across both repos.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#820
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/820
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0551] Port relevant thegent-managed flow implied by "[Feature] 能否增加/v1/embeddings 端点" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#818
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/818
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0552] Define non-subprocess integration path related to "模型带前缀并开启force_model_prefix后，以gemini格式获取模型列表中没有带前缀的模型" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#816
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/816
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0553] Operationalize "iFlow account error show on terminal" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#815
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/815
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0554] Convert "代理的codex 404" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#812
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/812
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0555] Add DX polish around "Set up Apprise on TrueNAS for notifications" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: install-and-ops
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#808
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/808
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0556] Expand docs and examples for "Request for maintenance team intervention: Changes in internal/translator needed" with copy-paste quickstart and troubleshooting section.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#806
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/806
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0557] Add QA scenarios for "feat(translator): integrate SanitizeFunctionName across Claude translators" including stream/non-stream parity and edge-case payloads.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#804
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/804
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0558] Refactor implementation behind "win10无法安装没反应，cmd安装提示，failed to read config file" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#801
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/801
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0559] Ensure rollout safety for "在cherry-studio中的流失响应似乎未生效" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#798
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/798
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0560] Standardize metadata and naming conventions touched by "Bug: ModelStates (BackoffLevel) lost when auth is reloaded or refreshed" across both repos.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#797
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/797
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0561] Create/refresh provider quickstart derived from "[Bug] Stream usage data is merged with finish_reason: "stop", causing Letta AI to crash (OpenAI Stream Options incompatibility)" including setup, auth, model select, and sanity-check commands.
- Priority: P1
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#796
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/796
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0562] Harden "[BUG] Codex 默认回调端口 1455 位于 Hyper-v 保留端口段内" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#793
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/793
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0563] Operationalize "【Bug】: High CPU usage when managing 50+ OAuth accounts" with observability, alerting thresholds, and runbook updates.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#792
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/792
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0564] Convert "使用上游提供的 Gemini API 和 URL 获取到的模型名称不对应" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#791
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/791
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0565] Add DX polish around "当在codex exec 中使用gemini 或claude 模型时 codex 无输出结果" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#790
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/790
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0566] Expand docs and examples for "Brew 版本更新延迟，能否在 github Actions 自动增加更新 brew 版本？" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#789
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/789
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0567] Add QA scenarios for "[Bug]: Gemini Models Output Truncated - Database Schema Exceeds Maximum Allowed Tokens (140k+ chars) in Claude Code" including stream/non-stream parity and edge-case payloads.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#788
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/788
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0568] Refactor implementation behind "可否增加一个轮询方式的设置，某一个账户额度用尽时再使用下一个" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#784
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/784
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0569] Ensure rollout safety for "[功能请求] 新增联网gemini 联网模型" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#779
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/779
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0570] Port relevant thegent-managed flow implied by "Support for parallel requests" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#778
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/778
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0571] Follow up on "当认证账户消耗完之后，不会自动切换到 AI 提供商账户" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#777
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/777
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0572] Harden "[功能请求] 假流式和非流式防超时" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#775
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/775
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0573] Operationalize "[功能请求]可否增加 google genai 的兼容" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#771
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/771
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0574] Convert "反重力账号额度同时消耗" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#768
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/768
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0575] Define non-subprocess integration path related to "iflow模型排除无效" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#762
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/762
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0576] Expand docs and examples for "support proxy for opencode" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#753
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/753
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0577] Add QA scenarios for "[BUG] thinking/思考链在 antigravity 反代下被截断/丢失（stream 分块处理过严）" including stream/non-stream parity and edge-case payloads.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#752
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/752
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0578] Create/refresh provider quickstart derived from "api-keys 필드에 placeholder 값이 있으면 invalid api key 에러 발생" including setup, auth, model select, and sanity-check commands.
- Priority: P1
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#751
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/751
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0579] Ensure rollout safety for "[Bug]Fix `invalid_request_error` (Field required) when assistant message has empty content with tool_calls" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#749
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/749
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0580] Add process-compose/HMR refresh workflow tied to "建议增加 kiro CLI" so local config and runtime can be reloaded deterministically.
- Priority: P2
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#748
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/748
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0581] Follow up on "[Bug] Streaming response 'message_start' event missing token counts (affects OpenCode/Vercel AI SDK)" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#747
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/747
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0582] Harden "[Bug] Invalid request error when using thinking with multi-turn conversations" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#746
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/746
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0583] Operationalize "Add output_tokens_details.reasoning_tokens for thinking models on /v1/messages" with observability, alerting thresholds, and runbook updates.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#744
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/744
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0584] Convert "qwen-code-plus not supoort guided-json Structured Output" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#743
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/743
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0585] Add DX polish around "Bash tool too slow" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#742
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/742
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0586] Expand docs and examples for "反代Antigravity，CC读图的时候似乎会触发bug？明明现在上下文还有很多，但是提示要compact了" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#741
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/741
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0587] Add QA scenarios for "Claude Code CLI's status line shows zero tokens" including stream/non-stream parity and edge-case payloads.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#740
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/740
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0588] Refactor implementation behind "Tool calls not emitted after thinking blocks" to reduce complexity and isolate transformation boundaries.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#739
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/739
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0589] Port relevant thegent-managed flow implied by "Pass through actual Anthropic token counts instead of estimating" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#738
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/738
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0590] Standardize metadata and naming conventions touched by "多渠道同一模型映射成一个显示" across both repos.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#737
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/737
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0591] Follow up on "Feature Request: Complete OpenAI Tool Calling Format Support for Claude Models (Cursor MCP Compatibility)" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#735
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/735
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0592] Harden "Bug: /v1/responses endpoint does not correctly convert message format for Anthropic API" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#736
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/736
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0593] Operationalize "请问有计划支持显示目前剩余额度吗" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#734
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/734
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0594] Convert "reasoning_content is null for extended thinking models (thinking goes to content instead)" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#732
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/732
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0595] Create/refresh provider quickstart derived from "Use actual Anthropic token counts instead of estimation for reasoning_tokens" including setup, auth, model select, and sanity-check commands.
- Priority: P1
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#731
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/731
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0596] Expand docs and examples for "400 error: messages.X.content.0.text.text: Field required" with copy-paste quickstart and troubleshooting section.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#730
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/730
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0597] Add QA scenarios for "[BUG] Antigravity Opus + Codex cannot read images" including stream/non-stream parity and edge-case payloads.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#729
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/729
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0598] Define non-subprocess integration path related to "[Feature] Usage Statistics Persistence to JSON File - PR Proposal" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#726
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/726
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0599] Ensure rollout safety for "反代的Antigravity的claude模型在opencode cli需要增强适配" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#725
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/725
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0600] Standardize metadata and naming conventions touched by "iflow日志提示：当前找我聊的人太多了，可以晚点再来问我哦。" across both repos.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#724
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/724
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0601] Follow up on "怎么加入多个反重力账号？" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#723
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/723
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0602] Harden "最新的版本无法构建成镜像" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P3
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#721
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/721
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0603] Operationalize "API Error: 400" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#719
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/719
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0604] Convert "是否可以支持/openai/v1/responses端点" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#718
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/718
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0605] Add DX polish around "证书是否可以停用而非删除" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#717
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/717
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0606] Expand docs and examples for "thinking.cache_control error" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#714
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/714
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0607] Add QA scenarios for "Feature: able to show the remaining quota of antigravity and gemini cli" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#713
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/713
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0608] Port relevant thegent-managed flow implied by "/context show system tools 1 tokens, mcp tools 4 tokens" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P3
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#712
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/712
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0609] Add process-compose/HMR refresh workflow tied to "报错：failed to download management asset" so local config and runtime can be reloaded deterministically.
- Priority: P2
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#711
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/711
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0610] Standardize metadata and naming conventions touched by "iFlow models don't work in CC anymore" across both repos.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#710
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/710
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0611] Follow up on "claude code 的指令/cotnext 裡token 計算不正確" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#709
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/709
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0612] Create/refresh provider quickstart derived from "Behavior is not consistent with codex" including setup, auth, model select, and sanity-check commands.
- Priority: P1
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#708
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/708
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0613] Operationalize "iflow cli更新 GLM4.7 & MiniMax M2.1 模型" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#707
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/707
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0614] Convert "Antigravity provider returns 400 error when extended thinking is enabled after tool calls" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#702
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/702
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0615] Add DX polish around "iflow-cli上线glm4.7和m2.1" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#701
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/701
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0616] Expand docs and examples for "[功能请求] 支持使用 Vertex AI的API Key 模式调用" with copy-paste quickstart and troubleshooting section.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#699
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/699
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0617] Add QA scenarios for "是否可以提供kiro的支持啊" including stream/non-stream parity and edge-case payloads.
- Priority: P3
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#698
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/698
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0618] Refactor implementation behind "6.6.49版本下Antigravity渠道的claude模型使用claude code缓存疑似失效" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#696
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/696
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0619] Ensure rollout safety for "Translator: support first-class system prompt override for codex" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#694
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/694
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0620] Standardize metadata and naming conventions touched by "Add efficient scalar operations API (mul_scalar, add_scalar, etc.)" across both repos.
- Priority: P3
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#691
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/691
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0621] Define non-subprocess integration path related to "[功能请求] 能不能给每个号单独配置代理？" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#690
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/690
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0622] Harden "[Feature request] Add support for checking remaining Antigravity quota" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#687
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/687
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0623] Operationalize "Feature Request: Priority-based Auth Selection for Specific Models" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#685
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/685
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0624] Convert "Update Gemini 3 model names: remove -preview suffix for gemini-3-pro and gemini-3-flash" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#683
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/683
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0625] Add DX polish around "Frequent Tool-Call Failures with Gemini-2.5-pro in OpenAI-Compatible Mode" through improved command ergonomics and faster feedback loops.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#682
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/682
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0626] Expand docs and examples for "Feature: Persist stats to disk (Docker-friendly) instead of in-memory only" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: install-and-ops
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#681
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/681
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0627] Port relevant thegent-managed flow implied by "Support developer role" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#680
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/680
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0628] Refactor implementation behind "[Bug] Token counting endpoint /v1/messages/count_tokens significantly undercounts tokens" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#679
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/679
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0629] Create/refresh provider quickstart derived from "[Feature] Automatic Censoring Logs" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#678
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/678
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0630] Standardize metadata and naming conventions touched by "Translator: remove Copilot mention in OpenAI->Claude stream comment" across both repos.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#677
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/677
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0631] Follow up on "iflow渠道凭证报错" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#669
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/669
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0632] Harden "[Feature Request] Add timeout configuration" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#668
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/668
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0633] Operationalize "Support Trae" with observability, alerting thresholds, and runbook updates.
- Priority: P3
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#666
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/666
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0634] Convert "Filter OTLP telemetry from Amp VS Code hitting /api/otel/v1/metrics" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#660
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/660
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0635] Add DX polish around "Handle OpenAI Responses-format payloads hitting /v1/chat/completions" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#659
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/659
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0636] Expand docs and examples for "[Feature Request] Support reverse proxy for 'mimo' to enable Codex CLI usage" with copy-paste quickstart and troubleshooting section.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#656
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/656
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0637] Add QA scenarios for "[Bug] Gemini API Error: 'defer_loading' field in function declarations results in 400 Invalid JSON payload" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#655
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/655
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0638] Add process-compose/HMR refresh workflow tied to "System message (role: "system") completely dropped when converting to Antigravity API format" so local config and runtime can be reloaded deterministically.
- Priority: P2
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#654
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/654
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0639] Ensure rollout safety for "Antigravity Provider Broken" via feature flags, staged defaults, and migration notes.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#650
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/650
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0640] Standardize metadata and naming conventions touched by "希望能支持 GitHub Copilot" across both repos.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#649
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/649
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0641] Follow up on "Request Wrap Cursor to use models as proxy" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#648
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/648
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0642] Harden "[BUG] calude chrome中使用 antigravity模型 tool call错误" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#642
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/642
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0643] Operationalize "get error when tools call in jetbrains ai assistant with openai BYOK" with observability, alerting thresholds, and runbook updates.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#639
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/639
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0644] Define non-subprocess integration path related to "[Bug] OAuth tokens have insufficient scopes for Gemini/Antigravity API - 401 "Invalid API key"" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P1
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#637
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/637
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0645] Add DX polish around "Large prompt failures w/ Claude Code vs Codex routes (gpt-5.2): cloudcode 'Prompt is too long' + codex SSE missing response.completed" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#636
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/636
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0646] Create/refresh provider quickstart derived from "Spam about server clients and configuration updated" including setup, auth, model select, and sanity-check commands.
- Priority: P3
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#635
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/635
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0647] Add QA scenarios for "Payload thinking overrides break requests with tool_choice (handoff fails)" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#630
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/630
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0648] Refactor implementation behind "我无法使用gpt5.2max而其他正常" to reduce complexity and isolate transformation boundaries.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#629
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/629
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0649] Ensure rollout safety for "[Feature Request] Add support for AWS Bedrock API" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#626
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/626
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0650] Standardize metadata and naming conventions touched by "[Question] Mapping different keys to different accounts for same provider" across both repos.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#625
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/625
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0651] Follow up on ""Requested entity was not found" for Gemini 3" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#620
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/620
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0652] Harden "[Feature Request] Set hard limits for CLIProxyAPI API Keys" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#617
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/617
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0653] Operationalize "Management routes (threads, user, auth) fail with 401/402 because proxy strips client auth and injects provider-only credentials" with observability, alerting thresholds, and runbook updates.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#614
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/614
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0654] Convert "Amp client fails with "unexpected EOF" when creating large files, while OpenAI-compatible clients succeed" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#613
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/613
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0655] Add DX polish around "Request support for codebuff access." through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#612
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/612
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0656] Expand docs and examples for "SDK Internal Package Dependency Issue" with copy-paste quickstart and troubleshooting section.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#607
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/607
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0657] Add QA scenarios for "Can't use Oracle tool in AMP Code" including stream/non-stream parity and edge-case payloads.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#606
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/606
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0658] Refactor implementation behind "Openai 5.2 Codex is launched" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: testing-and-quality
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#603
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/603
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0659] Ensure rollout safety for "Failing to do tool use from within Cursor" via feature flags, staged defaults, and migration notes.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#601
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/601
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0660] Standardize metadata and naming conventions touched by "[Bug] gpt-5.1-codex models return 400 error (no body) while other OpenAI models succeed" across both repos.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#600
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/600
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0661] Follow up on "调用deepseek-chat报错" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#599
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/599
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0662] Harden "‎" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#595
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/595
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0663] Create/refresh provider quickstart derived from "不能通过回调链接认证吗" including setup, auth, model select, and sanity-check commands.
- Priority: P3
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#594
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/594
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0664] Convert "bug: Streaming not working for Gemini 3 models (Flash/Pro Preview) via Gemini CLI/Antigravity" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#593
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/593
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0665] Port relevant thegent-managed flow implied by "[Bug] Antigravity prompt caching broken by random sessionId per request" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P3
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#592
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/592
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0666] Expand docs and examples for "Important Security & Integrity Alert regarding @Eric Tech" with copy-paste quickstart and troubleshooting section.
- Priority: P1
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#591
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/591
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0667] Define non-subprocess integration path related to "[Bug] Models from Codex (openai) are not accessible when Copilot is added" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#590
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/590
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0668] Refactor implementation behind "[Feature request] Add an enable switch for OpenAI-compatible providers and add model alias for antigravity" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#588
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/588
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0669] Ensure rollout safety for "[Bug] Gemini API rejects "optional" field in tool parameters" via feature flags, staged defaults, and migration notes.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#583
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/583
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0670] Standardize metadata and naming conventions touched by "github copilot problem" across both repos.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#578
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/578
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0671] Follow up on "amp使用时日志频繁出现下面报错" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#576
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/576
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0672] Harden "Github Copilot Error" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#574
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/574
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0673] Operationalize "Cursor support" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#573
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/573
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0674] Convert "Qwen CLI often stops working before finishing the task" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#567
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/567
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0675] Add DX polish around "gemini cli接入后，可以正常调用所属大模型；Antigravity通过OAuth成功认证接入后，无法调用所属的模型" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#566
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/566
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0676] Expand docs and examples for "Model ignores tool response and keeps repeating tool calls (Gemini 3 Pro / 2.5 Pro)" with copy-paste quickstart and troubleshooting section.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#565
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/565
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0677] Add QA scenarios for "fix(translator): emit message_start on first chunk regardless of role field" including stream/non-stream parity and edge-case payloads.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#563
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/563
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0678] Refactor implementation behind "Bug: OpenAI→Anthropic streaming translation fails with tool calls - missing message_start" to reduce complexity and isolate transformation boundaries.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#561
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/561
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0679] Ensure rollout safety for "stackTrace.format error in error response handling" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#559
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/559
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0680] Create/refresh provider quickstart derived from "docker运行的容器最近几个版本不会自动下载management.html了" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#557
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/557
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0681] Follow up on "Bug: AmpCode login routes incorrectly require API key authentication since v6.6.15" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#554
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/554
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0682] Harden "Github Copilot" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#551
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/551
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0683] Operationalize "Gemini3配置了thinkingConfig无效，模型调用名称被改为了gemini-3-pro-high" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#550
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/550
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0684] Port relevant thegent-managed flow implied by "Antigravity has no gemini-2.5-pro" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P3
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#548
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/548
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0685] Add DX polish around "Add General Request Queue with Windowed Concurrency for Reliable Pseudo-Concurrent Execution" through improved command ergonomics and faster feedback loops.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#546
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/546
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0686] Expand docs and examples for "The token file was not generated." with copy-paste quickstart and troubleshooting section.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#544
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/544
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0687] Add QA scenarios for "Suggestion: Retain statistics after each update." including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#541
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/541
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0688] Refactor implementation behind "Bug: Codex→Claude SSE content_block.index collisions break Claude clients" to reduce complexity and isolate transformation boundaries.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#539
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/539
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0689] Ensure rollout safety for "[Feature Request] Add logs rotation" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#535
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/535
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0690] Define non-subprocess integration path related to "[Bug] AI Studio 渠道流式响应 JSON 格式异常导致客户端解析失败" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#534
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/534
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0691] Follow up on "Feature: Add copilot-unlimited-mode config for copilot-api compatibility" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#532
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/532
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0692] Harden "Bug: content_block_start sent before message_start in OpenAI→Anthropic translation" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#530
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/530
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0693] Operationalize "CLIProxyAPI，通过gemini cli来实现对gemini-2.5-pro的调用，如果遇到输出长度在上万字的情况，总是遇到429错误" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#518
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/518
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0694] Convert "Antigravity Error 400" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#517
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/517
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0695] Add DX polish around "Add AiStudio error" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#513
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/513
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0696] Add process-compose/HMR refresh workflow tied to "Claude Code with Antigravity gemini-claude-sonnet-4-5-thinking error: Extra inputs are not permitted" so local config and runtime can be reloaded deterministically.
- Priority: P2
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#512
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/512
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0697] Create/refresh provider quickstart derived from "Claude code results in errors with "poor internet connection"" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#510
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/510
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0698] Refactor implementation behind "[Feature Request] Global Alias" to reduce complexity and isolate transformation boundaries.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#509
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/509
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0699] Ensure rollout safety for "GET /v1/models does not expose model capabilities (e.g. gpt-5.2 supports (xhigh) but cannot be discovered)" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#508
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/508
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0700] Standardize metadata and naming conventions touched by "[Bug] Load balancing is uneven: Requests are not distributed equally among available accounts" across both repos.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#506
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/506
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0701] Follow up on "openai兼容错误使用“alias”作为模型id请求" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#503
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/503
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0702] Harden "bug: antigravity oauth callback fails on windows due to hard-coded port 51121" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#499
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/499
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0703] Port relevant thegent-managed flow implied by "unexpected `tool_use_id` found in `tool_result` blocks" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P1
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#497
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/497
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0704] Convert "gpt5.2 cherry 报错" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#496
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/496
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0705] Add DX polish around "antigravity中反代的接口在claude code中无法使用thinking模式" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#495
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/495
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0706] Expand docs and examples for "Add support for gpt-5,2" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#493
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/493
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0707] Add QA scenarios for "OAI models not working." including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#492
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/492
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0708] Refactor implementation behind "Did the API change?" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#491
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/491
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0709] Ensure rollout safety for "5.2 missing. no automatic model discovery" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#490
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/490
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0710] Standardize metadata and naming conventions touched by "Tool calling fails when using Claude Opus 4.5 Thinking (AntiGravity) model via Zed Agent" across both repos.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#489
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/489
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0711] Follow up on "Issue with enabling logs in Mac settings." by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#484
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/484
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0712] Harden "How to configure thinking for Claude and Codex?" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#483
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/483
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0713] Define non-subprocess integration path related to "gpt-5-codex-(low,medium,high) models not listed anymore" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#482
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/482
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0714] Create/refresh provider quickstart derived from "CLIProxyAPI配置 Gemini CLI最后一步失败：Google账号权限设置不够" including setup, auth, model select, and sanity-check commands.
- Priority: P1
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#480
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/480
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0715] Add DX polish around "Files and images not working with Antigravity" through improved command ergonomics and faster feedback loops.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#478
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/478
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0716] Expand docs and examples for "antigravity渠道的claude模型在claude code中无法使用explore工具" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#477
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/477
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0717] Add QA scenarios for "Error with Antigravity" including stream/non-stream parity and edge-case payloads.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#476
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/476
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0718] Refactor implementation behind "fix(translator): skip empty functionResponse in OpenAI-to-Antigravity path" to reduce complexity and isolate transformation boundaries.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#475
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/475
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0719] Ensure rollout safety for "Antigravity API reports API Error: 400 with Claude Code" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#472
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/472
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0720] Standardize metadata and naming conventions touched by "fix(translator): preserve tool_use blocks on args parse failure" across both repos.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#471
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/471
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0721] Follow up on "Antigravity API reports API Error: 400 with Claude Code" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#463
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/463
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0722] Port relevant thegent-managed flow implied by "支持一下https://gemini.google.com/app" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#462
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/462
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0723] Operationalize "Streaming fails for "preview" and "thinking" models (response is buffered)" with observability, alerting thresholds, and runbook updates.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#460
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/460
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0724] Convert "failed to unmarshal function response: invalid character 'm' looking for beginning of value on droid" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#451
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/451
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0725] Add process-compose/HMR refresh workflow tied to "iFlow Cookie 登录流程BUG" so local config and runtime can be reloaded deterministically.
- Priority: P2
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#445
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/445
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0726] Expand docs and examples for "[Suggestion] Add ingress rate limiting and 403 circuit breaker for /v1/messages" with copy-paste quickstart and troubleshooting section.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#443
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/443
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0727] Add QA scenarios for "AGY Claude models" including stream/non-stream parity and edge-case payloads.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#442
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/442
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0728] Refactor implementation behind "【BUG】Infinite loop on startup if an auth file is removed (Windows)" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#440
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/440
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0729] Ensure rollout safety for "can I use models of droid in Claude Code?" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#438
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/438
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0730] Standardize metadata and naming conventions touched by "`[Bug/Question]: Antigravity models looping in Plan Mode & 400 Invalid Argument errors`" across both repos.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#437
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/437
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0731] Create/refresh provider quickstart derived from "[Bug] 400 Invalid Argument: 'thinking' block missing in ConvertClaudeRequestToAntigravity" including setup, auth, model select, and sanity-check commands.
- Priority: P1
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#436
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/436
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0732] Harden "gemini等模型没有按openai api的格式返回呀" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#433
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/433
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0733] Operationalize "[Feature Request] Persistent Storage for Usage Statistics" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: install-and-ops
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#431
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/431
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0734] Convert "Antigravity Claude *-thinking + tools only stream reasoning (no assistant content/tool_calls) via OpenAI-compatible API" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#425
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/425
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0735] Add DX polish around "Antigravity Claude by Claude Code `max_tokens` must be greater than `thinking.budget_tokens`" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#424
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/424
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0736] Define non-subprocess integration path related to "Antigravity: Permission denied on resource project [projectID]" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#421
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/421
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0737] Add QA scenarios for "Extended thinking blocks not preserved during tool use, causing API rejection" including stream/non-stream parity and edge-case payloads.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#420
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/420
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0738] Refactor implementation behind "Antigravity Claude via CLIProxyAPI: browsing enabled in Cherry but no actual web requests" to reduce complexity and isolate transformation boundaries.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#419
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/419
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0739] Ensure rollout safety for "OpenAI Compatibility with OpenRouter results in invalid JSON response despite 200 OK" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#417
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/417
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0740] Standardize metadata and naming conventions touched by "Bug: Claude proxy models fail with tools - `tools.0.custom.input_schema: Field required`" across both repos.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#415
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/415
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0741] Port relevant thegent-managed flow implied by "Gemini-CLI,gemini-2.5-pro调用触发限流之后(You have exhausted your capacity on this model. Your quota will reset after 51s.)，会自动切换请求gemini-2.5-pro-preview-06-05，但是这个模型貌似已经不存在了" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P1
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#414
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/414
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0742] Harden "invalid_request_error","message":"`max_tokens` must be greater than `thinking.budget_tokens`." with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#413
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/413
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0743] Operationalize "Which CLIs that support Antigravity?" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#412
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/412
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0744] Convert "[Feature Request] Dynamic Model Mapping & Custom Parameter Injection (e.g., iflow /tab)" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#411
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/411
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0745] Add DX polish around "iflow使用谷歌登录后，填入cookie无法正常使用" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#408
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/408
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0746] Expand docs and examples for "Antigravity not working" with copy-paste quickstart and troubleshooting section.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#407
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/407
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0747] Add QA scenarios for "大佬能不能出个zeabur部署的教程" including stream/non-stream parity and edge-case payloads.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#403
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/403
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0748] Create/refresh provider quickstart derived from "Gemini responses contain non-standard OpenAI fields causing parser failures" including setup, auth, model select, and sanity-check commands.
- Priority: P1
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#400
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/400
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0749] Ensure rollout safety for "HTTP Proxy Not Effective: Token Unobtainable After Google Account Authentication Success" via feature flags, staged defaults, and migration notes.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#397
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/397
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0750] Standardize metadata and naming conventions touched by "antigravity认证难以成功" across both repos.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#396
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/396
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0751] Follow up on "Could I use gemini-3-pro-preview by gmini cli？" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#391
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/391
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0752] Harden "Ports Reserved By Windows Hyper-V" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#387
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/387
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0753] Operationalize "Image gen not supported/enabled for gemini-3-pro-image-preview?" with observability, alerting thresholds, and runbook updates.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#374
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/374
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0754] Add process-compose/HMR refresh workflow tied to "Is it possible to support gemini native api for file upload?" so local config and runtime can be reloaded deterministically.
- Priority: P3
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#373
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/373
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0755] Add DX polish around "Web Search tool not working in AMP with cliproxyapi" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#370
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/370
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0756] Expand docs and examples for "1006怎么处理" with copy-paste quickstart and troubleshooting section.
- Priority: P3
- Effort: S
- Theme: install-and-ops
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#369
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/369
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0757] Add QA scenarios for "能否为kiro oauth提供支持？（附实现项目链接）" including stream/non-stream parity and edge-case payloads.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#368
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/368
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0758] Refactor implementation behind "antigravity 无法配置？" to reduce complexity and isolate transformation boundaries.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#367
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/367
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0759] Define non-subprocess integration path related to "Frequent 500 auth_unavailable and Codex CLI models disappearing from /v1/models" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P1
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#365
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/365
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0760] Port relevant thegent-managed flow implied by "Web Search tool not functioning in Claude Code" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#364
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/364
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0761] Follow up on "claude code Auto compact not triggered even after reaching autocompact buffer threshold" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#363
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/363
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0762] Harden "[Feature] 增加gemini business账号支持" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#361
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/361
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0763] Operationalize "[Bug] Codex Reasponses Sometimes Omit Reasoning Tokens" with observability, alerting thresholds, and runbook updates.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#356
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/356
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0764] Convert "[Bug] Codex Max Does Not Utilize XHigh Reasoning Effort" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#354
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/354
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0765] Create/refresh provider quickstart derived from "[Bug] Gemini 3 Does Not Utilize Reasoning Effort" including setup, auth, model select, and sanity-check commands.
- Priority: P1
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#353
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/353
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0766] Expand docs and examples for "API for iflow-cli is not work anymore: iflow executor: token refresh failed: iflow token: missing access token in response" with copy-paste quickstart and troubleshooting section.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#352
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/352
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0767] Add QA scenarios for "[Bug] Antigravity/Claude Code: "tools.0.custom.input_schema: Field required" error on all antigravity models" including stream/non-stream parity and edge-case payloads.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#351
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/351
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0768] Refactor implementation behind "[Feature Request] Amazonq Support" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#350
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/350
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0769] Ensure rollout safety for "Feature: Add tier-based provider prioritization" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#349
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/349
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0770] Standardize metadata and naming conventions touched by "Gemini 3 Pro + Codex CLI" across both repos.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#346
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/346
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0771] Follow up on "Add support for anthropic-beta header for Claude thinking models with tool use" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#344
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/344
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0772] Harden "Anitigravity models are not working in opencode cli, has serveral bugs" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#342
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/342
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0773] Operationalize "[Bug] Antigravity 渠道使用原生 Gemini 格式：模型列表缺失及 gemini-3-pro-preview 联网搜索不可用" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#341
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/341
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0774] Convert "checkSystemInstructions adds cache_control block causing 'maximum of 4 blocks' error" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#339
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/339
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0775] Add DX polish around "OpenAI and Gemini API: thinking/chain-of-thought broken or 400 error (max_tokens vs thinking.budget_tokens) for thinking models" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#338
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/338
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0776] Expand docs and examples for "[Bug] Commit 52c17f0 breaks OAuth authentication for Anthropic models" with copy-paste quickstart and troubleshooting section.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#337
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/337
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0777] Add QA scenarios for "Droid as provider" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#336
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/336
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0778] Refactor implementation behind "Support for JSON schema / structured output" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#335
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/335
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0779] Port relevant thegent-managed flow implied by "gemini-claude-sonnet-4-5-thinking: Chain-of-Thought (thinking) does not work on any API (OpenAI/Gemini/Claude)" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P1
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#332
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/332
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0780] Standardize metadata and naming conventions touched by "docker方式部署后，怎么登陆gemini账号呢？" across both repos.
- Priority: P2
- Effort: S
- Theme: install-and-ops
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#328
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/328
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0781] Follow up on "FR: Add support for beta headers for Claude models" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#324
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/324
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0782] Create/refresh provider quickstart derived from "FR: Add Opus 4.5 Support" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#321
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/321
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0783] Add process-compose/HMR refresh workflow tied to "`gemini-3-pro-preview` tool usage failures" so local config and runtime can be reloaded deterministically.
- Priority: P3
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#320
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/320
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0784] Convert "RooCode compatibility" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#319
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/319
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0785] Add DX polish around "undefined is not an object (evaluating 'T.match')" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#317
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/317
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0786] Expand docs and examples for "Nano Banana" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#316
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/316
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0787] Add QA scenarios for "Feature: 渠道关闭/开启切换按钮、渠道测试按钮、指定渠道模型调用" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#314
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/314
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0788] Refactor implementation behind "Previous request seem to be concatenated into new ones with Antigravity" to reduce complexity and isolate transformation boundaries.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#313
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/313
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0789] Ensure rollout safety for "Question: Is the Antigravity provider available and compatible with the sonnet 4.5 Thinking LLM model?" via feature flags, staged defaults, and migration notes.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#311
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/311
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0790] Standardize metadata and naming conventions touched by "cursor with gemini-claude-sonnet-4-5" across both repos.
- Priority: P3
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#310
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/310
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0791] Follow up on "Gemini not stream thinking result" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#308
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/308
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0792] Harden "[Suggestion] Improve Prompt Caching for Gemini CLI / Antigravity - Don't do round-robin for all every request" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#307
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/307
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0793] Operationalize "docker-compose启动错误" with observability, alerting thresholds, and runbook updates.
- Priority: P3
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#305
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/305
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0794] Convert "可以让不同的提供商分别设置代理吗?" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#304
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/304
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0795] Add DX polish around "如果能控制aistudio的认证文件启用就好了" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#302
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/302
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0796] Expand docs and examples for "Dynamic model provider not work" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#301
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/301
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0797] Add QA scenarios for "token无计数" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#300
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/300
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0798] Port relevant thegent-managed flow implied by "cursor with antigravity" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#298
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/298
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0799] Create/refresh provider quickstart derived from "认证未走代理" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#297
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/297
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0800] Standardize metadata and naming conventions touched by "[Feature Request] Add --manual-callback mode for headless/remote OAuth (especially for users behind proxy / Clash TUN in China)" across both repos.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#295
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/295
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0801] Follow up on "Regression: gemini-3-pro-preview unusable due to removal of 429 retry logic in d50b0f7" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#293
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/293
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0802] Harden "Gemini 3 Pro no response in Roo Code with AI Studio setup" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#291
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/291
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0803] Operationalize "CLIProxyAPI error in huggingface" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#290
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/290
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0804] Convert "Post "https://chatgpt.com/backend-api/codex/responses": Not Found" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#286
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/286
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0805] Define non-subprocess integration path related to "Feature: Add Image Support for Gemini 3" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#283
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/283
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0806] Expand docs and examples for "Bug: Gemini 3 Thinking Budget requires normalization in CLI Translator" with copy-paste quickstart and troubleshooting section.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#282
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/282
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0807] Add QA scenarios for "Feature Request: Support for Gemini 3 Pro Preview" including stream/non-stream parity and edge-case payloads.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#278
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/278
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0808] Refactor implementation behind "[Suggestion] Improve Prompt Caching - Don't do round-robin for all every request" to reduce complexity and isolate transformation boundaries.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#277
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/277
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0809] Ensure rollout safety for "Feature Request: Support Google Antigravity provider" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#273
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/273
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0810] Standardize metadata and naming conventions touched by "Add copilot cli proxy" across both repos.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#272
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/272
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0811] Follow up on "`gemini-3-pro-preview` is missing" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#271
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/271
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0812] Add process-compose/HMR refresh workflow tied to "Adjust gemini-3-pro-preview`s doc" so local config and runtime can be reloaded deterministically.
- Priority: P1
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#269
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/269
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0813] Operationalize "Account banned after using CLI Proxy API on VPS" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: install-and-ops
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#266
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/266
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0814] Convert "Bug: config.example.yaml has incorrect auth-dir default, causes auth files to be saved in wrong location" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#265
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/265
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0815] Add DX polish around "Security: Auth directory created with overly permissive 0o755 instead of 0o700" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#264
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/264
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0816] Create/refresh provider quickstart derived from "Gemini CLI Oauth with Claude Code" including setup, auth, model select, and sanity-check commands.
- Priority: P1
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#263
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/263
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0817] Port relevant thegent-managed flow implied by "Gemini cli使用不了" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#262
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/262
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0818] Refactor implementation behind "麻烦大佬能不能更进模型id，比如gpt已经更新了小版本5.1了" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#261
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/261
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0819] Ensure rollout safety for "Factory Droid: /compress (session compact) fails on Gemini 2.5 via CLIProxyAPI" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#260
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/260
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0820] Standardize metadata and naming conventions touched by "Feat Request: Support gpt-5-pro" across both repos.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#259
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/259
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0821] Follow up on "gemini oauth in droid cli: unknown provider" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#258
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/258
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0822] Harden "认证文件管理 主动触发同步" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#255
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/255
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0823] Operationalize "Kimi K2 Thinking" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#254
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/254
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0824] Convert "nano banana 水印的能解决？我使用CLIProxyAPI 6.1" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#253
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/253
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0825] Add DX polish around "ai studio 不能用" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: install-and-ops
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#252
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/252
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0826] Expand docs and examples for "Feature: scoped `auto` model (provider + pattern)" with copy-paste quickstart and troubleshooting section.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#251
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/251
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0827] Add QA scenarios for "wss 链接失败" including stream/non-stream parity and edge-case payloads.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#250
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/250
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0828] Define non-subprocess integration path related to "应该给GPT-5.1添加-none后缀适配以保持一致性" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P3
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#248
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/248
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0829] Ensure rollout safety for "不支持 candidate_count 功能，设置需要多版本回复的时候，只会输出1条" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#247
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/247
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0830] Standardize metadata and naming conventions touched by "gpt-5.1模型添加" across both repos.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#246
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/246
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0831] Follow up on "cli-proxy-api --gemini-web-auth" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#244
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/244
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0832] Harden "支持为模型设定默认请求参数" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#242
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/242
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0833] Create/refresh provider quickstart derived from "ClawCloud 如何结合NanoBanana 使用？" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#241
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/241
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0834] Convert "gemini cli 无法画图是不是必须要使用低版本了" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#240
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/240
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0835] Add DX polish around "[error] [iflow_executor.go:273] iflow executor: token refresh failed: iflow token: missing access token in response" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#239
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/239
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0836] Port relevant thegent-managed flow implied by "Codex API 配置中Base URL需要加v1嘛？" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#238
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/238
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0837] Add QA scenarios for "Feature Request: Support "auto" Model Selection for Seamless Provider Updates" including stream/non-stream parity and edge-case payloads.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#236
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/236
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0838] Refactor implementation behind "AI Studio途径，是否支持imagen图片生成模型？" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#235
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/235
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0839] Ensure rollout safety for "现在对话很容易就结束" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#234
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/234
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0840] Standardize metadata and naming conventions touched by "添加文件时重复添加" across both repos.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#233
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/233
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0841] Add process-compose/HMR refresh workflow tied to "Feature Request : Token Caching for Codex" so local config and runtime can be reloaded deterministically.
- Priority: P2
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#231
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/231
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0842] Harden "agentrouter problem" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#228
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/228
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0843] Operationalize "[Suggestion] Add suport iFlow CLI MiniMax-M2" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#223
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/223
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0844] Convert "Feature: Prevent infinite loop to allow direct access to Gemini-native features" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#220
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/220
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0845] Add DX polish around "Feature request: Support amazon-q-developer-cli" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#219
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/219
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0846] Expand docs and examples for "Gemini Cli 400 Error" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#218
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/218
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0847] Add QA scenarios for "/v1/responese connection error for version 0.55.0 of codex" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#216
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/216
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0848] Refactor implementation behind "https://huggingface.co/chat" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#212
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/212
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0849] Ensure rollout safety for "Codex trying to read from non-existant Bashes in Claude" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#211
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/211
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0850] Create/refresh provider quickstart derived from "Feature Request: Git-backed Configuration and Token Store for sync" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#210
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/210
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0851] Define non-subprocess integration path related to "CLIProxyAPI中的Gemini cli的图片生成，是不是无法使用了？" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#208
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/208
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0852] Harden "Model gemini-2.5-flash-image not work any more" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#203
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/203
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0853] Operationalize "qwen code和iflow的模型重复了" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#202
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/202
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0854] Convert "docker compose还会继续维护吗" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: install-and-ops
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#201
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/201
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0855] Port relevant thegent-managed flow implied by "Wrong Claude Model Recognized" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#200
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/200
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0856] Expand docs and examples for "Unable to Select Specific Model" with copy-paste quickstart and troubleshooting section.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#197
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/197
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0857] Add QA scenarios for "claude code with copilot" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#193
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/193
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0858] Refactor implementation behind "Feature Request: OAuth Aliases & Multiple Aliases" to reduce complexity and isolate transformation boundaries.
- Priority: P1
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#192
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/192
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0859] Ensure rollout safety for "[feature request] enable host or bind ip option / 添加 host 配置选项以允许外部网络访问" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: error-handling-retries
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#190
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/190
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0860] Standardize metadata and naming conventions touched by "Feature request: Add token cost statistics" across both repos.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#189
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/189
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0861] Follow up on "internal/translator下的翻译器对外暴露了吗？" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#188
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/188
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0862] Harden "API Key issue" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#181
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/181
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0863] Operationalize "[Request] Add support for Gemini Embeddings (AI Studio API key) and optional multi-key rotation" with observability, alerting thresholds, and runbook updates.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#179
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/179
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0864] Convert "希望增加渠道分类" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#178
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/178
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0865] Add DX polish around "gemini-cli `Request Failed: 400` exception" through improved command ergonomics and faster feedback loops.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#176
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/176
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0866] Expand docs and examples for "Possible JSON Marshal issue: Some Chars transformed to unicode while transforming Anthropic request to OpenAI compatible request" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#175
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/175
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0867] Create/refresh provider quickstart derived from "question about subagents:" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#174
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/174
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0868] Refactor implementation behind "MiniMax-M2 API error" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#172
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/172
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0869] Ensure rollout safety for "[feature request] pass model names without defining them [HAS PR]" via feature flags, staged defaults, and migration notes.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#171
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/171
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0870] Add process-compose/HMR refresh workflow tied to "MiniMax-M2 and other Anthropic compatible models" so local config and runtime can be reloaded deterministically.
- Priority: P2
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#170
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/170
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0871] Follow up on "Troublesome First Instruction" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#169
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/169
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0872] Harden "No Auth Status" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#168
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/168
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0873] Operationalize "Major Bug in transforming anthropic request to openai compatible request" with observability, alerting thresholds, and runbook updates.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#167
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/167
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0874] Port relevant thegent-managed flow implied by "Created an install script for linux" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P3
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#166
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/166
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0875] Add DX polish around "Feature Request: Add support for vision-model for Qwen-CLI" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#164
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/164
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0876] Expand docs and examples for "[Suggestion] Intelligent Model Routing" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#162
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/162
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0877] Add QA scenarios for "Clarification Needed: Is 'timeout' a Supported Config Parameter?" including stream/non-stream parity and edge-case payloads.
- Priority: P3
- Effort: S
- Theme: error-handling-retries
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#160
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/160
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0878] Refactor implementation behind "GeminiCLI的模型，总是会把历史问题全部回答一遍" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#159
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/159
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0879] Ensure rollout safety for "Gemini Cli With github copilot" via feature flags, staged defaults, and migration notes.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#158
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/158
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0880] Standardize metadata and naming conventions touched by "Enhancement: _FILE env vars for docker compose" across both repos.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#156
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/156
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0881] Follow up on "All-in-WSL2: Claude Code (sub-agents + MCP) via CLIProxyAPI — token-only Codex, gpt-5-high / gpt-5-low mapping, multi-account" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#154
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/154
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0882] Harden "OpenAI-compatible API not working properly with certain models (e.g. glm-4.6, kimi-k2, DeepSeek-V3.2)" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#153
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/153
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0883] Operationalize "OpenRouter Grok 4 Fast Bug" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#152
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/152
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0884] Create/refresh provider quickstart derived from "Question about models:" including setup, auth, model select, and sanity-check commands.
- Priority: P1
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#150
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/150
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0885] Add DX polish around "Feature Request: Add rovodev CLI Support" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#149
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/149
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0886] Expand docs and examples for "CC 使用 gpt-5-codex 模型几乎没有走缓存" with copy-paste quickstart and troubleshooting section.
- Priority: P3
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#148
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/148
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0887] Add QA scenarios for "Cannot create Auth files in docker container webui management page" including stream/non-stream parity and edge-case payloads.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#144
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/144
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0888] Refactor implementation behind "关于openai兼容供应商" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#143
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/143
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0889] Ensure rollout safety for "No System Prompt maybe possible?" via feature flags, staged defaults, and migration notes.
- Priority: P3
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#142
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/142
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0890] Standardize metadata and naming conventions touched by "Claude Code tokens counter" across both repos.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#140
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/140
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0891] Follow up on "API Error" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#137
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/137
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0892] Harden "代理在生成函数调用请求时使用了 Gemini API 不支持的 "const" 字段" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#136
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/136
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0893] Port relevant thegent-managed flow implied by "droid cli with CLIProxyAPI [codex,zai]" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#135
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/135
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0894] Convert "Claude Code ``/context`` command" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#133
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/133
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0895] Add DX polish around "Any interest in adding AmpCode support?" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#132
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/132
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0896] Expand docs and examples for "Agentrouter.org Support" with copy-paste quickstart and troubleshooting section.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#131
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/131
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0897] Define non-subprocess integration path related to "Geminicli api proxy error" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#129
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/129
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0898] Refactor implementation behind "Github Copilot Subscription" to reduce complexity and isolate transformation boundaries.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#128
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/128
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0899] Add process-compose/HMR refresh workflow tied to "Add Z.ai / GLM API Configuration" so local config and runtime can be reloaded deterministically.
- Priority: P2
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#124
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/124
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0900] Standardize metadata and naming conventions touched by "Gemini + Droid = Bug" across both repos.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#123
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/123
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0901] Create/refresh provider quickstart derived from "Custom models for AI Proviers" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#122
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/122
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0902] Harden "Web Search and other network tools" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#121
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/121
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0903] Operationalize "recommend using bufio to improve terminal visuals(reduce flickering)" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#120
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/120
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0904] Convert "视觉以及PDF适配" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#119
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/119
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0905] Add DX polish around "claude code接入gemini cli模型问题" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#115
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/115
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0906] Expand docs and examples for "Feat Request: Usage Limit Notifications + Timers + Per-Auth Usage" with copy-paste quickstart and troubleshooting section.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#112
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/112
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0907] Add QA scenarios for "Thinking toggle with GPT-5-Codex model" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#109
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/109
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0908] Refactor implementation behind "可否增加 请求 api-key = 渠道密钥模式" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#108
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/108
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0909] Ensure rollout safety for "Homebrew 安装的 CLIProxyAPI 如何设置配置文件？" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#106
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/106
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0910] Standardize metadata and naming conventions touched by "支持Gemini CLI 的全部模型" across both repos.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#105
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/105
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0911] Follow up on "gemini能否适配思考预算后缀?" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#103
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/103
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0912] Port relevant thegent-managed flow implied by "Bug: function calling error in the request on OpenAI completion for gemini-cli" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P2
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#102
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/102
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0913] Operationalize "增加 IFlow 支持模型" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#101
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/101
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0914] Convert "Feature Request: Grok usage" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#100
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/100
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0915] Add DX polish around "新版本的claude code2.0.X搭配本项目的使用问题" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#98
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/98
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0916] Expand docs and examples for "Huge error message when connecting to Gemini via Opencode, SanitizeSchemaForGemini not being used?" with copy-paste quickstart and troubleshooting section.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#97
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/97
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0917] Add QA scenarios for "可以支持z.ai 吗" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#96
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/96
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0918] Create/refresh provider quickstart derived from "Gemini and Qwen doesn't work with Opencode" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#93
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/93
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0919] Ensure rollout safety for "Agent Client Protocol (ACP)?" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#92
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/92
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0920] Define non-subprocess integration path related to "Auto compress - Error: B is not an Object. (evaluating '"object"in B')" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#91
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/91
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0921] Follow up on "Gemini Web Auto Refresh Token" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#89
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/89
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0922] Harden "Gemini API 能否添加设置Base URL 的选项" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#88
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/88
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0923] Operationalize "Some third-party claude code will return null when used with this project" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#87
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/87
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0924] Convert "Auto compress - Error: 500 status code (no body)" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#86
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/86
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0925] Add DX polish around "Add more model selection options" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#84
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/84
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0926] Expand docs and examples for "Error on switching models in Droid after hitting Usage Limit" with copy-paste quickstart and troubleshooting section.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#81
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/81
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0927] Add QA scenarios for "Command /context dont work in claude code" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#80
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/80
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0928] Add process-compose/HMR refresh workflow tied to "MacOS brew installation support?" so local config and runtime can be reloaded deterministically.
- Priority: P2
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#79
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/79
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0929] Ensure rollout safety for "[Feature Request] - Adding OAuth support of Z.AI and Kimi" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#76
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/76
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0930] Standardize metadata and naming conventions touched by "Bug: 500 Invalid resource field value in the request on OpenAI completion for gemini-cli" across both repos.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#75
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/75
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0931] Port relevant thegent-managed flow implied by "添加 Factor CLI 2api 选项" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P3
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#74
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/74
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0932] Harden "Support audio for gemini-cli" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#73
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/73
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0933] Operationalize "添加回调链接输入认证" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: install-and-ops
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#56
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/56
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0934] Convert "如果配置了gemini cli，再配置aistudio api key，会怎样？" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#48
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/48
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0935] Create/refresh provider quickstart derived from "Error walking auth directory: open C:\Users\xiaohu\AppData\Local\ElevatedDiagnostics: Access is denied" including setup, auth, model select, and sanity-check commands.
- Priority: P1
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#42
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/42
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0936] Expand docs and examples for "#38 Lobechat问题的可能性 暨 Get Models返回JSON规整化的建议" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#40
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/40
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0937] Add QA scenarios for "lobechat 添加自定义API服务商后无法使用" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: websocket-and-streaming
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#38
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/38
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0938] Refactor implementation behind "Missing API key" to reduce complexity and isolate transformation boundaries.
- Priority: P3
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#37
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/37
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0939] Ensure rollout safety for "登录默认跳转浏览器 没有url" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#35
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/35
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0940] Standardize metadata and naming conventions touched by "Qwen3-Max-Preview可以使用了吗" across both repos.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#34
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/34
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0941] Follow up on "使用docker-compose.yml搭建失败" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: install-and-ops
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#32
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/32
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0942] Harden "Claude Code 报错 API Error: Cannot read properties of undefined （reading 'filter')" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: S
- Theme: error-handling-retries
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#25
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/25
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0943] Define non-subprocess integration path related to "QQ group search not found, can we open a TG group?" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: S
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#24
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/24
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0944] Convert "Codex CLI 能中转到Claude Code吗？" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#22
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/22
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0945] Add DX polish around "客户端/终端可以正常访问该代理，但无法输出回复" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#21
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/21
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0946] Expand docs and examples for "希望支持iflow" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#20
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/20
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0947] Add QA scenarios for "希望可以加入对responses的支持。" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#19
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/19
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0948] Refactor implementation behind "关于gpt5" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: S
- Theme: error-handling-retries
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#18
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/18
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0949] Ensure rollout safety for "v1beta接口报错Please use a valid role: user, model." via feature flags, staged defaults, and migration notes.
- Priority: P3
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#17
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/17
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0950] Port relevant thegent-managed flow implied by "gemini使用project_id登录，会无限要求跳转链接，使用配置更改auth_dir无效" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P1
- Effort: S
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#14
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/14
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0951] Follow up on "新认证生成的auth文件，使用的时候提示：400 API key not valid." by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#13
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/13
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0952] Create/refresh provider quickstart derived from "500就一直卡死了" including setup, auth, model select, and sanity-check commands.
- Priority: P2
- Effort: S
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#12
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/12
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0953] Operationalize "无法使用/v1/messages端口" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#11
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/11
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0954] Convert "可用正常接入new-api这种api站吗？" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: S
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#10
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/10
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0955] Add DX polish around "Unexpected API Response: The language model did not provide any assistant messages. This may indicate an issue with the API or the model's output." through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#9
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/9
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0956] Expand docs and examples for "cli有办法像别的gemini一样关闭安全审查吗？" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: S
- Theme: cli-ux-dx
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#7
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/7
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0957] Add process-compose/HMR refresh workflow tied to "如果一个项目需要指定ID认证，则指定后一定也会失败" so local config and runtime can be reloaded deterministically.
- Priority: P1
- Effort: S
- Theme: dev-runtime-refresh
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#6
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/6
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0958] Refactor implementation behind "指定project_id登录，无限跳转登陆页面" to reduce complexity and isolate transformation boundaries.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#5
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/5
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0959] Ensure rollout safety for "Error walking auth directory" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: S
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#4
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/4
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0960] Standardize metadata and naming conventions touched by "Login error.win11" across both repos.
- Priority: P1
- Effort: S
- Theme: oauth-and-authentication
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#3
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/3
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0961] Follow up on "偶尔会弹出无效API key提示，“400 API key not valid. Please pass a valid API key.”" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: S
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPI issue#2
- Source URL: https://github.com/router-for-me/CLIProxyAPI/issues/2
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0962] Harden "Normalize Codex schema handling" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P3
- Effort: M
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#259
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/259
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0963] Operationalize "fix: add default copilot claude model aliases for oauth routing" with observability, alerting thresholds, and runbook updates.
- Priority: P1
- Effort: M
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#256
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/256
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0964] Convert "feat(registry): add GPT-4o model variants for GitHub Copilot" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: M
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#255
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/255
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0965] Add DX polish around "fix(kiro): stop duplicated thinking on OpenAI and preserve Claude multi-turn thinking" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: M
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#252
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/252
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0966] Define non-subprocess integration path related to "feat(registry): add Gemini 3.1 Pro to GitHub Copilot provider" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P2
- Effort: M
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#250
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/250
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0967] Add QA scenarios for "v6.8.22" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: M
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#249
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/249
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0968] Refactor implementation behind "v6.8.21" to reduce complexity and isolate transformation boundaries.
- Priority: P2
- Effort: M
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#248
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/248
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0969] Create/refresh provider quickstart derived from "fix(cline): add grantType to token refresh and extension headers" including setup, auth, model select, and sanity-check commands.
- Priority: P3
- Effort: M
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#247
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/247
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0970] Standardize metadata and naming conventions touched by "feat: add Claude Sonnet 4.6 model support for Kiro provider" across both repos.
- Priority: P2
- Effort: M
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#244
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/244
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0971] Follow up on "feat(registry): add Claude Sonnet 4.6 model definitions" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: M
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#243
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/243
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0972] Harden "Improve Copilot provider based on ericc-ch/copilot-api comparison" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: M
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#242
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/242
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0973] Operationalize "feat(registry): add Sonnet 4.6 to GitHub Copilot provider" with observability, alerting thresholds, and runbook updates.
- Priority: P2
- Effort: M
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#240
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/240
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0974] Convert "feat(registry): add GPT-5.3 Codex to GitHub Copilot provider" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: M
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#239
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/239
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0975] Add DX polish around "Fix Copilot 0x model incorrectly consuming premium requests" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: M
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#238
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/238
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0976] Expand docs and examples for "v6.8.18" with copy-paste quickstart and troubleshooting section.
- Priority: P2
- Effort: M
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#237
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/237
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0977] Add QA scenarios for "fix: add proxy_ prefix handling for tool_reference content blocks" including stream/non-stream parity and edge-case payloads.
- Priority: P1
- Effort: M
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#236
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/236
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0978] Refactor implementation behind "fix(codex): handle function_call_arguments streaming for both spark and non-spark models" to reduce complexity and isolate transformation boundaries.
- Priority: P1
- Effort: M
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#235
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/235
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0979] Ensure rollout safety for "Add Kilo Code provider with dynamic model fetching" via feature flags, staged defaults, and migration notes.
- Priority: P1
- Effort: M
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#234
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/234
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0980] Standardize metadata and naming conventions touched by "Fix Copilot codex model Responses API translation for Claude Code" across both repos.
- Priority: P1
- Effort: M
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#233
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/233
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0981] Follow up on "feat(models): add Thinking support to GitHub Copilot models" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P1
- Effort: M
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#231
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/231
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0982] Harden "fix(copilot): forward Claude-format tools to Copilot Responses API" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P1
- Effort: M
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#230
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/230
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0983] Operationalize "fix: preserve explicitly deleted kiro aliases across config reload" with observability, alerting thresholds, and runbook updates.
- Priority: P1
- Effort: M
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#229
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/229
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0984] Convert "fix(antigravity): add warn-level logging to silent failure paths in FetchAntigravityModels" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P2
- Effort: M
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#228
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/228
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0985] Add DX polish around "v6.8.15" through improved command ergonomics and faster feedback loops.
- Priority: P2
- Effort: M
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#227
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/227
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0986] Create/refresh provider quickstart derived from "refactor(kiro): Kiro Web Search Logic & Executor Alignment" including setup, auth, model select, and sanity-check commands.
- Priority: P1
- Effort: M
- Theme: docs-quickstarts
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#226
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/226
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0987] Add QA scenarios for "v6.8.13" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: M
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#225
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/225
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0988] Port relevant thegent-managed flow implied by "fix(kiro): prepend placeholder user message when conversation starts with assistant role" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Priority: P1
- Effort: M
- Theme: go-cli-extraction
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#224
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/224
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0989] Define non-subprocess integration path related to "fix(kiro): prepend placeholder user message when conversation starts with assistant role" (Go bindings surface + HTTP fallback contract + version negotiation).
- Priority: P1
- Effort: M
- Theme: integration-api-bindings
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#223
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/223
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-0990] Standardize metadata and naming conventions touched by "fix(kiro): 修复之前提交的错误的application/cbor请求处理逻辑" across both repos.
- Priority: P2
- Effort: M
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#220
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/220
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

### [CPB-0991] Follow up on "fix: prevent merging assistant messages with tool_calls" by closing compatibility gaps and preventing regressions in adjacent providers.
- Priority: P2
- Effort: M
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#218
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/218
- Implementation note: Implement normalized parameter ingestion with strict backward compatibility and explicit telemetry counters.

### [CPB-0992] Harden "增加kiro新模型并根据其他提供商同模型配置Thinking" with clearer validation, safer defaults, and defensive fallbacks.
- Priority: P2
- Effort: M
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#216
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/216
- Implementation note: Add regression tests that fail before fix and pass after patch; include fixture updates for cross-provider mapping.

### [CPB-0993] Operationalize "fix(auth): strip model suffix in GitHub Copilot executor before upstream call" with observability, alerting thresholds, and runbook updates.
- Priority: P1
- Effort: M
- Theme: thinking-and-reasoning
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#214
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/214
- Implementation note: Improve user-facing error messages and add deterministic remediation text with command examples.

### [CPB-0994] Convert "fix(kiro): filter orphaned tool_results from compacted conversations" into a provider-agnostic pattern and codify in shared translation utilities.
- Priority: P1
- Effort: M
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#212
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/212
- Implementation note: Document behavior in provider quickstart and compatibility matrix with concrete request/response examples.

### [CPB-0995] Add DX polish around "fix(kiro): fully implement Kiro web search tool via MCP integration" through improved command ergonomics and faster feedback loops.
- Priority: P1
- Effort: M
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#211
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/211
- Implementation note: Refactor handler to isolate transformation logic from transport concerns and reduce side effects.

### [CPB-0996] Expand docs and examples for "feat(config): add default Kiro model aliases for standard Claude model names" with copy-paste quickstart and troubleshooting section.
- Priority: P1
- Effort: M
- Theme: provider-model-registry
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#209
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/209
- Implementation note: Introduce structured logs for input config, normalized config, and outbound payload diff (sensitive fields redacted).

### [CPB-0997] Add QA scenarios for "v6.8.9" including stream/non-stream parity and edge-case payloads.
- Priority: P2
- Effort: M
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#207
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/207
- Implementation note: Add config toggles for safe rollout and default them to preserve existing deployments.

### [CPB-0998] Refactor implementation behind "fix(translator): fix nullable type arrays breaking Gemini/Antigravity API" to reduce complexity and isolate transformation boundaries.
- Priority: P1
- Effort: M
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#205
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/205
- Implementation note: Benchmark latency and memory before/after; gate merge on no regression for p50/p95.

### [CPB-0999] Ensure rollout safety for "v6.8.7" via feature flags, staged defaults, and migration notes.
- Priority: P2
- Effort: M
- Theme: general-polish
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#204
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/204
- Implementation note: Add API contract tests covering malformed input, missing fields, and mixed legacy/new parameter names.

### [CPB-1000] Standardize metadata and naming conventions touched by "fix(copilot): prevent premium request count inflation for Claude models" across both repos.
- Priority: P2
- Effort: M
- Theme: responses-and-chat-compat
- Status: proposed
- Source: router-for-me/CLIProxyAPIPlus pr#203
- Source URL: https://github.com/router-for-me/CLIProxyAPIPlus/pull/203
- Implementation note: Create migration note and changelog entry with explicit compatibility guarantees and caveats.

