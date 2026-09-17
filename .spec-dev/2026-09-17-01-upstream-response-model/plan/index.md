# 上游响应模型 实施计划

> **执行方式**：使用 spec-dev 的 executing-plans skill 逐任务执行本计划；无该 skill 的环境直接从任务 0 起按序执行至最终任务。任务状态由 `plan/progress.yaml` 跟踪（唯一状态源；任务文件步骤用「**步骤 N:**」标题式、不含复选框）；脱离项目携带时连同特性目录（含 spec）整体带走。
>
> **偏差处理**：执行中发现计划与现实不符——小偏差（路径笔误、明显遗漏但意图清楚）就地修正并在提交信息中注明；接口、数据结构等契约级偏差停下向计划作者确认，不猜着改。

**目标**：在 force-mapping 之前观测上游响应自报模型，经用量队列送到 Keeper，并在请求事件模型列于不一致时显示第三行。

**Spec**：`.spec-dev/2026-09-17-01-upstream-response-model/spec/upstream-response-model-design.md`

**架构**：`usage` 包放观察器与尝试上下文；`helps.ObserveUpstreamResponseBytes` 按抽取族解析原文；`Append*` / `Emit*` 在 request-log 门控前观测；`UsageReporter` 发布时抄到 `Record.UpstreamResponseModel`；redis 入队 `upstream_response_model`；Keeper 入库、列表、导出与两处表格展示。

**技术栈**：Go 1.26+（CLIProxyAPI）、Go + React/Vitest（cpa-usage-keeper）、既有 gjson、无新依赖。

**关联 skill**：
- executing-plans：执行本计划时；定义在 spec-dev `skills/executing-plans/SKILL.md`
- test-driven-development：T01–T08 行为票；定义在 spec-dev `skills/test-driven-development/SKILL.md`
- using-git-worktrees：T00 / 最终任务隔离；定义在 spec-dev `skills/using-git-worktrees/SKILL.md`
- acceptance-qa：T09 验收票；定义在 spec-dev `skills/acceptance-qa/SKILL.md`
- test-strategy：Lane 继承（本计划 TDD 为 fast，验收为 manual）；定义在 spec-dev `skills/test-strategy/SKILL.md`

**设计原则**：本计划遵循 spec-dev 设计原则（不留向后兼容垫片 / 最简实现 / 分层构建 / 不以未完成复杂性换可工作产品 / 模块化 / 优先成熟库 / 优先已有依赖 / 长期架构决策）；任务与代码不得违反，冲突时停下向计划作者确认。模块判据见 `skills/writing-plans/references/design-principles.md`。

## 全局约束

- 字段名固定为 `upstream_response_model` / `UpstreamResponseModel`，不得改称 `response_model`
- 观测不得修改出站响应字节，不得调用 force-mapping rewriter
- 观测必须在 `requestLogCaptureEnabled` 返回 false 时仍执行
- mismatch 只比较发往上游 `model`，忽略大小写，在 Keeper 展示层计算
- 第三行文案键 `usage_stats.upstream_response_model`：en `Upstream response`，zh `上游响应`，zh-TW `上游響應`
- 模型名 trim 后按 rune 截到 200；空值入队省略 JSON 键
- 不改 plugin API、计费、筛选、分析聚合、徽章、CLIProxyAPIHome
- CLIProxyAPI 改动后必须 `gofmt -w` 并 `go build -o test-output ./cmd/server && rm test-output`
- Keeper 改动在 `/Users/maverick/cpa-usage-keeper`，单独 worktree / PR
- 先合 CPA 入队字段，再合 Keeper

## 相关测试范围

无 affected 工具。范围 = 本特性将新增/修改的测试文件 + 直接 import 被改源文件的既有测试（一层）：

CLIProxyAPI（cwd `/Users/maverick/CLIProxyAPI`）：
- `go test ./sdk/cliproxy/usage/`
- `go test ./internal/runtime/executor/helps/`
- `go test ./internal/redisqueue/`

cpa-usage-keeper（cwd `/Users/maverick/cpa-usage-keeper`）：
- `go test ./internal/service/ ./internal/service/test/ ./internal/api/test/ ./internal/repository/ ./internal/repository/migration/`
- `pnpm --dir web exec vitest run src/components/usage/test/RequestEventsDetailsCardModelAlias.test.tsx src/components/usage/credentials/test/CredentialRequestEventsList.test.tsx src/i18n/index.test.ts`

静态快检：CLIProxyAPI `gofmt -l` 本票 Go 文件 + `go build -o test-output ./cmd/server && rm test-output`。Keeper 无强制 typecheck；前端改完跑上列 vitest。

## 体量说明

任务文件总和会略长于净代码增量：跨两个仓库，且每个 Scenario 都要带可运行断言。

## 任务导航

| 任务 | 依赖 | 消费接口 | 产出接口 |
|------|------|----------|----------|
| T00 | — | — | 隔离工作区就绪（CPA + Keeper） |
| T01 | T00 | — | `BeginUpstreamResponseModelObservation(ctx) context.Context`；`ObserveUpstreamResponseModel(ctx, model string, terminal bool)`；`RememberUpstreamResponseEventType(ctx, eventType string)`；`LastUpstreamResponseEventType(ctx) string`；`UpstreamResponseModelFromContext(ctx) string`；`BindUpstreamResponseModelFamily(ctx, family ExtractionFamily)`；`MapExecutorToExtractionFamily(provider, executorType string) ExtractionFamily`；`Record.UpstreamResponseModel string` |
| T02 | T01 | T01 全部产出 | `ObserveUpstreamResponseBytes(ctx context.Context, payload []byte)` |
| T03 | T01-T02 | T01 的 Begin / Bind / FromContext / Map / Record 字段；T02 的 `ObserveUpstreamResponseBytes` | `newUpstreamAttemptContext` 含 Begin；`NewUsageReporter` 绑定抽取族；`buildRecord`/`publishRecord` 填写 `UpstreamResponseModel` |
| T04 | T02 | T02 的 `ObserveUpstreamResponseBytes` | `AppendAPIResponseChunk` / `AppendAPIWebsocketResponse` / `EmitWebSocketResponseEvent` 在日志门控前观测 |
| T05 | T03 | `Record.UpstreamResponseModel` | redis JSON `upstream_response_model`（非空写出，空则省略） |
| T06 | T00 | 队列键 `upstream_response_model` | Keeper `UsageEvent.UpstreamResponseModel`；`DecodeRedisUsageMessage` 写入该字段 |
| T07 | T06 | T06 实体字段 | 列表 API 与 CSV/JSON 导出含 `upstream_response_model` |
| T08 | T07 | T07 JSON 字段 | 两处模型列 mismatch 第三行；i18n 键 `usage_stats.upstream_response_model` |
| T09 | T01-T08 | 端到端字段契约 | 验收：CPA 入队后 Keeper 两处表格出现第三行 |
| T10 | T09 | T00 工作区记录 | 合并、清理、`sync_commit` 锚定 |
