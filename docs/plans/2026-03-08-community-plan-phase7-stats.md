# Phase 7: 统计分析

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 实现使用数据采集、聚合计算、数据导出功能。

**Architecture:** `internal/community/stats/` 模块。与现有 `internal/usage/` 模块通过插件机制集成。

**Tech Stack:** Go 1.26

**Depends on:** Phase 1, 2, 3

---

### Task 1: 实现请求日志采集

**Files:**
- Create: `internal/community/stats/collector.go`

**Step 1:** 编写 `Collector` — 实现 `coreusage.Plugin` 接口，将使用记录写入数据库。
**Step 2:** 注册为 usage 插件。
**Step 3:** Commit

---

### Task 2: 实现聚合统计

**Files:**
- Create: `internal/community/stats/aggregator.go`
- Test: `internal/community/stats/aggregator_test.go`

**Step 1:** 编写 `Aggregator` — 全局统计、用户统计、模型维度统计。
**Step 2:** 实现 `GetGlobalStats()` 和 `GetUserStats(userID)` 方法。
**Step 3:** 测试聚合计算。
**Step 4:** Commit

---

### Task 3: 实现数据导出

**Files:**
- Create: `internal/community/stats/export.go`

**Step 1:** 实现 CSV 和 JSON 格式导出。
**Step 2:** 编写 Handler 暴露 `/api/v1/admin/stats/export` 端点。
**Step 3:** Commit

---

### Task 4: 统计 API Handler

**Files:**
- Create: `internal/community/stats/handler.go`

**Step 1:** 编写管理员和用户端统计 Handler。
**Step 2:** 注册路由。
**Step 3:** Commit
