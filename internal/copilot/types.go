package copilot

import "time"

type QuotaSnapshot struct {
	Entitlement int  `json:"entitlement"`
	Remaining   int  `json:"remaining"`
	Unlimited   bool `json:"unlimited"`
}

type QuotaSnapshots struct {
	PremiumInteractions QuotaSnapshot `json:"premium_interactions"`
	Completions         QuotaSnapshot `json:"completions"`
	Chat                QuotaSnapshot `json:"chat"`
}

type QuotaResponse struct {
	QuotaSnapshots    *QuotaSnapshots `json:"quota_snapshots"`
	QuotaResetDate    string          `json:"quota_reset_date"`
	QuotaResetDateUTC string          `json:"quota_reset_date_utc,omitempty"`
}

type AccountQuotaSnapshot struct {
	Entitlement int     `json:"entitlement"`
	Remaining   int     `json:"remaining"`
	Used        int     `json:"used"`
	PercentUsed float64 `json:"percent_used"`
	Unlimited   bool    `json:"unlimited"`
}

type AccountQuotaSnapshots struct {
	PremiumInteractions AccountQuotaSnapshot `json:"premium_interactions"`
	Completions         AccountQuotaSnapshot `json:"completions"`
	Chat                AccountQuotaSnapshot `json:"chat"`
}

type AccountQuota struct {
	AccountID      string                `json:"account_id"`
	Email          string                `json:"email"`
	Plan           string                `json:"plan,omitempty"`
	QuotaSnapshots AccountQuotaSnapshots `json:"quota_snapshots"`
	ResetDate      string                `json:"reset_date,omitempty"`
	CachedAt       time.Time             `json:"cached_at"`
	Error          string                `json:"error,omitempty"`
}

type ManagementResponse struct {
	Accounts        []AccountQuota `json:"accounts"`
	CacheTTLSeconds int            `json:"cache_ttl_seconds"`
	Message         string         `json:"message,omitempty"`
}

type TokenInfo struct {
	AccessToken string    `json:"access_token"`
	Email       string    `json:"email"`
	CreatedAt   time.Time `json:"created_at"`
}

type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type AccessTokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}
