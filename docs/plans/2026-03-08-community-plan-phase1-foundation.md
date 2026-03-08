# Phase 1: 数据库抽象层 + 配置扩展

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 建立公益站平台的数据基础——数据库抽象层（SQLite/PostgreSQL 双模式）和配置扩展。

**Architecture:** 在 `internal/db/` 创建存储接口和双实现，在 `internal/config/` 扩展配置结构体。

**Tech Stack:** Go 1.26 / SQLite (modernc.org/sqlite) / PostgreSQL (jackc/pgx/v5) / YAML

---

### Task 1: 添加 SQLite 依赖

**Files:**
- Modify: `go.mod`

**Step 1: 添加 modernc.org/sqlite 依赖**

Run: `go get modernc.org/sqlite`

> 注意：使用纯 Go 实现的 SQLite 驱动，无需 CGO，跨平台编译友好。

**Step 2: 验证依赖安装**

Run: `go mod tidy`
Expected: go.mod 和 go.sum 更新成功

**Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "deps: add modernc.org/sqlite for CGO-free SQLite support"
```

---

### Task 2: 创建数据库存储接口

**Files:**
- Create: `internal/db/interface.go`
- Test: `internal/db/interface_test.go`

**Step 1: 编写接口定义**

创建 `internal/db/interface.go`：

```go
package db

import (
	"context"
	"time"
)

// ============================================================
// 存储接口定义 — 公益站平台数据访问层
// 所有模块通过接口访问数据，不直接依赖具体数据库实现
// ============================================================

// Store 统一存储接口，聚合所有子接口
type Store interface {
	UserStore
	InviteCodeStore
	QuotaStore
	CredentialStore
	SecurityStore
	SettingsStore
	StatsStore
	Migrate(ctx context.Context) error
	Close() error
}

// ============================================================
// 用户相关
// ============================================================

type UserStore interface {
	CreateUser(ctx context.Context, user *User) error
	GetUserByID(ctx context.Context, id int64) (*User, error)
	GetUserByUUID(ctx context.Context, uuid string) (*User, error)
	GetUserByUsername(ctx context.Context, username string) (*User, error)
	GetUserByEmail(ctx context.Context, email string) (*User, error)
	GetUserByAPIKey(ctx context.Context, apiKey string) (*User, error)
	GetUserByOAuth(ctx context.Context, provider, oauthID string) (*User, error)
	UpdateUser(ctx context.Context, user *User) error
	DeleteUser(ctx context.Context, id int64) error
	ListUsers(ctx context.Context, opts ListUsersOpts) ([]*User, int64, error)
	CountUsers(ctx context.Context) (int64, error)
}

type ListUsersOpts struct {
	Page     int
	PageSize int
	Status   *string
	Role     *string
	Search   string // 模糊搜索用户名/邮箱
}

// ============================================================
// 邀请码 / 兑换码
// ============================================================

type InviteCodeStore interface {
	CreateInviteCode(ctx context.Context, code *InviteCode) error
	GetInviteCodeByCode(ctx context.Context, code string) (*InviteCode, error)
	ListInviteCodes(ctx context.Context, opts ListInviteCodesOpts) ([]*InviteCode, int64, error)
	UpdateInviteCode(ctx context.Context, code *InviteCode) error
	IncrementInviteCodeUsage(ctx context.Context, codeID int64) error
	RecordInviteCodeUsage(ctx context.Context, usage *InviteCodeUsage) error
}

type ListInviteCodesOpts struct {
	Page     int
	PageSize int
	Type     *string // admin_created / user_referral
	Status   *string
}

// ============================================================
// 额度
// ============================================================

type QuotaStore interface {
	CreateQuotaConfig(ctx context.Context, cfg *QuotaConfig) error
	GetQuotaConfigs(ctx context.Context) ([]*QuotaConfig, error)
	GetQuotaConfigByModel(ctx context.Context, model string) (*QuotaConfig, error)
	UpdateQuotaConfig(ctx context.Context, cfg *QuotaConfig) error
	DeleteQuotaConfig(ctx context.Context, id int64) error

	GetUserQuota(ctx context.Context, userID int64, model string) (*UserQuota, error)
	UpsertUserQuota(ctx context.Context, quota *UserQuota) error
	DeductUserQuota(ctx context.Context, userID int64, model string, requests int64, tokens int64) error
	ResetExpiredQuotas(ctx context.Context) (int64, error)
}

// ============================================================
// 凭证
// ============================================================

type CredentialStore interface {
	CreateCredential(ctx context.Context, cred *Credential) error
	GetCredentialByID(ctx context.Context, id string) (*Credential, error)
	ListCredentials(ctx context.Context, opts ListCredentialsOpts) ([]*Credential, int64, error)
	UpdateCredential(ctx context.Context, cred *Credential) error
	DeleteCredential(ctx context.Context, id string) error
	GetPublicPoolCredentials(ctx context.Context, provider string) ([]*Credential, error)
	GetUserCredentials(ctx context.Context, userID int64, provider string) ([]*Credential, error)
	RecordCredentialHealth(ctx context.Context, record *CredentialHealthRecord) error
}

type ListCredentialsOpts struct {
	Page     int
	PageSize int
	OwnerID  *int64  // nil = 公共池
	Provider *string
	Health   *string
}

// ============================================================
// 安全
// ============================================================

