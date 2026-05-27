package usagestats

import (
	"time"
)

// Event is a sanitized usage record safe for persistent storage.
// It never contains prompts, messages, response bodies, raw API keys,
// tokens, cookies, refresh tokens, or other secret material.
type Event struct {
	RequestID string `json:"request_id" db:"request_id"`
	// RequestedAt is the timestamp when the upstream request was initiated.
	RequestedAt time.Time `json:"requested_at" db:"requested_at"`

	CallType string `json:"call_type" db:"call_type"`

	Provider string `json:"provider" db:"provider"`
	Model    string `json:"model" db:"model"`
	Alias    string `json:"alias" db:"alias"`
	Endpoint string `json:"endpoint" db:"endpoint"`

	// APIKeyID is a non-secret stable identifier derived from the client API key.
	// It is never the raw API key value.
	APIKeyID string `json:"api_key_id" db:"api_key_id"`
	// AuthID is the auth record ID (non-secret file-based identifier).
	AuthID string `json:"auth_id" db:"auth_id"`
	// AuthIndex is a stable hash derived from auth credentials, never the raw credential.
	AuthIndex string `json:"auth_index" db:"auth_index"`
	// AuthType is the authentication kind, e.g. "oauth", "apikey".
	AuthType string `json:"auth_type" db:"auth_type"`
	// SourceLabel is a safe display label (email, project ID) for the upstream account.
	// It is never a raw API key, token, or cookie.
	SourceLabel string `json:"source_label" db:"source_label"`

	ReasoningEffort string `json:"reasoning_effort" db:"reasoning_effort"`

	Failed     bool `json:"failed" db:"failed"`
	StatusCode int  `json:"status_code" db:"status_code"`
	// ErrorType is a coarse error classification, e.g. "http_429", "upstream_error".
	// It is never a full error body.
	ErrorType string `json:"error_type" db:"error_type"`
	LatencyMs int64 `json:"latency_ms" db:"latency_ms"`

	InputTokens         int64 `json:"input_tokens" db:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens" db:"output_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens" db:"reasoning_tokens"`
	CachedTokens        int64 `json:"cached_tokens" db:"cached_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens" db:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens" db:"cache_creation_tokens"`
	TotalTokens         int64 `json:"total_tokens" db:"total_tokens"`

	CostKnown       bool  `json:"cost_known" db:"cost_known"`
	InputCostMicros int64 `json:"input_cost_micros" db:"input_cost_micros"`
	OutputCostMicros int64 `json:"output_cost_micros" db:"output_cost_micros"`
	TotalCostMicros  int64 `json:"total_cost_micros" db:"total_cost_micros"`
}

// ModelPrice defines manual per-token pricing for a specific provider+model pair.
type ModelPrice struct {
	Provider           string  `yaml:"provider" json:"provider"`
	Model              string  `yaml:"model" json:"model"`
	InputCostPerToken  float64 `yaml:"input_cost_per_token" json:"input_cost_per_token"`
	OutputCostPerToken float64 `yaml:"output_cost_per_token" json:"output_cost_per_token"`
}

// GroupBy defines the aggregation dimension for summary queries.
type GroupBy string

const (
	GroupByDay      GroupBy = "day"
	GroupByProvider GroupBy = "provider"
	GroupByModel    GroupBy = "model"
	GroupByAPIKey   GroupBy = "api_key"
	GroupByAuth     GroupBy = "auth"
	GroupByCallType GroupBy = "call_type"
)

// ValidGroupByValues returns all supported group_by values.
func ValidGroupByValues() []GroupBy {
	return []GroupBy{GroupByDay, GroupByProvider, GroupByModel, GroupByAPIKey, GroupByAuth, GroupByCallType}
}

// IsValidGroupBy checks whether a group_by value is valid.
func IsValidGroupBy(s string) bool {
	for _, gb := range ValidGroupByValues() {
		if string(gb) == s {
			return true
		}
	}
	return false
}

// ParseGroupBy parses and validates a group_by string. Returns the default
// GroupByDay for empty input.
func ParseGroupBy(s string) (GroupBy, bool) {
	if s == "" {
		return GroupByDay, true
	}
	if IsValidGroupBy(s) {
		return GroupBy(s), true
	}
	return "", false
}
