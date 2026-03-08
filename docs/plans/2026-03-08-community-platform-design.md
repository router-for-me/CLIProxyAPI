# CLIProxyAPI 公益站平台设计文档

> 日期：2026-03-08
> 状态：已确认
> 架构方案：方案 A（扩展层架构）

---

## 1. 项目概述

在现有 CLIProxyAPI 反向代理基础上，构建一个公益站平台，包含：

- 多方式用户注册登录体系
- 凭证管理与额度分配系统
- 使用统计后台与用户管理系统
- 安全防御体系
- 高性能异步并发轮询引擎（重写）
- 全新 React + Tailwind 前端面板（清新浅色系，替换原面板）
- 内嵌单体部署（编译为 Go 二进制）
- 数据库 SQLite + PostgreSQL 双模式

---

## 2. 整体架构

### 2.1 系统分层

```
┌─────────────────────────────────────────────────────┐
│                   React + Tailwind 前端面板           │
│  (用户面板 / 管理后台 / 统计仪表盘 / 设置中心)         │
├─────────────────────────────────────────────────────┤
│                    Gin HTTP API 层                   │
│  /api/v1/auth/*   /api/v1/user/*   /api/v1/admin/*  │
├───────────┬───────────┬───────────┬─────────────────┤
│  用户体系  │  额度引擎  │ 凭证管理  │  安全防御中间件   │
│  community │  community │ community │  community      │
│  /user     │  /quota    │ /credential│ /security      │
├───────────┴───────────┴───────────┴─────────────────┤
│              新轮询调度引擎 (router/)                  │
│  scheduler + pool manager + health checker           │
├─────────────────────────────────────────────────────┤
│           现有 CLIProxyAPI 核心 (不改动)               │
│  translator / executor / auth / registry / watcher   │
├─────────────────────────────────────────────────────┤
│         存储抽象层 (SQLite ←→ PostgreSQL)              │
└─────────────────────────────────────────────────────┘
```

### 2.2 新增目录结构

```
internal/
├── community/                 ← 公益站核心模块
│   ├── user/                  ← 用户体系
│   │   ├── model.go           ← User/Role 数据模型
│   │   ├── service.go         ← 业务逻辑
│   │   ├── repository.go      ← 数据访问接口
│   │   ├── auth_local.go      ← 邮箱+密码注册登录
│   │   ├── auth_oauth.go      ← OAuth 登录（GitHub/Google 可扩展）
│   │   ├── auth_invite.go     ← 邀请码/兑换码注册
│   │   └── email.go           ← QQ 邮箱 SMTP 发送
│   ├── quota/                 ← 额度引擎
│   │   ├── model.go           ← 额度模型（次数/token 双计量）
│   │   ├── engine.go          ← 额度扣减/检查/恢复
│   │   ├── pool.go            ← 独立池/公共池/贡献者池
│   │   └── policy.go          ← 管理员策略配置
│   ├── credential/            ← 凭证分发
│   │   ├── redemption.go      ← 兑换码生成/兑换
│   │   ├── template.go        ← 自助模板
│   │   └── referral.go        ← 裂变邀请
│   ├── security/              ← 安全防御
│   │   ├── ratelimit.go       ← 请求限流（IP/用户级）
│   │   ├── anomaly.go         ← 异常行为检测
│   │   ├── ipcontrol.go       ← IP 黑白名单
│   │   ├── jwt.go             ← JWT 鉴权
│   │   └── middleware.go      ← Gin 中间件集成
│   └── stats/                 ← 统计分析
│       ├── collector.go       ← 使用数据采集
│       ├── aggregator.go      ← 聚合计算
│       └── export.go          ← 数据导出
├── router/                    ← 新轮询调度引擎
│   ├── scheduler.go           ← 调度核心（权重/优先级/健康感知）
│   ├── pool.go                ← 凭证池管理
│   ├── health.go              ← 健康检查 + 熔断
│   ├── strategy.go            ← 策略接口（round-robin/weighted/fill-first）
│   └── metrics.go             ← 调度指标
├── panel/                     ← 新前端面板
│   └── web/                   ← React 项目
│       ├── src/
│       │   ├── pages/         ← 页面
│       │   ├── components/    ← 组件
│       │   ├── hooks/         ← React Hooks
│       │   ├── api/           ← API 调用层
│       │   └── stores/        ← 状态管理
│       └── dist/              ← 编译产物（go:embed）
└── db/                        ← 数据库抽象层
    ├── interface.go           ← 存储接口定义
    ├── sqlite.go              ← SQLite 实现
    ├── postgres.go            ← PostgreSQL 实现
    └── migrations/            ← 数据库迁移脚本
```

