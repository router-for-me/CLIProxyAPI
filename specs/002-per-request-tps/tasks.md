# Tasks: 每请求 TPS（Tokens Per Second）

Feature: 每请求 TPS（仅结构化日志，不对外暴露）
Branch: `002-per-request-tps`

## Phase 1 — Setup

- [X] T001 Ensure single time source utilities exist or add helper in `internal/runtime/executor/logging_helpers.go`
- [X] T002 Add rounding util (two decimals) for TPS in `internal/runtime/executor/logging_helpers.go`
- [X] T003 Wire request timing capture in middleware if missing in `internal/api/middleware/request_logging.go`

## Phase 2 — Foundational

- [X] T004 Define log schema alignment with `contracts/log_per_request_tps.schema.json` (doc only)
- [X] T005 Add log field constants: `tps_completion`, `tps_total` in `internal/runtime/executor/logging_helpers.go`
- [X] T006 Add guard rails: avoid divide-by-zero and negative durations in `internal/runtime/executor/logging_helpers.go`

## Phase 3 — [US1] 在结构化日志中查看每请求 TPS (P1)

- [X] T007 [US1] Capture non-stream timing window and tokens in `internal/runtime/executor/*executor.go`
- [X] T008 [US1] Compute completion TPS for non-stream path in `internal/runtime/executor/logging_helpers.go`
- [X] T009 [US1] Emit `tps_completion` to structured log in `internal/runtime/executor/logging_helpers.go`
- [X] T010 [US1] Verify rounding to two decimals in `internal/runtime/executor/logging_helpers.go`
- [ ] T011 [P] [US1] Manual integration check per quickstart in `specs/002-per-request-tps/quickstart.md`

## Phase 4 — [US2] 在结构化日志中记录 TPS (P2)

- [X] T012 [US2] Capture streaming first/last token timestamps in `internal/runtime/executor/*executor.go`
- [X] T013 [US2] Compute completion TPS for streaming path in `internal/runtime/executor/logging_helpers.go`
- [X] T014 [US2] Ensure `tps_total` logged when total window available in `internal/runtime/executor/logging_helpers.go`
- [X] T015 [P] [US2] Validate log entries match JSON Schema in `specs/002-per-request-tps/contracts/log_per_request_tps.schema.json`

## Phase 5 — [US3] 以 TPS 为条件进行外部分析/告警 (P3)

- [X] T016 [US3] Provide sample log export script in `examples/` (doc only, no code change)
- [X] T017 [P] [US3] Provide dashboard/alert examples referencing `tps_completion` (doc in `quickstart.md`)

## Final Phase — Polish & Cross-Cutting

### Post-implementation Notes
- 已新增独立开关 `tps-log`（仅控制 TPS 输出）；`request-log` 为请求日志总开关。
- TPS 输出复用全局日志模块（LogFormatter 与输出通道），不改变 `debug: true` 的原有样式与去向。
- 管理接口新增：`/v0/management/tps-log`（GET/PUT/PATCH）。
- Gin 上下文注入 `config`，TPS 输出前读取 `cfg.TPSLog` 门控。

- [X] T018 Add edge case handling for zero tokens and failures in `internal/runtime/executor/logging_helpers.go`
- [X] T019 Ensure no PII in logs (audit sampling) in `internal/runtime/executor/logging_helpers.go`
- [X] T020 Update CHANGELOG/Docs about new log fields in `docs/` (doc only)

## Dependencies (Story Order)

- US1 → US2 → US3

## Parallel Opportunities

- T011 与 T015、T017 可并行（文档/校验）；T007/T012 可并行于不同执行器文件。

## Implementation Strategy

- 先完成 US1（MVP）：计算并记录 `tps_completion`（非流式路径），验证两位小数与误差阈值。
- 扩展至 US2：流式路径与 `tps_total`（当可用）。
- US3 产出分析与告警示例（仅文档）。

## Phase 6 — [US4] 管理端聚合 TPS 查询（新增）

- [X] T021 [US4] 引入内存 TPS 聚合器（avg/median），自服务器启动起累计样本（`internal/usage/logger_plugin.go`）
- [X] T022 [US4] 新增管理端点 `GET /v0/management/tps` 返回 `{since, completion{count,avg,median}, total{count,avg,median}}`
- [X] T023 [US4] 支持查询参数 `window=<GoDuration>`（如 `5m`, `1h`），按窗口返回聚合（`usage.GetTPSAggregatesWindow`）
- [X] T024 [US4] 自动清理策略：
  - 后台定时清理：每 1m 清理早于 24h 的样本，并保证至少保留最近 10 个样本
  - 写时惰性清理：每新增 ~1000 个样本触发一次轻量清理
  - 仅清理 TPS 样本与对应累加和，不影响请求日志与使用统计
- [X] T025 [US4] 记录时机统一为“每请求一次，在请求完成后”：
  - 在 `updateAggregatedResponse` 中仅计算并写入上下文 `API_TPS_COMPLETION`/`API_TPS_TOTAL`
  - 由全局中间件在 `c.Next()` 返回后统一调用 `usage.RecordTPSSample(...)`（`internal/api/server.go`）
- [ ] T026 [US4] 文档补充：在 `quickstart.md` 增加 `/v0/management/tps` 示例与 `window` 说明（doc only）
