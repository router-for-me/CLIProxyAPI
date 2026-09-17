# Acceptance Report: upstream-response-model

> **Language / 语言**: 简体中文 / table headers in English
> Time: 2026-09-17 17:30 +08 | Triggered by: executing-plans wrap-up (T09) | Tier: standard
> Spec: `.spec-dev/2026-09-17-01-upstream-response-model/spec/upstream-response-model-design.md` | Evidence dir: `.spec-dev/2026-09-17-01-upstream-response-model/acceptance/`
> Spec status: active

## Overview

| Dimension | Execution | Pass | Fail | Warn | Unverified | Notes |
|-----------|-----------|------|------|------|------------|-------|
| unit | D | 2 | 0 | 0 | 0 | CPA 三包 + 卡片/i18n/凭证 upstream 单测 |
| integration | D | 1 | 0 | 0 | 0 | Keeper T09 Go 回归；T06 顺序表已补 |
| e2e | D + A | 4 | 0 | 0 | 0 | 入队 JSON、两处第三行、CSV 列 |
| visual | A | — | — | — | 1 | 截图字体栅格异常，不构成视觉结论 |
| a11y | — | — | — | — | 1 | 矩阵未要求，已裁剪 |
| perf-web | — | — | — | — | 1 | 矩阵未要求，已裁剪 |
| perf-api | — | — | — | — | 1 | k6 未装且矩阵未要求 |

## Requirement Coverage (when a spec exists)

| Matrix row (Scenario / check item) | Dimension | Status | Evidence |
|------------------------------------|-----------|--------|----------|
| CPA 入队后 Keeper 能展示 mismatch 行 | e2e | pass | `logs/usage-queue.json`；`logs/iso-events.json`；`logs/iso-request-events-model-cells.json`；`logs/iso-credential-model-cells.json`；`logs/iso-export.csv` |
| 队列写出观测值 | e2e | pass | 入队 `upstream_response_model=upstream-reported-model` |
| Keeper 持久化 | e2e | pass | 列表事件 id=1 字段值与入队一致 |
| 不一致才显示第三行 | e2e | pass | 请求事件 `上游响应: upstream-reported-model`；凭证 `上游响应 upstream-reported-model`；无徽章 |
| 一致或空值不显示 | e2e | pass | match 行仅模型+别名，无「上游响应」 |
| 导出始终带列 | e2e | pass | CSV 表头 `model,model_alias,upstream_response_model,reasoning_effort` |

## Requirement Reconciliation (delivery delta; filled by executing-plans wrap-up, remove when triggered standalone)

All 8 requirements DELIVERED — see Requirement Coverage above.

## Key Findings (by severity)

1. **[P3] 用户 docker compose 是官方 latest，不能验收本特性** (e2e) — `8317/8388` 跑 `eceasy/cli-proxy-api:latest` 与 `ghcr.io/willxup/cpa-usage-keeper:latest`：0 凭证、`usage-statistics-enabled: false`、CSV 无 `upstream_response_model`。未重启该栈。联调改用隔离 worktree 栈 `18080/18317/18388`。
2. **[P3] T06 漏更迁移顺序期望列表** (integration) — `TestOrderedMigrationsPreservesExecutionOrder` 未包含 `20260917_add_usage_event_upstream_response_model`；已在 Keeper worktree 补上，复跑转绿。
3. **[P3] 凭证列表完整 vitest 仍有 4 条虚拟滚动失败** (unit) — T08 已记 happy-dom 环境偏差；`-t upstream` 的 2 条新测绿。

## Diagnosis Details

Finding #2：若根因是「orderedMigrations 末尾新增版本但 want 列表未同步」，补一行后该测试应过。已验证：`go test ./internal/repository/migration/ -run TestOrderedMigrationsPreservesExecutionOrder` 退出码 0。

Finding #3：若根因是第三行改了默认行高，默认行（无上游响应）高度不应变。新测只覆盖 mismatch/隐藏且已绿，故仍归环境偏差，不回 T08 改生产代码。

## Evidence Index

- Contract JSON: `acceptance/check-items.json`
- 入队原文: `acceptance/logs/usage-queue.json`
- 隔离事件/CSV: `acceptance/logs/iso-events.json`、`acceptance/logs/iso-export.csv`
- 模型单元格: `acceptance/logs/iso-request-events-model-cells.json`、`acceptance/logs/iso-credential-model-cells.json`
- compose latest CSV 表头: `acceptance/logs/compose-latest-export-header.txt`（`model_alias` 后直接 `reasoning_effort`）
- 截图: `acceptance/screenshots/compose-request-events-empty.png`、`iso-request-events.png`、`iso-credential-request-events.png`
- 回归日志: `acceptance/logs/cpa-go-test.txt`、`keeper-go-test-2.txt`、`keeper-vitest-mismatch.txt`

联调栈（验收后仍在本机，未动用户 compose）：

- mock OpenAI `127.0.0.1:18080`
- worktree CPA `127.0.0.1:18317`（`--config acceptance/t09-cpa-config.yaml --local-model`）
- worktree Keeper `127.0.0.1:18388`（密码 `12345678`）

## coverage_note

矩阵只有一行 e2e，本报告补了 T09 规定的 CPA/Keeper 回归。visual/a11y/perf 已裁剪。用户 compose 仅作 latest 基线，不计入本特性失败。standard 档未派 pass 项独立证据审计。