### 2.3 与现有代码的集成点

| 集成点 | 方式 |
|--------|------|
| Gin 路由注册 | 在路由初始化中挂载 `/api/v1/` 新路由组 |
| 请求拦截 | Gin middleware chain 注入安全 + 额度中间件 |
| 轮询调度 | 新 `router/` 引擎替换现有 `routing.strategy` |
| 配置集成 | `config.yaml` 新增 `community:` 配置段 |
| 面板托管 | `go:embed` 嵌入 React 编译产物 |

---

## 3. 用户体系

### 3.1 数据模型

```go
type User struct {
    ID            int64
    UUID          string     // 外部唯一标识
    Username      string     // 唯一
    Email         string     // 可选，唯一
    PasswordHash  string     // bcrypt
    Role          Role       // admin / user
    Status        Status     // active / banned / pending
    APIKey        string     // 代理调用的 API Key
    OAuthProvider string     // github / google / 空
    OAuthID       string
    InvitedBy     *int64     // 邀请人 ID
    InviteCode    string     // 该用户的专属裂变码
    PoolMode      PoolMode   // private / public / contributor
    CreatedAt     time.Time
    UpdatedAt     time.Time
}
```

### 3.2 四种注册方式

| 方式 | 流程 | 管理员控制 |
|------|------|-----------|
| 邮箱注册 | 输入邮箱 → QQ邮箱 SMTP 发验证码 → 设密码 → 完成 | 开关、每日注册上限 |
| OAuth | 跳转 GitHub/Google → 回调 → 自动创建/关联账号 | 开关、支持的 Provider（预留扩展端口） |
| 管理员邀请码 | 管理员生成限定人数的邀请码 → 用户输入 → 可联动邮箱验证 → 注册完成 | 生成数量、每码人数上限、是否强制邮箱验证 |
| 用户裂变码 | 老用户分享裂变码 → 新用户注册时填写 → 可联动邮箱验证 → 双方获奖励 | 开关、奖励额度、每人邀请上限、是否强制邮箱验证 |
| 兑换码 | 管理员生成/模板自助 → 用户输入 → 获得对应模型额度 | 生成数量、绑定模型、单码额度 |

### 3.3 邀请码模型

```go
type InviteCode struct {
    ID            int64
    Code          string       // 唯一码
    Type          InviteType   // admin_created / user_referral
    CreatorID     int64        // 创建者 ID
    MaxUses       int          // 最大可使用次数
    UsedCount     int          // 已使用次数
    RequireEmail  bool         // 是否要求邮箱验证
    BonusQuota    *QuotaGrant  // 注册后赠送的额度
    ReferralBonus *QuotaGrant  // 邀请人获得的奖励
    ExpiresAt     *time.Time   // 过期时间
    Status        string       // active / exhausted / expired / disabled
}
```

### 3.4 JWT 鉴权

- Access Token 2h 过期 + Refresh Token 7d 过期（HS256，可配置密钥）
- 管理后台和用户面板通过 JWT 鉴权
- API 代理调用走 API Key 鉴权（兼容现有 `api-keys` 逻辑）

---

## 4. 额度引擎

### 4.1 三种凭证池模式

| 模式 | 说明 | 适用场景 |
|------|------|---------|
| 独立池 (Private) | 用户只能使用自己上传的凭证 | 高级/付费用户 |
| 公共池 (Public) | 所有用户共享公共凭证池 | 公益开放模式 |
| 贡献者池 (Contributor) | 只有上传过凭证的用户才能用公共池 | 鼓励贡献 |

管理员可全局切换模式，也可按用户覆盖。

### 4.2 额度计量

```go
// 管理员为每个模型独立配置
type QuotaConfig struct {
    ModelPattern  string    // 支持通配符 "claude-*"
    QuotaType     QuotaType // count / token / both
    MaxRequests   int64     // 最大请求次数
    RequestPeriod string    // daily / weekly / monthly
    MaxTokens     int64     // 最大 token 额度
    TokenPeriod   string    // daily / weekly / monthly / total
}

// 用户维度额度状态
type UserQuota struct {
    UserID        int64
    ModelPattern  string
    UsedRequests  int64
    UsedTokens    int64
    BonusRequests int64     // 兑换码/邀请奖励
    BonusTokens   int64
    PeriodStart   time.Time
}
```

### 4.3 全局统计大池