type SecurityStore interface {
	CreateIPRule(ctx context.Context, rule *IPRule) error
	ListIPRules(ctx context.Context) ([]*IPRule, error)
	DeleteIPRule(ctx context.Context, id int64) error

	CreateRiskMark(ctx context.Context, mark *UserRiskMark) error
	GetActiveRiskMarks(ctx context.Context, userID int64) ([]*UserRiskMark, error)
	ListRiskMarks(ctx context.Context, opts ListRiskMarksOpts) ([]*UserRiskMark, int64, error)
	ExpireRiskMarks(ctx context.Context) (int64, error)

	RecordAnomalyEvent(ctx context.Context, event *AnomalyEvent) error
	ListAnomalyEvents(ctx context.Context, opts ListAnomalyEventsOpts) ([]*AnomalyEvent, int64, error)

	CreateAuditLog(ctx context.Context, log *AuditLog) error
	ListAuditLogs(ctx context.Context, opts ListAuditLogsOpts) ([]*AuditLog, int64, error)
}

type ListRiskMarksOpts struct {
	Page     int
	PageSize int
	UserID   *int64
	Active   *bool
}

type ListAnomalyEventsOpts struct {
	Page     int
	PageSize int
	UserID   *int64
	After    *time.Time
}

type ListAuditLogsOpts struct {
	Page     int
	PageSize int
	UserID   *int64
	Action   *string
	After    *time.Time
}

// ============================================================
// 系统设置
// ============================================================

type SettingsStore interface {
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
	GetAllSettings(ctx context.Context) (map[string]string, error)
	DeleteSetting(ctx context.Context, key string) error
}

// ============================================================
// 统计
// ============================================================

type StatsStore interface {
	RecordRequest(ctx context.Context, log *RequestLog) error
	GetRequestStats(ctx context.Context, opts RequestStatsOpts) (*RequestStats, error)
	GetUserRequestStats(ctx context.Context, userID int64, opts RequestStatsOpts) (*UserRequestStats, error)
}

type RequestStatsOpts struct {
	After  *time.Time
	Before *time.Time
	Model  *string
}
```

**Step 2: 编写接口编译验证测试**

创建 `internal/db/interface_test.go`：

```go
package db_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// TestStoreInterfaceCompiles 验证 Store 接口可编译
func TestStoreInterfaceCompiles(t *testing.T) {
	// 编译时类型检查：确保接口定义无语法错误
	var _ db.Store = nil
	var _ db.UserStore = nil
	var _ db.QuotaStore = nil
	var _ db.CredentialStore = nil
	var _ db.SecurityStore = nil
	var _ db.SettingsStore = nil
	var _ db.StatsStore = nil
}
```

**Step 3: 运行测试验证编译**

Run: `go test ./internal/db/... -v`
Expected: PASS

**Step 4: Commit**

```bash
git add internal/db/
git commit -m "feat(db): define unified storage interface for community platform"
```

---

### Task 3: 创建数据模型

**Files:**
- Create: `internal/db/models.go`

**Step 1: 编写所有数据模型**

创建 `internal/db/models.go`：

```go
package db

import "time"

// ============================================================
// 枚举类型
// ============================================================

type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

type UserStatus string

const (
	StatusActive  UserStatus = "active"
	StatusBanned  UserStatus = "banned"
	StatusPending UserStatus = "pending"
)

type PoolMode string

const (
	PoolPrivate     PoolMode = "private"
	PoolPublic      PoolMode = "public"
	PoolContributor PoolMode = "contributor"
)

type InviteType string

const (
	InviteAdminCreated InviteType = "admin_created"
	InviteUserReferral InviteType = "user_referral"
)

type InviteStatus string

const (
	InviteActive    InviteStatus = "active"
	InviteExhausted InviteStatus = "exhausted"
	InviteExpired   InviteStatus = "expired"
	InviteDisabled  InviteStatus = "disabled"
)

type QuotaType string

const (
	QuotaCount QuotaType = "count"
	QuotaToken QuotaType = "token"
	QuotaBoth  QuotaType = "both"
)

type QuotaPeriod string

const (
	PeriodDaily   QuotaPeriod = "daily"
	PeriodWeekly  QuotaPeriod = "weekly"
	PeriodMonthly QuotaPeriod = "monthly"
	PeriodTotal   QuotaPeriod = "total"
)

type HealthStatus string

const (
	HealthHealthy  HealthStatus = "healthy"
	HealthDegraded HealthStatus = "degraded"
	HealthDown     HealthStatus = "down"
)

type RiskMarkType string

const (
	RiskRPMAbuse RiskMarkType = "rpm_abuse"
	RiskAnomaly  RiskMarkType = "anomaly"
	RiskManual   RiskMarkType = "manual"
)

type AnomalyAction string

const (
	ActionWarn     AnomalyAction = "warn"
	ActionThrottle AnomalyAction = "throttle"
	ActionBan      AnomalyAction = "ban"
)

// ============================================================
// 数据模型
// ============================================================

