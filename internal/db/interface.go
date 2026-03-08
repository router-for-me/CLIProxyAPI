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
	TemplateStore
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
	HasUserUsedCode(ctx context.Context, codeID, userID int64) (bool, error)
}

type ListInviteCodesOpts struct {
	Page      int
	PageSize  int
	Type      *string // admin_created / user_referral
	Status    *string
	CreatorID *int64  // 按创建者过滤
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
// 兑换码模板
// ============================================================

type TemplateStore interface {
	CreateTemplate(ctx context.Context, tpl *RedemptionTemplate) error
	GetTemplateByID(ctx context.Context, id int64) (*RedemptionTemplate, error)
	ListTemplates(ctx context.Context) ([]*RedemptionTemplate, error)
	IncrementTemplateIssuedCount(ctx context.Context, id int64) error
	CountTemplateClaimsByUser(ctx context.Context, userID, templateID int64) (int, error)
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
