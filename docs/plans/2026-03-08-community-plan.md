# CLIProxyAPI 公益站平台 — 实现计划

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 在 CLIProxyAPI 反向代理上构建公益站平台，包含用户体系、额度引擎、凭证管理、安全防御、新轮询引擎、React+Tailwind 前端面板。

**Architecture:** 扩展层架构（方案 A）——在 `internal/` 下平行新增 `community/`、`router/`、`panel/`、`db/` 模块，通过接口与现有代码解耦。前端编译为静态资源由 Go 嵌入托管。数据库支持 SQLite + PostgreSQL 双模式。

**Tech Stack:** Go 1.26 / Gin / PostgreSQL / SQLite / React 19 / Tailwind CSS 4 / Vite / Zustand / JWT (HS256)

---

## 实现顺序与阶段划分

| 阶段 | 模块 | 计划文件 | 依赖 |
|------|------|----------|------|
| Phase 1 | 数据库抽象层 + 配置扩展 | [phase1-foundation.md](./2026-03-08-community-plan-phase1-foundation.md) | 无 |
| Phase 2 | 用户体系 | [phase2-user-system.md](./2026-03-08-community-plan-phase2-user-system.md) | Phase 1 |
| Phase 3 | 额度引擎 | [phase3-quota-engine.md](./2026-03-08-community-plan-phase3-quota-engine.md) | Phase 1, 2 |
| Phase 4 | 安全防御 | [phase4-security.md](./2026-03-08-community-plan-phase4-security.md) | Phase 1, 2 |
| Phase 5 | 新轮询引擎 | [phase5-router-engine.md](./2026-03-08-community-plan-phase5-router-engine.md) | Phase 1, 3 |
| Phase 6 | 凭证管理 | [phase6-credential.md](./2026-03-08-community-plan-phase6-credential.md) | Phase 1, 2, 3 |
| Phase 7 | 统计分析 | [phase7-stats.md](./2026-03-08-community-plan-phase7-stats.md) | Phase 1, 2, 3 |
| Phase 8 | 后端 API 集成 | [phase8-api-integration.md](./2026-03-08-community-plan-phase8-api-integration.md) | Phase 1-7 |
| Phase 9 | 前端面板 | [phase9-frontend.md](./2026-03-08-community-plan-phase9-frontend.md) | Phase 8 |

---

## 关键集成点

以下是与现有代码交互的精确位置（实现时需修改的现有文件）：

### 1. 主入口 — 初始化公益站模块
- **文件:** `cmd/server/main.go`
- **位置:** ~Line 436（`managementasset.SetCurrentConfig` 之后）
- **操作:** 初始化 DB、注册 community 模块

### 2. Gin 路由注册 — 挂载新 API 路由
- **文件:** `internal/api/server.go`
- **位置:** `setupRoutes()` ~Line 328
- **操作:** 在 `/v1` 路由组之前注册 `/api/v1/auth/*`, `/api/v1/user/*`, `/api/v1/admin/*`
- **位置:** `NewServer()` ~Line 204
- **操作:** 注入安全中间件到 middleware chain

### 3. 轮询策略替换
- **文件:** `sdk/cliproxy/auth/selector.go`
- **位置:** `RoundRobinSelector` / `FillFirstSelector`
- **操作:** 新调度器实现相同接口，在 `builder.go` ~Line 210 切换实例化

### 4. 配置扩展
- **文件:** `internal/config/config.go`
- **位置:** `Config` struct ~Line 27
- **操作:** 新增 `Community CommunityConfig` 字段

### 5. 面板托管
- **文件:** `internal/api/server.go`
- **位置:** `setupRoutes()` 新增 `/panel/*` 路由
- **操作:** 使用 `go:embed` 嵌入 React 编译产物

---

## 文件拆分规则

实现过程中每个文件遵循：
- 预估 > 300 行 → 按职责拆分
- 预估 > 500 行 → 强制拆分
- Go 文件按 model / service / repository / handler 分层
- React 组件 < 200 行，超出则提取子组件

---

## 测试策略

- 每个模块先写测试再写实现（TDD）
- Go 测试: `go test ./internal/community/... -v`
- Go 测试覆盖: `go test ./internal/community/... -cover`
- 前端测试: Vitest + React Testing Library
- 集成测试: 在 `test/` 目录下按阶段添加

---

## 数据库迁移

所有迁移脚本保存在 `internal/db/migrations/` 下，按序号命名：
```
001_create_users.sql
002_create_invite_codes.sql
003_create_quota_configs.sql
004_create_user_quotas.sql
005_create_credentials.sql
006_create_security_tables.sql
007_create_stats_tables.sql
```

每个迁移脚本包含 SQLite 和 PostgreSQL 两种方言的 CREATE TABLE 语句（通过 Go 代码选择执行）。