// User 核心用户模型
type User struct {
	ID            int64      `json:"id"`
	UUID          string     `json:"uuid"`
	Username      string     `json:"username"`
	Email         string     `json:"email,omitempty"`
	PasswordHash  string     `json:"-"`
	Role          Role       `json:"role"`
	Status        UserStatus `json:"status"`
	APIKey        string     `json:"api_key"`
	OAuthProvider string     `json:"oauth_provider,omitempty"`
	OAuthID       string     `json:"oauth_id,omitempty"`
	InvitedBy     *int64     `json:"invited_by,omitempty"`
	InviteCode    string     `json:"invite_code"`
	PoolMode      PoolMode   `json:"pool_mode"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// InviteCode 邀请码 / 兑换码
type InviteCode struct {
	ID            int64        `json:"id"`
	Code          string       `json:"code"`
	Type          InviteType   `json:"type"`
	CreatorID     int64        `json:"creator_id"`
	MaxUses       int          `json:"max_uses"`
	UsedCount     int          `json:"used_count"`
	RequireEmail  bool         `json:"require_email"`
	BonusQuota    *QuotaGrant  `json:"bonus_quota,omitempty"`
	ReferralBonus *QuotaGrant  `json:"referral_bonus,omitempty"`
	ExpiresAt     *time.Time   `json:"expires_at,omitempty"`
	Status        InviteStatus `json:"status"`
	CreatedAt     time.Time    `json:"created_at"`
}

// QuotaGrant 额度赠予（嵌入 InviteCode 使用）
type QuotaGrant struct {
	ModelPattern string    `json:"model_pattern"`
	Requests     int64     `json:"requests,omitempty"`
	Tokens       int64     `json:"tokens,omitempty"`
	QuotaType    QuotaType `json:"quota_type"`
}

// InviteCodeUsage 邀请码使用记录
type InviteCodeUsage struct {
	ID         int64     `json:"id"`
	CodeID     int64     `json:"code_id"`
	UserID     int64     `json:"user_id"`
	UsedAt     time.Time `json:"used_at"`
}

// QuotaConfig 管理员配置的模型额度规则
type QuotaConfig struct {
	ID            int64       `json:"id"`
	ModelPattern  string      `json:"model_pattern"`
	QuotaType     QuotaType   `json:"quota_type"`
	MaxRequests   int64       `json:"max_requests"`
	RequestPeriod QuotaPeriod `json:"request_period"`
	MaxTokens     int64       `json:"max_tokens"`
	TokenPeriod   QuotaPeriod `json:"token_period"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
}

// UserQuota 用户额度状态
type UserQuota struct {
	ID            int64     `json:"id"`
	UserID        int64     `json:"user_id"`
	ModelPattern  string    `json:"model_pattern"`
	UsedRequests  int64     `json:"used_requests"`
	UsedTokens    int64     `json:"used_tokens"`
	BonusRequests int64     `json:"bonus_requests"`
	BonusTokens   int64     `json:"bonus_tokens"`
	PeriodStart   time.Time `json:"period_start"`
}

// Credential 凭证（公共池或用户私有）
type Credential struct {
	ID       string       `json:"id"`
	Provider string       `json:"provider"`
	OwnerID  *int64       `json:"owner_id,omitempty"`
	Data     string       `json:"-"` // 加密存储的凭证数据
	Health   HealthStatus `json:"health"`
	Weight   int          `json:"weight"`
	Enabled  bool         `json:"enabled"`
	AddedAt  time.Time    `json:"added_at"`
}

// CredentialHealthRecord 凭证健康记录
type CredentialHealthRecord struct {
	ID           int64        `json:"id"`
	CredentialID string       `json:"credential_id"`
	Status       HealthStatus `json:"status"`
	Latency      int64        `json:"latency_ms"`
	ErrorMsg     string       `json:"error_msg,omitempty"`
	CheckedAt    time.Time    `json:"checked_at"`
}

// IPRule IP 黑白名单规则
type IPRule struct {
	ID        int64     `json:"id"`
	CIDR      string    `json:"cidr"`
	RuleType  string    `json:"rule_type"` // whitelist / blacklist
	Comment   string    `json:"comment,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// UserRiskMark 用户风险标记
type UserRiskMark struct {
	ID          int64        `json:"id"`
	UserID      int64        `json:"user_id"`
	MarkType    RiskMarkType `json:"mark_type"`
	Reason      string       `json:"reason"`
	MarkedAt    time.Time    `json:"marked_at"`
	ExpiresAt   time.Time    `json:"expires_at"`
	AutoApplied bool         `json:"auto_applied"`
}

// AnomalyEvent 异常事件记录
type AnomalyEvent struct {
	ID        int64     `json:"id"`
	UserID    *int64    `json:"user_id,omitempty"`
	IP        string    `json:"ip"`
	EventType string    `json:"event_type"`
	Detail    string    `json:"detail"`
	Action    string    `json:"action"`
	CreatedAt time.Time `json:"created_at"`
}

// AuditLog 审计日志
type AuditLog struct {
	ID        int64     `json:"id"`
	UserID    *int64    `json:"user_id,omitempty"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	Detail    string    `json:"detail,omitempty"`
	IP        string    `json:"ip"`
	CreatedAt time.Time `json:"created_at"`
}