```go
type GlobalPool struct {
    TotalAccounts    int64  // 总凭证账号数
    ActiveAccounts   int64  // 活跃账号数
    TotalQuota       int64  // 总额度（自动统计 + 管理员自定义）
    UsedQuota        int64  // 已使用额度
    AdminCustomQuota int64  // 管理员手动添加的额度
    AutoCalculated   bool   // 是否开启自动统计
}
```

### 4.4 RPM + 概率限流 + 风险联动

```go
// RPM 限流
type RPMConfig struct {
    Enabled           bool
    ContributorRPM    int     // 贡献凭证用户 RPM
    NonContributorRPM int     // 未贡献凭证用户 RPM
}

// 概率限流
type ProbabilityRateLimit struct {
    Enabled              bool
    ContributorWeight    float64
    NonContributorWeight float64
}

// 风险联动
type RiskRule struct {
    Enabled            bool
    RPMExceedThreshold int            // 短时间超 RPM 次数阈值
    RPMExceedWindow    time.Duration  // 检测窗口
    PenaltyDuration    time.Duration  // 惩罚持续时间
    PenaltyProbability float64        // 惩罚期通过概率
}

// 用户风险标记
type UserRiskMark struct {
    UserID      int64
    MarkType    string     // rpm_abuse / anomaly / manual
    Reason      string
    MarkedAt    time.Time
    ExpiresAt   time.Time
    AutoApplied bool
}
```

### 4.5 限流执行链

```
请求到达
  → RPM 检查（滑动窗口）
    → 超限 → 429 + 记录超限次数
    → 通过 → 风险规则检查
      → 短期超限次数 > 阈值 → 标记用户 + 启动概率限流惩罚期
      → 正常 → 概率限流检查
        → 有风险标记在惩罚期 → 使用惩罚概率
        → 无标记 → 使用正常概率（按贡献状态区分）
        → 淘汰 → 429
        → 通过 → 额度检查 → 转发请求
```

---

## 5. 新轮询调度引擎

### 5.1 核心架构

```
Scheduler（调度器核心）
  ├── Strategy（策略引擎）
  │   ├── WeightedRoundRobin  — 带权重轮询
  │   ├── LeastLoad           — 最低负载优先
  │   ├── FillFirst           — 填满再切换
  │   └── Random              — 随机选择
  ├── PoolManager（池管理器）
  │   ├── 公共凭证池
  │   └── 用户私有池（per-user）
  └── HealthChecker（健康检查器）
      ├── 异步探测
      ├── 降级/熔断
      └── 半开恢复
```

### 5.2 凭证模型

```go
type Credential struct {
    ID            string
    Provider      string          // gemini / claude / codex / ...
    OwnerID       *int64          // nil = 公共凭证
    Health        HealthStatus    // healthy / degraded / down
    Weight        int             // 权重
    ActiveConns   atomic.Int64    // 活跃连接数
    TotalReqs     atomic.Int64    // 历史总请求
    LastUsed      atomic.Int64    // 上次使用时间戳
    CooldownUntil atomic.Int64   // 冷却截止时间
}
```

### 5.3 池模式集成

```
请求（已通过鉴权 + 额度检查）
  → 查询用户池模式
    → Private → 用户私有凭证池
    → Public  → 公共凭证池
    → Contributor → 检查贡献状态
      → 已贡献 → 公共池 + 私有池合并
      → 未贡献 → 403 拒绝
  → 调度策略选择凭证 → 转发
  → 响应返回 → 更新健康状态 + 负载指标
```

---

## 6. 安全防御体系

### 6.1 安全中间件栈（按顺序执行）

| 层级 | 模块 | 功能 |
|------|------|------|
| 1 | IP 访问控制 | 黑/白名单、GeoIP 地域限制 |
| 2 | 请求限流 | 全局 QPS + 单 IP RPM（滑动窗口） |
| 3 | JWT 鉴权 | Token 验证、角色区分 |
| 4 | RPM + 概率限流 | 用户级 RPM、风险联动概率限流 |
| 5 | 异常行为检测 | 模式识别、自动封禁 |
| 6 | 额度检查 | 扣减前余量检查 |

### 6.2 IP 访问控制

```go
type IPControl struct {
    Enabled    bool
    Whitelist  []string   // CIDR 或精确 IP
    Blacklist  []string
    GeoEnabled bool
    AllowedGeo []string   // 国家码
    AdminOnly  []string   // 管理端点 IP 限制
}
```

### 6.3 异常行为检测

