# Phase 6: 凭证管理

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 实现兑换码生成/兑换、自助模板、裂变邀请三种凭证分发方式。

**Architecture:** `internal/community/credential/` 模块。

**Tech Stack:** Go 1.26 / Gin

**Depends on:** Phase 1, 2, 3

---

### Task 1: 实现兑换码生成和兑换

**Files:**
- Create: `internal/community/credential/redemption.go`
- Test: `internal/community/credential/redemption_test.go`

**Step 1:** 编写 `RedemptionService` — 批量生成兑换码、绑定额度模型。
**Step 2:** 实现 `Redeem(ctx, userID, code)` — 验证兑换码、赠送额度。
**Step 3:** 测试生成和兑换流程。
**Step 4:** Commit

---

### Task 2: 实现自助兑换模板

**Files:**
- Create: `internal/community/credential/template.go`
- Test: `internal/community/credential/template_test.go`

**Step 1:** 编写 `TemplateService` — 管理员创建模板、用户自助领取。
**Step 2:** 实现自动生成兑换码并关联模板。
**Step 3:** 测试模板领取和限额。
**Step 4:** Commit

---

### Task 3: 实现裂变邀请

**Files:**
- Create: `internal/community/credential/referral.go`
- Test: `internal/community/credential/referral_test.go`

**Step 1:** 编写 `ReferralService` — 处理邀请码注册、双方奖励。
**Step 2:** 实现邀请统计查询。
**Step 3:** 测试邀请流程和奖励发放。
**Step 4:** Commit

---

### Task 4: 凭证管理 API Handler

**Files:**
- Create: `internal/community/credential/handler.go`

**Step 1:** 编写用户端和管理端 Handler（上传凭证、兑换码、模板、邀请码管理）。
**Step 2:** 注册路由到 `/api/v1/credential/*` 和 `/api/v1/admin/credential/*`。
**Step 3:** Commit
