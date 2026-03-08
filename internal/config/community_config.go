package config

// ============================================================
// 公益站平台配置
// ============================================================

// CommunityConfig 公益站总配置
type CommunityConfig struct {
	Enabled  bool             `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Database DatabaseConfig   `yaml:"database,omitempty" json:"database,omitempty"`
	Auth     AuthConfig       `yaml:"auth,omitempty" json:"auth,omitempty"`
	Quota    QuotaSettings    `yaml:"quota,omitempty" json:"quota,omitempty"`
	Security SecuritySettings `yaml:"security,omitempty" json:"security,omitempty"`
	SMTP     SMTPConfig       `yaml:"smtp,omitempty" json:"smtp,omitempty"`
	Panel    PanelConfig      `yaml:"panel,omitempty" json:"panel,omitempty"`
}

// DatabaseConfig 数据库配置
type DatabaseConfig struct {
	Driver string `yaml:"driver,omitempty" json:"driver,omitempty"` // sqlite / postgres
	DSN    string `yaml:"dsn,omitempty" json:"dsn,omitempty"`
}

// AuthConfig 认证配置
type AuthConfig struct {
	JWTSecret           string          `yaml:"jwt-secret,omitempty" json:"jwt_secret,omitempty"`
	AccessTokenTTL      int             `yaml:"access-token-ttl,omitempty" json:"access_token_ttl,omitempty"`   // 秒，默认 7200
	RefreshTokenTTL     int             `yaml:"refresh-token-ttl,omitempty" json:"refresh_token_ttl,omitempty"` // 秒，默认 604800
	EmailRegister       bool            `yaml:"email-register,omitempty" json:"email_register,omitempty"`
	MaxDailyRegister    int             `yaml:"max-daily-register,omitempty" json:"max_daily_register,omitempty"`
	OAuth               []OAuthProvider `yaml:"oauth,omitempty" json:"oauth,omitempty"`
	InviteRequired      bool            `yaml:"invite-required,omitempty" json:"invite_required,omitempty"`
	InviteEmailRequired bool            `yaml:"invite-email-required,omitempty" json:"invite_email_required,omitempty"`
	ReferralEnabled     bool            `yaml:"referral-enabled,omitempty" json:"referral_enabled,omitempty"`
	MaxReferrals        int             `yaml:"max-referrals,omitempty" json:"max_referrals,omitempty"`
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
	DefaultPoolMode  string              `yaml:"default-pool-mode,omitempty" json:"default_pool_mode,omitempty"` // public / private / contributor
	RPM              RPMSettings         `yaml:"rpm,omitempty" json:"rpm,omitempty"`
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
	IPControl     IPControlSettings     `yaml:"ip-control,omitempty" json:"ip_control,omitempty"`
	RateLimit     RateLimitSettings     `yaml:"rate-limit,omitempty" json:"rate_limit,omitempty"`
	AnomalyDetect AnomalyDetectSettings `yaml:"anomaly-detect,omitempty" json:"anomaly_detect,omitempty"`
}

// IPControlSettings IP 控制设置
type IPControlSettings struct {
	Enabled    bool     `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	GeoEnabled bool     `yaml:"geo-enabled,omitempty" json:"geo_enabled,omitempty"`
	AllowedGeo []string `yaml:"allowed-geo,omitempty" json:"allowed_geo,omitempty"`
}

// RateLimitSettings 全局限流设置
type RateLimitSettings struct {
	Enabled   bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	GlobalQPS int  `yaml:"global-qps,omitempty" json:"global_qps,omitempty"`
	PerIPRPM  int  `yaml:"per-ip-rpm,omitempty" json:"per_ip_rpm,omitempty"`
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