// RequestLog 请求日志
type RequestLog struct {
	ID           int64     `json:"id"`
	UserID       int64     `json:"user_id"`
	Model        string    `json:"model"`
	Provider     string    `json:"provider"`
	CredentialID string    `json:"credential_id"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	Latency      int64     `json:"latency_ms"`
	StatusCode   int       `json:"status_code"`
	CreatedAt    time.Time `json:"created_at"`
}

// RequestStats 全局请求统计
type RequestStats struct {
	TotalRequests  int64            `json:"total_requests"`
	TotalTokens    int64            `json:"total_tokens"`
	ByModel        map[string]int64 `json:"by_model"`
	ByProvider     map[string]int64 `json:"by_provider"`
	AvgLatency     float64          `json:"avg_latency_ms"`
}

// UserRequestStats 用户请求统计
type UserRequestStats struct {
	UserID         int64            `json:"user_id"`
	TotalRequests  int64            `json:"total_requests"`
	TotalTokens    int64            `json:"total_tokens"`
	ByModel        map[string]int64 `json:"by_model"`
}

// RedemptionTemplate 兑换码模板
type RedemptionTemplate struct {
	ID            int64       `json:"id"`
	Name          string      `json:"name"`
	Description   string      `json:"description"`
	BonusQuota    QuotaGrant  `json:"bonus_quota"`
	MaxPerUser    int         `json:"max_per_user"`
	TotalLimit    int         `json:"total_limit"`
	IssuedCount   int         `json:"issued_count"`
	Enabled       bool        `json:"enabled"`
	CreatedAt     time.Time   `json:"created_at"`
}
```

**Step 2: 运行编译检查**

Run: `go build ./internal/db/...`
Expected: 无错误

**Step 3: Commit**

```bash
git add internal/db/models.go
git commit -m "feat(db): add all data models for community platform"
```

---

### Task 4: 创建数据库迁移脚本

**Files:**
- Create: `internal/db/migrations.go`

**Step 1: 编写嵌入式迁移脚本**

创建 `internal/db/migrations.go`：

```go
package db

import "embed"

//go:embed migrations/*.sql
var MigrationFS embed.FS

// MigrationDialect 区分 SQL 方言
type MigrationDialect string

const (
	DialectSQLite   MigrationDialect = "sqlite"
	DialectPostgres MigrationDialect = "postgres"
)
```

**Step 2: 创建迁移目录和 SQL 文件**

创建 `internal/db/migrations/001_init_sqlite.sql`：

```sql
-- SQLite 初始化迁移
-- 创建公益站平台所有核心表

CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    uuid TEXT NOT NULL UNIQUE,
    username TEXT NOT NULL UNIQUE,
    email TEXT UNIQUE,
    password_hash TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL DEFAULT 'user',
    status TEXT NOT NULL DEFAULT 'active',
    api_key TEXT NOT NULL UNIQUE,
    oauth_provider TEXT NOT NULL DEFAULT '',
    oauth_id TEXT NOT NULL DEFAULT '',
    invited_by INTEGER REFERENCES users(id),
    invite_code TEXT NOT NULL DEFAULT '',
    pool_mode TEXT NOT NULL DEFAULT 'public',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS invite_codes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    code TEXT NOT NULL UNIQUE,
    type TEXT NOT NULL,
    creator_id INTEGER NOT NULL REFERENCES users(id),
    max_uses INTEGER NOT NULL DEFAULT 1,
    used_count INTEGER NOT NULL DEFAULT 0,
    require_email INTEGER NOT NULL DEFAULT 0,
    bonus_quota TEXT,
    referral_bonus TEXT,
    expires_at DATETIME,
    status TEXT NOT NULL DEFAULT 'active',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS invite_code_usage (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    code_id INTEGER NOT NULL REFERENCES invite_codes(id),
    user_id INTEGER NOT NULL REFERENCES users(id),
    used_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS quota_configs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    model_pattern TEXT NOT NULL UNIQUE,
    quota_type TEXT NOT NULL DEFAULT 'both',
    max_requests INTEGER NOT NULL DEFAULT 0,
    request_period TEXT NOT NULL DEFAULT 'daily',
    max_tokens INTEGER NOT NULL DEFAULT 0,
    token_period TEXT NOT NULL DEFAULT 'daily',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS user_quotas (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id),
    model_pattern TEXT NOT NULL,
    used_requests INTEGER NOT NULL DEFAULT 0,
    used_tokens INTEGER NOT NULL DEFAULT 0,
    bonus_requests INTEGER NOT NULL DEFAULT 0,
    bonus_tokens INTEGER NOT NULL DEFAULT 0,
    period_start DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, model_pattern)
);

CREATE TABLE IF NOT EXISTS credentials (
    id TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    owner_id INTEGER REFERENCES users(id),
    data TEXT NOT NULL,
    health TEXT NOT NULL DEFAULT 'healthy',
    weight INTEGER NOT NULL DEFAULT 1,
    enabled INTEGER NOT NULL DEFAULT 1,
    added_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS credential_health (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    credential_id TEXT NOT NULL REFERENCES credentials(id),
    status TEXT NOT NULL,
    latency_ms INTEGER NOT NULL DEFAULT 0,
    error_msg TEXT NOT NULL DEFAULT '',
    checked_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS ip_rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    cidr TEXT NOT NULL,
    rule_type TEXT NOT NULL,
    comment TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS risk_marks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id),
    mark_type TEXT NOT NULL,
    reason TEXT NOT NULL,
    marked_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME NOT NULL,
    auto_applied INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS anomaly_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER,
    ip TEXT NOT NULL DEFAULT '',
    event_type TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS audit_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER,
    action TEXT NOT NULL,
    target TEXT NOT NULL DEFAULT '',
    detail TEXT NOT NULL DEFAULT '',
    ip TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS request_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    model TEXT NOT NULL,
    provider TEXT NOT NULL,
    credential_id TEXT NOT NULL DEFAULT '',
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    latency_ms INTEGER NOT NULL DEFAULT 0,
    status_code INTEGER NOT NULL DEFAULT 200,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS system_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS redemption_templates (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    bonus_quota TEXT NOT NULL,
    max_per_user INTEGER NOT NULL DEFAULT 1,
    total_limit INTEGER NOT NULL DEFAULT 0,
    issued_count INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
CREATE INDEX IF NOT EXISTS idx_users_api_key ON users(api_key);
CREATE INDEX IF NOT EXISTS idx_users_oauth ON users(oauth_provider, oauth_id);
CREATE INDEX IF NOT EXISTS idx_invite_codes_code ON invite_codes(code);
CREATE INDEX IF NOT EXISTS idx_invite_codes_creator ON invite_codes(creator_id);
CREATE INDEX IF NOT EXISTS idx_user_quotas_user_model ON user_quotas(user_id, model_pattern);
CREATE INDEX IF NOT EXISTS idx_credentials_owner ON credentials(owner_id);
CREATE INDEX IF NOT EXISTS idx_credentials_provider ON credentials(provider);
CREATE INDEX IF NOT EXISTS idx_risk_marks_user ON risk_marks(user_id);
CREATE INDEX IF NOT EXISTS idx_risk_marks_expires ON risk_marks(expires_at);
CREATE INDEX IF NOT EXISTS idx_anomaly_events_user ON anomaly_events(user_id);
CREATE INDEX IF NOT EXISTS idx_anomaly_events_time ON anomaly_events(created_at);
CREATE INDEX IF NOT EXISTS idx_audit_logs_user ON audit_logs(user_id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_time ON audit_logs(created_at);
CREATE INDEX IF NOT EXISTS idx_request_logs_user ON request_logs(user_id);
CREATE INDEX IF NOT EXISTS idx_request_logs_time ON request_logs(created_at);
CREATE INDEX IF NOT EXISTS idx_request_logs_model ON request_logs(model);
```

创建 `internal/db/migrations/001_init_postgres.sql`：

```sql
-- PostgreSQL 初始化迁移
-- 与 SQLite 版本功能相同，使用 PG 语法

CREATE TABLE IF NOT EXISTS users (
    id BIGSERIAL PRIMARY KEY,
    uuid TEXT NOT NULL UNIQUE,
    username TEXT NOT NULL UNIQUE,
    email TEXT UNIQUE,
    password_hash TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL DEFAULT 'user',
    status TEXT NOT NULL DEFAULT 'active',
    api_key TEXT NOT NULL UNIQUE,
    oauth_provider TEXT NOT NULL DEFAULT '',
    oauth_id TEXT NOT NULL DEFAULT '',
    invited_by BIGINT REFERENCES users(id),
    invite_code TEXT NOT NULL DEFAULT '',
    pool_mode TEXT NOT NULL DEFAULT 'public',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS invite_codes (
    id BIGSERIAL PRIMARY KEY,
    code TEXT NOT NULL UNIQUE,
    type TEXT NOT NULL,
    creator_id BIGINT NOT NULL REFERENCES users(id),
    max_uses INTEGER NOT NULL DEFAULT 1,
    used_count INTEGER NOT NULL DEFAULT 0,
    require_email BOOLEAN NOT NULL DEFAULT FALSE,
    bonus_quota JSONB,
    referral_bonus JSONB,
    expires_at TIMESTAMPTZ,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS invite_code_usage (
    id BIGSERIAL PRIMARY KEY,
    code_id BIGINT NOT NULL REFERENCES invite_codes(id),
    user_id BIGINT NOT NULL REFERENCES users(id),
    used_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS quota_configs (
    id BIGSERIAL PRIMARY KEY,
    model_pattern TEXT NOT NULL UNIQUE,
    quota_type TEXT NOT NULL DEFAULT 'both',
    max_requests BIGINT NOT NULL DEFAULT 0,
    request_period TEXT NOT NULL DEFAULT 'daily',
    max_tokens BIGINT NOT NULL DEFAULT 0,
    token_period TEXT NOT NULL DEFAULT 'daily',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS user_quotas (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    model_pattern TEXT NOT NULL,
    used_requests BIGINT NOT NULL DEFAULT 0,
    used_tokens BIGINT NOT NULL DEFAULT 0,
    bonus_requests BIGINT NOT NULL DEFAULT 0,
    bonus_tokens BIGINT NOT NULL DEFAULT 0,
    period_start TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_id, model_pattern)
);

CREATE TABLE IF NOT EXISTS credentials (
    id TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    owner_id BIGINT REFERENCES users(id),
    data TEXT NOT NULL,
    health TEXT NOT NULL DEFAULT 'healthy',
    weight INTEGER NOT NULL DEFAULT 1,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    added_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS credential_health (
    id BIGSERIAL PRIMARY KEY,
    credential_id TEXT NOT NULL REFERENCES credentials(id),
    status TEXT NOT NULL,
    latency_ms BIGINT NOT NULL DEFAULT 0,
    error_msg TEXT NOT NULL DEFAULT '',
    checked_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS ip_rules (
    id BIGSERIAL PRIMARY KEY,
    cidr TEXT NOT NULL,
    rule_type TEXT NOT NULL,
    comment TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS risk_marks (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    mark_type TEXT NOT NULL,
    reason TEXT NOT NULL,
    marked_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    auto_applied BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE IF NOT EXISTS anomaly_events (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT,
    ip TEXT NOT NULL DEFAULT '',
    event_type TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS audit_logs (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT,
    action TEXT NOT NULL,
    target TEXT NOT NULL DEFAULT '',
    detail TEXT NOT NULL DEFAULT '',
    ip TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS request_logs (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL,
    model TEXT NOT NULL,
    provider TEXT NOT NULL,
    credential_id TEXT NOT NULL DEFAULT '',
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    latency_ms BIGINT NOT NULL DEFAULT 0,
    status_code INTEGER NOT NULL DEFAULT 200,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS system_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS redemption_templates (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    bonus_quota JSONB NOT NULL,
    max_per_user INTEGER NOT NULL DEFAULT 1,
    total_limit INTEGER NOT NULL DEFAULT 0,
    issued_count INTEGER NOT NULL DEFAULT 0,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
CREATE INDEX IF NOT EXISTS idx_users_api_key ON users(api_key);
CREATE INDEX IF NOT EXISTS idx_users_oauth ON users(oauth_provider, oauth_id);
CREATE INDEX IF NOT EXISTS idx_invite_codes_code ON invite_codes(code);
CREATE INDEX IF NOT EXISTS idx_invite_codes_creator ON invite_codes(creator_id);
CREATE INDEX IF NOT EXISTS idx_user_quotas_user_model ON user_quotas(user_id, model_pattern);
CREATE INDEX IF NOT EXISTS idx_credentials_owner ON credentials(owner_id);
CREATE INDEX IF NOT EXISTS idx_credentials_provider ON credentials(provider);
CREATE INDEX IF NOT EXISTS idx_risk_marks_user ON risk_marks(user_id);
CREATE INDEX IF NOT EXISTS idx_risk_marks_expires ON risk_marks(expires_at);
CREATE INDEX IF NOT EXISTS idx_anomaly_events_user ON anomaly_events(user_id);
CREATE INDEX IF NOT EXISTS idx_anomaly_events_time ON anomaly_events(created_at);
CREATE INDEX IF NOT EXISTS idx_audit_logs_user ON audit_logs(user_id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_time ON audit_logs(created_at);
CREATE INDEX IF NOT EXISTS idx_request_logs_user ON request_logs(user_id);
CREATE INDEX IF NOT EXISTS idx_request_logs_time ON request_logs(created_at);
CREATE INDEX IF NOT EXISTS idx_request_logs_model ON request_logs(model);
```

**Step 3: 运行编译检查**

Run: `go build ./internal/db/...`
Expected: 无错误

**Step 4: Commit**

```bash
git add internal/db/migrations.go internal/db/migrations/
git commit -m "feat(db): add database migration scripts for SQLite and PostgreSQL"
```

---

### Task 5: 实现 SQLite 存储后端

**Files:**
- Create: `internal/db/sqlite.go`
- Create: `internal/db/sqlite_user.go`
- Create: `internal/db/sqlite_invite.go`
- Create: `internal/db/sqlite_quota.go`
- Create: `internal/db/sqlite_credential.go`
- Create: `internal/db/sqlite_security.go`
- Create: `internal/db/sqlite_settings.go`
- Create: `internal/db/sqlite_stats.go`
- Test: `internal/db/sqlite_test.go`

> 注意：SQLite 后端代码量较大，按职责拆分为多个文件。每个文件实现对应的子接口方法。

**Step 1: 编写 SQLite 核心连接和迁移**

创建 `internal/db/sqlite.go`：

```go
package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// SQLiteStore 基于 SQLite 的存储实现
type SQLiteStore struct {
	db *sql.DB
}

// 编译时接口检查
var _ Store = (*SQLiteStore)(nil)

// NewSQLiteStore 创建 SQLite 存储实例
func NewSQLiteStore(ctx context.Context, dsn string) (*SQLiteStore, error) {
	if dsn == "" {
		dsn = "./community.db"
	}
	// 启用 WAL 模式和外键约束
	connStr := fmt.Sprintf("%s?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000", dsn)
	sqlDB, err := sql.Open("sqlite", connStr)
	if err != nil {
		return nil, fmt.Errorf("打开 SQLite 数据库失败: %w", err)
	}
	// SQLite 单写多读，限制连接数
	sqlDB.SetMaxOpenConns(1)
	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("SQLite 连接测试失败: %w", err)
	}
	return &SQLiteStore{db: sqlDB}, nil
}

// Migrate 执行数据库迁移
func (s *SQLiteStore) Migrate(ctx context.Context) error {
	data, err := MigrationFS.ReadFile("migrations/001_init_sqlite.sql")
	if err != nil {
		return fmt.Errorf("读取迁移脚本失败: %w", err)
	}
	// 按分号拆分并逐条执行
	statements := splitSQL(string(data))
	for _, stmt := range statements {
		if stmt = strings.TrimSpace(stmt); stmt == "" {
			continue
		}
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("执行迁移语句失败: %w\nSQL: %s", err, stmt)
		}
	}
	return nil
}

// Close 关闭数据库连接
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// splitSQL 按分号拆分 SQL（跳过注释行）
func splitSQL(sql string) []string {
	var result []string
	for _, stmt := range strings.Split(sql, ";") {
		trimmed := strings.TrimSpace(stmt)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		result = append(result, trimmed)
	}
	return result
}
```

> 后续 Step 2-8 分别创建 sqlite_user.go / sqlite_invite.go / sqlite_quota.go / sqlite_credential.go / sqlite_security.go / sqlite_settings.go / sqlite_stats.go，每个文件实现对应的子接口方法。模式相同：接收 `*SQLiteStore`，使用 `s.db` 执行 SQL。

**Step 2: 编写测试验证 SQLite 存储**

创建 `internal/db/sqlite_test.go`：

```go
package db_test

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

func newTestStore(t *testing.T) db.Store {
	t.Helper()
	ctx := context.Background()
	store, err := db.NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("创建测试存储失败: %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestSQLiteStore_MigrateAndClose(t *testing.T) {
	store := newTestStore(t)
	_ = store // 验证迁移成功
}
```

**Step 3: 运行测试**

Run: `go test ./internal/db/... -v -run TestSQLiteStore`
Expected: PASS

**Step 4: Commit**

```bash
git add internal/db/sqlite*.go
git commit -m "feat(db): implement SQLite storage backend with migration support"
```

---

### Task 6: 扩展配置结构体

**Files:**
- Modify: `internal/config/config.go` (~Line 27, Config struct)
- Create: `internal/config/community_config.go`

**Step 1: 创建公益站配置结构体**

创建 `internal/config/community_config.go`：

```go
package config

// ============================================================
// 公益站平台配置
// ============================================================

// CommunityConfig 公益站总配置
type CommunityConfig struct {
	Enabled  bool              `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Database DatabaseConfig    `yaml:"database,omitempty" json:"database,omitempty"`
	Auth     AuthConfig        `yaml:"auth,omitempty" json:"auth,omitempty"`
	Quota    QuotaSettings     `yaml:"quota,omitempty" json:"quota,omitempty"`
	Security SecuritySettings  `yaml:"security,omitempty" json:"security,omitempty"`
	SMTP     SMTPConfig        `yaml:"smtp,omitempty" json:"smtp,omitempty"`
	Panel    PanelConfig       `yaml:"panel,omitempty" json:"panel,omitempty"`
}