```go
type AnomalyRule struct {
    Name        string
    Pattern     AnomalyPattern  // HighFrequency / ModelScan / LargePayload / ErrorSpike / OffHoursSurge
    Threshold   int
    Window      time.Duration
    Action      Action          // warn / throttle / ban
    BanDuration time.Duration
    NotifyAdmin bool
}
```

### 6.4 管理员控制

- 每个安全模块独立 `Enabled` 开关
- 管理后台一键开启/关闭
- 所有安全事件写入审计日志
- 封禁/解封实时生效

---

## 7. 前端面板设计

### 7.1 设计规范

- 风格：清新浅色系（类 macOS 风格）
- 主色调：#F8FAFC (背景) + #3B82F6 (强调蓝) + #10B981 (成功绿)
- 圆角卡片 + 柔和阴影 + 充足留白
- 字体：系统 sans-serif，16px 基础字号
- 交互：hover 微动效 + 加载骨架屏
- 技术栈：React + Tailwind CSS + Vite + Zustand

### 7.2 用户端页面

| 页面 | 功能 |
|------|------|
| 仪表盘 | 额度概览、最近使用、API Key 展示 |
| 额度详情 | 各模型使用量图表、周期重置倒计时 |
| 凭证管理 | 上传/管理凭证、贡献状态 |
| 兑换中心 | 输入兑换码/邀请码、可用模板 |
| 个人设置 | 修改密码、绑定邮箱、API Key 重置 |

### 7.3 管理端页面

| 页面 | 功能 |
|------|------|
| 管理仪表盘 | 全局统计（用户/请求/凭证池/健康） |
| 用户管理 | 列表、角色、封禁/解封、额度调整 |
| 额度配置 | 按模型设置规则（次数/token/周期） |
| 凭证池管理 | 公共池状态、健康、添加/移除 |
| 兑换码管理 | 批量生成、模板、使用统计 |
| 邀请码管理 | 生成、人数限制、裂变统计 |
| 安全中心 | 模块开关、IP 管理、风险用户、审计日志 |
| 系统设置 | 池模式、SMTP、OAuth、通用设置 |
| 轮询引擎 | 策略切换、权重配置、健康检查设置 |

### 7.4 嵌入方式

React 编译产物 `dist/` 通过 `go:embed` 嵌入 Go 二进制，路由 `/panel/*` 由 Gin 静态文件中间件托管。

---

## 8. 数据库设计

### 8.1 双模式存储

通过配置选择后端：
```yaml
community:
  database:
    driver: "sqlite"       # 或 "postgres"
    dsn: "./community.db"  # 或 postgres DSN
```

### 8.2 核心表

| 表名 | 用途 |
|------|------|
| users | 用户表 |
| user_oauth | OAuth 关联 |
| invite_codes | 邀请码/兑换码 |
| invite_code_usage | 邀请码使用记录 |
| quota_configs | 额度规则配置（按模型） |
| user_quotas | 用户额度状态 |
| credentials | 凭证表（公共 + 私有） |
| credential_health | 凭证健康记录 |
| request_logs | 请求日志 |
| risk_marks | 用户风险标记 |
| ip_rules | IP 黑白名单 |
| anomaly_events | 异常事件 |
| system_settings | 系统设置（KV） |
| audit_logs | 审计日志 |
| redemption_templates | 兑换码模板 |

### 8.3 存储抽象层

```go
type Store interface {
    UserStore
    QuotaStore
    CredentialStore
    SecurityStore
    SettingsStore
    Close() error
}
```

---

## 9. 改进方案

| 编号 | 改进项 | 说明 |
|------|--------|------|
| 1 | 配置热重载 | `community:` 配置段支持热重载 |
| 2 | 国际化 | 前端面板中英文切换 |
| 3 | WebSocket 实时推送 | 管理后台仪表盘实时更新 |
| 4 | 数据导出 | CSV/JSON 格式导出统计和用户数据 |
| 5 | 通知系统 | 告警通知（邮箱/Webhook） |
| 6 | 备份恢复 | SQLite 定时备份 + 管理后台一键恢复 |
| 7 | API 文档 | 内嵌 Swagger/OpenAPI |
| 8 | 迁移工具 | 旧 config.yaml API Keys 迁移到新用户体系 |

---

## 10. 文件拆分策略

实现中对每个文件预估行数，遵循：

- 预估 > 300 行 → 按职责拆分
- 预估 > 500 行 → 强制拆分

拆分维度：
1. 按职责：model / service / repository / handler
2. 按功能：auth_local / auth_oauth / auth_invite
3. 按子域：quota_engine / quota_pool / quota_policy
4. 前端：每页面独立文件，组件提取到 components/
