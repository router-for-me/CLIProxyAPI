# Phase 9: 前端面板

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 使用 React + Tailwind 构建全新的管理面板和用户面板，清新浅色系设计风格，嵌入 Go 二进制。

**Architecture:** `internal/panel/web/` 目录下的 React 项目，使用 Vite 构建，Zustand 状态管理。

**Tech Stack:** React 19 / Tailwind CSS 4 / Vite / Zustand / React Router / Recharts

**Depends on:** Phase 8

> **注意:** 前端实现时必须使用 `superpowers:frontend-design` skill 来确保设计质量。

---

### Task 1: 初始化 React 项目

**Files:**
- Create: `internal/panel/web/package.json`
- Create: `internal/panel/web/vite.config.ts`
- Create: `internal/panel/web/tailwind.config.js`
- Create: `internal/panel/web/tsconfig.json`
- Create: `internal/panel/web/index.html`
- Create: `internal/panel/web/src/main.tsx`

**Step 1:** 在 `internal/panel/web/` 下执行 `npx create-vite . --template react-ts`
**Step 2:** 安装依赖：`npm install tailwindcss @tailwindcss/vite zustand react-router-dom recharts`
**Step 3:** 配置 Tailwind 和 Vite。
**Step 4:** Commit

---

### Task 2: 创建布局和路由

**Files:**
- Create: `internal/panel/web/src/App.tsx`
- Create: `internal/panel/web/src/components/layout/Sidebar.tsx`
- Create: `internal/panel/web/src/components/layout/Navbar.tsx`
- Create: `internal/panel/web/src/components/layout/MainLayout.tsx`

**Step 1:** 实现主布局（侧边栏 + 顶部导航 + 主内容区）。

设计规范：
- 背景色: #F8FAFC
- 强调蓝: #3B82F6
- 成功绿: #10B981
- 圆角: rounded-xl
- 阴影: shadow-sm
- 字体: 系统 sans-serif, text-base
- 侧边栏: 固定宽度 240px, 白色背景

**Step 2:** 配置 React Router 路由：
- `/panel/login` — 登录页
- `/panel/register` — 注册页
- `/panel/dashboard` — 用户仪表盘
- `/panel/quota` — 额度详情
- `/panel/credentials` — 凭证管理
- `/panel/redeem` — 兑换中心
- `/panel/settings` — 个人设置
- `/panel/admin/dashboard` — 管理仪表盘
- `/panel/admin/users` — 用户管理
- `/panel/admin/quota` — 额度配置
- `/panel/admin/pool` — 凭证池管理
- `/panel/admin/codes` — 兑换码管理
- `/panel/admin/invites` — 邀请码管理
- `/panel/admin/security` — 安全中心
- `/panel/admin/settings` — 系统设置
- `/panel/admin/router` — 轮询引擎

**Step 3:** Commit

---

### Task 3: 实现 API 调用层和状态管理

**Files:**
- Create: `internal/panel/web/src/api/client.ts`
- Create: `internal/panel/web/src/api/auth.ts`
- Create: `internal/panel/web/src/api/user.ts`
- Create: `internal/panel/web/src/api/admin.ts`
- Create: `internal/panel/web/src/stores/auth.ts`
- Create: `internal/panel/web/src/stores/user.ts`

**Step 1:** 编写 API 客户端封装（fetch + JWT 自动注入 + 刷新 Token）。
**Step 2:** 编写 Zustand stores（认证状态、用户信息）。
**Step 3:** Commit

---

### Task 4: 实现认证页面

**Files:**
- Create: `internal/panel/web/src/pages/auth/Login.tsx`
- Create: `internal/panel/web/src/pages/auth/Register.tsx`

**Step 1:** 实现登录页（用户名/邮箱 + 密码、OAuth 按钮、链接到注册）。
**Step 2:** 实现注册页（多步骤：选择方式 → 填写信息 → 邮箱验证 → 完成）。
**Step 3:** Commit

---

### Task 5: 实现用户端页面

**Files:**
- Create: `internal/panel/web/src/pages/user/Dashboard.tsx`
- Create: `internal/panel/web/src/pages/user/Quota.tsx`
- Create: `internal/panel/web/src/pages/user/Credentials.tsx`
- Create: `internal/panel/web/src/pages/user/Redeem.tsx`
- Create: `internal/panel/web/src/pages/user/Settings.tsx`

**Step 1:** 仪表盘 — 额度概览卡片、API Key 展示（可复制）、最近使用记录。
**Step 2:** 额度详情 — 各模型使用量柱状图、周期重置倒计时。
**Step 3:** 凭证管理 — 上传凭证表单、已有凭证列表、健康状态。
**Step 4:** 兑换中心 — 输入兑换码/邀请码、可用模板列表、领取按钮。
**Step 5:** 个人设置 — 修改密码、绑定邮箱、重置 API Key、退出登录。
**Step 6:** Commit

---

### Task 6: 实现管理端页面

**Files:**
- Create: `internal/panel/web/src/pages/admin/Dashboard.tsx`
- Create: `internal/panel/web/src/pages/admin/Users.tsx`
- Create: `internal/panel/web/src/pages/admin/QuotaConfig.tsx`
- Create: `internal/panel/web/src/pages/admin/CredentialPool.tsx`
- Create: `internal/panel/web/src/pages/admin/RedemptionCodes.tsx`
- Create: `internal/panel/web/src/pages/admin/InviteCodes.tsx`
- Create: `internal/panel/web/src/pages/admin/Security.tsx`
- Create: `internal/panel/web/src/pages/admin/SystemSettings.tsx`
- Create: `internal/panel/web/src/pages/admin/RouterEngine.tsx`

**Step 1:** 管理仪表盘 — 全局统计卡片（总用户/总请求/凭证池状态/系统健康）+ 趋势图。
**Step 2:** 用户管理 — 可搜索表格、角色切换、封禁/解封、额度手动调整对话框。
**Step 3:** 额度配置 — 按模型设置规则的表格 + 新增/编辑表单。
**Step 4:** 凭证池管理 — 公共池凭证卡片列表、健康状态指示灯、添加/移除操作。
**Step 5:** 兑换码管理 — 批量生成表单、使用统计表格。
**Step 6:** 邀请码管理 — 生成表单（人数限制）、使用记录。
**Step 7:** 安全中心 — 各模块开关卡片、IP 规则管理、风险用户列表、审计日志时间线。
**Step 8:** 系统设置 — 池模式切换、SMTP 配置表单、OAuth Provider 管理、通用设置。
**Step 9:** 轮询引擎 — 策略切换下拉框、凭证权重调整滑块、健康检查配置。
**Step 10:** Commit

---

### Task 7: 构建和嵌入

**Step 1:** 在 `internal/panel/web/` 下运行 `npm run build`，确认 `dist/` 目录生成。
**Step 2:** 确认 `internal/panel/embed.go` 的 `go:embed` 能正确嵌入 dist 文件。
**Step 3:** 运行 `go build ./cmd/server/` 验证完整编译。
**Step 4:** Commit

---

### Task 8: 国际化支持

**Files:**
- Create: `internal/panel/web/src/i18n/zh.ts`
- Create: `internal/panel/web/src/i18n/en.ts`
- Create: `internal/panel/web/src/i18n/index.ts`

**Step 1:** 提取所有中文文案到 `zh.ts`，创建对应 `en.ts`。
**Step 2:** 实现语言切换 hook `useI18n()`。
**Step 3:** Commit
