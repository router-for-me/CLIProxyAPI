# Next 50 Work Items (CP2K)

- Source: `docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
- Selection rule: `status=proposed` and `implementation_ready=yes`
- Batch size: 50

| # | ID | Priority | Effort | Wave | Theme | Title |
|---|---|---|---|---|---|---|
| 1 | CP2K-0011 | P1 | S | wave-1 | general-polish | Follow up "kiro账号被封" by closing compatibility gaps and locking in regression coverage. |
| 2 | CP2K-0014 | P1 | S | wave-1 | thinking-and-reasoning | Generalize "Add support for proxying models from kilocode CLI" into provider-agnostic translation/utilities to reduce duplicate logic. |
| 3 | CP2K-0015 | P1 | S | wave-1 | responses-and-chat-compat | Improve CLI UX around "[Bug] Kiro 与 Ampcode 的 Bash 工具参数不兼容" with clearer commands, flags, and immediate validation feedback. |
| 4 | CP2K-0016 | P1 | S | wave-1 | provider-model-registry | Extend docs for "[Feature Request] Add default oauth-model-alias for Kiro channel (like Antigravity)" with quickstart snippets and troubleshooting decision trees. |
| 5 | CP2K-0017 | P1 | S | wave-1 | docs-quickstarts | Create or refresh provider quickstart derived from "bug: Nullable type arrays in tool schemas cause 400 error on Antigravity/Droid Factory" with setup/auth/model/sanity-check flow. |
| 6 | CP2K-0018 | P1 | S | wave-1 | thinking-and-reasoning | Refactor internals touched by "GitHub Copilot CLI 使用方法" to reduce coupling and improve maintainability. |
| 7 | CP2K-0021 | P1 | S | wave-1 | provider-model-registry | Follow up "Cursor CLI \ Auth Support" by closing compatibility gaps and locking in regression coverage. |
| 8 | CP2K-0022 | P1 | S | wave-1 | oauth-and-authentication | Harden "Why no opus 4.6 on github copilot auth" with stricter validation, safer defaults, and explicit fallback semantics. |
| 9 | CP2K-0025 | P1 | S | wave-1 | thinking-and-reasoning | Improve CLI UX around "Claude thought_signature forwarded to Gemini causes Base64 decode error" with clearer commands, flags, and immediate validation feedback. |
| 10 | CP2K-0030 | P1 | S | wave-1 | responses-and-chat-compat | Standardize naming/metadata affected by "fix(kiro): handle empty content in messages to prevent Bad Request errors" across both repos and docs. |
| 11 | CP2K-0031 | P1 | S | wave-1 | oauth-and-authentication | Follow up "在配置文件中支持为所有 OAuth 渠道自定义上游 URL" by closing compatibility gaps and locking in regression coverage. |
| 12 | CP2K-0034 | P1 | S | wave-1 | docs-quickstarts | Create or refresh provider quickstart derived from "请求docker部署支持arm架构的机器！感谢。" with setup/auth/model/sanity-check flow. |
| 13 | CP2K-0036 | P1 | S | wave-1 | responses-and-chat-compat | Extend docs for "[Bug]进一步完善 openai兼容模式对 claude 模型的支持（完善 协议格式转换 ）" with quickstart snippets and troubleshooting decision trees. |
| 14 | CP2K-0037 | P1 | S | wave-1 | responses-and-chat-compat | Add robust stream/non-stream parity tests for "完善 claude openai兼容渠道的格式转换" across supported providers. |
| 15 | CP2K-0039 | P1 | S | wave-1 | responses-and-chat-compat | Prepare safe rollout for "kiro idc登录需要手动刷新状态" via flags, migration docs, and backward-compat tests. |
| 16 | CP2K-0040 | P1 | S | wave-1 | thinking-and-reasoning | Standardize naming/metadata affected by "[Bug Fix] 修复 Kiro 的Claude模型非流式请求 output_tokens 为 0 导致的用量统计缺失" across both repos and docs. |
| 17 | CP2K-0045 | P1 | S | wave-1 | responses-and-chat-compat | Improve CLI UX around "Error 403" with clearer commands, flags, and immediate validation feedback. |
| 18 | CP2K-0047 | P1 | S | wave-1 | thinking-and-reasoning | Add robust stream/non-stream parity tests for "enterprise 账号 Kiro不是很稳定，很容易就403不可用了" across supported providers. |
| 19 | CP2K-0048 | P1 | S | wave-1 | oauth-and-authentication | Refactor internals touched by "-kiro-aws-login 登录后一直封号" to reduce coupling and improve maintainability. |
| 20 | CP2K-0050 | P1 | S | wave-1 | oauth-and-authentication | Standardize naming/metadata affected by "Antigravity authentication failed" across both repos and docs. |
| 21 | CP2K-0051 | P1 | S | wave-1 | docs-quickstarts | Create or refresh provider quickstart derived from "大佬，什么时候搞个多账号管理呀" with setup/auth/model/sanity-check flow. |
| 22 | CP2K-0052 | P1 | S | wave-1 | oauth-and-authentication | Harden "日志中,一直打印auth file changed (WRITE)" with stricter validation, safer defaults, and explicit fallback semantics. |
| 23 | CP2K-0053 | P1 | S | wave-1 | oauth-and-authentication | Operationalize "登录incognito参数无效" with observability, runbook updates, and deployment safeguards. |
| 24 | CP2K-0054 | P1 | S | wave-1 | thinking-and-reasoning | Generalize "OpenAI-compat provider hardcodes /v1/models (breaks Z.ai v4: /api/coding/paas/v4/models)" into provider-agnostic translation/utilities to reduce duplicate logic. |
| 25 | CP2K-0056 | P1 | S | wave-1 | responses-and-chat-compat | Extend docs for "Kiro currently has no authentication available" with quickstart snippets and troubleshooting decision trees. |
| 26 | CP2K-0059 | P1 | S | wave-1 | thinking-and-reasoning | Prepare safe rollout for "Bug: Kiro/BuilderId tokens can collide when email/profile_arn are empty; refresh token lifecycle not handled" via flags, migration docs, and backward-compat tests. |
| 27 | CP2K-0060 | P1 | S | wave-1 | responses-and-chat-compat | Standardize naming/metadata affected by "[Bug] Amazon Q endpoint returns HTTP 400 ValidationException (wrong CLI/KIRO_CLI origin)" across both repos and docs. |
| 28 | CP2K-0062 | P1 | S | wave-1 | responses-and-chat-compat | Harden "Cursor Issue" with stricter validation, safer defaults, and explicit fallback semantics. |
| 29 | CP2K-0063 | P1 | S | wave-1 | thinking-and-reasoning | Operationalize "Feature request: Configurable HTTP request timeout for Extended Thinking models" with observability, runbook updates, and deployment safeguards. |
| 30 | CP2K-0064 | P1 | S | wave-1 | websocket-and-streaming | Generalize "kiro请求偶尔报错event stream fatal" into provider-agnostic translation/utilities to reduce duplicate logic. |
| 31 | CP2K-0066 | P1 | S | wave-1 | oauth-and-authentication | Extend docs for "[建议] 技术大佬考虑可以有机会新增一堆逆向平台" with quickstart snippets and troubleshooting decision trees. |
| 32 | CP2K-0068 | P1 | S | wave-1 | docs-quickstarts | Create or refresh provider quickstart derived from "kiro请求的数据好像一大就会出错,导致cc写入文件失败" with setup/auth/model/sanity-check flow. |
| 33 | CP2K-0073 | P1 | S | wave-1 | oauth-and-authentication | Operationalize "How to use KIRO with IAM?" with observability, runbook updates, and deployment safeguards. |
| 34 | CP2K-0074 | P1 | S | wave-1 | provider-model-registry | Generalize "[Bug] Models from Codex (openai) are not accessible when Copilot is added" into provider-agnostic translation/utilities to reduce duplicate logic. |
| 35 | CP2K-0075 | P1 | S | wave-1 | responses-and-chat-compat | Improve CLI UX around "model gpt-5.1-codex-mini is not accessible via the /chat/completions endpoint" with clearer commands, flags, and immediate validation feedback. |
| 36 | CP2K-0079 | P1 | S | wave-1 | thinking-and-reasoning | Prepare safe rollout for "lack of thinking signature in kiro's non-stream response cause incompatibility with some ai clients (specifically cherry studio)" via flags, migration docs, and backward-compat tests. |
| 37 | CP2K-0080 | P1 | S | wave-1 | oauth-and-authentication | Standardize naming/metadata affected by "I did not find the Kiro entry in the Web UI" across both repos and docs. |
| 38 | CP2K-0081 | P1 | S | wave-1 | thinking-and-reasoning | Follow up "Kiro (AWS CodeWhisperer) - Stream error, status: 400" by closing compatibility gaps and locking in regression coverage. |
| 39 | CP2K-0251 | P1 | S | wave-1 | oauth-and-authentication | Follow up "Why a separate repo?" by closing compatibility gaps and locking in regression coverage. |
| 40 | CP2K-0252 | P1 | S | wave-1 | oauth-and-authentication | Harden "How do I perform GitHub OAuth authentication? I can't find the entrance." with stricter validation, safer defaults, and explicit fallback semantics. |
| 41 | CP2K-0255 | P1 | S | wave-1 | docs-quickstarts | Create or refresh provider quickstart derived from "feat: support image content in tool result messages (OpenAI ↔ Claude translation)" with setup/auth/model/sanity-check flow. |
| 42 | CP2K-0257 | P1 | S | wave-1 | responses-and-chat-compat | Add robust stream/non-stream parity tests for "Need maintainer-handled codex translator compatibility for Responses compaction fields" across supported providers. |
| 43 | CP2K-0258 | P1 | S | wave-1 | responses-and-chat-compat | Refactor internals touched by "codex: usage_limit_reached (429) should honor resets_at/resets_in_seconds as next_retry_after" to reduce coupling and improve maintainability. |
| 44 | CP2K-0260 | P1 | S | wave-1 | thinking-and-reasoning | Standardize naming/metadata affected by "fix(claude): token exchange blocked by Cloudflare managed challenge on console.anthropic.com" across both repos and docs. |
| 45 | CP2K-0263 | P1 | S | wave-1 | responses-and-chat-compat | Operationalize "All credentials for model claude-sonnet-4-6 are cooling down" with observability, runbook updates, and deployment safeguards. |
| 46 | CP2K-0265 | P1 | S | wave-1 | thinking-and-reasoning | Improve CLI UX around "Claude Sonnet 4.5 models are deprecated - please remove from panel" with clearer commands, flags, and immediate validation feedback. |
| 47 | CP2K-0267 | P1 | S | wave-1 | thinking-and-reasoning | Add robust stream/non-stream parity tests for "codex 返回 Unsupported parameter: response_format" across supported providers. |
| 48 | CP2K-0268 | P1 | S | wave-1 | thinking-and-reasoning | Refactor internals touched by "Bug: Invalid JSON payload when tool_result has no content field (antigravity translator)" to reduce coupling and improve maintainability. |
| 49 | CP2K-0272 | P1 | S | wave-1 | docs-quickstarts | Create or refresh provider quickstart derived from "是否支持微软账号的反代？" with setup/auth/model/sanity-check flow. |
| 50 | CP2K-0274 | P1 | S | wave-1 | thinking-and-reasoning | Generalize "Claude Sonnet 4.5 is no longer available. Please switch to Claude Sonnet 4.6." into provider-agnostic translation/utilities to reduce duplicate logic. |

## Execution Notes
- This is a queued handoff batch for implementation lanes.
- Items remain unimplemented until code + tests + quality checks are merged.