// DatabaseConfig 数据库配置
type DatabaseConfig struct {
	Driver string `yaml:"driver,omitempty" json:"driver,omitempty"` // sqlite / postgres
	DSN    string `yaml:"dsn,omitempty" json:"dsn,omitempty"`
}

// AuthConfig 认证配置
type AuthConfig struct {
	JWTSecret       string       `yaml:"jwt-secret,omitempty" json:"jwt_secret,omitempty"`
	AccessTokenTTL  int          `yaml:"access-token-ttl,omitempty" json:"access_token_ttl,omitempty"`   // 秒，默认 7200
	RefreshTokenTTL int          `yaml:"refresh-token-ttl,omitempty" json:"refresh_token_ttl,omitempty"` // 秒，默认 604800
	EmailRegister   bool         `yaml:"email-register,omitempty" json:"email_register,omitempty"`
	MaxDailyRegister int         `yaml:"max-daily-register,omitempty" json:"max_daily_register,omitempty"`
	OAuth           []OAuthProvider `yaml:"oauth,omitempty" json:"oauth,omitempty"`
	InviteRequired  bool         `yaml:"invite-required,omitempty" json:"invite_required,omitempty"`
	InviteEmailRequired bool     `yaml:"invite-email-required,omitempty" json:"invite_email_required,omitempty"`
	ReferralEnabled bool         `yaml:"referral-enabled,omitempty" json:"referral_enabled,omitempty"`
	MaxReferrals    int          `yaml:"max-referrals,omitempty" json:"max_referrals,omitempty"`
}

