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
	ID     int64     `json:"id"`
	CodeID int64     `json:"code_id"`
	UserID int64     `json:"user_id"`
	UsedAt time.Time `json:"used_at"`
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
	TotalRequests int64            `json:"total_requests"`
	TotalTokens   int64            `json:"total_tokens"`
	ByModel       map[string]int64 `json:"by_model"`
	ByProvider    map[string]int64 `json:"by_provider"`
	AvgLatency    float64          `json:"avg_latency_ms"`
}

// UserRequestStats 用户请求统计
type UserRequestStats struct {
	UserID        int64            `json:"user_id"`
	TotalRequests int64            `json:"total_requests"`
	TotalTokens   int64            `json:"total_tokens"`
	ByModel       map[string]int64 `json:"by_model"`
}

// RedemptionTemplate 兑换码模板
type RedemptionTemplate struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	BonusQuota  QuotaGrant `json:"bonus_quota"`
	MaxPerUser  int        `json:"max_per_user"`
	TotalLimit  int        `json:"total_limit"`
	IssuedCount int        `json:"issued_count"`
	Enabled     bool       `json:"enabled"`
	CreatedAt   time.Time  `json:"created_at"`
}
