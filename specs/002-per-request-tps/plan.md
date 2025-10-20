# Implementation Plan: 每请求 TPS（Tokens Per Second）

**Branch**: `002-per-request-tps` | **Date**: 2025-10-20 | **Spec**: specs/002-per-request-tps/spec.md
**Input**: Feature specification from `/specs/002-per-request-tps/spec.md`

**Note**: This plan follows the `/speckit.plan` workflow and aligns with the Constitution gates.

## Summary

基于规范，实现“每请求 TPS”计算并仅在结构化日志中记录：
- 计算 completion TPS（必有）与 total TPS（可用则记录），统一两位小数、四舍五入。
- 时间窗：非流式（请求开始→完成）、流式（首个输出→最后输出）。
- 不扩展响应结构，不对外暴露字段；仅日志可见，满足隐私与向后兼容。
- 开销目标：≤ 1% 或 ≤ 5ms（取更严格者）。

### Changes After Implementation
- 新增独立开关 `tps-log` 用于单独控制 TPS 事件；`request-log` 保持为请求级日志总开关。
- 复用全局日志模块（LogFormatter 与输出通道）；`debug: true` 的既有输出样式与去向不变。
- 在 Gin 上下文注入 `config` 以便门控读取，管理端新增 `/v0/management/tps-log`（GET/PUT/PATCH）。

### Changes After Enhancement (US4)
- 新增管理端查询端点 `/v0/management/tps`：
  - 返回：`since`（近似服务器启动时间）、`completion{count,avg,median}`、`total{count,avg,median}`
  - 支持查询参数 `window=<GoDuration>`（如 `30s`/`5m`/`1h`），在窗口内进行聚合
  - 统计值保留两位小数
- TPS 样本记录策略：
  - 在请求执行过程中计算 TPS 并写入 `API_TPS_COMPLETION`/`API_TPS_TOTAL` 至 Gin 上下文
  - 在请求完成（`c.Next()` 返回后）的全局中间件中统一记录一次样本（流式与非流式一致，每请求仅一次）
- 自动清理策略（内存受控）：
  - 后台定时清理：每 1 分钟清理早于 24 小时的样本，同时确保至少保留最近 10 个样本
  - 写时惰性清理：每新增约 1000 个样本触发一次轻量清理
  - 仅清理 TPS 样本及其累加和，不影响请求日志与使用统计聚合

## Technical Context

### Changes After Implementation (supplement)
- Logger formatter 保持为 LogFormatter 而非 JSON，TPS 事件通过 WithFields 输出，随全局 formatter 渲染。
- `logging-to-file` 仍通过 ConfigureLogOutput 控制输出到 `logs/main.log`；未启用时输出 stdout。

**Language/Version**: Go 1.24.0
**Primary Dependencies**: Gin（HTTP，github.com/gin-gonic/gin），Logrus（结构化日志，github.com/sirupsen/logrus），Lumberjack（日志滚动），pgx/v5（不直接涉及本特性），MinIO SDK（不涉及本特性）
**Storage**: N/A（本特性不持久化，仅输出结构化日志）
**Testing**: Go 原生 testing（`go test`），优先验收/集成测试覆盖日志字段与数值正确性
**Target Platform**: Linux server（容器化可选，现有 Dockerfile）
**Project Type**: 单体 Go 模块（`internal/` + `cmd/`）
**Performance Goals**: 额外延迟 ≤ 1% 或 ≤ 5ms；日志覆盖率 100%；相对误差 ≤ 5%
**Constraints**: 不记录个人敏感信息；仅记录数值与时间窗；命名稳定（`tps_completion`, `tps_total`）；统一时间源
**Scale/Scope**: 预期 RPS 与并发未强约束，本特性设计与吞吐无耦合；基准测试以现有集成环境流量为准

## Constitution Check

- 规格优先：规范明确用户故事、验收、成功标准与边界，技术无关（通过）。
- 契约与测试驱动：无对外接口变更；为日志产物提供契约（JSON Schema）并以集成测试校验（通过）。
- 隐私优先：不记录用户内容，仅输出数值与窗口信息（通过）。
- 简单稳定与向后兼容：不修改响应结构，对外零破坏（通过）。
- 可观测与可运维：结构化日志、字段命名稳定、可被外部平台消费（通过）。

Re-check after Phase 1：仍应满足上述 Gate（预期通过）。

## Project Structure

### Documentation (this feature)
```
specs/002-per-request-tps/
├── plan.md              # 本计划（当前文件）
├── research.md          # Phase 0 决策与备选方案
├── data-model.md        # Phase 1 数据模型（日志记录结构）
├── quickstart.md        # Phase 1 快速开始与验证
└── contracts/
    ├── README.md        # 契约说明（无外部API变更）
    └── log_per_request_tps.schema.json  # 日志记录的 JSON Schema
```

### Source Code (repository root)
```
cmd/
└── server/
    └── main.go

internal/
├── api/
│   ├── server.go
│   └── middleware/
│       └── request_logging.go      # 请求日志与上下文
├── runtime/
│   └── executor/
│       ├── logging_helpers.go      # 记录/辅助（预期改动点）
│       └── *executor.go            # 各上游执行器（产生 tokens 的路径）
├── logging/
│   └── *                           # 统一日志封装（若存在）
└── usage/                          # 用量统计（若复用计数来源）
```

**Structure Decision**: 维持单体结构；在 `internal/runtime/executor/` 补充或复用日志辅助，按需要在 `internal/api/middleware/request_logging.go` 读取/注入计时上下文。避免新增无关模块。

## Complexity Tracking

（无）