// OAuthProvider OAuth 供应商配置
type OAuthProvider struct {
	Name         string `yaml:"name,omitempty" json:"name,omitempty"`
	ClientID     string `yaml:"client-id,omitempty" json:"client_id,omitempty"`
	ClientSecret string `yaml:"client-secret,omitempty" json:"client_secret,omitempty"`
	RedirectURL  string `yaml:"redirect-url,omitempty" json:"redirect_url,omitempty"`
}

// QuotaSettings 额度全局设置
type QuotaSettings struct {
	DefaultPoolMode  string        `yaml:"default-pool-mode,omitempty" json:"default_pool_mode,omitempty"` // public / private / contributor
	RPM              RPMSettings   `yaml:"rpm,omitempty" json:"rpm,omitempty"`
	ProbabilityLimit ProbabilitySettings `yaml:"probability-limit,omitempty" json:"probability_limit,omitempty"`
	RiskRule         RiskRuleSettings    `yaml:"risk-rule,omitempty" json:"risk_rule,omitempty"`
}

// RPMSettings RPM 限流设置
type RPMSettings struct {
	Enabled           bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	ContributorRPM    int  `yaml:"contributor-rpm,omitempty" json:"contributor_rpm,omitempty"`
	NonContributorRPM int  `yaml:"non-contributor-rpm,omitempty" json:"non_contributor_rpm,omitempty"`
}

