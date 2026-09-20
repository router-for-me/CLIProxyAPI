package billing

import "time"

type Config struct {
	Enabled            bool            `yaml:"enabled" json:"enabled"`
	Mode               string          `yaml:"mode" json:"mode"`
	Currency           string          `yaml:"currency" json:"currency"`
	UnknownPricePolicy string          `yaml:"unknown-price-policy" json:"unknown-price-policy"`
	Token              TokenConfig     `yaml:"token" json:"token"`
	RateLimit          RateLimitConfig `yaml:"rate-limit" json:"rate-limit"`
	Quota              QuotaConfig     `yaml:"quota" json:"quota"`
	Users              []UserConfig    `yaml:"users" json:"users"`
	PriceBook          PriceBookConfig `yaml:"price-book" json:"price-book"`
	Postgres           PostgresConfig  `yaml:"postgres" json:"postgres"`
	// LedgerPath is a local JSONL WAL fallback for billing events when PostgreSQL is unavailable.
	LedgerPath string `yaml:"ledger-path" json:"ledger_path"`
}

type PostgresConfig struct {
	DSNEnv string `yaml:"dsn-env" json:"dsn_env"`
	Schema string `yaml:"schema" json:"schema"`
}

type TokenConfig struct {
	PepperEnv     string `yaml:"pepper-env" json:"pepper-env"`
	LegacyAPIKeys string `yaml:"legacy-api-keys" json:"legacy-api-keys"`
}

type RateLimitConfig struct {
	RPM        int `yaml:"rpm" json:"rpm"`
	Concurrent int `yaml:"concurrent" json:"concurrent"`
}

type QuotaConfig struct {
	DailyNanos   int64 `yaml:"daily-nanos" json:"daily-nanos"`
	MonthlyNanos int64 `yaml:"monthly-nanos" json:"monthly-nanos"`
}

type UserConfig struct {
	ID     string             `yaml:"id" json:"id"`
	Name   string             `yaml:"name" json:"name"`
	Email  string             `yaml:"email,omitempty" json:"email,omitempty"`
	Status string             `yaml:"status" json:"status"`
	Tokens []TokenConfigEntry `yaml:"tokens" json:"tokens"`
	Quota  QuotaConfig        `yaml:"quota" json:"quota"`
	Rate   RateLimitConfig    `yaml:"rate" json:"rate"`
}

type TokenConfigEntry struct {
	ID          string          `yaml:"id" json:"id"`
	Name        string          `yaml:"name" json:"name"`
	Token       string          `yaml:"token,omitempty" json:"-"`
	TokenDigest string          `yaml:"token-digest,omitempty" json:"-"`
	Hint        string          `yaml:"hint,omitempty" json:"hint,omitempty"`
	Status      string          `yaml:"status" json:"status"`
	ExpiresAt   *time.Time      `yaml:"expires-at,omitempty" json:"expires_at,omitempty"`
	Quota       QuotaConfig     `yaml:"quota" json:"quota"`
	Rate        RateLimitConfig `yaml:"rate" json:"rate"`
}

type PriceBookConfig struct {
	Version string      `yaml:"version" json:"version"`
	Rules   []PriceRule `yaml:"rules" json:"rules"`
}

type PriceRule struct {
	ID                             string `yaml:"id" json:"id"`
	BillingProvider                string `yaml:"billing-provider" json:"billing_provider"`
	Model                          string `yaml:"model" json:"model"`
	ServiceTier                    string `yaml:"service-tier" json:"service_tier"`
	InputUncachedNanosPerToken     int64  `yaml:"input-uncached-nanos-per-token" json:"input_uncached_nanos_per_token"`
	InputCacheReadNanosPerToken    int64  `yaml:"input-cache-read-nanos-per-token" json:"input_cache_read_nanos_per_token"`
	InputCacheWrite5mNanosPerToken int64  `yaml:"input-cache-write-5m-nanos-per-token" json:"input_cache_write_5m_nanos_per_token"`
	InputCacheWrite1hNanosPerToken int64  `yaml:"input-cache-write-1h-nanos-per-token" json:"input_cache_write_1h_nanos_per_token"`
	OutputTextNanosPerToken        int64  `yaml:"output-text-nanos-per-token" json:"output_text_nanos_per_token"`
	OutputReasoningNanosPerToken   int64  `yaml:"output-reasoning-nanos-per-token" json:"output_reasoning_nanos_per_token"`
}

type Principal struct {
	UserID   string
	TokenID  string
	TeamID   string
	Provider string
}

type Event struct {
	EventID                     string     `json:"event_id"`
	RequestID                   string     `json:"request_id"`
	TraceID                     string     `json:"trace_id,omitempty"`
	UserID                      string     `json:"user_id"`
	TokenID                     string     `json:"token_id"`
	Provider                    string     `json:"provider"`
	Model                       string     `json:"model"`
	Alias                       string     `json:"alias,omitempty"`
	AuthIndex                   string     `json:"auth_index,omitempty"`
	SessionID                   string     `json:"session_id,omitempty"`
	Failed                      bool       `json:"failed"`
	BillingPolicy               string     `json:"billing_policy"`
	BillingQuality              string     `json:"billing_quality"`
	PriceBookVersion            string     `json:"price_book_version,omitempty"`
	PriceRuleID                 string     `json:"price_rule_id,omitempty"`
	PriceSnapshot               *PriceRule `json:"price_snapshot,omitempty"`
	InputUncachedTokens         int64      `json:"input_uncached_tokens"`
	InputCacheReadTokens        int64      `json:"input_cache_read_tokens"`
	InputCacheWrite5mTokens     int64      `json:"input_cache_write_5m_tokens"`
	InputCacheWrite1hTokens     int64      `json:"input_cache_write_1h_tokens"`
	OutputTextTokens            int64      `json:"output_text_tokens"`
	OutputReasoningTokens       int64      `json:"output_reasoning_tokens"`
	TotalTokens                 int64      `json:"total_tokens"`
	CustomerCostNanos           *int64     `json:"customer_cost_nanos,omitempty"`
	UpstreamEquivalentCostNanos *int64     `json:"upstream_equivalent_cost_nanos,omitempty"`
	RequestedAt                 time.Time  `json:"requested_at"`
	CompletedAt                 time.Time  `json:"completed_at"`
	LatencyMs                   int64      `json:"latency_ms"`
}

type Summary struct {
	Enabled                     bool                   `json:"enabled"`
	Mode                        string                 `json:"mode"`
	Currency                    string                 `json:"currency"`
	PriceBookVersion            string                 `json:"price_book_version"`
	Requests                    int                    `json:"requests"`
	PricedRequests              int                    `json:"priced_requests"`
	UnpricedRequests            int                    `json:"unpriced_requests"`
	CustomerCostNanos           int64                  `json:"customer_cost_nanos"`
	UpstreamEquivalentCostNanos int64                  `json:"upstream_equivalent_cost_nanos"`
	Users                       map[string]UserSummary `json:"users"`
}

type UserSummary struct {
	Requests          int                     `json:"requests"`
	CustomerCostNanos int64                   `json:"customer_cost_nanos"`
	Tokens            map[string]TokenSummary `json:"tokens"`
}

type TokenSummary struct {
	Requests          int   `json:"requests"`
	CustomerCostNanos int64 `json:"customer_cost_nanos"`
}
