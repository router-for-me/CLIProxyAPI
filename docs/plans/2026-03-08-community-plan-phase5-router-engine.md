# Phase 5: 新轮询调度引擎

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 重写轮询引擎，支持权重、健康感知、池隔离的高性能异步调度器，替换现有 round-robin / fill-first。

**Architecture:** `internal/router/` 模块。实现与现有 `sdk/cliproxy/auth/selector.go` 的 `Selector` 接口兼容，可在 `sdk/cliproxy/builder.go` 中无缝替换。

**Tech Stack:** Go 1.26 / sync/atomic / goroutine 异步健康检查

**Depends on:** Phase 1, Phase 3

---

### Task 1: 定义调度策略接口

**Files:**
- Create: `internal/router/strategy.go`

**Step 1:** 定义 `Strategy` 接口和 `RequestContext` struct。
**Step 2:** 实现四种策略：WeightedRoundRobin / LeastLoad / FillFirst / Random。
**Step 3:** 测试每种策略的选择逻辑。
**Step 4:** Commit

---

### Task 2: 实现调度器核心

**Files:**
- Create: `internal/router/scheduler.go`
- Test: `internal/router/scheduler_test.go`

**Step 1:** 编写 `Scheduler` struct，集成策略引擎 + 池管理 + 健康检查。
**Step 2:** 实现 `Select(ctx, userID, provider, model)` → 选择最优凭证。
**Step 3:** 实现 `ReportResult(credentialID, success, latency)` → 更新健康指标。
**Step 4:** 测试凭证选择和健康降级。
**Step 5:** Commit

---

### Task 3: 实现健康检查 + 熔断器

**Files:**
- Create: `internal/router/health.go`
- Test: `internal/router/health_test.go`

**Step 1:** 编写 `HealthChecker` — 后台 goroutine 定期探测凭证状态。
**Step 2:** 编写 `CircuitBreaker` — 三状态（closed/open/half-open）。
**Step 3:** 测试连续失败触发熔断和半开恢复。
**Step 4:** Commit

---

### Task 4: 实现与现有 Selector 接口的适配器

**Files:**
- Create: `internal/router/adapter.go`

**Step 1:** 编写适配器，使新调度器兼容现有 `sdk/cliproxy/auth.Selector` 接口。
**Step 2:** 修改 `sdk/cliproxy/builder.go` 的策略初始化逻辑，支持新引擎。
**Step 3:** 测试兼容性。
**Step 4:** Commit

---

### Task 5: 调度指标采集

**Files:**
- Create: `internal/router/metrics.go`

**Step 1:** 编写指标采集器（活跃连接数、请求延迟分布、健康状态分布）。
**Step 2:** 暴露 API 端点供管理面板查询。
**Step 3:** Commit