// ProbabilitySettings 概率限流设置
type ProbabilitySettings struct {
	Enabled              bool    `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	ContributorWeight    float64 `yaml:"contributor-weight,omitempty" json:"contributor_weight,omitempty"`
	NonContributorWeight float64 `yaml:"non-contributor-weight,omitempty" json:"non_contributor_weight,omitempty"`
}

// RiskRuleSettings 风险联动设置
type RiskRuleSettings struct {
	Enabled            bool    `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	RPMExceedThreshold int     `yaml:"rpm-exceed-threshold,omitempty" json:"rpm_exceed_threshold,omitempty"`
	RPMExceedWindowSec int     `yaml:"rpm-exceed-window-sec,omitempty" json:"rpm_exceed_window_sec,omitempty"`
	PenaltyDurationSec int     `yaml:"penalty-duration-sec,omitempty" json:"penalty_duration_sec,omitempty"`
	PenaltyProbability float64 `yaml:"penalty-probability,omitempty" json:"penalty_probability,omitempty"`
}

// SecuritySettings 安全设置
type SecuritySettings struct {
	IPControl      IPControlSettings      `yaml:"ip-control,omitempty" json:"ip_control,omitempty"`
	RateLimit      RateLimitSettings      `yaml:"rate-limit,omitempty" json:"rate_limit,omitempty"`
	AnomalyDetect  AnomalyDetectSettings  `yaml:"anomaly-detect,omitempty" json:"anomaly_detect,omitempty"`
}

// IPControlSettings IP 控制设置
type IPControlSettings struct {
	Enabled    bool     `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	GeoEnabled bool     `yaml:"geo-enabled,omitempty" json:"geo_enabled,omitempty"`
	AllowedGeo []string `yaml:"allowed-geo,omitempty" json:"allowed_geo,omitempty"`
}

// RateLimitSettings 全局限流设置
type RateLimitSettings struct {
	Enabled    bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	GlobalQPS  int  `yaml:"global-qps,omitempty" json:"global_qps,omitempty"`
	PerIPRPM   int  `yaml:"per-ip-rpm,omitempty" json:"per_ip_rpm,omitempty"`
}

// AnomalyDetectSettings 异常检测设置
type AnomalyDetectSettings struct {
	Enabled bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
}

// SMTPConfig SMTP 邮件配置
type SMTPConfig struct {
	Host     string `yaml:"host,omitempty" json:"host,omitempty"`
	Port     int    `yaml:"port,omitempty" json:"port,omitempty"`
	Username string `yaml:"username,omitempty" json:"username,omitempty"`
	Password string `yaml:"password,omitempty" json:"password,omitempty"`
	From     string `yaml:"from,omitempty" json:"from,omitempty"`
	UseTLS   bool   `yaml:"use-tls,omitempty" json:"use_tls,omitempty"`
}

// PanelConfig 面板配置
type PanelConfig struct {
	Enabled  bool   `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	BasePath string `yaml:"base-path,omitempty" json:"base_path,omitempty"` // 默认 /panel
}
```

**Step 2: 在 Config struct 中添加 Community 字段**

修改 `internal/config/config.go`，在 Config struct 中添加：

```go
Community CommunityConfig `yaml:"community,omitempty" json:"community,omitempty"`
```

**Step 3: 运行编译检查**

Run: `go build ./internal/config/...`
Expected: 无错误

**Step 4: Commit**

```bash
git add internal/config/community_config.go internal/config/config.go
git commit -m "feat(config): add community platform configuration structures"
```

---

### Task 7: 创建存储工厂函数

**Files:**
- Create: `internal/db/factory.go`
- Test: `internal/db/factory_test.go`

**Step 1: 编写工厂函数**

创建 `internal/db/factory.go`：

```go
package db

import (
	"context"
	"fmt"
	"strings"
)

// NewStore 根据驱动类型创建存储实例
func NewStore(ctx context.Context, driver, dsn string) (Store, error) {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case "sqlite", "":
		store, err := NewSQLiteStore(ctx, dsn)
		if err != nil {
			return nil, err
		}
		if err := store.Migrate(ctx); err != nil {
			store.Close()
			return nil, fmt.Errorf("SQLite 迁移失败: %w", err)
		}
		return store, nil
	case "postgres", "postgresql":
		store, err := NewPostgresStore(ctx, dsn)
		if err != nil {
			return nil, err
		}
		if err := store.Migrate(ctx); err != nil {
			store.Close()
			return nil, fmt.Errorf("PostgreSQL 迁移失败: %w", err)
		}
		return store, nil
	default:
		return nil, fmt.Errorf("不支持的数据库驱动: %s（支持: sqlite, postgres）", driver)
	}
}
```

**Step 2: 编写工厂测试**

创建 `internal/db/factory_test.go`：

```go
package db_test

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

func TestNewStore_SQLite(t *testing.T) {
	ctx := context.Background()
	store, err := db.NewStore(ctx, "sqlite", ":memory:")
	if err != nil {
		t.Fatalf("创建 SQLite store 失败: %v", err)
	}
	defer store.Close()
}

func TestNewStore_InvalidDriver(t *testing.T) {
	ctx := context.Background()
	_, err := db.NewStore(ctx, "mysql", "")
	if err == nil {
		t.Fatal("期望返回错误，但得到 nil")
	}
}
```

**Step 3: 运行测试**

Run: `go test ./internal/db/... -v -run TestNewStore`
Expected: PASS

**Step 4: Commit**

```bash
git add internal/db/factory.go internal/db/factory_test.go
git commit -m "feat(db): add store factory with driver-based initialization"
```
